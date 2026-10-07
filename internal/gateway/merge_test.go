package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"opencode2api/internal/config"
	"opencode2api/internal/httpx"
	"opencode2api/internal/jsonutil"
	wire "opencode2api/internal/protocol"
	"opencode2api/internal/telemetry"
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
				httpx.WriteJSON(w, status, map[string]any{"error": map[string]string{"message": "anonymous rejected"}})
			}))
			defer server.Close()

			cfg := gatewayTestConfig(server.URL)
			cfg.Anonymous = true
			cfg.ZenKeys = []string{"must-not-be-used"}
			cfg.Retry.MaxAttempts = 3
			gateway := newGatewayForTest(t, cfg)
			gateway.monitor = telemetry.NewMonitor()
			gateway.catalog.Replace([]string{"catalog-model"}, nil)

			recorder := httptest.NewRecorder()
			body := bytes.NewBufferString(`{"model":"catalog-model","messages":[]}`)
			gateway.handleInference(wire.Chat)(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body))
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
		httpx.WriteJSON(w, http.StatusTooManyRequests, map[string]any{"error": map[string]string{"message": "busy"}})
	}))
	defer zen.Close()
	goUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keyedHeader = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&keyedPayload); err != nil {
			t.Errorf("decode keyed fallback body: %v", err)
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"id": "response", "choices": []any{}})
	}))
	defer goUpstream.Close()

	cfg := gatewayTestConfig(zen.URL)
	cfg.Anonymous = true
	cfg.GoKeys = []string{"go-secret"}
	cfg.Prefer = config.TierGo
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
	gateway.handleInference(wire.Chat)(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := keyedHeader.Get("Authorization"); got != "Bearer go-secret" {
		t.Fatalf("keyed fallback Authorization = %q", got)
	}
	assertOpenCodeIdentityHeadersEqual(t, anonymousHeader, keyedHeader)
	messages := jsonutil.SliceAt(keyedPayload, "messages")
	assistant, _ := messages[0].(map[string]any)
	if strings.TrimSpace(jsonutil.StringAt(assistant, "reasoning_content")) == "" {
		t.Fatalf("Go fallback did not receive target-specific reasoning normalization: %#v", assistant)
	}
}
