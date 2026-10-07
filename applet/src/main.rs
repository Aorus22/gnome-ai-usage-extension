//! COSMIC panel applet for the antigravity-usage daemon (dev.local.AntigravityUsage).
mod app;
mod daemon;

fn main() -> cosmic::iced::Result {
    tracing_subscriber::fmt()
        .with_env_filter(tracing_subscriber::EnvFilter::from_default_env())
        .init();

    // Starts the applet's event loop with `()` as the application's flags.
    cosmic::applet::run::<app::App>(())
}
