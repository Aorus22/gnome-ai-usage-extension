package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Provider fetches the raw payload of one source (command stdout or HTTP body).
type Provider interface {
	Poll(ctx context.Context) ([]byte, error)
}

// NewProvider builds the provider named by cfg.Provider.
func NewProvider(cfg SourceCfg) (Provider, error) {
	switch cfg.Provider {
	case "command":
		return &CommandProvider{cfg: cfg}, nil
	case "http":
		return &HTTPProvider{cfg: cfg, client: &http.Client{Timeout: 15 * time.Second}}, nil
	default:
		return nil, fmt.Errorf("unknown provider %q (want \"command\" or \"http\")", cfg.Provider)
	}
}

// ---- output parsers (shared by command and http) ----

func parseJSONPath(raw []byte, cfg ParseCfg) (float64, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, fmt.Errorf("invalid JSON: %w", err)
	}
	cur := v
	for _, seg := range strings.Split(cfg.Path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[seg]
			if !ok {
				return 0, fmt.Errorf("key %q not found", seg)
			}
			cur = next
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil {
				// "[key~=value]" — first element whose obj[key] contains value.
				// Arrays like model lists are re-sorted by the source, so
				// match by content instead of position.
				if kv, ok := matchSeg(seg); ok {
					found := false
					for _, el := range node {
						if obj, ok := el.(map[string]any); ok {
							if s, ok := obj[kv[0]].(string); ok && strings.Contains(s, kv[1]) {
								cur = obj
								found = true
								break
							}
						}
					}
					if found {
						continue
					}
					return 0, fmt.Errorf("no element matching %q", seg)
				}
				// Plain key — map over every element carrying it, so an
				// aggregate can collapse the array afterwards.
				mapped := make([]any, 0, len(node))
				for _, el := range node {
					if obj, ok := el.(map[string]any); ok {
						if next, ok := obj[seg]; ok {
							mapped = append(mapped, next)
						}
					}
				}
				cur = mapped
				continue
			}
			if idx < 0 || idx >= len(node) {
				return 0, fmt.Errorf("bad array index %q", seg)
			}
			cur = node[idx]
		default:
			return 0, fmt.Errorf("cannot descend into %T at %q", cur, seg)
		}
	}
	if arr, ok := cur.([]any); ok {
		switch cfg.Aggregate {
		case "sum":
			total := 0.0
			for _, el := range arr {
				if n, err := numeric(el); err == nil {
					total += n
				}
			}
			return total, nil
		case "last":
			for i := len(arr) - 1; i >= 0; i-- {
				if arr[i] != nil {
					return numeric(arr[i])
				}
			}
			return 0, fmt.Errorf("aggregate \"last\": no non-null value")
		}
	}
	return numeric(cur)
}

// matchSeg parses a "[key~=value]" path segment.
func matchSeg(seg string) ([2]string, bool) {
	var out [2]string
	k, v, ok := strings.Cut(seg, "~=")
	if !ok || k == "" || v == "" {
		return out, false
	}
	out[0], out[1] = k, v
	return out, true
}

func numeric(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case json.Number:
		return n.Float64()
	case string:
		// ISO 8601 timestamps (e.g. quota reset times) become unix epochs.
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(n)); err == nil {
			return float64(t.Unix()), nil
		}
		return strconv.ParseFloat(strings.TrimSpace(n), 64)
	default:
		return 0, fmt.Errorf("value %v (%T) is not numeric", v, v)
	}
}

// extract pulls a number out of a raw payload using cfg.
func extract(cfg ParseCfg, raw []byte) (float64, error) {
	switch cfg.Type {
	case "json":
		if cfg.Path == "" {
			return 0, fmt.Errorf(`parse.path is required when type = "json"`)
		}
		return parseJSONPath(raw, cfg)
	case "regex", "":
		pattern := cfg.Pattern
		if pattern == "" {
			pattern = `([0-9]+(?:\.[0-9]+)?)`
		}
		return parseRegex(raw, pattern)
	default:
		return 0, fmt.Errorf("unknown type %q (want \"json\" or \"regex\")", cfg.Type)
	}
}

func parseRegex(raw []byte, pattern string) (float64, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return 0, fmt.Errorf("bad regex: %w", err)
	}
	m := re.FindSubmatch(raw)
	if m == nil {
		return 0, fmt.Errorf("pattern %q did not match the output", pattern)
	}
	group := m[0]
	if len(m) > 1 {
		group = m[1]
	}
	return numeric(strings.TrimSpace(string(group)))
}

// jsonfileRe matches ${jsonfile:PATH:DOTPATH} references, e.g.
// ${jsonfile:~/.codex/auth.json:tokens.access_token} — the file is re-read on
// every expansion so rotated credentials stay fresh.
var jsonfileRe = regexp.MustCompile(`\$\{jsonfile:([^:]+):([^}]+)\}`)

// expandValue resolves ${ENV}, ~/ , ${file:PATH} and ${jsonfile:PATH:DOTPATH}
// references.
func expandValue(s string) string {
	// ${file:PATH} — raw file contents (trimmed), e.g. a session cookie.
	if fileRe := regexp.MustCompile(`\$\{file:([^}]+)\}`); fileRe.MatchString(s) {
		s = fileRe.ReplaceAllStringFunc(s, func(m string) string {
			path := fileRe.FindStringSubmatch(m)[1]
			if strings.HasPrefix(path, "~/") {
				home, err := os.UserHomeDir()
				if err != nil {
					return ""
				}
				path = filepath.Join(home, path[2:])
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(data))
		})
	}
	s = jsonfileRe.ReplaceAllStringFunc(s, func(m string) string {
		parts := jsonfileRe.FindStringSubmatch(m)
		path, dotpath := parts[1], parts[2]
		if strings.HasPrefix(path, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return ""
			}
			path = filepath.Join(home, path[2:])
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return ""
		}
		var cur any
		if err := json.Unmarshal(data, &cur); err != nil {
			return ""
		}
		for _, seg := range strings.Split(dotpath, ".") {
			node, ok := cur.(map[string]any)
			if !ok {
				return ""
			}
			if cur, ok = node[seg]; !ok {
				return ""
			}
		}
		switch v := cur.(type) {
		case string:
			return v
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		case bool:
			return strconv.FormatBool(v)
		default:
			return ""
		}
	})
	return os.ExpandEnv(s)
}

// extractString pulls a string value (email, ids, ...) out of a raw payload.
func extractString(cfg ParseCfg, raw []byte) (string, error) {
	switch cfg.Type {
	case "json":
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return "", fmt.Errorf("invalid JSON: %w", err)
		}
		cur := v
		for _, seg := range strings.Split(cfg.Path, ".") {
			switch node := cur.(type) {
			case map[string]any:
				next, ok := node[seg]
				if !ok {
					return "", fmt.Errorf("key %q not found", seg)
				}
				cur = next
			case []any:
				idx, err := strconv.Atoi(seg)
				if err != nil || idx < 0 || idx >= len(node) {
					return "", fmt.Errorf("bad array index %q", seg)
				}
				cur = node[idx]
			default:
				return "", fmt.Errorf("cannot descend into %T at %q", cur, seg)
			}
		}
		switch s := cur.(type) {
		case string:
			return s, nil
		case nil:
			return "", nil
		default:
			return fmt.Sprintf("%v", s), nil
		}
	case "regex":
		re, err := regexp.Compile(cfg.Pattern)
		if err != nil {
			return "", fmt.Errorf("bad regex: %w", err)
		}
		m := re.FindSubmatch(raw)
		if m == nil {
			return "", fmt.Errorf("pattern %q did not match the output", cfg.Pattern)
		}
		if len(m) > 1 {
			return string(m[1]), nil
		}
		return string(m[0]), nil
	default:
		return "", fmt.Errorf("unknown type %q", cfg.Type)
	}
}

// ---- command provider ----

// CommandProvider runs an external command and captures its stdout.
type CommandProvider struct {
	cfg SourceCfg
}

func (p *CommandProvider) Poll(ctx context.Context) ([]byte, error) {
	if len(p.cfg.Command) == 0 {
		return nil, fmt.Errorf("command is not set")
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, p.cfg.Command[0], p.cfg.Command[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Surface the tool's own message (e.g. "Antigravity is not running")
		// instead of a bare exit status.
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			msg = strings.SplitN(msg, "\n", 2)[0]
			msg = strings.TrimLeft(msg, "❌✅⚠️ ✗·\t")
			return nil, fmt.Errorf("%s", msg)
		}
		return nil, fmt.Errorf("command failed: %w", err)
	}
	return stdout.Bytes(), nil
}

// ---- http provider ----

// HTTPProvider polls a REST endpoint and parses the response body.
type HTTPProvider struct {
	cfg    SourceCfg
	client *http.Client
}

func (p *HTTPProvider) Poll(ctx context.Context) ([]byte, error) {
	if p.cfg.URL == "" {
		return nil, fmt.Errorf("url is not set")
	}
	method := p.cfg.Method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method,
		expandValue(p.cfg.URL),
		strings.NewReader(expandValue(p.cfg.Body)))
	if err != nil {
		return nil, err
	}
	for k, v := range p.cfg.Headers {
		req.Header.Set(k, expandValue(v))
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, p.cfg.URL)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return body, nil
}
