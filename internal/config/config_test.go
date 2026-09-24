package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadAppliesDefaultsForAbsentKeys(t *testing.T) {
	path := write(t, "gmail:\n  token_file: custom-token.json\n")

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got.Gmail.TokenFile != "custom-token.json" {
		t.Errorf("Gmail.TokenFile = %q, want %q", got.Gmail.TokenFile, "custom-token.json")
	}
	if got.Gmail.CredentialsFile != "client_secret.json" {
		t.Errorf("Gmail.CredentialsFile = %q, want default %q", got.Gmail.CredentialsFile, "client_secret.json")
	}
	if got.Laya.Timeout.Std() != 30*time.Second {
		t.Errorf("Laya.Timeout = %v, want 30s", got.Laya.Timeout.Std())
	}
	if got.Policy.MinConfidenceJunk != 0.95 {
		t.Errorf("MinConfidenceJunk = %v, want 0.95", got.Policy.MinConfidenceJunk)
	}
	if got.Labels["people"] != "cleaner/people" {
		t.Errorf("Labels[people] = %q, want cleaner/people", got.Labels["people"])
	}
	if got.Extract.BodyPreviewChars != 800 {
		t.Errorf("BodyPreviewChars = %d, want 800", got.Extract.BodyPreviewChars)
	}
	if got.Audit.Dir != "audit" {
		t.Errorf("Audit.Dir = %q, want audit", got.Audit.Dir)
	}
}

func TestLoadRejectsMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("Load() error = nil, want an error for a missing file")
	}
}

func TestLoadRejectsMalformedYAML(t *testing.T) {
	if _, err := Load(write(t, "gmail: [also not a map\n")); err == nil {
		t.Fatal("Load() error = nil, want a parse error")
	}
}

func TestLoadRejectsThresholdOutOfRange(t *testing.T) {
	for _, body := range []string{
		"policy:\n  min_confidence_junk: 1.5\n",
		"policy:\n  min_confidence_topic: 0\n",
	} {
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("Load(%q) error = nil, want a range error", body)
		}
	}
}

func TestLoadOverridesOnlyPresentKeys(t *testing.T) {
	path := write(t, "labels:\n  people: my/people\n")

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Labels["people"] != "my/people" {
		t.Errorf("Labels[people] = %q, want my/people", got.Labels["people"])
	}
	if got.Labels["security"] != "cleaner/security" {
		t.Errorf("Labels[security] = %q, want the default to survive", got.Labels["security"])
	}
}

func TestDurationRejectsInvalidString(t *testing.T) {
	if _, err := Load(write(t, "laya:\n  timeout: soon\n")); err == nil {
		t.Fatal("Load() error = nil, want a duration parse error")
	}
}
