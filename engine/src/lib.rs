// The crate's `eprintln!` shadows std's, so every existing stderr line also reaches the engine log
// ring buffer (`telemetry::logbuf`). Declared before every `mod` so textual scoping applies crate-wide.
#[macro_export]
macro_rules! eprintln {
    ($($arg:tt)*) => { $crate::telemetry::logbuf::emit(None, format_args!($($arg)*)) };
}
#[macro_export]
macro_rules! log_error { ($($arg:tt)*) => { $crate::telemetry::logbuf::emit(Some($crate::proto::LogLevel::Error), format_args!($($arg)*)) }; }
#[macro_export]
macro_rules! log_warn { ($($arg:tt)*) => { $crate::telemetry::logbuf::emit(Some($crate::proto::LogLevel::Warn), format_args!($($arg)*)) }; }
#[macro_export]
macro_rules! log_info { ($($arg:tt)*) => { $crate::telemetry::logbuf::emit(Some($crate::proto::LogLevel::Info), format_args!($($arg)*)) }; }
#[macro_export]
macro_rules! log_debug { ($($arg:tt)*) => { $crate::telemetry::logbuf::emit(Some($crate::proto::LogLevel::Debug), format_args!($($arg)*)) }; }

pub mod acl;
pub mod authoritative;
pub mod bootstrap;
pub mod cache;
pub mod cert_renewal;
pub mod clock;
pub mod control;
pub mod edns;
pub mod filter;
pub mod inflight;
pub mod lifecycle;
pub mod mdns;
pub mod proto;
pub mod recursor;
pub mod runtime;
pub mod server;
pub mod snapshot;
pub mod snapshot_m3;
pub mod statefs;
pub mod telemetry;
pub mod tsig;
#[cfg(test)]
mod tsig_tests;
pub mod upstream;
pub mod wire;
pub mod zonemd;

/// The build version: `NEXORA_VERSION` at compile time (the image tag, set by the Dockerfile),
/// `dev` for an unstamped build. Reported by `--version` and to the management plane.
pub const VERSION: &str = match option_env!("NEXORA_VERSION") {
    Some(v) => v,
    None => "dev",
};

/// The full commit hash: `NEXORA_COMMIT` at compile time, empty for an unstamped build.
pub const COMMIT: &str = match option_env!("NEXORA_COMMIT") {
    Some(v) => v,
    None => "",
};
