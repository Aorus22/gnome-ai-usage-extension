//! Applet model: panel badge + popup, mirroring the GNOME extension's behavior
//! (extension/extension.js) with COSMIC theming.

use std::sync::LazyLock;
use std::time::Duration;

use cosmic::iced::alignment::{Alignment, Horizontal, Vertical};
use cosmic::iced::platform_specific::shell::wayland::commands::popup::{destroy_popup, get_popup};
use cosmic::iced::window::Id;
use cosmic::iced::{Length, Limits, Subscription};
use cosmic::widget::autosize::autosize;
use cosmic::widget::space::horizontal;
use cosmic::{Action, Application, Element, Task, widget};

use crate::daemon::{self, DaemonMsg, Metric, Source};

const POPUP_WIDTH: f32 = 360.0; // popup_container's fixed width
const CONTENT_PADDING: f32 = 12.0;
const LABEL_WIDTH: f32 = 84.0;
const PCT_WIDTH: f32 = 46.0;
const ROW_SPACING: f32 = 8.0;
// The bar track gets whatever is left of the fixed popup width.
const TRACK_WIDTH: f32 =
    POPUP_WIDTH - 2.0 * CONTENT_PADDING - LABEL_WIDTH - PCT_WIDTH - 2.0 * ROW_SPACING;

const OFFLINE_HINT: &str = "systemctl --user start antigravity-usage";
const TOOLTIP_TEXT: &str =
    "Daemon not running — start it with:\nsystemctl --user start antigravity-usage";

// Brand icons shared with the GNOME extension (icons/ at the repo root).
const GENERIC_SVG: &[u8] = include_bytes!("../../icons/generic.svg");
const ANTIGRAVITY_SVG: &[u8] = include_bytes!("../../icons/antigravity.svg");
const CODEX_SVG: &[u8] = include_bytes!("../../icons/codex.svg");
const COREWEAVE_SVG: &[u8] = include_bytes!("../../icons/coreweave.svg");

// The applet surface is pinned to the badge size in init (the panel only
// honors resizes on compositor configure events), with the label in a fixed
// width so the badge never outgrows it; autosize is kept as a harmless
// fallback like cosmic-applet-time.
static AUTOSIZE_MAIN_ID: LazyLock<cosmic::widget::Id> =
    LazyLock::new(|| cosmic::widget::Id::new("autosize-main"));

pub struct App {
    core: cosmic::Core,
    /// Panel icon size, captured before the surface size is pinned: with a
    /// hardcoded applet size, suggested_size reports the badge, not the icon.
    icon_size: (u16, u16),
    popup: Option<Id>,
    online: bool,
    sources: Option<Vec<Source>>,
    refreshing: bool,
}

#[derive(Debug, Clone)]
pub enum Message {
    TogglePopup,
    PopupClosed(Id),
    Daemon(DaemonMsg),
    Refresh,
    RefreshTimeout,
    OpenConfig,
}

impl Application for App {
    type Executor = cosmic::executor::Default;
    type Flags = ();
    type Message = Message;

    const APP_ID: &'static str = "dev.local.AntigravityUsageApplet";

    fn core(&self) -> &cosmic::Core {
        &self.core
    }

    fn core_mut(&mut self) -> &mut cosmic::Core {
        &mut self.core
    }

    fn init(mut core: cosmic::Core, _flags: Self::Flags) -> (Self, Task<Action<Message>>) {
        // Pin the applet surface to the badge size up front. The panel only
        // honors applet resizes on compositor configure events, so a surface
        // created icon-sized would stay icon-sized and clip the label.
        let icon_size = core.applet.suggested_size(false);
        let badge_w = icon_size.0 + 4 + (f32::from(icon_size.0) * 1.75).round() as u16;
        core.applet.window_size(badge_w, icon_size.1);
        (
            App {
                core,
                icon_size,
                popup: None,
                online: false,
                sources: None,
                refreshing: false,
            },
            Task::none(),
        )
    }

    fn on_close_requested(&self, id: Id) -> Option<Message> {
        Some(Message::PopupClosed(id))
    }

    /// Panel button: brand icon of the live source + its percent.
    fn view(&self) -> Element<'_, Message> {
        let (icon_bytes, label) = self.panel_badge();
        let (icon_w, icon_h) = self.icon_size;
        // Flat AppletIcon style with the panel's suggested padding, like
        // applet.text_button, but sized to the full icon+percent row.
        let (major_pad, minor_pad) = self.core.applet.suggested_padding(true);
        let (hp, vp) = if self.core.applet.is_horizontal() {
            (major_pad, minor_pad)
        } else {
            (minor_pad, major_pad)
        };
        // The label sits in a width reserved off the icon size, so the badge's
        // natural size never changes with the data and the pinned surface
        // always fits it.
        let label = widget::container(self.core.applet.text(label))
            .width(Length::Fixed(f32::from(icon_w) * 1.75))
            .align_x(Horizontal::Center);
        let badge = widget::row::with_capacity(2)
            .spacing(4)
            .align_y(Alignment::Center)
            .push(
                widget::icon(widget::icon::from_svg_bytes(icon_bytes))
                    .width(Length::Fixed(f32::from(icon_w)))
                    .height(Length::Fixed(f32::from(icon_h))),
            )
            .push(label);
        let button = widget::button::custom(badge)
            .padding([vp, hp])
            .class(cosmic::theme::Button::AppletIcon)
            .on_press_down(Message::TogglePopup);
        autosize(button, AUTOSIZE_MAIN_ID.clone()).into()
    }

    /// Popup content, drawn once per popup window id.
    fn view_window(&self, id: Id) -> Element<'_, Message> {
        if self.popup.as_ref() != Some(&id) {
            return horizontal().width(Length::Fixed(1.0)).into();
        }

        let mut content = widget::column::with_capacity(2).spacing(8);
        if !self.online {
            content = content
                .push(widget::text::title4("Daemon not running"))
                .push(widget::text::caption(OFFLINE_HINT));
        } else {
            let sources = self.sources.as_deref().unwrap_or(&[]);
            // A source whose every metric failed carries no numbers — omit it.
            let shown: Vec<&Source> = sources
                .iter()
                .filter(|s| s.metrics.iter().any(|m| !is_error_metric(m)))
                .collect();
            if shown.is_empty() {
                content = content.push(widget::text::caption("No provider data"));
            }
            for source in shown {
                content = content.push(source_view(source));
            }
        }
        content = content.push(footer(self.online, self.refreshing));

        // No custom background here: popup_container already paints the
        // theme's frosted layer (transparent + compositor blur), like the
        // calendar applet. An opaque inner container would cover that blur.
        let content = widget::container(content)
            .width(Length::Fill)
            .padding(CONTENT_PADDING);

        self.core.applet.popup_container(content).into()
    }

    fn subscription(&self) -> Subscription<Message> {
        // The daemon task already emits app Messages.
        Subscription::run(|| {
            cosmic::iced::stream::channel(8, |out| async move {
                daemon::run(out).await;
            })
        })
    }

    fn update(&mut self, message: Message) -> Task<Action<Message>> {
        match message {
            Message::Daemon(DaemonMsg::Sources(sources)) => {
                self.online = true;
                self.sources = Some(sources);
                self.refreshing = false;
            }
            Message::Daemon(DaemonMsg::Offline) => {
                self.online = false;
                self.sources = None;
                self.refreshing = false;
            }
            Message::Refresh => return self.refresh_task(),
            Message::RefreshTimeout => self.refreshing = false,
            Message::TogglePopup => {
                return if let Some(popup) = self.popup.take() {
                    destroy_popup(popup)
                } else {
                    let new_id = Id::unique();
                    self.popup.replace(new_id);
                    let mut popup_settings = self.core.applet.get_popup_settings(
                        self.core.main_window_id().unwrap(),
                        new_id,
                        None,
                        None,
                        None,
                    );
                    popup_settings.positioner.size_limits = Limits::NONE
                        .min_width(POPUP_WIDTH)
                        .max_width(POPUP_WIDTH)
                        .min_height(1.0)
                        .max_height(1080.0);
                    let open = get_popup(popup_settings);
                    // Freshen the numbers on every popup open.
                    if self.online {
                        let refresh = self.refresh_task();
                        return Task::batch([open, refresh]);
                    }
                    open
                };
            }
            Message::PopupClosed(id) => {
                if self.popup.as_ref() == Some(&id) {
                    self.popup = None;
                }
            }
            Message::OpenConfig => {
                let dir = config_dir();
                return Task::future(async move {
                    let _ = std::process::Command::new("xdg-open").arg(dir).spawn();
                    Action::<Message>::None
                });
            }
        }
        Task::none()
    }

    fn style(&self) -> Option<cosmic::iced::theme::Style> {
        Some(cosmic::applet::style())
    }
}

impl App {
    /// Ask the daemon to poll all sources now, with a 10s spinner guard.
    fn refresh_task(&mut self) -> Task<Action<Message>> {
        if self.refreshing {
            return Task::none();
        }
        self.refreshing = true;
        Task::batch([
            // The daemon emits SourcesChanged once its polls complete.
            Task::perform(daemon::trigger_refresh(), |result| {
                if let Err(error) = result {
                    tracing::warn!("refresh failed: {error}");
                }
                Action::<Message>::None
            }),
            Task::perform(tokio::time::sleep(Duration::from_secs(10)), |_| {
                Action::App(Message::RefreshTimeout)
            }),
        ])
    }

    /// (icon bytes, label) for the panel: percent of the first live metric of
    /// the primary source, falling back to any source with live data so the
    /// badge stays useful while e.g. the IDE is closed.
    fn panel_badge(&self) -> (&'static [u8], String) {
        let sources = self.sources.as_deref().unwrap_or(&[]);
        let pick = sources
            .first()
            .and_then(first_live)
            .or_else(|| sources.iter().find_map(first_live));
        match pick {
            Some((source, metric)) => {
                let pct = (metric.used / metric.limit * 100.0).clamp(0.0, 100.0);
                (icon_bytes(&source.icon), format!("{}%", pct.round()))
            }
            // No live data: keep the primary's icon (or generic) with an ellipsis.
            None => match sources.first() {
                Some(source) => (icon_bytes(&source.icon), "…".to_string()),
                None => (GENERIC_SVG, "…".to_string()),
            },
        }
    }
}

/// First metric with a limit, i.e. one that carries usable numbers.
fn first_live(source: &Source) -> Option<(&Source, &Metric)> {
    source
        .metrics
        .iter()
        .find(|metric| metric.limit > 0.0)
        .map(|metric| (source, metric))
}

fn icon_bytes(name: &str) -> &'static [u8] {
    match name {
        "antigravity" => ANTIGRAVITY_SVG,
        "codex" => CODEX_SVG,
        "coreweave" => COREWEAVE_SVG,
        _ => GENERIC_SVG,
    }
}

/// A metric whose detail starts with "error" is a failed poll or parse —
/// it carries no numbers, so callers drop it rather than paint a message
/// where a bar should be.
fn is_error_metric(metric: &Metric) -> bool {
    metric.detail.starts_with("error")
}

/// Progress-bar color by usage: calm under 60%, warning, then alert.
fn bar_color(pct: f32) -> cosmic::iced::Color {
    if pct >= 85.0 {
        cosmic::iced::Color::from_rgb8(0xea, 0x43, 0x35)
    } else if pct >= 60.0 {
        cosmic::iced::Color::from_rgb8(0xfb, 0xbc, 0x04)
    } else {
        cosmic::iced::Color::from_rgb8(0x3d, 0xdc, 0x84)
    }
}

fn config_dir() -> std::path::PathBuf {
    let base = match std::env::var_os("XDG_CONFIG_HOME") {
        Some(dir) if !dir.is_empty() => std::path::PathBuf::from(dir),
        _ => std::env::var_os("HOME")
            .map(std::path::PathBuf::from)
            .unwrap_or_default()
            .join(".config"),
    };
    base.join("antigravity-usage")
}

fn truncate_ellipsis(text: &str, max: usize) -> String {
    if text.chars().count() <= max {
        return text.to_string();
    }
    let cut: String = text.chars().take(max).collect();
    format!("{cut}…")
}

fn source_view(source: &Source) -> Element<'_, Message> {
    let header = widget::row::with_capacity(4)
        .spacing(8)
        .push(
            widget::icon(widget::icon::from_svg_bytes(icon_bytes(&source.icon)))
                .width(Length::Fixed(16.0))
                .height(Length::Fixed(16.0)),
        )
        .push(widget::text(source.label.clone()).font(cosmic::font::semibold()))
        .push(horizontal().width(Length::Fill))
        .push_maybe(
            (!source.email.is_empty())
                .then(|| widget::text::caption(truncate_ellipsis(&source.email, 28))),
        );

    let mut column = widget::column::with_capacity(source.metrics.len() + 1)
        .spacing(2)
        .push(header);
    // Failed metrics are dropped, not painted.
    for metric in source.metrics.iter().filter(|m| !is_error_metric(m)) {
        column = column.push(metric_row(metric));
        if !metric.detail.is_empty() {
            // Indent details under the bar track, as in the extension.
            column = column.push(
                widget::row::with_capacity(2)
                    .push(horizontal().width(Length::Fixed(LABEL_WIDTH + ROW_SPACING)))
                    .push(widget::text::caption(metric.detail.clone())),
            );
        }
    }
    widget::container(column).width(Length::Fill).into()
}

fn metric_row(metric: &Metric) -> Element<'_, Message> {
    let pct = if metric.limit > 0.0 {
        ((metric.used / metric.limit * 100.0) as f32).clamp(0.0, 100.0)
    } else {
        0.0
    };
    let fill_px = if pct > 0.0 {
        ((TRACK_WIDTH * pct / 100.0).round() as u16).max(4)
    } else {
        0
    };

    let fill = widget::container(horizontal().height(Length::Fixed(0.0)))
        .width(Length::Fixed(f32::from(fill_px)))
        .height(Length::Fill)
        .style(move |_| widget::container::Style {
            background: Some(bar_color(pct).into()),
            border: cosmic::iced::Border {
                radius: 2.0.into(),
                ..Default::default()
            },
            ..Default::default()
        });

    let track = widget::container(fill)
        .width(Length::Fill)
        .height(Length::Fixed(5.0))
        .align_x(Alignment::Start)
        .align_y(Vertical::Center)
        .style(|theme: &cosmic::Theme| widget::container::Style {
            background: Some(theme.cosmic().palette.neutral_5.into()),
            border: cosmic::iced::Border {
                radius: 2.0.into(),
                ..Default::default()
            },
            ..Default::default()
        });

    widget::row::with_capacity(3)
        .spacing(ROW_SPACING)
        .push(
            widget::text(metric.label.clone())
                .width(Length::Fixed(LABEL_WIDTH))
                .align_y(Vertical::Center),
        )
        .push(track)
        .push(
            widget::text(format!("{}%", pct.round()))
                .width(Length::Fixed(PCT_WIDTH))
                .font(cosmic::font::semibold()),
        )
        .align_y(Alignment::Center)
        .into()
}

fn footer(online: bool, refreshing: bool) -> Element<'static, Message> {
    let mut footer = widget::row::with_capacity(3).spacing(6);
    // A red dot (with hover tooltip) only when the daemon is unreachable.
    if !online {
        let dot = widget::container(horizontal())
            .width(Length::Fixed(9.0))
            .height(Length::Fixed(9.0))
            .style(|_| widget::container::Style {
                background: Some(cosmic::iced::Color::from_rgb8(0xea, 0x43, 0x35).into()),
                border: cosmic::iced::Border {
                    radius: 5.0.into(),
                    ..Default::default()
                },
                ..Default::default()
            });
        footer = footer.push(widget::tooltip(
            dot,
            widget::text::caption(TOOLTIP_TEXT),
            widget::tooltip::Position::Top,
        ));
    }
    footer = footer.push(horizontal().width(Length::Fill));
    if online {
        let refresh: Element<'static, Message> = if refreshing {
            widget::button::custom(widget::text("…"))
                .padding([4.0, 6.0])
                .on_press(Message::Refresh)
                .into()
        } else {
            widget::button::icon(
                widget::icon::from_name("view-refresh-symbolic")
                    .size(14)
                    .symbolic(true),
            )
            .on_press(Message::Refresh)
            .into()
        };
        footer = footer.push(refresh);
    }
    let config = widget::button::icon(
        widget::icon::from_name("preferences-system-symbolic")
            .size(14)
            .symbolic(true),
    )
    .on_press(Message::OpenConfig);
    footer = footer.push(config);
    footer.into()
}
