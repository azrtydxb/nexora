//! Authoritative service for zones hosted in Nexora: the NZF1 zone format and the in-memory zone
//! model (M4). Answering and runtime wiring build on these modules.

pub mod answer;
pub mod dispatch;
pub mod loader;
pub mod lookup;
pub mod msg;
pub mod name;
pub mod nzf;
pub mod set;
pub mod state;
pub mod writer;
pub mod zone;

pub use state::after_apply;

#[cfg(test)]
mod answer_tests;
#[cfg(test)]
mod loader_tests;
#[cfg(test)]
mod zone_tests;

pub const T_A: u16 = 1;
pub const T_NS: u16 = 2;
pub const T_CNAME: u16 = 5;
pub const T_SOA: u16 = 6;
pub const T_PTR: u16 = 12;
pub const T_MX: u16 = 15;
pub const T_TXT: u16 = 16;
pub const T_AAAA: u16 = 28;
pub const T_LOC: u16 = 29;
pub const T_SRV: u16 = 33;
pub const T_NAPTR: u16 = 35;
pub const T_DNAME: u16 = 39;
pub const T_OPT: u16 = 41;
pub const T_DS: u16 = 43;
pub const T_SSHFP: u16 = 44;
pub const T_RRSIG: u16 = 46;
pub const T_NSEC: u16 = 47;
pub const T_DNSKEY: u16 = 48;
pub const T_NSEC3: u16 = 50;
pub const T_NSEC3PARAM: u16 = 51;
pub const T_TLSA: u16 = 52;
pub const T_CDS: u16 = 59;
pub const T_CDNSKEY: u16 = 60;
pub const T_SVCB: u16 = 64;
pub const T_HTTPS: u16 = 65;
pub const T_TSIG: u16 = 250;
pub const T_IXFR: u16 = 251;
pub const T_AXFR: u16 = 252;
pub const T_ANY: u16 = 255;
pub const T_CAA: u16 = 257;
