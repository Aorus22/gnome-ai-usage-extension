//! zbus client for the usage daemon. The D-Bus contract lives in
//! daemon/dbus.go and daemon/manager.go: GetSources returns a(sssa(sdds)s),
//! Refresh takes nothing, SourcesChanged carries no arguments.

use std::time::Duration;

use cosmic::iced::futures::channel::mpsc::Sender;
use cosmic::iced::futures::{FutureExt, SinkExt, StreamExt};
use serde::Deserialize;
use zbus::names::BusName;
use zbus::zvariant::Type;

use crate::app::Message;

const NAME: &str = "dev.local.AntigravityUsage";
// Object path /dev/local/AntigravityUsage is declared on the proxy below.

/// One progress bar under a source: (label, used, limit, detail).
#[derive(Debug, Clone, Deserialize, Type)]
#[zvariant(signature = "(sdds)")]
pub struct Metric {
    pub label: String,
    pub used: f64,
    pub limit: f64,
    pub detail: String,
}

/// One source: (id, label, icon, metrics, email) — primary source first.
#[derive(Debug, Clone, Deserialize, Type)]
pub struct Source {
    #[allow(dead_code)] // part of the a(sssa(sdds)s) contract; UI uses label/icon
    pub id: String,
    pub label: String,
    pub icon: String,
    pub metrics: Vec<Metric>,
    pub email: String,
}

#[zbus::proxy(
    interface = "dev.local.AntigravityUsage",
    default_service = "dev.local.AntigravityUsage",
    default_path = "/dev/local/AntigravityUsage"
)]
trait AntigravityUsage {
    /// All sources, primary first.
    fn get_sources(&self) -> zbus::Result<Vec<Source>>;

    /// Trigger an immediate poll of every source.
    fn refresh(&self) -> zbus::Result<()>;

    /// Emitted by the daemon after every poll (no arguments).
    #[zbus(signal)]
    fn sources_changed(&self) -> zbus::Result<()>;
}

/// State changes pushed from the daemon to the applet.
#[derive(Debug, Clone)]
pub enum DaemonMsg {
    Sources(Vec<Source>),
    Offline,
}

/// Long-lived subscription task: follows the daemon across restarts and
/// forwards snapshots/online state to the applet.
pub async fn run(mut out: Sender<Message>) {
    loop {
        if let Err(error) = drive(&mut out).await {
            tracing::warn!("daemon connection failed: {error}");
        }
        let _ = out.send(Message::Daemon(DaemonMsg::Offline)).await;
        tokio::time::sleep(Duration::from_secs(2)).await;
    }
}

async fn drive(out: &mut Sender<Message>) -> Result<(), zbus::Error> {
    let conn = zbus::Connection::session().await?;
    let dbus = zbus::fdo::DBusProxy::new(&conn).await?;
    let proxy = AntigravityUsageProxy::new(&conn).await?;

    let mut changed = proxy.receive_sources_changed().await?;
    let mut owners = dbus
        .receive_name_owner_changed_with_args(&[(0, NAME)])
        .await?;

    // Initial state: the daemon may already be on the bus.
    let name = BusName::try_from(NAME).expect("valid bus name");
    if dbus.get_name_owner(name).await.is_ok() {
        fetch(&proxy, out).await;
    } else {
        let _ = out.send(Message::Daemon(DaemonMsg::Offline)).await;
    }

    loop {
        tokio::select! {
            signal = changed.next() => {
                if signal.is_none() {
                    return Ok(());
                }
                // Coalesce bursts of SourcesChanged into a single refetch:
                // wait out the window, then drop everything queued meanwhile.
                tokio::time::sleep(Duration::from_millis(150)).await;
                loop {
                    match changed.next().now_or_never() {
                        Some(Some(_)) => {}
                        Some(None) => return Ok(()),
                        None => break,
                    }
                }
                fetch(&proxy, out).await;
            }
            event = owners.next() => {
                let Some(event) = event else { return Ok(()); };
                let gone = event
                    .args()
                    .map(|args| optional_is_none(args.new_owner))
                    .unwrap_or(true);
                if gone {
                    let _ = out.send(Message::Daemon(DaemonMsg::Offline)).await;
                } else {
                    // Daemon appeared or restarted — fetch immediately.
                    fetch(&proxy, out).await;
                }
            }
        }
    }
}

/// One snapshot; a fetch error keeps the last known state on screen.
async fn fetch(proxy: &AntigravityUsageProxy<'_>, out: &mut Sender<Message>) {
    match proxy.get_sources().await {
        Ok(sources) => {
            let _ = out.send(Message::Daemon(DaemonMsg::Sources(sources))).await;
        }
        Err(error) => tracing::warn!("GetSources failed: {error}"),
    }
}

/// Ask the daemon to poll all sources now. Best-effort: an unreachable
/// daemon just leaves the applet state unchanged.
pub async fn trigger_refresh() -> Result<(), zbus::Error> {
    let conn = zbus::Connection::session().await?;
    let proxy = AntigravityUsageProxy::new(&conn).await?;
    proxy.refresh().await
}

fn optional_is_none<T>(value: zbus::zvariant::Optional<T>) -> bool {
    Option::<T>::from(value).is_none()
}
