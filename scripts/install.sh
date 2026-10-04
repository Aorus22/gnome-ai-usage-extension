#!/usr/bin/env bash
# Install / update Antigravity Usage (Go daemon + GNOME Shell extension).
# Idempotent: safe to run again for updates.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
EXT_UUID="antigravity-usage@local"
BIN="$HOME/.local/bin/antigravity-usage-daemon"
EXT_DIR="$HOME/.local/share/gnome-shell/extensions/$EXT_UUID"
CONFIG_DIR="$HOME/.config/antigravity-usage"
UNIT_DIR="$HOME/.config/systemd/user"

echo "==> Building daemon"
(cd "$ROOT/daemon" && go build -o "$BIN" .)

echo "==> Installing extension to $EXT_DIR"
mkdir -p "$EXT_DIR/icons"
cp "$ROOT/extension/metadata.json" "$EXT_DIR/"
cp "$ROOT/extension/extension.js" "$EXT_DIR/"
cp "$ROOT/extension/stylesheet.css" "$EXT_DIR/" 2>/dev/null || true
cp "$ROOT"/extension/icons/*.svg "$EXT_DIR/icons/"

echo "==> Installing sample config (kept if one already exists)"
mkdir -p "$CONFIG_DIR"
if [ ! -f "$CONFIG_DIR/config.toml" ]; then
    # Let the daemon write its annotated sample config on first start.
    touch /dev/null
else
    echo "    existing config kept: $CONFIG_DIR/config.toml"
fi

echo "==> Installing systemd user service"
mkdir -p "$UNIT_DIR"
cp "$ROOT/deploy/antigravity-usage.service" "$UNIT_DIR/"
systemctl --user daemon-reload
systemctl --user enable --now antigravity-usage.service

export DBUS_SESSION_BUS_ADDRESS="${DBUS_SESSION_BUS_ADDRESS:-unix:path=$XDG_RUNTIME_DIR/bus}"
gnome-extensions enable "$EXT_UUID" 2>/dev/null || true

echo
echo "Done. Daemon:  systemctl --user status antigravity-usage"
echo "     Config:  $CONFIG_DIR/config.toml"
echo "     NOTE: reload GNOME Shell (log out/in on Wayland) to pick up extension changes."
