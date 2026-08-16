package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "config.json", "path to config.json")
	listen := flag.String("listen", "", "override the configured API listen address")
	webListen := flag.String("web-listen", "", "override the configured WebUI listen address")
	flag.Parse()

	cfg, err := LoadConfigOrBootstrap(*configPath)
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	if *webListen != "" {
		cfg.WebUI.Listen = *webListen
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	level := new(slog.LevelVar)
	setLogLevel(level, cfg.Logging.Level)
	hub := NewLogHub(cfg.Logging.RingSize)
	redactor := NewSecretRedactor()
	redactor.Replace(cfg)
	logger := NewStructuredLogger(level, hub, redactor)
	monitor := NewMonitor()
	manager, err := NewRuntimeManager(ctx, *configPath, cfg, logger, monitor, hub, redactor, level)
	if err != nil {
		logger.Error("failed to initialize runtime", "component", "runtime", "event", "runtime_initialization_failed", "error", err)
		os.Exit(1)
	}
	defer manager.Shutdown()

	var adminHandler http.Handler
	if cfg.WebUI.Enabled {
		admin := NewAdminServer(manager, monitor, hub, logger)
		adminHandler = admin.Handler()
	}
	services := configuredHTTPServices(cfg, manager.Handler(), adminHandler)
	for _, service := range services {
		go serveHTTP(cancel, logger, service.server, service.component)
	}

	<-ctx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	for _, service := range services {
		if err := service.server.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "component", "server", "event", "shutdown_failed", "address", service.server.Addr, "error", err)
		}
	}
}

type httpService struct {
	server    *http.Server
	component string
}

func configuredHTTPServices(cfg Config, apiHandler, adminHandler http.Handler) []httpService {
	if cfg.WebUI.Enabled && cfg.Listen == cfg.WebUI.Listen {
		return []httpService{{server: newHTTPServer(cfg.Listen, singlePortHandler(apiHandler, adminHandler)), component: "api+webui"}}
	}
	services := []httpService{{server: newHTTPServer(cfg.Listen, apiHandler), component: "api"}}
	if cfg.WebUI.Enabled {
		services = append(services, httpService{server: newHTTPServer(cfg.WebUI.Listen, adminHandler), component: "webui"})
	}
	return services
}

func newHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 120 * time.Second}
}

func singlePortHandler(apiHandler, adminHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/v1/") {
			apiHandler.ServeHTTP(w, r)
			return
		}
		adminHandler.ServeHTTP(w, r)
	})
}

func serveHTTP(cancel context.CancelFunc, logger *slog.Logger, server *http.Server, component string) {
	logger.Info("server listening", "component", component, "event", "server_started", "address", server.Addr, "version", version)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server stopped unexpectedly", "component", component, "event", "server_failed", "address", server.Addr, "error", err)
		cancel()
	}
}
