#!/usr/bin/env bash
# Install / update the COSMIC panel applet.
# The Go daemon (systemd user unit) is shared with the GNOME variant and
# is NOT touched by this script — install it once via scripts/install.sh.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_ID="dev.local.AntigravityUsageApplet"
BIN_NAME="cosmic-applet-antigravity-usage"
BIN="$HOME/.local/bin/$BIN_NAME"
DESKTOP="$HOME/.local/share/applications/$APP_ID.desktop"

echo "==> Building applet (first build downloads libcosmic; takes a while)"
(cd "$ROOT/applet" && cargo build --release)
install -D "$ROOT/applet/target/release/$BIN_NAME" "$BIN"

echo "==> Installing applet entry: $DESKTOP"
cat > "$DESKTOP" <<EOF
[Desktop Entry]
Type=Application
Name=AI Usage
Comment=Antigravity usage monitor (dev.local.AntigravityUsage)
Exec=$BIN
NoDisplay=true
X-CosmicApplet=true
X-OverflowPriority=50
X-OverflowMinSize=2
Categories=COSMIC
EOF
update-desktop-database "$HOME/.local/share/applications" 2>/dev/null || true

echo
echo "Done. Panel registration (one-time): COSMIC Settings > Panel > Applets > Add > \"AI Usage\"."
echo "Daemon status:  systemctl --user status antigravity-usage"
echo "Uninstall:      remove the applet from the panel, then:"
echo "                rm $BIN '$DESKTOP'"
