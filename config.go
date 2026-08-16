package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

var bootstrapEnvironmentVariables = []string{
	"OPENCODE2API_SERVER_KEYS",
	"OPENCODE2API_ZEN_KEYS",
	"OPENCODE2API_GO_KEYS",
	"OPENCODE2API_WEBUI_USERNAME",
	"OPENCODE2API_WEBUI_PASSWORD",
	"OPENCODE2API_WEBUI_ENABLED",
}

type Config struct {
	Listen      string            `json:"listen"`
	ServerKeys  []string          `json:"server_keys"`
	ZenKeys     []string          `json:"zen_keys"`
	GoKeys      []string          `json:"go_keys"`
	Proxies     []string          `json:"proxies"`
	ProxyFile   string            `json:"proxyfile"`
	Upstream    UpstreamConfig    `json:"upstream"`
	Fingerprint FingerprintConfig `json:"fingerprint"`
	Retry       RetryConfig       `json:"retry"`
	Models      ModelsConfig      `json:"models"`
	Performance PerformanceConfig `json:"performance"`
	Logging     LoggingConfig     `json:"logging"`
	WebUI       WebUIConfig       `json:"webui"`
	Prefer      Tier              `json:"prefer"`

	effectiveProxies []string
}

type FingerprintConfig struct {
	Versions []string `json:"versions"`
}

type UpstreamConfig struct {
	Zen string `json:"zen"`
	Go  string `json:"go"`
}

type RetryConfig struct {
	MaxAttempts    int `json:"max_attempts"`
	TimeoutSeconds int `json:"timeout_seconds"`
}

type ModelsConfig struct {
	RefreshSeconds int               `json:"refresh_seconds"`
	Protocols      map[string]string `json:"protocols"`
}

type LoggingConfig struct {
	Level    string `json:"level"`
	RingSize int    `json:"ring_size"`
}

type WebUIConfig struct {
	Enabled           bool   `json:"enabled"`
	Listen            string `json:"listen"`
	Username          string `json:"username"`
	Password          string `json:"password,omitempty"`
	PasswordHash      string `json:"password_hash,omitempty"`
	SessionTTLMinutes int    `json:"session_ttl_minutes"`
}

type PerformanceConfig struct {
	MaxIdleConns           int `json:"max_idle_conns"`
	MaxIdleConnsPerHost    int `json:"max_idle_conns_per_host"`
	MaxConnsPerHost        int `json:"max_conns_per_host"`
	IdleConnTimeoutSeconds int `json:"idle_conn_timeout_seconds"`
	ConnectTimeoutSeconds  int `json:"connect_timeout_seconds"`
	FailureCooldownSeconds int `json:"failure_cooldown_seconds"`
}

func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	data, err = stripJSONComments(data)
	if err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg := defaultConfig()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := ensureJSONEOF(dec); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return NormalizeConfig(path, cfg)
}

func defaultConfig() Config {
	return Config{
		Listen:      "127.0.0.1:8080",
		Upstream:    UpstreamConfig{Zen: "https://opencode.ai/zen", Go: "https://opencode.ai/zen/go"},
		Fingerprint: FingerprintConfig{Versions: append([]string(nil), defaultOpenCodeVersions...)},
		Retry:       RetryConfig{MaxAttempts: 3, TimeoutSeconds: 300},
		Models:      ModelsConfig{RefreshSeconds: 300, Protocols: map[string]string{}},
		Performance: PerformanceConfig{MaxIdleConns: 2048, MaxIdleConnsPerHost: 256, MaxConnsPerHost: 0, IdleConnTimeoutSeconds: 120, ConnectTimeoutSeconds: 5, FailureCooldownSeconds: 15},
		Logging:     LoggingConfig{Level: "info", RingSize: 2000},
		WebUI:       WebUIConfig{Listen: "0.0.0.0:8081", SessionTTLMinutes: 720},
		Prefer:      TierGo,
	}
}

// LoadConfigOrBootstrap gives an existing config file absolute priority. When
// the file does not exist, the presence of any OPENCODE2API_* bootstrap
// variable creates a complete initial config and persists it atomically.
func LoadConfigOrBootstrap(path string) (Config, error) {
	_, statErr := os.Lstat(path)
	if statErr == nil {
		// Once the config itself exists it is authoritative. In particular, a
		// missing proxyfile or another dependency must never be mistaken for a
		// missing config and trigger a destructive bootstrap rewrite.
		return LoadConfig(path)
	}
	if !errors.Is(statErr, os.ErrNotExist) {
		return Config{}, fmt.Errorf("stat %s: %w", path, statErr)
	}
	if !bootstrapEnvironmentPresent() {
		return Config{}, fmt.Errorf("read %s: %w", path, statErr)
	}

	cfg := defaultConfig()
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

	cfg, err = NormalizeConfig(path, cfg)
	if err != nil {
		return Config{}, fmt.Errorf("bootstrap configuration: %w", err)
	}
	if cfg.WebUI.Password != "" {
		hash, hashErr := hashPassword(cfg.WebUI.Password)
		if hashErr != nil {
			return Config{}, fmt.Errorf("hash bootstrap WebUI password: %w", hashErr)
		}
		cfg.WebUI.PasswordHash = hash
		cfg.WebUI.Password = ""
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return Config{}, fmt.Errorf("create config directory: %w", err)
	}
	if err := SaveConfigAtomic(path, cfg); err != nil {
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

// NormalizeConfig resolves external inputs and validates a Config supplied by
// either the JSON file or the authenticated management API.
func NormalizeConfig(path string, cfg Config) (Config, error) {
	trimList(&cfg.ServerKeys)
	trimList(&cfg.ZenKeys)
	trimList(&cfg.GoKeys)
	trimList(&cfg.Fingerprint.Versions)
	cfg.Fingerprint.Versions = uniqueStrings(cfg.Fingerprint.Versions)
	cfg.ProxyFile = strings.TrimSpace(cfg.ProxyFile)
	if err := resolveProxyFiles(path, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.Prefer != TierZen && cfg.Prefer != TierGo {
		return Config{}, errors.New("prefer must be \"zen\" or \"go\"")
	}
	if cfg.Listen == "" {
		return Config{}, errors.New("listen must not be empty")
	}
	if len(cfg.ServerKeys) == 0 {
		return Config{}, errors.New("server_keys must contain at least one local key")
	}
	if len(cfg.Fingerprint.Versions) == 0 {
		return Config{}, errors.New("fingerprint.versions must contain at least one version")
	}
	for _, value := range cfg.Fingerprint.Versions {
		if !safeSemverPattern.MatchString(value) {
			return Config{}, fmt.Errorf("fingerprint.versions contains invalid version %q", value)
		}
	}
	if cfg.Retry.MaxAttempts < 1 {
		return Config{}, errors.New("retry.max_attempts must be at least 1")
	}
	if cfg.Retry.TimeoutSeconds < 1 {
		return Config{}, errors.New("retry.timeout_seconds must be at least 1")
	}
	if cfg.Models.RefreshSeconds < 1 {
		return Config{}, errors.New("models.refresh_seconds must be at least 1")
	}
	if cfg.Performance.MaxIdleConns < 1 || cfg.Performance.MaxIdleConnsPerHost < 1 || cfg.Performance.MaxConnsPerHost < 0 || cfg.Performance.IdleConnTimeoutSeconds < 1 || cfg.Performance.ConnectTimeoutSeconds < 1 || cfg.Performance.FailureCooldownSeconds < 1 {
		return Config{}, errors.New("performance values must be positive (max_conns_per_host may be zero for unlimited)")
	}
	if cfg.Logging.Level != "debug" && cfg.Logging.Level != "info" && cfg.Logging.Level != "warn" && cfg.Logging.Level != "error" {
		return Config{}, errors.New("logging.level must be debug, info, warn, or error")
	}
	if cfg.Logging.RingSize < 100 || cfg.Logging.RingSize > 50000 {
		return Config{}, errors.New("logging.ring_size must be between 100 and 50000")
	}
	if cfg.WebUI.Password != "" && len(cfg.WebUI.Password) < 10 {
		return Config{}, errors.New("webui.password must contain at least 10 characters")
	}
	if cfg.WebUI.Enabled {
		cfg.WebUI.Listen = strings.TrimSpace(cfg.WebUI.Listen)
		cfg.WebUI.Username = strings.TrimSpace(cfg.WebUI.Username)
		if cfg.WebUI.Listen == "" {
			return Config{}, errors.New("webui.listen must not be empty when webui is enabled")
		}
		if cfg.WebUI.Username == "" {
			return Config{}, errors.New("webui.username must not be empty when webui is enabled")
		}
		if cfg.WebUI.Password == "" && cfg.WebUI.PasswordHash == "" {
			return Config{}, errors.New("webui.password is required for first-time setup")
		}
		if cfg.WebUI.SessionTTLMinutes < 5 || cfg.WebUI.SessionTTLMinutes > 10080 {
			return Config{}, errors.New("webui.session_ttl_minutes must be between 5 and 10080")
		}
	}
	for _, raw := range cfg.RuntimeProxies() {
		if raw == "direct" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return Config{}, fmt.Errorf("invalid proxy URL %q", redactURL(raw))
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https", "socks5", "socks5h":
		default:
			return Config{}, fmt.Errorf("unsupported proxy scheme %q", u.Scheme)
		}
	}
	for model, protocol := range cfg.Models.Protocols {
		if model == "" || !validProtocol(Protocol(protocol)) {
			return Config{}, fmt.Errorf("models.protocols contains invalid mapping %q: %q", model, protocol)
		}
	}
	return cfg, nil
}

var safeSemverPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// RuntimeProxies returns the resolved proxy list, including proxyfile entries.
// Config.Proxies intentionally remains the list stored in config.json so a
// save never duplicates values loaded from proxyfile.
func (cfg Config) RuntimeProxies() []string {
	if len(cfg.effectiveProxies) > 0 {
		return cfg.effectiveProxies
	}
	if len(cfg.Proxies) > 0 {
		return cfg.Proxies
	}
	return []string{"direct"}
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

// stripJSONComments removes // and /* */ comments without changing newlines,
// so syntax errors still point at the correct line in config.json. Comment
// markers inside JSON strings (for example, https:// URLs) are preserved.
func stripJSONComments(data []byte) ([]byte, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	out := make([]byte, 0, len(data))
	inString := false
	escaped := false
	lineComment := false
	blockComment := false

	for i := 0; i < len(data); i++ {
		current := data[i]
		if lineComment {
			if current == '\n' || current == '\r' {
				lineComment = false
				out = append(out, current)
			} else {
				out = append(out, ' ')
			}
			continue
		}
		if blockComment {
			if current == '*' && i+1 < len(data) && data[i+1] == '/' {
				out = append(out, ' ', ' ')
				i++
				blockComment = false
			} else if current == '\n' || current == '\r' {
				out = append(out, current)
			} else {
				out = append(out, ' ')
			}
			continue
		}
		if inString {
			out = append(out, current)
			if escaped {
				escaped = false
			} else if current == '\\' {
				escaped = true
			} else if current == '"' {
				inString = false
			}
			continue
		}

		switch {
		case current == '"':
			inString = true
			out = append(out, current)
		case current == '/' && i+1 < len(data) && data[i+1] == '/':
			lineComment = true
			out = append(out, ' ', ' ')
			i++
		case current == '/' && i+1 < len(data) && data[i+1] == '*':
			blockComment = true
			out = append(out, ' ', ' ')
			i++
		default:
			out = append(out, current)
		}
	}
	if blockComment {
		return nil, errors.New("unterminated block comment")
	}
	return out, nil
}

func resolveProxyFiles(configPath string, cfg *Config) error {
	trimList(&cfg.Proxies)
	effective := append([]string(nil), cfg.Proxies...)
	if cfg.ProxyFile != "" {
		resolved := cfg.ProxyFile
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(filepath.Dir(configPath), resolved)
		}
		proxies, err := readProxyFile(resolved)
		if err != nil {
			return fmt.Errorf("load proxy file %s: %w", resolved, err)
		}
		effective = append(effective, proxies...)
	}

	effective = uniqueStrings(effective)
	if len(effective) == 0 {
		effective = []string{"direct"}
	}
	cfg.effectiveProxies = effective
	return nil
}

// SaveConfigAtomic writes normalized JSON and keeps the preceding file as
// config.json.bak. The temporary file is created beside the target so the
// final rename stays on the same filesystem.
func SaveConfigAtomic(path string, cfg Config) error {
	cfg.PasswordForSave()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err = temp.Write(data); err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write temporary config: %w", err)
	}
	if info, statErr := os.Stat(path); statErr == nil {
		_ = os.Chmod(tempPath, info.Mode().Perm())
	}

	backup := path + ".bak"
	if runtime.GOOS == "windows" {
		_ = os.Remove(backup)
		if _, statErr := os.Stat(path); statErr == nil {
			if err := os.Rename(path, backup); err != nil {
				return fmt.Errorf("backup config: %w", err)
			}
		}
		if err := os.Rename(tempPath, path); err != nil {
			_ = os.Rename(backup, path)
			return fmt.Errorf("replace config: %w", err)
		}
		return nil
	}
	if _, statErr := os.Stat(path); statErr == nil {
		if err := copyFile(path, backup); err != nil {
			return fmt.Errorf("backup config: %w", err)
		}
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

// PasswordForSave ensures resolved-only data is excluded. The method is kept
// separate to make accidental persistence of effective proxy values obvious.
func (cfg *Config) PasswordForSave() {
	cfg.effectiveProxies = nil
}

func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	mode := os.FileMode(0600)
	if info, statErr := in.Stat(); statErr == nil {
		mode = info.Mode().Perm()
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_ = out.Chmod(mode)
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func readProxyFile(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var proxies []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		value := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		value = strings.TrimSpace(stripProxyLineComment(value))
		if value != "" {
			proxies = append(proxies, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return proxies, nil
}

func stripProxyLineComment(line string) string {
	for i := 0; i < len(line); i++ {
		if i > 0 && line[i-1] != ' ' && line[i-1] != '\t' {
			continue
		}
		if line[i] == '#' || line[i] == ';' || (line[i] == '/' && i+1 < len(line) && line[i+1] == '/') {
			return line[:i]
		}
	}
	return line
}

func uniqueStrings(items []string) []string {
	out := items[:0]
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func trimList(items *[]string) {
	out := (*items)[:0]
	for _, item := range *items {
		if value := strings.TrimSpace(item); value != "" {
			out = append(out, value)
		}
	}
	*items = out
}

func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid>"
	}
	if u.User != nil {
		u.User = url.User("***")
	}
	return u.String()
}
