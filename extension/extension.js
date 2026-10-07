import GObject from 'gi://GObject';
import Gio from 'gi://Gio';
import GLib from 'gi://GLib';
import Clutter from 'gi://Clutter';
import Pango from 'gi://Pango';
import St from 'gi://St';
import Shell from 'gi://Shell';

import { Extension } from 'resource:///org/gnome/shell/extensions/extension.js';
import * as Main from 'resource:///org/gnome/shell/ui/main.js';
import * as PanelMenu from 'resource:///org/gnome/shell/ui/panelMenu.js';
import * as PopupMenu from 'resource:///org/gnome/shell/ui/popupMenu.js';

const DAEMON_NAME = 'dev.local.AntigravityUsage';
const DAEMON_PATH = '/dev/local/AntigravityUsage';
const UI_PATH = '/dev/local/AntigravityUsage/UI';

const DaemonIface = `
<node>
  <interface name="dev.local.AntigravityUsage">
    <method name="GetSources">
      <arg type="a(sssa(sdds)s)" direction="out" name="sources"/>
    </method>
    <method name="Refresh"/>
    <signal name="SourcesChanged"/>
  </interface>
</node>`;

// Test-only hook so automated headless sessions can open the popup menu and
// capture the stage (the D-Bus Screenshot API is allowlist-gated).
const UIIface = `
<node>
  <interface name="dev.local.AntigravityUsage.UI">
    <method name="OpenMenu">
      <arg type="b" direction="in" name="open"/>
    </method>
    <method name="TakeScreenshot">
      <arg type="s" direction="in" name="filename"/>
      <arg type="b" direction="out" name="success"/>
    </method>
  </interface>
</node>`;

const DaemonProxy = Gio.DBusProxy.makeProxyWrapper(DaemonIface);

// GJS D-Bus dispatch wraps in-args in an Array for async methods; unwrap
// single-argument calls so both sync and async handlers get plain values.
function unwrapArg(v) {
    return Array.isArray(v) && v.length === 1 ? v[0] : v;
}

// Progress-bar color by usage: calm under 60%, warning, then alert.
function barColor(pct) {
    if (pct >= 85)
        return '#ea4335';
    if (pct >= 60)
        return '#fbbc04';
    return '#3ddc84';
}

function getProviderIcon(name, extensionPath) {
    // 1. Shipped brand SVG (<extension>/icons/<name>.svg)
    // 2. Theme icon (<name>-symbolic)
    // 3. Shipped generic icon
    if (!name)
        name = 'generic';
    const file = Gio.File.new_for_path(`${extensionPath}/icons/${name}.svg`);
    if (file.query_exists(null))
        return new Gio.FileIcon({ file });
    if (name.endsWith('-symbolic'))
        return new Gio.ThemedIcon({ name });
    const fallback = Gio.File.new_for_path(`${extensionPath}/icons/generic.svg`);
    return new Gio.FileIcon({ file: fallback });
}

// A metric whose detail starts with "error" is a failed poll or parse — it
// carries no numbers, so callers drop it rather than paint a message where
// a bar should be.
function isErrorMetric(metric) {
    const detail = metric[3];
    return !!detail && detail.startsWith('error');
}

const MetricRow = GObject.registerClass(
class MetricRow extends St.BoxLayout {
    _init(metric) {
        super._init({ vertical: true });
        const [label, used, limit, detail] = metric;

        const row = new St.BoxLayout({ vertical: false, style: 'spacing: 8px; margin-top: 6px;' });
        const pct = limit > 0 ? Math.max(0, Math.min(100, used / limit * 100)) : 0;

        row.add_child(new St.Label({
            text: label,
            style: 'width: 84px;',
            style_class: 'aau-metric-label',
            y_align: Clutter.ActorAlign.CENTER,
        }));

        // y_align CENTER is load-bearing: without it the row layout stretches
        // the track to the text height instead of its CSS height.
        const track = new St.BoxLayout({
            style_class: 'aau-metric-track',
            x_expand: true,
            y_align: Clutter.ActorAlign.CENTER,
        });
        if (pct > 0) {
            const fill = new St.Widget({
                y_align: Clutter.ActorAlign.CENTER,
            });
            track.add_child(fill);
            // The fill spans a percentage of the track's REAL allocated width,
            // so the bar reaches the percent column whatever the menu width is.
            let lastPx = -1;
            const apply = () => {
                const w = track.allocation.get_width();
                if (w <= 0)
                    return;
                const px = Math.max(4, Math.round(w * pct / 100));
                if (px === lastPx)
                    return;
                lastPx = px;
                fill.set_style(`width: ${px}px; height: 5px;` +
                    `background-color: ${barColor(pct)}; border-radius: 2px;`);
            };
            track.connect('notify::allocation', apply);
        }
        row.add_child(track);

        row.add_child(new St.Label({
            text: `${Math.round(pct)}%`,
            style: 'width: 46px;',
            style_class: 'aau-metric-pct',
            y_align: Clutter.ActorAlign.CENTER,
        }));

        this.add_child(row);

        if (detail) {
            const detailRow = new St.BoxLayout({
                vertical: false,
                style: 'spacing: 8px; margin-top: 2px;',
            });
            detailRow.add_child(new St.Widget({ style: 'width: 92px;' }));
            const detailLabel = new St.Label({
                text: detail,
                style_class: 'aau-metric-detail',
            });
            detailLabel.get_clutter_text().set_line_wrap(true);
            detailLabel.get_clutter_text().set_line_wrap_mode(Pango.WrapMode.WORD_CHAR);
            detailRow.add_child(detailLabel);
            this._detailRow = detailRow;
        }
    }

    // Detail is appended below the bar by the caller after construction.
    attachDetailTo(container) {
        if (this._detailRow)
            container.add_child(this._detailRow);
    }
});

const SourceRow = GObject.registerClass(
class SourceRow extends PopupMenu.PopupBaseMenuItem {
    _init(source, extensionPath) {
        super._init({ reactive: false, style_class: 'aau-source-row' });

        // D-Bus struct (sssa(sdds)s) unpacks to a plain array.
        const [, label, icon, metrics, email] = source;

        const column = new St.BoxLayout({ vertical: true, x_expand: true });

        // Icon + provider name on the left, account email on the right.
        const header = new St.BoxLayout({ vertical: false, style: 'spacing: 10px;' });
        header.add_child(new St.Icon({
            gicon: getProviderIcon(icon, extensionPath),
            icon_size: 16,
            y_align: Clutter.ActorAlign.CENTER,
        }));
        header.add_child(new St.Label({
            text: label,
            style_class: 'aau-source-name',
            y_align: Clutter.ActorAlign.CENTER,
        }));
        header.add_child(new St.Widget({ x_expand: true }));
        if (email) {
            const emailLabel = new St.Label({
                text: email,
                style_class: 'aau-source-email',
                y_align: Clutter.ActorAlign.CENTER,
            });
            emailLabel.get_clutter_text().set_ellipsize(Pango.EllipsizeMode.END);
            header.add_child(emailLabel);
        }
        column.add_child(header);

        // Failed metrics are dropped, not painted; a source with nothing
        // usable left is skipped by the caller entirely.
        for (const metric of (metrics ?? []).filter(m => !isErrorMetric(m))) {
            const row = new MetricRow(metric);
            column.add_child(row);
            row.attachDetailTo(column);
        }

        this.add_child(column);
    }
});

const AntigravityIndicator = GObject.registerClass(
class AntigravityIndicator extends PanelMenu.Button {
    _init(extension) {
        super._init(0.0, 'AI Usage', false);
        this._extension = extension;
        this._sources = null;
        this._refreshing = false;
        this._fetchPending = false;

        const box = new St.BoxLayout({ style_class: 'panel-status-menu-box' });
        this._icon = new St.Icon({
            gicon: getProviderIcon('generic', extension.path),
            icon_size: 16,
        });
        this._label = new St.Label({
            text: '…',
            y_align: Clutter.ActorAlign.CENTER,
            style_class: 'aau-panel-value',
        });
        box.add_child(this._icon);
        box.add_child(this._label);
        this.add_child(box);

        this.menu.box.add_style_class_name('aau-menu');
        this._rebuildOffline();
        this.menu.connect('open-state-changed', (menu, open) => {
            if (!open) {
                this._hideTooltip();
                return;
            }
            // Freshen the numbers on every popup open, so what you see is
            // current even with a long poll interval.
            if (this._proxy.g_name_owner)
                this._onRefresh();
        });

        this._proxy = new DaemonProxy(Gio.DBus.session, DAEMON_NAME, DAEMON_PATH,
            (proxy, error) => {
                if (error)
                    console.warn(`antigravity-usage: proxy error: ${error}`);
                this._connectProxySignals();
                this._fetchSources();
            });
    }

    _connectProxySignals() {
        if (this._ownerNotifyId)
            return;
        this._ownerNotifyId = this._proxy.connect('notify::g-name-owner', () =>
            this._fetchSources());
        this._signalId = this._proxy.connectSignal('SourcesChanged', () =>
            this._scheduleFetch());
    }

    _scheduleFetch() {
        // Coalesce bursts of SourcesChanged into a single refetch.
        if (this._fetchPending)
            return;
        this._fetchPending = true;
        GLib.timeout_add(GLib.PRIORITY_DEFAULT, 150, () => {
            this._fetchPending = false;
            this._fetchSources();
            return GLib.SOURCE_REMOVE;
        });
    }

    _fetchSources() {
        if (!this._proxy.g_name_owner) {
            this._sources = null;
            this._rebuildOffline();
            this._updatePanel(null);
            return;
        }
        this._proxy.GetSourcesRemote((out, error) => {
            if (error) {
                console.warn(`antigravity-usage: GetSources failed: ${error}`);
                return;
            }
            // makeProxyWrapper hands out-args over as a tuple-array: [sources]
            const sources = Array.isArray(out) ? out[0] : out;
            this._sources = sources ?? [];
            this._refreshing = false;
            this._rebuildMenu();
            this._updatePanel(this._sources[0] ?? null, this._sources);
        });
    }

    _updatePanel(primary, allSources) {
        if (!primary) {
            this._label.text = '…';
            this._icon.gicon = getProviderIcon('generic', this._extension.path);
            return;
        }
        const pickLive = (source) => {
            const metrics = source[3] ?? [];
            const live = metrics.find(([, , limit]) => limit > 0);
            return live ? { source, metric: live } : null;
        };
        // Primary source first; fall back to any source with live data so the
        // badge stays useful while e.g. the Antigravity IDE is closed.
        let pick = pickLive(primary);
        if (!pick && Array.isArray(allSources)) {
            for (const source of allSources) {
                pick = pickLive(source);
                if (pick)
                    break;
            }
        }
        if (!pick) {
            this._label.text = '…';
            this._icon.gicon = getProviderIcon(primary[2] || 'generic', this._extension.path);
            return;
        }
        const [, used, limit] = pick.metric;
        const pct = Math.max(0, Math.min(100, used / limit * 100));
        this._label.text = `${Math.round(pct)}%`;
        this._icon.gicon = getProviderIcon(pick.source[2] || 'generic', this._extension.path);
    }

    _buildFooter(offline) {
        const footer = new PopupMenu.PopupBaseMenuItem({ reactive: false, style_class: 'aau-footer' });

        // No status dot while connected; a red one (with hover tooltip) only
        // when the daemon is unreachable.
        if (offline) {
            const dot = new St.Widget({
                style_class: 'aau-status-dot offline',
                y_align: Clutter.ActorAlign.CENTER,
                reactive: true,
            });
            dot.connect('notify::hover', () => {
                if (dot.hover)
                    this._showTooltip(dot,
                        'Daemon not running — start it with:\nsystemctl --user start antigravity-usage');
                else
                    this._hideTooltip();
            });
            footer.add_child(dot);
        }

        footer.add_child(new St.Widget({ x_expand: true }));

        // Icon-only actions, bottom-right.
        if (!offline) {
            const refreshBtn = new St.Button({
                style_class: 'aau-footer-btn',
                y_align: Clutter.ActorAlign.CENTER,
            });
            // St.Spinner actor was removed in GNOME 48; SpinnerContent is the
            // replacement and needs an explicit-sized actor to paint into.
            refreshBtn.set_child(this._refreshing
                ? new St.Widget({ content: new St.SpinnerContent(), width: 14, height: 14 })
                : new St.Icon({ icon_name: 'view-refresh-symbolic', icon_size: 14 }));
            refreshBtn.connect('clicked', () => this._onRefresh());
            footer.add_child(refreshBtn);
        }

        const configBtn = new St.Button({
            style_class: 'aau-footer-btn',
            y_align: Clutter.ActorAlign.CENTER,
        });
        configBtn.set_child(new St.Icon({
            icon_name: 'preferences-system-symbolic',
            icon_size: 14,
        }));
        configBtn.connect('clicked', () => this._openConfig());
        footer.add_child(configBtn);

        return footer;
    }

    _onRefresh() {
        if (this._refreshing)
            return;
        this._refreshing = true;
        try {
            this._proxy.RefreshSync();
        } catch (e) {
            console.warn(`antigravity-usage: refresh failed: ${e}`);
            this._refreshing = false;
        }
        this._rebuildMenu();
        GLib.timeout_add(GLib.PRIORITY_DEFAULT, 10000, () => {
            if (this._refreshing) {
                this._refreshing = false;
                this._rebuildMenu();
            }
            return GLib.SOURCE_REMOVE;
        });
    }

    _openConfig() {
        const dir = GLib.build_filenamev([GLib.get_user_config_dir(), 'antigravity-usage']);
        try {
            Gio.AppInfo.create_from_commandline(`xdg-open "${dir}"`, null,
                Gio.AppInfoCreateFlags.NONE).launch([], null);
        } catch (e) {
            console.warn(`antigravity-usage: open config failed: ${e}`);
        }
    }

    _rebuildMenu() {
        this._hideTooltip();
        this.menu.removeAll();

        // A source whose every metric failed carries no numbers — omit its
        // section entirely instead of painting an error line.
        const shown = (this._sources ?? []).filter(source =>
            (source[3] ?? []).some(metric => !isErrorMetric(metric)));

        if (shown.length === 0) {
            const empty = new PopupMenu.PopupBaseMenuItem({
                reactive: false,
                style_class: 'aau-source-row',
            });
            empty.add_child(new St.Label({
                text: 'No provider data',
                style_class: 'aau-metric-detail',
            }));
            this.menu.addMenuItem(empty);
        }

        for (const source of shown)
            this.menu.addMenuItem(new SourceRow(source, this._extension.path));

        this.menu.addMenuItem(new PopupMenu.PopupSeparatorMenuItem());
        this.menu.addMenuItem(this._buildFooter(false));
    }

    _rebuildOffline() {
        this._hideTooltip();
        this.menu.removeAll();

        const offline = new PopupMenu.PopupBaseMenuItem({ reactive: false, style_class: 'aau-source-row' });
        const box = new St.BoxLayout({ vertical: true });
        const line1 = new St.Label({ text: 'Daemon not running', style_class: 'aau-source-name' });
        const line2 = new St.Label({
            text: 'systemctl --user start antigravity-usage',
            style_class: 'aau-metric-detail',
        });
        box.add_child(line1);
        box.add_child(line2);
        offline.add_child(box);
        this.menu.addMenuItem(offline);

        this.menu.addMenuItem(new PopupMenu.PopupSeparatorMenuItem());
        this.menu.addMenuItem(this._buildFooter(true));
    }

    _setMenuOpen(open) {
        if (open)
            this.menu.open(false);
        else
            this.menu.close(false);
    }

    _showTooltip(actor, text) {
        this._hideTooltip();
        const tip = new St.Label({ text, style_class: 'aau-tooltip' });
        Main.uiGroup.add_child(tip);
        const [ax, ay] = actor.get_transformed_position();
        const [, ah] = actor.get_transformed_size();
        tip.set_position(Math.max(8, Math.round(ax - 150)), Math.round(ay + ah + 10));
        tip.raise_top();
        this._tooltip = tip;
    }

    _hideTooltip() {
        this._tooltip?.destroy();
        this._tooltip = null;
    }

    destroy() {
        this._hideTooltip();
        if (this._ownerNotifyId) {
            this._proxy.disconnect(this._ownerNotifyId);
            this._ownerNotifyId = null;
        }
        if (this._signalId) {
            this._proxy.disconnectSignal(this._signalId);
            this._signalId = null;
        }
        super.destroy();
    }
});

export default class AntigravityUsageExtension extends Extension {
    enable() {
        this._indicator = new AntigravityIndicator(this);
        Main.panel.addToStatusArea(this.uuid, this._indicator);

        // Test-only: let automated headless sessions open the popup menu and
        // capture the stage (the D-Bus Screenshot API is allowlist-gated).
        this._ui = Gio.DBusExportedObject.wrapJSObject(UIIface, {
            OpenMenu: open => this._indicator._setMenuOpen(unwrapArg(open)),
            TakeScreenshotAsync: (filename, invocation) => {
                filename = unwrapArg(filename);
                const file = Gio.File.new_for_path(filename);
                let stream;
                try {
                    stream = file.replace(null, false, Gio.FileCreateFlags.NONE, null);
                } catch (e) {
                    invocation.return_gerror(e);
                    return;
                }
                const shot = new Shell.Screenshot();
                Promise.resolve(shot.screenshot(false, stream))
                    .then(() => {
                        stream.close(null);
                        invocation.return_value(new GLib.Variant('(b)', [true]));
                    })
                    .catch(e => {
                        console.warn(`antigravity-usage: debug screenshot failed: ${e}`);
                        stream.close(null);
                        invocation.return_value(new GLib.Variant('(b)', [false]));
                    });
            },
        });
        this._ui.export(Gio.DBus.session, UI_PATH);
    }

    disable() {
        this._ui?.unexport();
        this._ui = null;
        this._indicator?.destroy();
        this._indicator = null;
    }
}
