package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"opencode2api/internal/config"
	"opencode2api/internal/httpx"
	"opencode2api/internal/identity"
	"opencode2api/internal/models"
	wire "opencode2api/internal/protocol"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFetchModelsAuthenticationAndCompleteCatalog(t *testing.T) {
	wantModels := []string{"paid-model", "mimo-v2.5-free", "gemini-2.5-pro"}
	identity := identity.Internal("models-test", "req_models_test", config.Defaults().Fingerprint.Versions)
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
			var headers http.Header
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				authorization = r.Header.Get("Authorization")
				headers = r.Header.Clone()
				httpx.WriteJSON(w, http.StatusOK, map[string]any{"object": "list", "data": []map[string]string{
					{"id": wantModels[0]}, {"id": wantModels[1]}, {"id": wantModels[2]},
				}})
			}))
			defer server.Close()

			models, status, err := models.FetchModels(context.Background(), server.Client(), server.URL, test.key, identity)
			if err != nil {
				t.Fatalf("models.FetchModels() error = %v", err)
			}
			if status != http.StatusOK {
				t.Fatalf("models.FetchModels() status = %d, want %d", status, http.StatusOK)
			}
			if authorization != test.wantAuthorization {
				t.Fatalf("Authorization = %q, want %q", authorization, test.wantAuthorization)
			}
			assertOpenCodeHeaders(t, headers, identity)
			for _, name := range openCodeIdentityHeaderNames {
				if test.key != "" && strings.Contains(headers.Get(name), test.key) {
					t.Fatalf("%s leaked the upstream key", name)
				}
			}
			if !reflect.DeepEqual(models, wantModels) {
				t.Fatalf("models.FetchModels() = %#v, want complete upstream catalog %#v", models, wantModels)
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
		protocol          wire.Protocol
		key               string
		wantAuthorization string
		wantAPIKey        string
		wantVersion       string
	}{
		{name: "anonymous chat", protocol: wire.Chat},
		{name: "keyed responses", protocol: wire.Responses, key: "zen-secret", wantAuthorization: "Bearer zen-secret"},
		{name: "anonymous anthropic", protocol: wire.Anthropic, wantVersion: "2023-06-01"},
		{name: "keyed anthropic", protocol: wire.Anthropic, key: "zen-secret", wantAPIKey: "zen-secret", wantVersion: "2023-06-01"},
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
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"id": "response", "choices": []any{}})
	}))
	defer server.Close()

	cfg := gatewayTestConfig(server.URL)
	gateway := newGatewayForTest(t, cfg)
	body := bytes.NewBufferString(`{"model":"paid-model","messages":[]}`)
	recorder := httptest.NewRecorder()
	gateway.handleInference(wire.Chat)(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("anonymous inference status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if authorization != "" || apiKey != "" {
		t.Fatalf("anonymous inference sent authentication headers: Authorization=%q x-api-key=%q", authorization, apiKey)
	}
	if len(gateway.cfg.ZenKeys) != 0 || gateway.zenNodes.Len() != 0 || gateway.anonymous.Len() != 1 {
		t.Fatalf("anonymous runtime node invariant failed: cfg keys=%#v keyed nodes=%#v anonymous=%d", gateway.cfg.ZenKeys, gateway.zenNodes.nodes, gateway.anonymous.Len())
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
		httpx.WriteJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"message": "Missing API key."}})
	}))
	defer server.Close()

	gateway := newGatewayForTest(t, gatewayTestConfig(server.URL))
	gateway.catalog.Replace([]string{"gemini-3.6-flash"}, nil)
	recorder := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"model":"gemini-3.6-flash","messages":[]}`)
	gateway.handleInference(wire.Chat)(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body))
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
	cfg.Prefer = config.TierGo
	gateway := newGatewayForTest(t, cfg)
	gateway.catalog.Replace([]string{"zen-only", "both"}, []string{"go-only", "both"})

	tests := []struct {
		model string
		want  config.Tier
	}{
		{model: "zen-only", want: config.TierZen},
		{model: "go-only", want: config.TierGo},
		{model: "both", want: config.TierGo},
	}
	for _, test := range tests {
		route, err := gateway.catalog.Route(test.model, gateway.zenNodes.Len() > 0, gateway.goNodes.Len() > 0, gateway.anonymous.Len() > 0)
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
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"id": "response", "choices": []any{}})
	}))
	defer server.Close()

	cfg := gatewayTestConfig(server.URL)
	cfg.ZenKeys = []string{"zen-secret"}
	gateway := newGatewayForTest(t, cfg)
	recorder := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"model":"paid-model","messages":[]}`)
	gateway.handleInference(wire.Chat)(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body))
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
	if len(resources.Keys) != 1 || resources.Keys[0].ID != "anonymous" || !resources.Keys[0].Anonymous || resources.Keys[0].Tier != string(config.TierZen) {
		t.Fatalf("anonymous KeyStatus = %#v", resources.Keys)
	}
	if len(resources.Proxies) != 1 || resources.Proxies[0].ZenKeys != 1 {
		t.Fatalf("anonymous proxy binding missing: %#v", resources.Proxies)
	}
}

func TestInferenceIdentityIsStableAcrossRetryAndSession(t *testing.T) {
	var captured []http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = append(captured, r.Header.Clone())
		if len(captured) == 1 {
			httpx.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]string{"message": "retry"}})
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"id": "response", "choices": []any{}})
	}))
	defer server.Close()

	cfg := gatewayTestConfig(server.URL)
	cfg.Retry.MaxAttempts = 2
	gateway := newGatewayForTest(t, cfg)
	request := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"paid-model","messages":[]}`))
		r.Header.Set("x-session-id", "stable-retry-session")
		return r
	}
	firstRecorder := httptest.NewRecorder()
	gateway.handleInference(wire.Chat)(firstRecorder, request())
	if firstRecorder.Code != http.StatusOK || len(captured) != 2 {
		t.Fatalf("first inference status = %d, attempts = %d, body = %s", firstRecorder.Code, len(captured), firstRecorder.Body.String())
	}
	assertOpenCodeIdentityHeadersEqual(t, captured[0], captured[1])
	if got := captured[0].Get("User-Agent"); !strings.HasPrefix(got, "opencode/") || strings.ContainsAny(got, " ()\t\r\n") {
		t.Fatalf("retry User-Agent = %q", got)
	}
	if got := captured[0].Get("x-opencode-project"); len(got) != 40 {
		t.Fatalf("retry project ID = %q, want lowercase SHA-1", got)
	}

	secondRecorder := httptest.NewRecorder()
	gateway.handleInference(wire.Chat)(secondRecorder, request())
	if secondRecorder.Code != http.StatusOK || len(captured) != 3 {
		t.Fatalf("second inference status = %d, total attempts = %d", secondRecorder.Code, len(captured))
	}
	for _, name := range []string{"User-Agent", "x-opencode-client", "x-opencode-session", "x-opencode-project"} {
		if captured[2].Get(name) != captured[0].Get(name) {
			t.Fatalf("same session changed %s: %q != %q", name, captured[2].Get(name), captured[0].Get(name))
		}
	}
	if firstRecorder.Header().Get("x-request-id") == "" || firstRecorder.Header().Get("x-request-id") == secondRecorder.Header().Get("x-request-id") {
		t.Fatal("new inference reused the local request ID")
	}
	for _, headers := range captured {
		if headers.Get("x-opencode-request") != "" {
			t.Fatal("legacy request header reached upstream")
		}
	}
}

func TestModelRefreshRetryUsesCompleteStableAnonymousIdentity(t *testing.T) {
	var captured []http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = append(captured, r.Header.Clone())
		if len(captured) == 1 {
			http.Error(w, "retry", http.StatusInternalServerError)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": []map[string]string{{"id": "model-a"}}})
	}))
	defer server.Close()

	cfg := gatewayTestConfig(server.URL)
	cfg.Retry.MaxAttempts = 2
	gateway := newGatewayForTest(t, cfg)
	models := gateway.refreshZen(context.Background(), "req_refresh_round")
	if !reflect.DeepEqual(models, []string{"model-a"}) || len(captured) != 2 {
		t.Fatalf("refresh models = %#v, attempts = %d", models, len(captured))
	}
	assertOpenCodeIdentityHeadersEqual(t, captured[0], captured[1])
	if got := captured[0].Get("x-opencode-request"); got != "" {
		t.Fatalf("local refresh trace leaked to the wire: %q", got)
	}
	if got := captured[0].Get("Authorization"); got != "" {
		t.Fatalf("anonymous refresh sent Authorization = %q", got)
	}
	for _, name := range openCodeIdentityHeaderNames {
		if captured[0].Get(name) == "" {
			t.Fatalf("anonymous refresh omitted %s", name)
		}
	}
}

func TestParallelModelRefreshUsesDistinctTierSessions(t *testing.T) {
	type capturedRequest struct {
		tier      config.Tier
		requestID string
	}
	captured := make(chan capturedRequest, 2)
	server := func(tier config.Tier) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			captured <- capturedRequest{tier: tier, requestID: r.Header.Get("x-opencode-session-id")}
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": []map[string]string{{"id": string(tier) + "-model"}}})
		}))
	}
	zenServer := server(config.TierZen)
	defer zenServer.Close()
	goServer := server(config.TierGo)
	defer goServer.Close()

	cfg := gatewayTestConfig(zenServer.URL)
	cfg.Upstream.Go = goServer.URL
	cfg.GoKeys = []string{"go-key"}
	cfg.Models.RefreshSeconds = 3600
	gateway := newGatewayForTest(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gateway.StartModelRefresh(ctx)

	requestIDs := make(map[config.Tier]string, 2)
	for len(requestIDs) < 2 {
		select {
		case request := <-captured:
			requestIDs[request.tier] = request.requestID
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for parallel model refreshes: %#v", requestIDs)
		}
	}
	cancel()
	if requestIDs[config.TierZen] == "" || requestIDs[config.TierGo] == "" {
		t.Fatalf("parallel refresh omitted session ID: %#v", requestIDs)
	}
	if requestIDs[config.TierZen] == requestIDs[config.TierGo] {
		t.Fatalf("independent Zen and Go refreshes shared session ID %q", requestIDs[config.TierZen])
	}
}

func TestModelRefreshIdentityDoesNotExposeKey(t *testing.T) {
	const key = "refresh-key-must-not-leak"
	var captured http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Clone()
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": []map[string]string{{"id": "model-a"}}})
	}))
	defer server.Close()
	cfg := gatewayTestConfig(server.URL)
	cfg.ZenKeys = []string{key}
	gateway := newGatewayForTest(t, cfg)
	gateway.refreshTier(context.Background(), config.TierZen, server.URL, gateway.zenNodes, "req_keyed_refresh")
	if got := captured.Get("Authorization"); got != "Bearer "+key {
		t.Fatalf("keyed refresh Authorization = %q", got)
	}
	for _, name := range openCodeIdentityHeaderNames {
		if strings.Contains(captured.Get(name), key) {
			t.Fatalf("%s leaked the upstream key", name)
		}
	}
}

func TestProxyHealthCheckUsesLegalDefaultUserAgent(t *testing.T) {
	var userAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userAgent = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	pool, err := newTransportPool([]string{"direct"}, config.Defaults().Performance, time.Second)
	if err != nil {
		t.Fatalf("newTransportPool() error = %v", err)
	}
	proxy := pool.items[0]
	proxy.checking.Store(true)
	result := pool.checkClaimedProxy(context.Background(), proxy, server.URL, time.Second)
	if result.err != nil {
		t.Fatalf("health check error = %v", result.err)
	}
	if userAgent != httpx.UserAgent() || !strings.HasPrefix(userAgent, "opencode/latest/") || !strings.HasSuffix(userAgent, "/cli") {
		t.Fatalf("health check User-Agent = %q", userAgent)
	}
}

var openCodeIdentityHeaderNames = []string{
	"User-Agent",
	"x-opencode-client",
	"x-opencode-session",
	"x-opencode-session-id",
	"x-opencode-project",
}

func assertOpenCodeHeaders(t *testing.T, headers http.Header, want identity.RequestIDs) {
	t.Helper()
	wantValues := map[string]string{
		"User-Agent":            want.UserAgent,
		"x-opencode-client":     "cli",
		"x-opencode-session":    want.Session,
		"x-opencode-session-id": want.Session,
		"x-opencode-project":    want.Project,
	}
	for name, value := range wantValues {
		if got := headers.Get(name); got != value {
			t.Fatalf("%s = %q, want %q", name, got, value)
		}
	}
}

func assertOpenCodeIdentityHeadersEqual(t *testing.T, first, second http.Header) {
	t.Helper()
	for _, name := range openCodeIdentityHeaderNames {
		if first.Get(name) != second.Get(name) {
			t.Fatalf("retry changed %s: %q != %q", name, first.Get(name), second.Get(name))
		}
	}
}

func gatewayTestConfig(zenURL string) config.Config {
	cfg := config.Defaults()
	cfg.ServerKeys = []string{"local-secret"}
	cfg.Upstream.Zen = zenURL
	cfg.ZenKeys = nil
	cfg.GoKeys = nil
	cfg.Retry.MaxAttempts = 1
	cfg.WebUI.Enabled = false
	return cfg
}

func newGatewayForTest(t *testing.T, cfg config.Config) *Gateway {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	gateway, err := New(cfg, logger, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return gateway
}
