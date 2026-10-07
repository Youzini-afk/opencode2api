package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"opencode2api/internal/config"
	"opencode2api/internal/httpx"
	"opencode2api/internal/models"
	wire "opencode2api/internal/protocol"
	"testing"
)

func TestDynamicTierProtocolsAndSystemOne(t *testing.T) {
	type received struct {
		path   string
		header http.Header
		body   map[string]any
	}
	captured := make(chan received, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		captured <- received{r.URL.Path, r.Header.Clone(), body}
		switch r.URL.Path {
		case "/v1/systemone":
			httpx.WriteJSON(w, 200, map[string]any{"answers": []any{true}})
		case "/v1/messages":
			httpx.WriteJSON(w, 200, map[string]any{"id": "reply", "type": "message", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "ok"}}, "stop_reason": "end_turn"})
		default:
			httpx.WriteJSON(w, 200, map[string]any{"error": "wrong protocol"})
		}
	}))
	defer upstream.Close()
	cfg := gatewayTestConfig(upstream.URL)
	cfg.GoKeys = []string{"go-key"}
	cfg.Upstream.Go = upstream.URL
	g := newGatewayForTest(t, cfg)
	g.catalog.ReplaceWithCapabilities([]string{"alias", "decision"}, []string{"alias"},
		map[config.Tier]map[string]wire.Protocol{config.TierZen: {"alias": wire.Chat, "decision": wire.SystemOne}, config.TierGo: {"alias": wire.Anthropic}},
		map[config.Tier]map[string]bool{config.TierZen: {"alias": true}},
		map[config.Tier]map[string]models.Metadata{config.TierGo: {"alias": {ContextWindow: 12345}}})
	recorder := httptest.NewRecorder()
	g.handleInference(wire.Chat)(recorder, httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(`{"model":"alias","messages":[{"role":"user","content":"hi"}]}`)))
	if recorder.Code != 200 {
		t.Fatalf("dynamic protocol conversion failed: %d %s", recorder.Code, recorder.Body.String())
	}
	actual := <-captured
	if actual.path != "/v1/messages" || actual.header.Get("x-api-key") != "go-key" {
		t.Fatalf("preferred tier protocol ignored: %#v", actual)
	}
	recorder = httptest.NewRecorder()
	g.handleSystemOne(recorder, httptest.NewRequest("POST", "/v1/systemone", bytes.NewBufferString(`{"model":"decision","state":"example","questions":[{"type":"boolean","question":"ready?"}]}`)))
	if recorder.Code != 200 {
		t.Fatalf("System One failed: %d %s", recorder.Code, recorder.Body.String())
	}
	actual = <-captured
	if actual.path != "/v1/systemone" || actual.body["state"] != "example" || actual.body["tools"] != nil || actual.body["stream"] != nil || actual.header.Get("Authorization") != "" {
		t.Fatalf("System One payload/credential changed: %#v", actual)
	}
}
