//! RFC 5155 NSEC3 owner hashing (SHA-1) and the base32hex owner label. Allocation-free.

use ring::digest::{Context, SHA1_FOR_LEGACY_USE_ONLY};

/// NSEC3PARAM hash algorithm SHA-1, the only one defined.
const ALG_SHA1: u8 = 1;
const ALPHABET: &[u8; 32] = b"0123456789abcdefghijklmnopqrstuv";

/// Hash parameters from the zone's NSEC3PARAM RDATA.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Params<'a> {
    pub iterations: u16,
    pub salt: &'a [u8],
}

impl<'a> Params<'a> {
    /// Parses NSEC3PARAM RDATA (algorithm, flags, iterations, salt length, salt); `None` for an
    /// unknown hash algorithm or malformed RDATA.
    pub fn parse(rdata: &'a [u8]) -> Option<Params<'a>> {
        let (&alg, rest) = rdata.split_first()?;
        let salt_len = usize::from(*rest.get(3)?);
        let salt = rest.get(4..4 + salt_len)?;
        (alg == ALG_SHA1 && rest.len() == 4 + salt_len).then(|| Params {
            iterations: u16::from_be_bytes([rest[1], rest[2]]),
            salt,
        })
    }
}

fn sha1(a: &[u8], b: &[u8]) -> [u8; 20] {
    let mut c = Context::new(&SHA1_FOR_LEGACY_USE_ONLY);
    c.update(a);
    c.update(b);
    let mut out = [0u8; 20];
    out.copy_from_slice(c.finish().as_ref());
    out
}

/// IH(salt, name, iterations); `lower_wire_name` is the lowercase uncompressed wire name.
pub fn hash(lower_wire_name: &[u8], iterations: u16, salt: &[u8]) -> [u8; 20] {
    let mut out = sha1(lower_wire_name, salt);
    for _ in 0..iterations {
        out = sha1(&out, salt);
    }
    out
}

/// Lowercase unpadded base32hex (RFC 4648 §7) of a SHA-1 hash.
pub fn b32hex(hash: &[u8; 20]) -> [u8; 32] {
    let mut out = [0u8; 32];
    let mut bits: u64 = 0;
    let (mut nbits, mut o) = (0u32, 0usize);
    for &b in hash {
        bits = (bits << 8) | u64::from(b);
        nbits += 8;
        while nbits >= 5 {
            nbits -= 5;
            out[o] = ALPHABET[((bits >> nbits) & 31) as usize];
            o += 1;
        }
    }
    out
}

/// Decodes a 32-character base32hex label (any case).
pub fn decode_b32hex(label: &[u8]) -> Option<[u8; 20]> {
    if label.len() != 32 {
        return None;
    }
    let mut out = [0u8; 20];
    let mut bits: u64 = 0;
    let (mut nbits, mut o) = (0u32, 0usize);
    for &c in label {
        let v = match c.to_ascii_lowercase() {
            d @ b'0'..=b'9' => d - b'0',
            l @ b'a'..=b'v' => l - b'a' + 10,
            _ => return None,
        };
        bits = (bits << 5) | u64::from(v);
        nbits += 5;
        if nbits >= 8 {
            nbits -= 8;
            out[o] = (bits >> nbits) as u8;
            o += 1;
        }
    }
    Some(out)
}
