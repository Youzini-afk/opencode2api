package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseBootstrapList(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "JSON array", raw: `[" first ", "", "second"]`, want: []string{"first", "second"}},
		{name: "comma and newline", raw: " first,\n second\r\n, ,third ", want: []string{"first", "second", "third"}},
		{name: "empty", raw: " \n, ", want: []string{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseBootstrapList(test.raw)
			if err != nil {
				t.Fatalf("parseBootstrapList() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parseBootstrapList() = %#v, want %#v", got, test.want)
			}
		})
	}
	if _, err := parseBootstrapList(`["unterminated"`); err == nil {
		t.Fatal("parseBootstrapList() accepted malformed JSON array")
	}
}

func TestLoadConfigOrBootstrapCreatesFirstConfig(t *testing.T) {
	clearBootstrapEnvironment(t)
	t.Setenv("OPENCODE2API_SERVER_KEYS", `[" local-one ", "", "local-two"]`)
	t.Setenv("OPENCODE2API_ZEN_KEYS", " zen-one,\nzen-two ")
	t.Setenv("OPENCODE2API_WEBUI_PASSWORD", "a-strong-bootstrap-password")

	path := filepath.Join(t.TempDir(), "nested", "config.json")
	cfg, err := LoadConfigOrBootstrap(path)
	if err != nil {
		t.Fatalf("LoadConfigOrBootstrap() error = %v", err)
	}
	if !reflect.DeepEqual(cfg.ServerKeys, []string{"local-one", "local-two"}) {
		t.Fatalf("ServerKeys = %#v", cfg.ServerKeys)
	}
	if !reflect.DeepEqual(cfg.ZenKeys, []string{"zen-one", "zen-two"}) {
		t.Fatalf("ZenKeys = %#v", cfg.ZenKeys)
	}
	if !cfg.WebUI.Enabled || cfg.WebUI.Username != "admin" || cfg.WebUI.Password != "" || cfg.WebUI.PasswordHash == "" {
		t.Fatalf("unexpected WebUI bootstrap config: %#v", cfg.WebUI)
	}
	if !verifyPassword(cfg.WebUI.PasswordHash, "a-strong-bootstrap-password") {
		t.Fatal("persisted bootstrap password hash does not verify")
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("persisted config cannot be loaded: %v", err)
	}
	if !reflect.DeepEqual(loaded.ServerKeys, cfg.ServerKeys) || loaded.WebUI.Password != "" || loaded.WebUI.PasswordHash == "" {
		t.Fatalf("persisted config differs from bootstrap result: %#v", loaded)
	}
	if _, err := os.Stat(path + ".bak"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bootstrap left an unexpected backup file: %v", err)
	}
}

func TestLoadConfigOrBootstrapExistingConfigWins(t *testing.T) {
	clearBootstrapEnvironment(t)
	path := filepath.Join(t.TempDir(), "config.json")
	existing := validTestConfig()
	if err := SaveConfigAtomic(path, existing); err != nil {
		t.Fatalf("SaveConfigAtomic() error = %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE2API_SERVER_KEYS", "replacement-key")
	t.Setenv("OPENCODE2API_ZEN_KEYS", "replacement-upstream-key")
	t.Setenv("OPENCODE2API_WEBUI_PASSWORD", "replacement-password")

	cfg, err := LoadConfigOrBootstrap(path)
	if err != nil {
		t.Fatalf("LoadConfigOrBootstrap() error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("existing config was rewritten by bootstrap environment")
	}
	if !reflect.DeepEqual(cfg.ServerKeys, existing.ServerKeys) {
		t.Fatalf("ServerKeys = %#v, want existing %#v", cfg.ServerKeys, existing.ServerKeys)
	}
}

func TestLoadConfigOrBootstrapDoesNotReplaceConfigWithMissingProxyFile(t *testing.T) {
	clearBootstrapEnvironment(t)
	path := filepath.Join(t.TempDir(), "config.json")
	existing := validTestConfig()
	existing.ProxyFile = "temporarily-missing-proxies.txt"
	if err := SaveConfigAtomic(path, existing); err != nil {
		t.Fatalf("SaveConfigAtomic() error = %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE2API_SERVER_KEYS", "replacement-key")
	t.Setenv("OPENCODE2API_ZEN_KEYS", "replacement-upstream-key")
	t.Setenv("OPENCODE2API_WEBUI_PASSWORD", "replacement-password")

	_, err = LoadConfigOrBootstrap(path)
	if err == nil || !strings.Contains(err.Error(), "temporarily-missing-proxies.txt") {
		t.Fatalf("LoadConfigOrBootstrap() error = %v, want missing proxyfile error", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("existing config was replaced after a dependency load error")
	}
}

func TestLoadConfigOrBootstrapValidation(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "server key required", env: map[string]string{"OPENCODE2API_ZEN_KEYS": "zen"}, want: "server_keys must contain at least one local key"},
		{name: "upstream key required", env: map[string]string{"OPENCODE2API_SERVER_KEYS": "local"}, want: "zen_keys or go_keys must contain at least one upstream key"},
		{name: "WebUI password required", env: map[string]string{"OPENCODE2API_SERVER_KEYS": "local", "OPENCODE2API_GO_KEYS": "go"}, want: "webui.password is required for first-time setup"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearBootstrapEnvironment(t)
			for name, value := range test.env {
				t.Setenv(name, value)
			}
			_, err := LoadConfigOrBootstrap(filepath.Join(t.TempDir(), "config.json"))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadConfigOrBootstrap() error = %v, want containing %q", err, test.want)
			}
		})
	}

	t.Run("disabled WebUI does not require password", func(t *testing.T) {
		clearBootstrapEnvironment(t)
		t.Setenv("OPENCODE2API_SERVER_KEYS", "local")
		t.Setenv("OPENCODE2API_GO_KEYS", "go")
		t.Setenv("OPENCODE2API_WEBUI_ENABLED", "false")
		cfg, err := LoadConfigOrBootstrap(filepath.Join(t.TempDir(), "config.json"))
		if err != nil {
			t.Fatalf("LoadConfigOrBootstrap() error = %v", err)
		}
		if cfg.WebUI.Enabled {
			t.Fatal("WebUI.Enabled = true, want false")
		}
	})
}

func validTestConfig() Config {
	cfg := defaultConfig()
	cfg.ServerKeys = []string{"existing-local-key"}
	cfg.GoKeys = []string{"existing-upstream-key"}
	return cfg
}

func clearBootstrapEnvironment(t *testing.T) {
	t.Helper()
	type previousValue struct {
		value  string
		exists bool
	}
	previous := make(map[string]previousValue, len(bootstrapEnvironmentVariables))
	for _, name := range bootstrapEnvironmentVariables {
		value, exists := os.LookupEnv(name)
		previous[name] = previousValue{value: value, exists: exists}
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for name, value := range previous {
			if value.exists {
				_ = os.Setenv(name, value.value)
			} else {
				_ = os.Unsetenv(name)
			}
		}
	})
}
