package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/godbus/dbus/v5"
)

const sampleConfig = `# AI Usage daemon configuration.
#
# Every [[source]] becomes one block in the panel menu. Each [[source.metric]]
# is one progress bar under it. The source whose id matches "primary" is shown
# as the panel badge (icon + percent of its first metric).
#
# Providers:
#   command — run a command, capture its stdout
#   http    — fetch a URL (headers support ${ENV_VAR} expansion), capture the body
# Metric extraction from the same payload:
#   percent    — a direct 0-100 value
#   used + limit — both numbers; the bar shows used/limit,
#                  detail renders as "$186.4 / $250" (unit "$" prefixes)
#                  or "7800 / 10000 tokens" (unit suffixed)
#   reset      — unix epoch (seconds) of the next quota reset; rendered as
#                "resets in 2d 4h" after the usage detail
# Parsers: { type = "json", path = "a.b.0.c" } or { type = "regex", pattern = "..." }

primary = "antigravity"

[[source]]
id = "antigravity"
label = "Antigravity"
icon = "antigravity"
provider = "command"
interval_secs = 60
command = ["sh", "-c", "printf '{\"five_hour\": 62, \"five_hour_reset\": %d, \"seven_day\": 31, \"seven_day_reset\": %d}' $(($(date +%s)+11220)) $(($(date +%s)+189000))"]

[[source.metric]]
label = "5-hour"
percent = { type = "json", path = "five_hour" }
reset = { type = "json", path = "five_hour_reset" }

[[source.metric]]
label = "7-day"
percent = { type = "json", path = "seven_day" }
reset = { type = "json", path = "seven_day_reset" }

# Demo source — point "command" at the real Codex CLI output when you have it.
[[source]]
id = "codex"
label = "Codex"
icon = "codex"
provider = "command"
interval_secs = 60
command = ["sh", "-c", "printf '{\"windows\": {\"5h\": {\"used\": 7800, \"limit\": 10000, \"reset\": %d}, \"7d\": {\"used\": 420000, \"limit\": 2000000, \"reset\": %d}}}' $(($(date +%s)+12000)) $(($(date +%s)+187200))"]

[[source.metric]]
label = "5-hour"
used = { type = "json", path = "windows.5h.used" }
limit_parse = { type = "json", path = "windows.5h.limit" }
reset = { type = "json", path = "windows.5h.reset" }
unit = "tokens"

[[source.metric]]
label = "7-day"
used = { type = "json", path = "windows.7d.used" }
limit_parse = { type = "json", path = "windows.7d.limit" }
reset = { type = "json", path = "windows.7d.reset" }
unit = "tokens"

# Demo source — replace with the commented HTTP example below.
[[source]]
id = "coreweave"
label = "CoreWeave"
icon = "coreweave"
provider = "command"
interval_secs = 60
command = ["sh", "-c", "printf '{\"spend\": 186.4, \"reset\": %d}' $(($(date +%s)+1036800))"]

[[source.metric]]
label = "Monthly"
used = { type = "json", path = "spend" }
limit = 250
reset = { type = "json", path = "reset" }
unit = "$"

# Real HTTP example (CoreWeave-style monthly budget):
# [[source]]
# id = "coreweave"
# label = "CoreWeave"
# icon = "coreweave"
# provider = "http"
# interval_secs = 300
# url = "https://api.coreweave.com/v1/spend"
# headers = { Authorization = "Bearer ${COREWEAVE_TOKEN}" }
#
# [[source.metric]]
# label = "Monthly"
# used = { type = "json", path = "data.month_spend" }
# limit = 250
# reset = { type = "json", path = "data.next_reset" }
# unit = "$"
`

// writeSampleConfig installs the commented sample config if none exists yet.
func writeSampleConfig(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	log.Printf("no config found — writing a sample to %s", path)
	return os.WriteFile(path, []byte(sampleConfig), 0o644)
}

func main() {
	configPath := flag.String("config", DefaultConfigPath(), "path to config.toml")
	flag.Parse()

	cfg, err := LoadConfig(*configPath)
	if os.IsNotExist(err) {
		if werr := writeSampleConfig(*configPath); werr != nil {
			log.Fatalf("cannot write sample config: %v", werr)
		}
		cfg, err = LoadConfig(*configPath)
	}
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	conn, err := dbus.SessionBus()
	if err != nil {
		log.Fatalf("session bus: %v (running inside a desktop session?)", err)
	}
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		log.Fatalf("request name: %v", err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		log.Fatalf("bus name %s is already owned — is another daemon running?", busName)
	}

	mgr, err := NewManager(cfg, conn)
	if err != nil {
		log.Fatalf("%v", err)
	}
	if err := conn.Export(&Service{mgr: mgr}, objPath, ifaceName); err != nil {
		log.Fatalf("export: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	mgr.Start(ctx)
	log.Printf("antigravity-usage daemon started (%d sources, primary=%q)",
		len(mgr.sources), cfg.Primary)

	<-ctx.Done()
	log.Println("shutting down")
}
