package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opencode2api/internal/config"
	"opencode2api/internal/jsonutil"
	"opencode2api/internal/models"
	wire "opencode2api/internal/protocol"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOfficialAnonymousCompatibilityAndDirectoryFallback(t *testing.T) {
	cfg := gatewayTestConfig("https://opencode.ai/zen")
	cfg.Fingerprint.Versions = []string{"2.0.29"}
	g := newGatewayForTest(t, cfg)
	var modelRequests int
	g.transports.items[0].client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer public" || r.Header.Get("x-opencode-request") != "" || r.Header.Get("x-opencode-session") != r.Header.Get("x-opencode-session-id") || r.Header.Get("User-Agent") != "opencode/latest/2.0.29/cli" {
			t.Fatalf("invalid official anonymous headers: %v", r.Header)
		}
		status, contentType, body := 200, "application/json", ""
		if r.Method == "GET" {
			modelRequests++
			if r.URL.Path == "/inference/v1/models" {
				status, body = 404, `{}`
			} else if r.URL.Path == "/zen/v1/models" {
				body = `{"data":[{"id":"paid-model"},{"id":"gemini-test"},{"id":"example-free"}]}`
			} else {
				t.Fatalf("unexpected directory target: %s", r.URL)
			}
		} else {
			if r.URL.Path != "/inference/v1/chat/completions" {
				t.Fatalf("anonymous inference used the old host: %s", r.URL)
			}
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for _, raw := range jsonutil.SliceAt(payload, "tools") {
				tool, _ := raw.(map[string]any)
				name := jsonutil.StringAt(tool, "function", "name")
				seen[name] = true
				if len(jsonutil.StringAt(tool, "function", "description")) < 30 {
					t.Fatalf("stub core tool sent upstream: %v", tool)
				}
			}
			if !jsonutil.BoolAt(payload, "stream") || !seen["shell"] || !seen["read"] {
				t.Fatalf("missing official agent shape: %v", payload)
			}
			contentType = "text/event-stream"
			body = "data: {\"id\":\"reply\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	models := g.refreshZen(context.Background(), "req_directory")
	if len(models) != 3 || modelRequests != 2 {
		t.Fatalf("directory fallback filtered models: %v (%d requests)", models, modelRequests)
	}
	g.catalog.Replace(models, nil)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"paid-model","messages":[]}`))
	g.handleInference(wire.Chat)(w, r)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/json" || !strings.Contains(w.Body.String(), "ok") {
		t.Fatalf("non-streaming client did not get collapsed JSON: %d %s", w.Code, w.Body.String())
	}
}

func TestAttemptDeadlineAlsoBoundsSystemOne(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	for _, path := range []string{"/v1/chat/completions", "/v1/systemone"} {
		r, _ := http.NewRequestWithContext(context.Background(), "POST", "http://example.test"+path, nil)
		_, err := doInferenceAttempt(client, r, 0, 10*time.Millisecond)
		if !errors.Is(err, errAttemptTimeout) {
			t.Fatalf("%s did not use the configured attempt deadline: %v", path, err)
		}
	}
}

func TestAnonymousEndpointMigrationKeepsCustomRelays(t *testing.T) {
	for _, tc := range []struct{ zen, explicit, want string }{
		{"https://opencode.ai/zen", "", "https://opencode.ai/inference"},
		{"https://custom.example/zen", "", "https://custom.example/zen"},
		{"https://opencode.ai/zen", "https://custom.example/inference", "https://custom.example/inference"},
	} {
		cfg := config.UpstreamConfig{Zen: tc.zen, Anonymous: tc.explicit}
		if got := cfg.AnonymousBase(); got != tc.want {
			t.Fatalf("AnonymousBase(%v)=%q, want %q", cfg, got, tc.want)
		}
	}
}

func TestAutoEffortIsScopedToActualEndpointAndLane(t *testing.T) {
	cfg := gatewayTestConfig("https://zen.example.test")
	cfg.ZenKeys = []string{"zen-key"}
	cfg.Upstream.Anonymous = "https://anonymous.example.test"
	cfg.Reasoning.Effort = "auto"
	g := newGatewayForTest(t, cfg)
	g.catalog.ReplaceWithCapabilities([]string{"test-free"}, nil, nil, nil,
		map[config.Tier]map[string]models.Metadata{config.TierZen: {"test-free": {Reasoning: true}}})
	g.availability, _ = models.NewAvailabilityStore("")
	err := g.availability.Record("test-free", 0, true, "probe_succeeded", "anonymous", time.Now(), models.EffortProbe{
		Effort: "high", Protocol: string(wire.Chat), Endpoint: cfg.Upstream.Anonymous, Anonymous: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := g.resolvedEffort("test-free", config.TierZen, wire.Chat, true); got != "high" {
		t.Fatalf("matching observation was lost: %q", got)
	}
	if got := g.resolvedEffort("test-free", config.TierZen, wire.Chat, false); got != "" {
		t.Fatalf("anonymous observation leaked to keyed fallback: %q", got)
	}
	body := g.applyAutoEffort([]byte(`{"model":"test-free","reasoning_effort":"low"}`), "test-free", config.TierZen, wire.Chat, true)
	var payload map[string]any
	json.Unmarshal(body, &payload)
	if payload["reasoning_effort"] != "low" {
		t.Fatalf("auto overrode caller effort: %s", body)
	}
	g.cfg.Upstream.Anonymous = "https://different.example.test"
	if got := g.resolvedEffort("test-free", config.TierZen, wire.Chat, true); got != "" {
		t.Fatalf("old endpoint observation survived a host change: %q", got)
	}
	err = g.availability.Record("test-free", 0, true, "probe_succeeded", "zen", time.Now(), models.EffortProbe{
		Effort: "high", Protocol: string(wire.Chat), Endpoint: cfg.Upstream.Zen,
	})
	if err != nil {
		t.Fatal(err)
	}
	g.transports.items[0].client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var payload map[string]any
		json.NewDecoder(r.Body).Decode(&payload)
		if payload["reasoning_effort"] != "high" || payload["tools"] != nil {
			t.Fatalf("custom keyed auto effort lost or unwanted shaping applied: %v", payload)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[]}`)), Request: r}, nil
	})}
	w := httptest.NewRecorder()
	g.handleInference(wire.Chat)(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"test-free","messages":[]}`)))
	if w.Code != 200 {
		t.Fatalf("custom keyed auto inference failed: %d %s", w.Code, w.Body.String())
	}
}
