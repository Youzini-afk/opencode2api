package protocol

import (
	"bufio"
	"encoding/json"
	"net/http/httptest"
	"opencode2api/internal/jsonutil"
	"strings"
	"testing"
)

func TestAnthropicThinkingUsesAdaptiveModeWhenManualBudgetWouldBeInvalid(t *testing.T) {
	base := map[string]any{
		"model":    "claude-test",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	}
	high := jsonutil.CloneMap(base)
	high["reasoning_effort"] = "high"
	converted, err := PrepareRequest(Chat, Anthropic, high, "https://example.test")
	if err != nil {
		t.Fatal(err)
	}
	if got := jsonutil.StringAt(converted, "thinking", "type"); got != "adaptive" {
		t.Fatalf("high effort thinking type = %q, want adaptive; body=%#v", got, converted)
	}
	if got := jsonutil.StringAt(converted, "output_config", "effort"); got != "high" {
		t.Fatalf("output_config.effort = %q, want high", got)
	}
	if _, exists := jsonutil.MapAt(converted, "thinking")["budget_tokens"]; exists {
		t.Fatalf("adaptive thinking unexpectedly contains budget_tokens: %#v", converted["thinking"])
	}

	manual := jsonutil.CloneMap(base)
	manual["max_tokens"] = 4096
	manual["reasoning"] = map[string]any{"type": "enabled", "budget_tokens": 8192}
	converted, err = PrepareRequest(Chat, Anthropic, manual, "https://example.test")
	if err != nil {
		t.Fatal(err)
	}
	if got := jsonutil.StringAt(converted, "thinking", "type"); got != "adaptive" {
		t.Fatalf("invalid manual budget was preserved: %#v", converted["thinking"])
	}
}

func TestAnthropicStreamContentBlocksNeverOverlap(t *testing.T) {
	recorder := httptest.NewRecorder()
	emitter := newBridgeStreamEmitter(recorder, recorder, Anthropic, "test-model")
	events := []bridgeStreamEvent{
		{Kind: "tool_start", ToolKey: "a", ToolID: "call-a", ToolName: "first"},
		{Kind: "tool_delta", ToolKey: "a", Text: `{"a":1}`},
		{Kind: "tool_start", ToolKey: "b", ToolID: "call-b", ToolName: "second"},
		{Kind: "tool_delta", ToolKey: "b", Text: `{"b":2}`},
		{Kind: "text", Text: "after tools"},
		{Kind: "reasoning", Text: "late reasoning"},
		{Kind: "reasoning_signature", Signature: "signature"},
	}
	for _, event := range events {
		if err := emitter.Emit(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := emitter.Finish(); err != nil {
		t.Fatal(err)
	}

	active := false
	toolStop := false
	starts, stops := 0, 0
	scanner := bufio.NewScanner(strings.NewReader(recorder.Body.String()))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil {
			continue
		}
		switch jsonutil.StringAt(event, "type") {
		case "message_delta":
			toolStop = jsonutil.StringAt(event, "delta", "stop_reason") == "tool_use"
		case "content_block_start":
			if active {
				t.Fatalf("content block started before previous block stopped: %s", recorder.Body.String())
			}
			active = true
			starts++
		case "content_block_stop":
			if !active {
				t.Fatalf("content block stopped without an active block: %s", recorder.Body.String())
			}
			active = false
			stops++
		}
	}
	if active || starts != 4 || stops != starts {
		t.Fatalf("invalid Anthropic block lifecycle: starts=%d stops=%d active=%v\n%s", starts, stops, active, recorder.Body.String())
	}
	if !toolStop {
		t.Fatalf("buffered tool calls lost tool_use stop reason: %s", recorder.Body.String())
	}
}

func TestResponsesTerminalEventWithoutUsageIsNotReported(t *testing.T) {
	parser := &bridgeStreamParser{
		protocol: Responses, tools: map[string]bool{}, toolIDs: map[string]string{}, toolNames: map[string]string{},
		responseArgs: map[string]bool{}, responseReasoning: map[string]bool{},
	}
	events, err := parser.Parse("response.completed", `{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "usage" || event.Usage != nil {
			t.Fatalf("terminal event without usage produced a usage report: %#v", events)
		}
	}
}
