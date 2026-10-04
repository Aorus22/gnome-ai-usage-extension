package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// Metric is one progress bar under a source: (label, used, limit, detail).
type Metric struct {
	Label  string
	Used   float64
	Limit  float64
	Detail string
}

// Source is the D-Bus view of one source: (id, label, icon, metrics, email).
type Source struct {
	ID      string
	Label   string
	Icon    string
	Metrics []Metric
	Email   string
}

// runtimeState couples a config stanza with its provider and latest payload.
type runtimeState struct {
	cfg      SourceCfg
	prov     Provider
	raw      []byte
	pollErr  error
	hasData  bool
	email    string
	refreshC chan struct{}
}

// Manager owns all sources: scheduling, state and change notification.
type Manager struct {
	mu        sync.Mutex
	sources   []*runtimeState
	primaryID string
	conn      *dbus.Conn
}

func NewManager(cfg *Config, conn *dbus.Conn) (*Manager, error) {
	m := &Manager{primaryID: cfg.Primary, conn: conn}
	for _, sc := range cfg.Sources {
		prov, err := NewProvider(sc)
		if err != nil {
			log.Printf("skipping source %q: %v", sc.ID, err)
			continue
		}
		m.sources = append(m.sources, &runtimeState{
			cfg:      sc,
			prov:     prov,
			refreshC: make(chan struct{}, 1),
		})
	}
	if len(m.sources) == 0 {
		return nil, fmt.Errorf("no usable sources in config")
	}
	return m, nil
}

// Start launches one poll loop per source and returns immediately.
func (m *Manager) Start(ctx context.Context) {
	for _, s := range m.sources {
		go m.loop(ctx, s)
	}
}

// RefreshAll asks every source to poll again as soon as possible.
func (m *Manager) RefreshAll() {
	for _, s := range m.sources {
		select {
		case s.refreshC <- struct{}{}:
		default: // a refresh is already pending
		}
	}
}

func (m *Manager) loop(ctx context.Context, s *runtimeState) {
	interval := time.Duration(s.cfg.IntervalSecs) * time.Second
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	m.poll(ctx, s) // publish an initial sample right away
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.poll(ctx, s)
		case <-s.refreshC:
			m.poll(ctx, s)
		}
	}
}

func (m *Manager) poll(ctx context.Context, s *runtimeState) {
	raw, err := s.prov.Poll(ctx)

	m.mu.Lock()
	s.raw = raw
	s.pollErr = err
	if err == nil {
		s.hasData = true
		if s.cfg.Email.Type != "" {
			if e, err := extractString(s.cfg.Email, raw); err == nil {
				s.email = e
			}
		}
	}
	m.mu.Unlock()

	m.emitChanged()
}

// Snapshot returns all sources with the primary one first.
// Callers get freshly computed metrics on every call.
func (m *Manager) Snapshot() []Source {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]Source, 0, len(m.sources))
	for _, s := range m.sources {
		label := s.cfg.Label
		if label == "" {
			label = s.cfg.ID
		}
		icon := s.cfg.Icon
		if icon == "" {
			icon = "generic"
		}
		out = append(out, Source{
			ID:      s.cfg.ID,
			Label:   label,
			Icon:    icon,
			Metrics: s.computeMetrics(),
			Email:   s.email,
		})
	}

	for i, s := range out {
		if s.ID == m.primaryID && i != 0 {
			out[0], out[i] = out[i], out[0]
			break
		}
	}
	return out
}

// computeMetrics turns the latest payload into displayable metrics.
// Must be called with m.mu held.
func (s *runtimeState) computeMetrics() []Metric {
	if s.pollErr != nil {
		// One error line for the whole source — repeating it on every
		// configured metric is just noise.
		return []Metric{{Detail: "error: " + truncate(s.pollErr.Error(), 90)}}
	}
	if !s.hasData {
		return []Metric{{Detail: "waiting for first update"}}
	}
	if len(s.cfg.Metrics) == 0 {
		// Legacy single-value config (parse = ... on the source itself).
		value, err := extract(s.cfg.Parse, s.raw)
		if err != nil {
			return []Metric{{Detail: "error: " + truncate(err.Error(), 80)}}
		}
		return []Metric{{Used: value, Detail: strings.TrimSpace(fmtNum(value) + " " + s.cfg.Unit)}}
	}
	metrics := make([]Metric, 0, len(s.cfg.Metrics))
	for _, mc := range s.cfg.Metrics {
		metrics = append(metrics, mc.compute(s.raw))
	}
	return metrics
}

// compute extracts one metric from a payload.
func (mc MetricCfg) compute(raw []byte) Metric {
	m := Metric{Label: mc.Label}
	var parts []string
	var used, limit float64

	if mc.Percent.Type != "" {
		v, err := extract(mc.Percent, raw)
		if err != nil {
			m.Detail = "error: " + truncate(err.Error(), 60)
			return m
		}
		if mc.Scale > 0 {
			v *= mc.Scale
		}
		if mc.Invert {
			v = 100 - v
		}
		used, limit = math.Max(0, math.Min(100, v)), 100
	} else {
		u, err := extract(mc.Used, raw)
		if err != nil {
			m.Detail = "error: " + truncate(err.Error(), 60)
			return m
		}
		if mc.Scale > 0 {
			u *= mc.Scale
		}
		used = u
		if mc.LimitParse.Type != "" {
			limit, err = extract(mc.LimitParse, raw)
			if err != nil {
				m.Detail = "error: " + truncate(err.Error(), 60)
				return m
			}
		} else {
			limit = mc.Limit
		}
		if usage := fmtUsage(mc.Unit, used, limit); usage != "" {
			parts = append(parts, usage)
		}
	}

	if resetIn := mc.resetDetail(raw); resetIn != "" {
		parts = append(parts, resetIn)
	}

	m.Used, m.Limit = used, limit
	m.Detail = strings.Join(parts, " · ")
	return m
}

// resetDetail renders "resets in …" when a reset timestamp is configured.
func (mc MetricCfg) resetDetail(raw []byte) string {
	if mc.Reset.Type == "" {
		return ""
	}
	v, err := extract(mc.Reset, raw)
	if err != nil || v <= 0 {
		return ""
	}
	var sec int64
	if mc.ResetRelativeMs {
		sec = int64(v) / 1000 // milliseconds from now
	} else {
		sec = int64(v) - time.Now().Unix()
	}
	if sec <= 0 {
		return ""
	}
	return "resets in " + fmtDuration(sec)
}

// fmtDuration renders a human countdown like "2d 4h" or "3h 20m".
func fmtDuration(sec int64) string {
	days := sec / 86400
	hours := (sec % 86400) / 3600
	mins := (sec % 3600) / 60
	switch {
	case days >= 2:
		return fmt.Sprintf("%dd %dh", days, hours)
	case days == 1:
		return fmt.Sprintf("1d %dh", hours)
	case hours >= 1:
		return fmt.Sprintf("%dh %dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

func fmtUsage(unit string, used, limit float64) string {
	if limit <= 0 {
		return strings.TrimSpace(fmtNum(used) + " " + unit)
	}
	if unit == "$" {
		return fmt.Sprintf("$%.2f / $%.2f", used, limit)
	}
	out := fmtNum(used) + " / " + fmtNum(limit)
	if unit != "" {
		out += " " + unit
	}
	return out
}

// fmtNum prints a float without trailing zeros or scientific notation,
// compacting large values ("420k", "2.1B", "2.11T") to keep menu rows tidy.
func fmtNum(v float64) string {
	a := math.Abs(v)
	switch {
	case a >= 1e12:
		return strconv.FormatFloat(v/1e12, 'f', 2, 64) + "T"
	case a >= 1e9:
		return strconv.FormatFloat(v/1e9, 'f', 2, 64) + "B"
	case a >= 1e5:
		return strconv.FormatFloat(v/1e3, 'f', 1, 64) + "k"
	default:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
}

func (m *Manager) emitChanged() {
	if err := m.conn.Emit(objPath, ifaceName+".SourcesChanged"); err != nil {
		log.Printf("emit SourcesChanged: %v", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
