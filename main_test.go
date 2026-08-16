package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSinglePortHandlerDispatchesByPath(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Test-Handler", "api")
		w.WriteHeader(http.StatusNoContent)
	})
	admin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Test-Handler", "admin")
		w.WriteHeader(http.StatusNoContent)
	})
	handler := singlePortHandler(api, admin)

	tests := []struct {
		path string
		want string
	}{
		{path: "/v1/models", want: "api"},
		{path: "/v1/unknown", want: "api"},
		{path: "/healthz", want: "api"},
		{path: "/api/auth/session", want: "admin"},
		{path: "/", want: "admin"},
		{path: "/missing-static-route", want: "admin"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
			if got := recorder.Header().Get("X-Test-Handler"); got != test.want {
				t.Fatalf("handler = %q, want %q", got, test.want)
			}
		})
	}
}

func TestConfiguredHTTPServicesSingleAndSeparateListeners(t *testing.T) {
	api := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	admin := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	cfg := Config{Listen: "0.0.0.0:8080", WebUI: WebUIConfig{Enabled: true, Listen: "0.0.0.0:8080"}}

	services := configuredHTTPServices(cfg, api, admin)
	if len(services) != 1 || services[0].component != "api+webui" {
		t.Fatalf("single-listener services = %#v", services)
	}

	cfg.WebUI.Listen = "0.0.0.0:8081"
	services = configuredHTTPServices(cfg, api, admin)
	if len(services) != 2 || services[0].component != "api" || services[1].component != "webui" {
		t.Fatalf("separate-listener services = %#v", services)
	}
	if services[0].server.Addr != cfg.Listen || services[1].server.Addr != cfg.WebUI.Listen {
		t.Fatalf("separate-listener addresses = %q, %q", services[0].server.Addr, services[1].server.Addr)
	}
}
