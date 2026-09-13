//! Zero-copy parser for NZF1 zone images and deltas (layout: `.procoder/plans/nexora-v1-m4.md`,
//! Task 1). Input comes from blobs fetched from the management plane and is parsed defensively:
//! every length is bounds-checked before use and nothing is allocated from an unchecked count.

use std::io::Read;

use super::name::is_subdomain;

const MAGIC: &[u8; 4] = b"NZF1";
/// owner_len + 1-octet owner + type + class + ttl + rdlen.
const MIN_RECORD: usize = 11;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct RecordRef<'a> {
    pub owner: &'a [u8],
    pub rtype: u16,
    pub class: u16,
    pub ttl: u32,
    pub rdata: &'a [u8],
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Kind {
    Full,
    Delta,
}

/// A parsed NZF1 buffer. Full: `serial` is the image serial, `a` all records, `b` empty.
/// Delta: `serial` is the target serial, `a` the deleted and `b` the added records.
#[derive(Debug)]
pub struct Parsed<'a> {
    pub kind: Kind,
    pub origin: &'a [u8],
    pub serial: u32,
    pub from_serial: u32,
    pub a: Vec<RecordRef<'a>>,
    pub b: Vec<RecordRef<'a>>,
}

#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum NzfError {
    #[error("nzf: truncated")]
    Truncated,
    #[error("nzf: bad magic")]
    BadMagic,
    #[error("nzf: bad kind or reserved octet")]
    BadKind,
    #[error("nzf: malformed name")]
    BadName,
    #[error("nzf: owner outside the zone origin")]
    OutOfZone,
    #[error("nzf: malformed record header")]
    BadRecord,
    #[error("nzf: trailing octets")]
    Trailing,
    #[error("nzf: record count or size too large")]
    TooLarge,
    #[error("nzf: zstd: {0}")]
    Zstd(String),
}

struct Reader<'a> {
    buf: &'a [u8],
    off: usize,
}

impl<'a> Reader<'a> {
    fn take(&mut self, n: usize) -> Result<&'a [u8], NzfError> {
        if self.buf.len() - self.off < n {
            return Err(NzfError::Truncated);
        }
        let s = &self.buf[self.off..self.off + n];
        self.off += n;
        Ok(s)
    }

    fn u8(&mut self) -> Result<u8, NzfError> {
        Ok(self.take(1)?[0])
    }

    fn u16(&mut self) -> Result<u16, NzfError> {
        let s = self.take(2)?;
        Ok(u16::from_be_bytes([s[0], s[1]]))
    }

    fn u32(&mut self) -> Result<u32, NzfError> {
        let s = self.take(4)?;
        Ok(u32::from_be_bytes([s[0], s[1], s[2], s[3]]))
    }

    fn name(&mut self) -> Result<&'a [u8], NzfError> {
        let len = self.u8()? as usize;
        let name = self.take(len)?;
        check_name(name)?;
        Ok(name)
    }

    fn record(&mut self, origin: &[u8]) -> Result<RecordRef<'a>, NzfError> {
        let owner = self.name()?;
        if !is_subdomain(owner, origin) {
            return Err(NzfError::OutOfZone);
        }
        let rtype = self.u16()?;
        let class = self.u16()?;
        if class != 1 {
            return Err(NzfError::BadRecord);
        }
        let ttl = self.u32()?;
        let rdlen = self.u16()? as usize;
        let rdata = self.take(rdlen)?;
        Ok(RecordRef {
            owner,
            rtype,
            class,
            ttl,
            rdata,
        })
    }
}

/// Requires `name` to be exactly one uncompressed wire name: label lengths < 0x40, at most 255
/// octets, ending with the root label at its last octet.
fn check_name(name: &[u8]) -> Result<(), NzfError> {
    if name.is_empty() || name.len() > 255 {
        return Err(NzfError::BadName);
    }
    let mut i = 0usize;
    loop {
        let Some(&l) = name.get(i) else {
            return Err(NzfError::BadName);
        };
        if l == 0 {
            return if i + 1 == name.len() {
                Ok(())
            } else {
                Err(NzfError::BadName)
            };
        }
        if l >= 0x40 {
            return Err(NzfError::BadName);
        }
        i += l as usize + 1;
    }
}

pub fn parse(buf: &[u8]) -> Result<Parsed<'_>, NzfError> {
    let mut r = Reader { buf, off: 0 };
    if r.take(4)? != MAGIC {
        return Err(NzfError::BadMagic);
    }
    let kind = match r.u8()? {
        1 => Kind::Full,
        2 => Kind::Delta,
        _ => return Err(NzfError::BadKind),
    };
    if r.u8()? != 0 {
        return Err(NzfError::BadKind);
    }
    let origin = r.name()?;
    if origin.len() < 2 {
        return Err(NzfError::BadName);
    }
    let serial = r.u32()?;
    let from_serial = r.u32()?;
    let count_a = r.u32()? as usize;
    let count_b = r.u32()? as usize;
    if count_a.saturating_add(count_b) > (buf.len() - r.off) / MIN_RECORD {
        return Err(NzfError::TooLarge);
    }
    if kind == Kind::Full && (from_serial != 0 || count_b != 0) {
        return Err(NzfError::BadRecord);
    }
    let mut a = Vec::with_capacity(count_a);
    for _ in 0..count_a {
        a.push(r.record(origin)?);
    }
    let mut b = Vec::with_capacity(count_b);
    for _ in 0..count_b {
        b.push(r.record(origin)?);
    }
    if r.off != buf.len() {
        return Err(NzfError::Trailing);
    }
    Ok(Parsed {
        kind,
        origin,
        serial,
        from_serial,
        a,
        b,
    })
}

/// Inflates a zstd zone blob, refusing output larger than `max_size` octets. Streams into a
/// growing buffer, so memory tracks the actual output rather than `max_size`.
pub fn decompress(blob: &[u8], max_size: usize) -> Result<Vec<u8>, NzfError> {
    let dec = zstd::stream::read::Decoder::new(blob).map_err(|e| NzfError::Zstd(e.to_string()))?;
    let mut out = Vec::new();
    dec.take(max_size as u64 + 1)
        .read_to_end(&mut out)
        .map_err(|e| NzfError::Zstd(e.to_string()))?;
    if out.len() > max_size {
        return Err(NzfError::TooLarge);
    }
    Ok(out)
}
