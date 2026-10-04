package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// ParseCfg describes how to extract a numeric usage value from raw output.
type ParseCfg struct {
	// Type is "json" (walk a dot-path into a JSON document) or
	// "regex" (first capture group of the first match).
	Type    string `toml:"type"`
	Path    string `toml:"path"`
	Pattern string `toml:"pattern"`
	// Aggregate collapses an array reached mid-path: "sum" adds its numeric
	// values, "last" takes the last non-null one.
	Aggregate string `toml:"aggregate"`
}

// MetricCfg is one [[source.metric]] stanza: a bar shown under the source.
type MetricCfg struct {
	Label string `toml:"label"`
	// Percent parses a direct 0-100 value; when set, used/limit are ignored.
	// With invert=true the parsed value is "remaining" and 100-v is shown.
	Percent ParseCfg `toml:"percent"`
	Invert  bool     `toml:"invert"`
	// Used parses the consumed amount; pair with Limit or LimitParse.
	Used       ParseCfg `toml:"used"`
	Limit      float64  `toml:"limit"`       // literal limit
	LimitParse ParseCfg `toml:"limit_parse"` // or parse the limit from the payload
	// Reset parses a unix epoch timestamp (seconds) of the next quota reset.
	// With reset_relative_ms=true the parsed value counts milliseconds from now.
	Reset           ParseCfg `toml:"reset"`
	ResetRelativeMs bool     `toml:"reset_relative_ms"`
	// Scale multiplies the extracted percent/used value (e.g. scale = 100
	// when the source reports a 0..1 fraction instead of 0..100).
	Scale float64 `toml:"scale"`
	Unit  string  `toml:"unit"` // e.g. "$" (prefixed) or "tokens" (suffixed)
}

// SourceCfg gains an optional account email, extracted from the same payload.

// SourceCfg is one [[source]] stanza from config.toml.
type SourceCfg struct {
	ID           string            `toml:"id"`
	Label        string            `toml:"label"`
	Icon         string            `toml:"icon"`
	Provider     string            `toml:"provider"` // "command" or "http"
	IntervalSecs int               `toml:"interval_secs"`
	Unit         string            `toml:"unit"`
	Command      []string          `toml:"command"` // provider = command
	URL          string            `toml:"url"`     // provider = http
	Method       string            `toml:"method"`
	Headers      map[string]string `toml:"headers"`
	Body         string            `toml:"body"`
	Parse        ParseCfg          `toml:"parse"` // legacy single-value mode
	Metrics      []MetricCfg       `toml:"metric"`
	// Email extracts the account email shown next to the source name.
	Email ParseCfg `toml:"email"`
}

// Config is the whole config.toml document.
type Config struct {
	// Primary is the id of the source shown as the panel badge.
	// Empty means the first declared source.
	Primary string      `toml:"primary"`
	Sources []SourceCfg `toml:"source"`
}

func DefaultConfigPath() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "antigravity-usage", "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "antigravity-usage", "config.toml")
}

// LoadConfig reads and validates the daemon configuration.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(cfg.Sources) == 0 {
		return nil, fmt.Errorf("no [[source]] defined in %s", path)
	}
	seen := map[string]bool{}
	for _, s := range cfg.Sources {
		if s.ID == "" {
			return nil, fmt.Errorf("a [[source]] is missing its id (%s)", path)
		}
		if seen[s.ID] {
			return nil, fmt.Errorf("duplicate source id %q (%s)", s.ID, path)
		}
		seen[s.ID] = true
	}
	if cfg.Primary != "" && !seen[cfg.Primary] {
		return nil, fmt.Errorf("primary %q does not match any source id", cfg.Primary)
	}
	return &cfg, nil
}
