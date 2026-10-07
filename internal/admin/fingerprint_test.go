package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"opencode2api/internal/config"
	"opencode2api/internal/gateway"
	"opencode2api/internal/telemetry"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAdminConfigFingerprintPUTRoundTrip(t *testing.T) {
	cfg := config.Defaults()
	cfg.ServerKeys = []string{"local"}
	cfg.WebUI.Enabled = false
	path := filepath.Join(t.TempDir(), "config.json")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	monitor := telemetry.NewMonitor()
	hub := telemetry.NewLogHub(cfg.Logging.RingSize)
	redactor := config.NewSecretRedactor()
	level := &slog.LevelVar{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	manager, err := gateway.NewRuntimeManager(ctx, path, cfg, logger, monitor, hub, redactor, level)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Shutdown)
	admin := New(manager, monitor, hub, logger)
	view := admin.configView()
	if !reflect.DeepEqual(view.Fingerprint.Versions, cfg.Fingerprint.Versions) {
		t.Fatalf("ConfigView fingerprint = %#v, want %#v", view.Fingerprint.Versions, cfg.Fingerprint.Versions)
	}

	secretInputs := func(values []SecretView) []SecretInput {
		result := make([]SecretInput, 0, len(values))
		for _, value := range values {
			result = append(result, SecretInput{ID: value.ID})
		}
		return result
	}
	update := ConfigUpdate{
		Listen:      view.Listen,
		ServerKeys:  secretInputs(view.ServerKeys),
		ZenKeys:     secretInputs(view.ZenKeys),
		GoKeys:      secretInputs(view.GoKeys),
		Anonymous:   true,
		Proxies:     secretInputs(view.Proxies),
		ProxyFile:   view.ProxyFile,
		Upstream:    view.Upstream,
		Fingerprint: &config.FingerprintConfig{Versions: []string{" 2.3.4 ", "1.18.18", "2.3.4"}},
		Retry:       view.Retry,
		Models:      view.Models,
		Performance: view.Performance,
		Logging:     view.Logging,
		Prefer:      view.Prefer,
		WebUI:       view.WebUI,
	}
	body, err := json.Marshal(update)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body))
	admin.handlePutConfig(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	want := []string{"2.3.4", "1.18.18"}
	var response struct {
		Config ConfigView `json:"config"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode PUT response: %v", err)
	}
	if !reflect.DeepEqual(response.Config.Fingerprint.Versions, want) {
		t.Fatalf("PUT response fingerprint = %#v, want %#v", response.Config.Fingerprint.Versions, want)
	}
	persisted, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load() after PUT error = %v", err)
	}
	if !reflect.DeepEqual(persisted.Fingerprint.Versions, want) {
		t.Fatalf("persisted fingerprint = %#v, want %#v", persisted.Fingerprint.Versions, want)
	}
	if !persisted.Anonymous || !response.Config.Anonymous {
		t.Fatalf("anonymous setting did not round-trip: persisted=%v response=%v", persisted.Anonymous, response.Config.Anonymous)
	}
}
