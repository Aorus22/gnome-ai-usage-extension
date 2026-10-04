package main

import (
	"os"
	"testing"
)

func TestExtractStringEmail(t *testing.T) {
	raw, err := os.ReadFile("/tmp/codex-resp.json")
	if err != nil {
		t.Skip("no saved payload")
	}
	cfg := ParseCfg{Type: "json", Path: "email"}
	got, err := extractString(cfg, raw)
	if err != nil {
		t.Fatalf("extractString: %v", err)
	}
	t.Logf("email = %q", got)
}
