# AI Usage

Live usage bars for AI providers (Antigravity, Codex, CoreWeave, ...) in your
desktop panel — like Docker's tray indicator, but driven by your own
configurable sources. One Go daemon publishes data over D-Bus; two thin UI
clients render it: a GNOME Shell extension (GJS) and a COSMIC panel applet
(Rust).

```
┌─────────────────────────┐   D-Bus session bus    ┌──────────────────────────┐
│ antigravity-usage-daemon │  GetSources() method   │ panel client             │
│ (Go, systemd --user)     │  Refresh() method      │  · GNOME Shell extension │
│                          │  SourcesChanged signal │  · COSMIC applet (Rust)  │
│ config.toml ─▶ providers │                        │  panel: [icon] [value]   │
│  ├─ command (CLI output) │                        │  popup: row per source   │
│  └─ http (REST + JSON)   │                        │  icons per provider      │
└─────────────────────────┘                        └──────────────────────────┘
```

Adding a new usage source (Codex, CoreWeave, anything with a CLI or REST API)
is a config change — the clients render whatever the daemon publishes.
A source whose poll or parse fails is hidden from the menu entirely rather
than shown as an error line.

## Layout

| Path | What |
|---|---|
| `daemon/` | Go daemon: config parsing, `command`/`http` providers, poll scheduler, D-Bus service (`dev.local.AntigravityUsage`) |
| `extension/` | GNOME Shell client (GJS): `extension.js`, `stylesheet.css` |
| `applet/` | COSMIC panel applet (Rust + libcosmic + zbus) |
| `icons/` | Brand SVGs shared by both clients (extension copies them, applet embeds them) |
| `deploy/antigravity-usage.service` | systemd user unit (shared) |
| `scripts/install.sh` | GNOME: build + install + enable everything (idempotent) |
| `scripts/install-cosmic.sh` | COSMIC: build + install the applet |

## Install

GNOME Shell:

```bash
./scripts/install.sh
# then reload GNOME Shell: log out and back in (Wayland)
```

COSMIC:

```bash
./scripts/install-cosmic.sh
# one-time registration: COSMIC Settings > Panel > Applets > Add > "AI Usage"
```

Both clients share the same daemon and `~/.config/antigravity-usage/config.toml`;
installing one does not affect the other.

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
surface the weekly bucket. Data only flows while the IDE is open; while
it is closed the source's section stays hidden from the menu until it
answers again.

## Icons

Each source gets an icon looked up as:
1. `icons/<icon>.svg` at the repo root — drop your own SVG here to override
   (the GNOME installer copies it, the COSMIC applet embeds it at build time),
2. the theme icon `<icon>-symbolic` (GNOME only),
3. the shipped `generic.svg` fallback.

## D-Bus API

Bus name `dev.local.AntigravityUsage`, path `/dev/local/AntigravityUsage`:

- `GetSources() → a(sssa(sdds)s)` — sources as
  `(id, label, icon, [(metric_label, used, limit, detail)], email)`, primary first
- `Refresh()` — poll all sources now
- signal `SourcesChanged()` — emitted whenever a poll updates state

## COSMIC applet

`applet/` is a libcosmic panel applet (Rust, zbus) speaking the same D-Bus
API — the daemon is untouched. `scripts/install-cosmic.sh` builds it with
`cargo build --release`, installs the binary to `~/.local/bin` and registers
a `X-CosmicApplet=true` desktop entry in `~/.local/share/applications`; add
"AI Usage" from COSMIC Settings > Panel > Applets (or append the applet id
`dev.local.AntigravityUsageApplet` to
`~/.config/cosmic/com.system76.CosmicPanel.Panel/v1/plugins_center` while the
panel is stopped).

Run it in-place from a COSMIC session for development:

```bash
cd applet && cargo run
```

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

COSMIC applet: remove it from the panel, then:

```bash
rm ~/.local/bin/cosmic-applet-antigravity-usage \
   ~/.local/share/applications/dev.local.AntigravityUsageApplet.desktop
```
