package config

import (
	"net/url"
	"strings"
)

// OpenCodeEndpoint identifies the official endpoints that require the current
// CLI wire shape. Custom relays remain transparent unless agent_shaping is set.
func OpenCodeEndpoint(base string) bool {
	u, err := url.Parse(base)
	if err != nil || !strings.EqualFold(u.Hostname(), "opencode.ai") {
		return false
	}
	switch strings.TrimRight(u.Path, "/") {
	case "/zen", "/zen/go", "/inference":
		return true
	}
	return false
}

// AnonymousBase upgrades the official Zen endpoint without rerouting an
// existing custom Zen relay to a third-party host. An explicit URL always wins.
func (cfg UpstreamConfig) AnonymousBase() string {
	if base := strings.TrimSpace(cfg.Anonymous); base != "" {
		return base
	}
	if OpenCodeEndpoint(cfg.Zen) {
		return "https://opencode.ai/inference"
	}
	return strings.TrimSpace(cfg.Zen)
}
