//! Authoritative response writer: header, the question copied verbatim, RRs with owner names
//! compressed against the question name only, and truncation. Writes into a caller buffer.

use super::msg::Question;
use super::name::label_offsets;

const HEADER_LEN: usize = 12;
const FLAG_QR: u8 = 0x80;
const FLAG_AA: u8 = 0x04;
const FLAG_TC: u8 = 0x02;
const FLAG_RD: u8 = 0x01;
const FLAG_RA: u8 = 0x80;
const FLAG_CD: u8 = 0x10;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Section {
    Answer = 0,
    Authority = 1,
    Additional = 2,
}

/// The RR did not fit; the writer is unchanged.
#[derive(Debug, PartialEq, Eq)]
pub struct Overflow;

pub struct Writer<'b> {
    buf: &'b mut [u8],
    limit: usize,
    len: usize,
    question_end: usize,
    counts: [u16; 3],
    tc: bool,
    qname_len: usize,
    qname_label_starts: [u16; 128],
    qname_labels: usize,
}

impl<'b> Writer<'b> {
    /// Writes header and question; `None` when they do not fit in `min(limit, out.len())`.
    pub fn new(out: &'b mut [u8], limit: usize, q: &Question<'_>) -> Option<Writer<'b>> {
        let limit = limit.min(out.len());
        let question_end = HEADER_LEN + q.qname.len() + 4;
        if question_end > limit {
            return None;
        }
        out[..2].copy_from_slice(&q.id.to_be_bytes());
        out[2] = FLAG_QR | ((q.flags >> 8) as u8 & (0x78 | FLAG_RD));
        out[3] = q.flags as u8 & FLAG_CD;
        out[4..6].copy_from_slice(&1u16.to_be_bytes());
        out[6..12].fill(0);
        let qn_end = HEADER_LEN + q.qname.len();
        out[HEADER_LEN..qn_end].copy_from_slice(q.qname);
        out[qn_end..qn_end + 2].copy_from_slice(&q.qtype.to_be_bytes());
        out[qn_end + 2..question_end].copy_from_slice(&q.qclass.to_be_bytes());
        let mut starts = [0u16; 128];
        let labels = label_offsets(q.qname, &mut starts);
        Some(Writer {
            buf: out,
            limit,
            len: question_end,
            question_end,
            counts: [0; 3],
            tc: false,
            qname_len: q.qname.len(),
            qname_label_starts: starts,
            qname_labels: labels,
        })
    }

    fn put(&mut self, bytes: &[u8]) -> Result<(), Overflow> {
        let end = self.len + bytes.len();
        if end > self.limit {
            return Err(Overflow);
        }
        self.buf[self.len..end].copy_from_slice(bytes);
        self.len = end;
        Ok(())
    }

    /// Writes an uncompressed wire name, replacing its longest suffix that is also a suffix of
    /// the question name (at a label boundary, case-insensitive) with a pointer.
    fn put_name(&mut self, name: &[u8]) -> Result<(), Overflow> {
        let mut offs = [0u16; 128];
        let n = label_offsets(name, &mut offs);
        for &off in &offs[..n] {
            let suffix = &name[off as usize..];
            if suffix.len() > self.qname_len {
                continue;
            }
            let at = self.qname_len - suffix.len();
            let q = &self.buf[HEADER_LEN..HEADER_LEN + self.qname_len];
            if q[at..].eq_ignore_ascii_case(suffix)
                && self.qname_label_starts[..self.qname_labels].contains(&(at as u16))
            {
                self.put(&name[..off as usize])?;
                return self.put(&(0xC000u16 | (HEADER_LEN + at) as u16).to_be_bytes());
            }
        }
        self.put(name)
    }

    /// Appends one class-IN RR; on overflow nothing of it remains.
    pub fn rr(
        &mut self,
        section: Section,
        owner: &[u8],
        rtype: u16,
        ttl: u32,
        rdata: &[u8],
    ) -> Result<(), Overflow> {
        let before = self.len;
        let r = self.put_name(owner).and_then(|()| {
            let rdlen = u16::try_from(rdata.len()).map_err(|_| Overflow)?;
            let mut fixed = [0u8; 10];
            fixed[..2].copy_from_slice(&rtype.to_be_bytes());
            fixed[2..4].copy_from_slice(&1u16.to_be_bytes());
            fixed[4..8].copy_from_slice(&ttl.to_be_bytes());
            fixed[8..].copy_from_slice(&rdlen.to_be_bytes());
            self.put(&fixed)?;
            self.put(rdata)
        });
        match r {
            Ok(()) => {
                self.counts[section as usize] += 1;
                Ok(())
            }
            Err(e) => {
                self.len = before;
                Err(e)
            }
        }
    }

    /// Drops every RR and sets TC.
    pub fn truncate(&mut self) {
        self.len = self.question_end;
        self.counts = [0; 3];
        self.tc = true;
    }

    /// Drops every RR without setting TC (for error responses).
    pub fn reset(&mut self) {
        self.len = self.question_end;
        self.counts = [0; 3];
    }

    /// Patches flags and counts; returns the message length.
    pub fn finish(self, rcode: u8, aa: bool, ra: bool) -> usize {
        let b = self.buf;
        if aa {
            b[2] |= FLAG_AA;
        }
        if self.tc {
            b[2] |= FLAG_TC;
        }
        if ra {
            b[3] |= FLAG_RA;
        }
        b[3] |= rcode & 0x0f;
        b[6..8].copy_from_slice(&self.counts[0].to_be_bytes());
        b[8..10].copy_from_slice(&self.counts[1].to_be_bytes());
        b[10..12].copy_from_slice(&self.counts[2].to_be_bytes());
        self.len
    }
}
