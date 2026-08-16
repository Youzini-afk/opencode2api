package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestFetchModelsAuthenticationAndCompleteCatalog(t *testing.T) {
	wantModels := []string{"paid-model", "mimo-v2.5-free", "gemini-2.5-pro"}
	tests := []struct {
		name              string
		key               string
		wantAuthorization string
	}{
		{name: "anonymous", wantAuthorization: ""},
		{name: "keyed", key: "zen-secret", wantAuthorization: "Bearer zen-secret"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var authorization string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				authorization = r.Header.Get("Authorization")
				writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": []map[string]string{
					{"id": wantModels[0]}, {"id": wantModels[1]}, {"id": wantModels[2]},
				}})
			}))
			defer server.Close()

			models, status, err := fetchModels(context.Background(), server.Client(), server.URL, test.key)
			if err != nil {
				t.Fatalf("fetchModels() error = %v", err)
			}
			if status != http.StatusOK {
				t.Fatalf("fetchModels() status = %d, want %d", status, http.StatusOK)
			}
			if authorization != test.wantAuthorization {
				t.Fatalf("Authorization = %q, want %q", authorization, test.wantAuthorization)
			}
			if !reflect.DeepEqual(models, wantModels) {
				t.Fatalf("fetchModels() = %#v, want complete upstream catalog %#v", models, wantModels)
			}
		})
	}
}

func TestAnonymousCatalogReturnsCompleteUpstreamDirectory(t *testing.T) {
	gateway := newGatewayForTest(t, gatewayTestConfig("https://zen.example.test"))
	gateway.catalog.Replace([]string{"paid-model", "mimo-v2.5-free", "gemini-2.5-pro"}, nil)
	recorder := httptest.NewRecorder()
	gateway.handleModels(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("models status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode models response: %v", err)
	}
	got := make([]string, 0, len(response.Data))
	for _, model := range response.Data {
		got = append(got, model.ID)
	}
	want := []string{"gemini-2.5-pro", "mimo-v2.5-free", "paid-model"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("exposed models = %#v, want complete upstream directory %#v", got, want)
	}
}

func TestSetUpstreamAuthentication(t *testing.T) {
	tests := []struct {
		name              string
		protocol          Protocol
		key               string
		wantAuthorization string
		wantAPIKey        string
		wantVersion       string
	}{
		{name: "anonymous chat", protocol: ProtocolChat},
		{name: "keyed responses", protocol: ProtocolResponses, key: "zen-secret", wantAuthorization: "Bearer zen-secret"},
		{name: "anonymous anthropic", protocol: ProtocolAnthropic, wantVersion: "2023-06-01"},
		{name: "keyed anthropic", protocol: ProtocolAnthropic, key: "zen-secret", wantAPIKey: "zen-secret", wantVersion: "2023-06-01"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "https://example.test/v1/inference", nil)
			setUpstreamAuthentication(req, test.protocol, test.key)
			if got := req.Header.Get("Authorization"); got != test.wantAuthorization {
				t.Fatalf("Authorization = %q, want %q", got, test.wantAuthorization)
			}
			if got := req.Header.Get("x-api-key"); got != test.wantAPIKey {
				t.Fatalf("x-api-key = %q, want %q", got, test.wantAPIKey)
			}
			if got := req.Header.Get("anthropic-version"); got != test.wantVersion {
				t.Fatalf("anthropic-version = %q, want %q", got, test.wantVersion)
			}
		})
	}
}

func TestInferenceUsesAnonymousZenBeforeCatalogRefresh(t *testing.T) {
	var authorization, apiKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		apiKey = r.Header.Get("x-api-key")
		writeJSON(w, http.StatusOK, map[string]any{"id": "response", "choices": []any{}})
	}))
	defer server.Close()

	cfg := gatewayTestConfig(server.URL)
	gateway := newGatewayForTest(t, cfg)
	body := bytes.NewBufferString(`{"model":"paid-model","messages":[]}`)
	recorder := httptest.NewRecorder()
	gateway.handleInference(ProtocolChat)(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("anonymous inference status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if authorization != "" || apiKey != "" {
		t.Fatalf("anonymous inference sent authentication headers: Authorization=%q x-api-key=%q", authorization, apiKey)
	}
	if len(gateway.cfg.ZenKeys) != 0 || gateway.zenNodes.Len() != 1 || gateway.zenNodes.nodes[0].key != "" {
		t.Fatalf("anonymous runtime node invariant failed: cfg keys=%#v nodes=%#v", gateway.cfg.ZenKeys, gateway.zenNodes.nodes)
	}
}

func TestAnonymousInferenceLetsUpstreamDecideForCatalogModel(t *testing.T) {
	type capturedRequest struct {
		model string
		err   error
	}
	captured := make(chan capturedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			captured <- capturedRequest{err: err}
			http.Error(w, "invalid test request", http.StatusBadRequest)
			return
		}
		model, _ := payload["model"].(string)
		captured <- capturedRequest{model: model}
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"message": "Missing API key."}})
	}))
	defer server.Close()

	gateway := newGatewayForTest(t, gatewayTestConfig(server.URL))
	gateway.catalog.Replace([]string{"gemini-3.6-flash"}, nil)
	recorder := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"model":"gemini-3.6-flash","messages":[]}`)
	gateway.handleInference(ProtocolChat)(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body))
	var request capturedRequest
	select {
	case request = <-captured:
	default:
		t.Fatal("catalog model was rejected before reaching the upstream")
	}
	if request.err != nil {
		t.Fatalf("decode upstream request: %v", request.err)
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s; want upstream 401", recorder.Code, recorder.Body.String())
	}
	if request.model != "gemini-3.6-flash" {
		t.Fatalf("upstream model = %q, want catalog model to be attempted", request.model)
	}
}

func TestAnonymousZenCatalogRoutesWithGoPoolAndPreference(t *testing.T) {
	cfg := gatewayTestConfig("https://zen.example.test")
	cfg.GoKeys = []string{"go-secret"}
	cfg.Prefer = TierGo
	gateway := newGatewayForTest(t, cfg)
	gateway.catalog.Replace([]string{"zen-only", "both"}, []string{"go-only", "both"})

	tests := []struct {
		model string
		want  Tier
	}{
		{model: "zen-only", want: TierZen},
		{model: "go-only", want: TierGo},
		{model: "both", want: TierGo},
	}
	for _, test := range tests {
		route, err := gateway.catalog.Route(test.model, gateway.zenNodes.Len() > 0, gateway.goNodes.Len() > 0)
		if err != nil {
			t.Fatalf("Route(%q) error = %v", test.model, err)
		}
		if route.Tier != test.want {
			t.Fatalf("Route(%q) tier = %q, want %q", test.model, route.Tier, test.want)
		}
	}
}

func TestKeyedZenInferenceStillSendsAuthorization(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		writeJSON(w, http.StatusOK, map[string]any{"id": "response", "choices": []any{}})
	}))
	defer server.Close()

	cfg := gatewayTestConfig(server.URL)
	cfg.ZenKeys = []string{"zen-secret"}
	gateway := newGatewayForTest(t, cfg)
	recorder := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"model":"paid-model","messages":[]}`)
	gateway.handleInference(ProtocolChat)(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("keyed inference status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if authorization != "Bearer zen-secret" {
		t.Fatalf("Authorization = %q, want keyed bearer token", authorization)
	}
}

func TestAnonymousZenHealthAndKeyStatus(t *testing.T) {
	cfg := gatewayTestConfig("https://zen.example.test")
	gateway := newGatewayForTest(t, cfg)
	gateway.catalog.Replace([]string{"paid-model"}, nil)

	recorder := httptest.NewRecorder()
	gateway.handleHealth(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("health status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var health healthResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &health); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if health.Keys.Zen != 0 || health.Keys.Go != 0 || health.Keys.Total != 0 {
		t.Fatalf("health counted anonymous node as a real key: %#v", health.Keys)
	}
	for _, issue := range health.Issues {
		if issue == "no_upstream_keys" {
			t.Fatalf("health reported no_upstream_keys with anonymous Zen node: %#v", health.Issues)
		}
	}

	manager := &RuntimeManager{}
	manager.current.Store(&gatewayRuntime{config: cfg, gateway: gateway})
	resources := manager.Resources()
	if len(resources.Keys) != 1 || resources.Keys[0].ID != "anonymous" || !resources.Keys[0].Anonymous || resources.Keys[0].Tier != string(TierZen) {
		t.Fatalf("anonymous KeyStatus = %#v", resources.Keys)
	}
	if len(resources.Proxies) != 1 || resources.Proxies[0].ZenKeys != 1 {
		t.Fatalf("anonymous proxy binding missing: %#v", resources.Proxies)
	}
}

func gatewayTestConfig(zenURL string) Config {
	cfg := defaultConfig()
	cfg.ServerKeys = []string{"local-secret"}
	cfg.Upstream.Zen = zenURL
	cfg.ZenKeys = nil
	cfg.GoKeys = nil
	cfg.Retry.MaxAttempts = 1
	cfg.WebUI.Enabled = false
	return cfg
}

func newGatewayForTest(t *testing.T, cfg Config) *Gateway {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	gateway, err := NewGateway(cfg, logger, nil)
	if err != nil {
		t.Fatalf("NewGateway() error = %v", err)
	}
	return gateway
}
