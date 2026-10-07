package gateway

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	wire "opencode2api/internal/protocol"
)

func TestFirstEventRetryKeepsIdentityAndDoesNotLeakFailedPrelude(t *testing.T) {
	var attempts atomic.Int32
	headers := make(chan http.Header, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		if attempts.Add(1) == 1 {
			fmt.Fprint(w, ": failed-attempt-keepalive\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, "data: {\"id\":\"reply\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	cfg := gatewayTestConfig(upstream.URL)
	cfg.Retry.MaxAttempts = 2
	cfg.Performance.FirstEventTimeoutSeconds = 1
	g := newGatewayForTest(t, cfg)
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"catalog-model","messages":[],"stream":true}`))
	r = r.WithContext(WithDiagnosticRequest(r.Context()))
	w := httptest.NewRecorder()
	g.handleInference(wire.Chat)(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "[DONE]") || strings.Contains(w.Body.String(), "failed-attempt-keepalive") {
		t.Fatalf("failed attempt committed bytes or retry did not succeed: %d %s", w.Code, w.Body.String())
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}
	assertOpenCodeIdentityHeadersEqual(t, <-headers, <-headers)
}
