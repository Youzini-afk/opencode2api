package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeFingerprintVersions(t *testing.T) {
	cfg := validTestConfig()
	cfg.Fingerprint.Versions = []string{" 1.18.18 ", "1.18.17", "1.18.18", "\t1.18.16\n"}
	normalized, err := NormalizeConfig(filepath.Join(t.TempDir(), "config.json"), cfg)
	if err != nil {
		t.Fatalf("NormalizeConfig() error = %v", err)
	}
	want := []string{"1.18.18", "1.18.17", "1.18.16"}
	if !reflect.DeepEqual(normalized.Fingerprint.Versions, want) {
		t.Fatalf("normalized versions = %#v, want %#v", normalized.Fingerprint.Versions, want)
	}
}

func TestNormalizeFingerprintVersionsRejectsUnsafeValues(t *testing.T) {
	tests := []struct {
		name     string
		versions []string
	}{
		{name: "empty", versions: nil},
		{name: "header injection", versions: []string{"1.18.18\r\nx-evil: true"}},
		{name: "prerelease", versions: []string{"1.18.19-beta.1"}},
		{name: "two segments", versions: []string{"1.18"}},
		{name: "leading zero", versions: []string{"1.018.1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validTestConfig()
			cfg.Fingerprint.Versions = test.versions
			_, err := NormalizeConfig(filepath.Join(t.TempDir(), "config.json"), cfg)
			if err == nil || !strings.Contains(err.Error(), "fingerprint.versions") {
				t.Fatalf("NormalizeConfig() error = %v, want fingerprint.versions validation error", err)
			}
		})
	}
}

func TestFingerprintConfigPersistsAndClonesDeeply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := validTestConfig()
	cfg.Fingerprint.Versions = []string{"2.3.4", "1.18.18"}
	if err := SaveConfigAtomic(path, cfg); err != nil {
		t.Fatalf("SaveConfigAtomic() error = %v", err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if !reflect.DeepEqual(loaded.Fingerprint.Versions, cfg.Fingerprint.Versions) {
		t.Fatalf("persisted versions = %#v, want %#v", loaded.Fingerprint.Versions, cfg.Fingerprint.Versions)
	}

	cloned := cloneConfig(loaded)
	cloned.Fingerprint.Versions[0] = "9.9.9"
	if loaded.Fingerprint.Versions[0] == cloned.Fingerprint.Versions[0] {
		t.Fatal("cloneConfig shared fingerprint.versions backing storage")
	}
}

func TestLoadLegacyConfigUsesDefaultFingerprintVersions(t *testing.T) {
	cfg := validTestConfig()
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err := json.Unmarshal(encoded, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "fingerprint")
	encoded, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() legacy config error = %v", err)
	}
	if !reflect.DeepEqual(loaded.Fingerprint.Versions, defaultOpenCodeVersions) {
		t.Fatalf("legacy fingerprint versions = %#v, want defaults %#v", loaded.Fingerprint.Versions, defaultOpenCodeVersions)
	}
}
