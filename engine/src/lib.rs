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

/// The build version: `NEXORA_VERSION` at compile time (the image tag, set by the Dockerfile),
/// `dev` for an unstamped build. Reported by `--version` and to the management plane.
pub const VERSION: &str = match option_env!("NEXORA_VERSION") {
    Some(v) => v,
    None => "dev",
};
