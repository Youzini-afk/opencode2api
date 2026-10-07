package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	adminui "opencode2api/internal/admin"
	"opencode2api/internal/config"
	"opencode2api/internal/gateway"
	"opencode2api/internal/telemetry"
)

// Exercise the actual shared listener, management authentication, persistence
// and inference after reloading an existing fork config into the new packages.
func TestSinglePortManagementReloadAndInference(t *testing.T) {
	captured := make(chan http.Header, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"reply","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := config.Defaults()
	cfg.ServerKeys = []string{"test-local-key"}
	cfg.Listen = "127.0.0.1:8080"
	cfg.Upstream.Zen = upstream.URL
	cfg.Upstream.Go = upstream.URL
	cfg.WebUI.Enabled = true
	cfg.WebUI.Listen = cfg.Listen
	cfg.WebUI.Username = "admin"
	cfg.WebUI.Password = "test-management-password"
	cfg.Fingerprint.Versions = []string{"1.18.17"}
	if err := config.SaveAtomic(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadOrBootstrap(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	monitor := telemetry.NewMonitor()
	hub := telemetry.NewLogHub(cfg.Logging.RingSize)
	redactor := config.NewSecretRedactor()
	level := new(slog.LevelVar)
	manager, err := gateway.NewRuntimeManager(ctx, path, loaded, logger, monitor, hub, redactor, level)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Shutdown()
	admin := adminui.New(manager, monitor, hub, logger)
	services := configuredHTTPServices(loaded, manager.Handler(), admin.Handler())
	if len(services) != 1 {
		t.Fatal("API and WebUI did not share the listener")
	}
	server := httptest.NewServer(services[0].server.Handler)
	defer server.Close()
	response, err := http.Get(server.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatal("local key authentication bypass")
	}
	response, err = http.Post(server.URL+"/api/auth/login", "application/json", bytes.NewBufferString(`{"username":"admin","password":"test-management-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	err = json.NewDecoder(response.Body).Decode(&session)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || len(response.Cookies()) == 0 {
		t.Fatalf("management login failed: status=%d err=%v", response.StatusCode, err)
	}
	cookie := response.Cookies()[0]
	request, _ := http.NewRequest("GET", server.URL+"/api/config", nil)
	request.AddCookie(cookie)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var view adminui.ConfigView
	err = json.NewDecoder(response.Body).Decode(&view)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Fingerprint.Versions) != 1 || view.Fingerprint.Versions[0] != "1.18.17" {
		t.Fatal("legacy version pool lost")
	}
	keys := []adminui.SecretInput{{ID: view.ServerKeys[0].ID}}
	update := adminui.ConfigUpdate{Listen: view.Listen, ServerKeys: keys, Upstream: view.Upstream, Retry: view.Retry, Models: view.Models, Performance: view.Performance, Logging: view.Logging, Prefer: view.Prefer, WebUI: view.WebUI, Fingerprint: &config.FingerprintConfig{Versions: []string{"1.18.18"}}}
	encoded, _ := json.Marshal(update)
	request, _ = http.NewRequest("PUT", server.URL+"/api/config", bytes.NewReader(encoded))
	request.AddCookie(cookie)
	request.Header.Set("X-CSRF-Token", session.CSRF)
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("configuration PUT failed: %d %s", response.StatusCode, body)
	}
	if _, err := manager.Reload(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		request, _ = http.NewRequest("POST", server.URL+"/v1/chat/completions", bytes.NewBufferString(`{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`))
		request.Header.Set("Authorization", "Bearer test-local-key")
		request.Header.Set("x-session-id", "integration-conversation")
		response, err = http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, _ = io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("inference failed after reload: %d %s", response.StatusCode, body)
		}
	}
	first, second := <-captured, <-captured
	for _, name := range []string{"User-Agent", "x-opencode-session", "x-opencode-project"} {
		if first.Get(name) == "" || first.Get(name) != second.Get(name) {
			t.Fatalf("%s drifted after reload", name)
		}
	}
	if first.Get("User-Agent") != "opencode/1.18.18" || first.Get("x-opencode-request") == second.Get("x-opencode-request") || first.Get("Authorization") != "" {
		t.Fatalf("invalid keyless identity: %v %v", first, second)
	}
}
