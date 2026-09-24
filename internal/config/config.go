// Package config loads the single YAML file that configures the cleaner.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration decodes YAML duration strings such as "30s" or "2m".
// yaml.v3 does not do this for time.Duration on its own.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	parsed, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", value.Value, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) Std() time.Duration { return time.Duration(d) }

type Gmail struct {
	CredentialsFile string `yaml:"credentials_file"`
	TokenFile       string `yaml:"token_file"`
}

type Laya struct {
	Endpoint  string   `yaml:"endpoint"`
	APIKeyEnv string   `yaml:"api_key_env"`
	Timeout   Duration `yaml:"timeout"`
	Workers   int      `yaml:"workers"`
}

type Policy struct {
	MinConfidenceJunk  float64 `yaml:"min_confidence_junk"`
	MinConfidenceTopic float64 `yaml:"min_confidence_topic"`
}

type Extract struct {
	BodyPreviewChars int `yaml:"body_preview_chars"`
}

type Audit struct {
	Dir string `yaml:"dir"`
}

type Config struct {
	Gmail   Gmail             `yaml:"gmail"`
	Laya    Laya              `yaml:"laya"`
	Policy  Policy            `yaml:"policy"`
	Labels  map[string]string `yaml:"labels"`
	Extract Extract           `yaml:"extract"`
	Audit   Audit             `yaml:"audit"`
}

// Default returns the full configuration with every value from the spec.
func Default() *Config {
	return &Config{
		Gmail: Gmail{
			CredentialsFile: "client_secret.json",
			TokenFile:       "token.json",
		},
		Laya: Laya{
			Endpoint:  "http://127.0.0.1:8000",
			APIKeyEnv: "LAYA_API_KEY",
			Timeout:   Duration(30 * time.Second),
			Workers:   8,
		},
		Policy: Policy{
			MinConfidenceJunk:  0.90,
			MinConfidenceTopic: 0.70,
		},
		Labels: map[string]string{
			"people":        "cleaner/people",
			"action":        "cleaner/action",
			"security":      "cleaner/security",
			"accounts":      "cleaner/accounts",
			"opportunities": "cleaner/opportunities",
			"unclassified":  "cleaner/unclassified",
		},
		Extract: Extract{BodyPreviewChars: 800},
		Audit:   Audit{Dir: "audit"},
	}
}

// Load reads path over the defaults. A missing file is an error: silently
// running with defaults would point the tool at the wrong mailbox.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := Default()
	if err := yaml.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.Gmail.CredentialsFile == "" {
		return fmt.Errorf("gmail.credentials_file must not be empty")
	}
	if c.Gmail.TokenFile == "" {
		return fmt.Errorf("gmail.token_file must not be empty")
	}
	for name, v := range map[string]float64{
		"policy.min_confidence_junk":  c.Policy.MinConfidenceJunk,
		"policy.min_confidence_topic": c.Policy.MinConfidenceTopic,
	} {
		if v <= 0 || v > 1 {
			return fmt.Errorf("%s must be in (0, 1], got %v", name, v)
		}
	}
	if len(c.Labels) == 0 {
		return fmt.Errorf("labels must not be empty")
	}
	return nil
}
