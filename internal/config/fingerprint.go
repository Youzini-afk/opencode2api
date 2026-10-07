package config

import "regexp"

type FingerprintConfig struct {
	Versions []string `json:"versions"`
}

var defaultOpenCodeVersions = []string{
	"1.18.18", "1.18.17", "1.18.16", "1.18.15", "1.18.14", "1.18.13",
	"1.18.12", "1.18.11", "1.18.10", "1.18.9", "1.18.8", "1.18.7",
}

var safeSemverPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// AnonymousEnabled preserves keyless Zen for legacy configurations.
func (cfg Config) AnonymousEnabled() bool {
	return cfg.Anonymous || len(cfg.ZenKeys) == 0
}
