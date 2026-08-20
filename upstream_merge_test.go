package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAnonymousAuthenticationErrorsDoNotRetryOrUseKeys(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if authorization := r.Header.Get("Authorization"); authorization != "" {
					t.Errorf("authentication error triggered keyed fallback: %q", authorization)
				}
				writeJSON(w, status, map[string]any{"error": map[string]string{"message": "anonymous rejected"}})
			}))
			defer server.Close()

			cfg := gatewayTestConfig(server.URL)
			cfg.Anonymous = true
			cfg.ZenKeys = []string{"must-not-be-used"}
			cfg.Retry.MaxAttempts = 3
			gateway := newGatewayForTest(t, cfg)
			gateway.monitor = NewMonitor()
			gateway.catalog.Replace([]string{"catalog-model"}, nil)

			recorder := httptest.NewRecorder()
			body := bytes.NewBufferString(`{"model":"catalog-model","messages":[]}`)
			gateway.handleInference(ProtocolChat)(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body))
			if recorder.Code != status {
				t.Fatalf("status = %d, body = %s; want %d", recorder.Code, recorder.Body.String(), status)
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("anonymous HTTP %d made %d upstream requests; want exactly one", status, got)
			}
			recent := gateway.monitor.Snapshot().Upstream.Recent
			if len(recent) != 1 || recent[0].Outcome != "rejected" {
				t.Fatalf("anonymous HTTP %d attempt outcome = %#v, want rejected", status, recent)
			}
		})
	}
}

func TestAnonymous429FallsBackToPreferredKeyWithStableIdentityAndTargetNormalization(t *testing.T) {
	var anonymousHeader, keyedHeader http.Header
	var keyedPayload map[string]any
	zen := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anonymousHeader = r.Header.Clone()
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("anonymous request sent Authorization = %q", got)
		}
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": map[string]string{"message": "busy"}})
	}))
	defer zen.Close()
	goUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keyedHeader = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&keyedPayload); err != nil {
			t.Errorf("decode keyed fallback body: %v", err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "response", "choices": []any{}})
	}))
	defer goUpstream.Close()

	cfg := gatewayTestConfig(zen.URL)
	cfg.Anonymous = true
	cfg.GoKeys = []string{"go-secret"}
	cfg.Prefer = TierGo
	cfg.Retry.MaxAttempts = 1
	// The endpoint hint activates the existing tool-reasoning compatibility
	// path only for the actual Go fallback target.
	cfg.Upstream.Go = goUpstream.URL + "/deepseek"
	gateway := newGatewayForTest(t, cfg)
	gateway.catalog.Replace([]string{"alias-model"}, []string{"alias-model"})

	payload := `{"model":"alias-model","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"ok"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
	request.Header.Set("x-session-id", "fallback-session")
	recorder := httptest.NewRecorder()
	gateway.handleInference(ProtocolChat)(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := keyedHeader.Get("Authorization"); got != "Bearer go-secret" {
		t.Fatalf("keyed fallback Authorization = %q", got)
	}
	assertOpenCodeIdentityHeadersEqual(t, anonymousHeader, keyedHeader)
	messages := sliceAt(keyedPayload, "messages")
	assistant, _ := messages[0].(map[string]any)
	if strings.TrimSpace(stringAt(assistant, "reasoning_content")) == "" {
		t.Fatalf("Go fallback did not receive target-specific reasoning normalization: %#v", assistant)
	}
}

func TestAnthropicThinkingUsesAdaptiveModeWhenManualBudgetWouldBeInvalid(t *testing.T) {
	base := map[string]any{
		"model":    "claude-test",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	}
	high := cloneMap(base)
	high["reasoning_effort"] = "high"
	converted, err := prepareUpstreamRequest(ProtocolChat, ProtocolAnthropic, high, "https://example.test")
	if err != nil {
		t.Fatal(err)
	}
	if got := stringAt(converted, "thinking", "type"); got != "adaptive" {
		t.Fatalf("high effort thinking type = %q, want adaptive; body=%#v", got, converted)
	}
	if got := stringAt(converted, "output_config", "effort"); got != "high" {
		t.Fatalf("output_config.effort = %q, want high", got)
	}
	if _, exists := mapAt(converted, "thinking")["budget_tokens"]; exists {
		t.Fatalf("adaptive thinking unexpectedly contains budget_tokens: %#v", converted["thinking"])
	}

	manual := cloneMap(base)
	manual["max_tokens"] = 4096
	manual["reasoning"] = map[string]any{"type": "enabled", "budget_tokens": 8192}
	converted, err = prepareUpstreamRequest(ProtocolChat, ProtocolAnthropic, manual, "https://example.test")
	if err != nil {
		t.Fatal(err)
	}
	if got := stringAt(converted, "thinking", "type"); got != "adaptive" {
		t.Fatalf("invalid manual budget was preserved: %#v", converted["thinking"])
	}
}

func TestAnthropicStreamContentBlocksNeverOverlap(t *testing.T) {
	recorder := httptest.NewRecorder()
	emitter := newBridgeStreamEmitter(recorder, recorder, ProtocolAnthropic, "test-model")
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
		switch stringAt(event, "type") {
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
}

func TestResponsesTerminalEventWithoutUsageIsNotReported(t *testing.T) {
	parser := &bridgeStreamParser{
		protocol: ProtocolResponses, tools: map[string]bool{}, toolIDs: map[string]string{}, toolNames: map[string]string{},
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
