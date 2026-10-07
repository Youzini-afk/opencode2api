package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var bootstrapEnvironmentVariables = []string{"OPENCODE2API_SERVER_KEYS", "OPENCODE2API_ZEN_KEYS", "OPENCODE2API_GO_KEYS", "OPENCODE2API_WEBUI_USERNAME", "OPENCODE2API_WEBUI_PASSWORD", "OPENCODE2API_WEBUI_ENABLED"}

// LoadOrBootstrap gives an existing config file absolute priority. When
// the file does not exist, the presence of any OPENCODE2API_* bootstrap
// variable creates a complete initial config and persists it atomically.
func LoadOrBootstrap(path string) (Config, error) {
	_, statErr := os.Lstat(path)
	if statErr == nil {
		// Once the config itself exists it is authoritative. In particular, a
		// missing proxyfile or another dependency must never be mistaken for a
		// missing config and trigger a destructive bootstrap rewrite.
		return Load(path)
	}
	if !errors.Is(statErr, os.ErrNotExist) {
		return Config{}, fmt.Errorf("stat %s: %w", path, statErr)
	}
	if !bootstrapEnvironmentPresent() {
		return Config{}, fmt.Errorf("read %s: %w", path, statErr)
	}

	cfg := Defaults()
	var err error
	if cfg.ServerKeys, err = parseBootstrapList(os.Getenv("OPENCODE2API_SERVER_KEYS")); err != nil {
		return Config{}, fmt.Errorf("OPENCODE2API_SERVER_KEYS: %w", err)
	}
	if cfg.ZenKeys, err = parseBootstrapList(os.Getenv("OPENCODE2API_ZEN_KEYS")); err != nil {
		return Config{}, fmt.Errorf("OPENCODE2API_ZEN_KEYS: %w", err)
	}
	if cfg.GoKeys, err = parseBootstrapList(os.Getenv("OPENCODE2API_GO_KEYS")); err != nil {
		return Config{}, fmt.Errorf("OPENCODE2API_GO_KEYS: %w", err)
	}
	cfg.WebUI.Enabled = true
	if raw, exists := os.LookupEnv("OPENCODE2API_WEBUI_ENABLED"); exists {
		enabled, parseErr := strconv.ParseBool(strings.TrimSpace(raw))
		if parseErr != nil {
			return Config{}, fmt.Errorf("OPENCODE2API_WEBUI_ENABLED must be true or false: %w", parseErr)
		}
		cfg.WebUI.Enabled = enabled
	}
	cfg.WebUI.Username = "admin"
	if username, exists := os.LookupEnv("OPENCODE2API_WEBUI_USERNAME"); exists {
		cfg.WebUI.Username = username
	}
	cfg.WebUI.Password = os.Getenv("OPENCODE2API_WEBUI_PASSWORD")

	cfg, err = Normalize(path, cfg)
	if err != nil {
		return Config{}, fmt.Errorf("bootstrap configuration: %w", err)
	}
	if cfg.WebUI.Password != "" {
		hash, hashErr := HashPassword(cfg.WebUI.Password)
		if hashErr != nil {
			return Config{}, fmt.Errorf("hash bootstrap WebUI password: %w", hashErr)
		}
		cfg.WebUI.PasswordHash = hash
		cfg.WebUI.Password = ""
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return Config{}, fmt.Errorf("create config directory: %w", err)
	}
	if err := SaveAtomic(path, cfg); err != nil {
		return Config{}, fmt.Errorf("persist bootstrap configuration: %w", err)
	}
	return cfg, nil
}

func bootstrapEnvironmentPresent() bool {
	for _, name := range bootstrapEnvironmentVariables {
		if _, exists := os.LookupEnv(name); exists {
			return true
		}
	}
	return false
}

func parseBootstrapList(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "[") {
		var values []string
		if err := json.Unmarshal([]byte(trimmed), &values); err != nil {
			return nil, fmt.Errorf("must be a JSON string array or comma/newline-separated list: %w", err)
		}
		trimList(&values)
		return values, nil
	}
	values := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	trimList(&values)
	return values, nil
}
