package main

import (
	"testing"
)

func TestEmailConfig(t *testing.T) {
	cfg, err := LoadConfig("/home/aorus/.config/antigravity-usage/config.toml")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range cfg.Sources {
		t.Logf("%s: email.Type=%q pattern=%q", s.ID, s.Email.Type, s.Email.Pattern)
	}
}

func TestAccountRegex(t *testing.T) {
	raw := []byte("Gemini Models\tWeekly Limit Remaining\t98%\t2026-10-09T13:39:49Z\naccount\ttest@mail.com\n")
	cfg := ParseCfg{Type: "regex", Pattern: `account\s+(\S+)`}
	got, err := extractString(cfg, raw)
	if err != nil {
		t.Fatalf("extractString: %v", err)
	}
	t.Logf("email = %q", got)
}
