# AI Usage

Live usage bars for AI providers (Antigravity, Codex, CoreWeave, ...) in the
GNOME Shell top bar — like Docker's tray indicator, but driven by your own
configurable sources.

```
┌─────────────────────────┐   D-Bus session bus    ┌──────────────────────────┐
│ antigravity-usage-daemon │  GetSources() method   │ gnome-shell extension    │
│ (Go, systemd --user)     │  Refresh() method      │ (GJS, thin UI layer)     │
│                          │  SourcesChanged signal │  panel: [icon] [value]   │
│ config.toml ─▶ providers │                        │  menu: row per source    │
│  ├─ command (CLI output) │                        │  icons per provider      │
│  └─ http (REST + JSON)   │                        └──────────────────────────┘
└─────────────────────────┘
```

Adding a new usage source (Codex, CoreWeave, anything with a CLI or REST API)
is a config change — the extension renders whatever the daemon publishes.

## Layout

| Path | What |
|---|---|
| `daemon/` | Go daemon: config parsing, `command`/`http` providers, poll scheduler, D-Bus service (`dev.local.AntigravityUsage`) |
| `extension/` | GJS source of truth: `extension.js`, `stylesheet.css`, provider SVGs in `icons/` |
| `deploy/antigravity-usage.service` | systemd user unit |
| `scripts/install.sh` | Build + install + enable everything (idempotent) |

## Install

```bash
./scripts/install.sh
# then reload GNOME Shell: log out and back in (Wayland)
```

## Configuration

`~/.config/antigravity-usage/config.toml` — the daemon writes an annotated
sample on first start. Each source polls on its own interval and defines one
or more metrics (progress bars); the source whose `id` matches `primary`
becomes the panel badge (icon + percent of its first metric).

```toml
primary = "antigravity"

[[source]]
id = "antigravity"
label = "Antigravity"
icon = "antigravity"            # resolves to extension icons/antigravity.svg
provider = "command"
interval_secs = 60
command = ["antigravity-usage-cli", "usage", "--json"]

[[source.metric]]
label = "5-hour"
percent = { type = "json", path = "five_hour.percent" }

[[source.metric]]
label = "7-day"
percent = { type = "json", path = "seven_day.percent" }

[[source]]
id = "coreweave"
label = "CoreWeave"
icon = "coreweave"
provider = "http"
interval_secs = 300
url = "https://api.coreweave.com/v1/spend"
headers = { Authorization = "Bearer ${COREWEAVE_TOKEN}" }   # env expansion

[[source.metric]]
label = "Monthly"
used = { type = "json", path = "data.month_spend" }
limit = 250                     # or limit_parse = { type = "json", path = ... }
unit = "$"                      # detail renders as "$186.4 / $250"
```

Metric modes: `percent` (a direct 0-100 value, `invert = true` when the source
reports *remaining*), or `used` + `limit` (`limit` literal or `limit_parse`
from the same payload). `reset` parses unix epoch seconds; set
`reset_relative_ms = true` when the value counts milliseconds from now.
Parsers: `json` (dot-path) or `regex` (first capture group).

Header/URL/body values expand `${ENV_VAR}` and
`${jsonfile:PATH:DOTPATH}` — the file is re-read on every poll, so rotated
credential files (e.g. `~/.codex/auth.json`) stay fresh without copying
secrets into the config.

### Antigravity

The source calls the **official Antigravity CLI** (`agy -p "/usage"`), whose
print mode expands the `/usage` slash command into the same weekly /
five-hour family limits the IDE panel shows — including ISO reset times
(the daemon parses RFC 3339). Requires the CLI to be logged in; it shares
the desktop session credentials.

The community CLI [`antigravity-usage`](https://github.com/skainguyen1412/antigravity-usage)
(`bun install -g antigravity-usage`) remains an alternative: it queries the
local language server for per-model remaining fractions, but does not
surface the weekly bucket. Data only flows while the IDE is open; when it
is closed the bars keep their last value and show the error detail.

## Icons

Each source gets an icon looked up as:
1. `<extension>/icons/<icon>.svg` — drop your own SVG here to override,
2. the theme icon `<icon>-symbolic`,
3. the shipped `generic.svg` fallback.

## D-Bus API

Bus name `dev.local.AntigravityUsage`, path `/dev/local/AntigravityUsage`:

- `GetSources() → a(ssdsss)` — `(id, label, value, unit, detail, icon)`, primary first
- `Refresh()` — poll all sources now
- signal `SourcesChanged()` — emitted whenever a poll updates state

## Debug / development

Headless end-to-end check (no session restart needed):

```bash
dbus-run-session -- bash -c '
  daemon/antigravity-usage-daemon --config /tmp/aau-test-config.toml &
  gnome-shell --headless --virtual-monitor 1280x720 &
  sleep 20
  gnome-extensions info antigravity-usage@local
'
```

The extension also exports a test-only interface at
`/dev/local/AntigravityUsage/UI` (`OpenMenu(b)`, `TakeScreenshot(s)`) used by
this loop to capture panel/menu screenshots.

## Uninstall

```bash
systemctl --user disable --now antigravity-usage.service
rm ~/.config/systemd/user/antigravity-usage.service
rm -rf ~/.local/share/gnome-shell/extensions/antigravity-usage@local
```
