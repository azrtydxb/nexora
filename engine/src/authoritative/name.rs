//! Uncompressed wire-format name helpers for the authoritative zone model.

/// Records the offset of each label (root excluded) in `out` and returns the label count. Stops at
/// the root label, at a label that would run past `wire`, or after 128 labels, so it never reads
/// out of bounds even for malformed input.
pub fn label_offsets(wire: &[u8], out: &mut [u16; 128]) -> usize {
    let (mut i, mut n) = (0usize, 0usize);
    while i < wire.len() && wire[i] != 0 && n < 128 && i + 1 + wire[i] as usize <= wire.len() {
        out[n] = i as u16;
        n += 1;
        i += wire[i] as usize + 1;
    }
    n
}

/// Copies at most 255 octets of `wire` into `buf` with ASCII letters lowercased.
pub fn lowercase_into<'b>(wire: &[u8], buf: &'b mut [u8; 255]) -> &'b [u8] {
    let n = wire.len().min(buf.len());
    for (d, s) in buf[..n].iter_mut().zip(wire) {
        *d = s.to_ascii_lowercase();
    }
    &buf[..n]
}

/// Appends the RFC 4034 §6.1 canonical sort key of `wire` to `out`: labels from the root,
/// lowercase, each terminated by 0x00, label octets 0x00/0x01 escaped to 0x01 0x01 / 0x01 0x02.
/// Twin of Go `nzf.CanonicalKey`; both must produce identical bytes. A key of an ancestor is a
/// prefix of every descendant's key.
pub fn canon_key(wire: &[u8], out: &mut Vec<u8>) {
    let mut offs = [0u16; 128];
    let n = label_offsets(wire, &mut offs);
    for k in (0..n).rev() {
        let o = offs[k] as usize;
        for &b in &wire[o + 1..o + 1 + wire[o] as usize] {
            match b.to_ascii_lowercase() {
                0 => out.extend_from_slice(&[1, 1]),
                1 => out.extend_from_slice(&[1, 2]),
                c => out.push(c),
            }
        }
        out.push(0);
    }
}

/// Room for the canonical key of any valid name (at most 508 octets) plus a `*` label.
pub const KEY_BUF: usize = 512;

/// [`canon_key`] into a stack buffer, for the query path; returns the key length. Input longer
/// than a valid name is cut off where the buffer ends.
pub fn canon_key_buf(wire: &[u8], out: &mut [u8; KEY_BUF]) -> usize {
    let mut offs = [0u16; 128];
    let n = label_offsets(wire, &mut offs);
    let mut len = 0usize;
    let mut push = |out: &mut [u8; KEY_BUF], b: u8| {
        if len < KEY_BUF {
            out[len] = b;
            len += 1;
        }
    };
    for k in (0..n).rev() {
        let o = offs[k] as usize;
        for &b in &wire[o + 1..o + 1 + wire[o] as usize] {
            match b.to_ascii_lowercase() {
                0 => {
                    push(out, 1);
                    push(out, 1);
                }
                1 => {
                    push(out, 1);
                    push(out, 2);
                }
                c => push(out, c),
            }
        }
        push(out, 0);
    }
    len
}

/// Whether `child` equals `parent` or lies below it at a label boundary (case-insensitive).
pub fn is_subdomain(child: &[u8], parent: &[u8]) -> bool {
    if parent.is_empty() || parent.len() > child.len() {
        return false;
    }
    let start = child.len() - parent.len();
    if !child[start..].eq_ignore_ascii_case(parent) {
        return false;
    }
    let mut offs = [0u16; 128];
    let n = label_offsets(child, &mut offs);
    start == child.len() - 1 || offs[..n].iter().any(|&o| o as usize == start)
}

/// Converts a dotted ASCII name (no escapes, trailing dot optional) to uncompressed wire format.
/// For tests and configuration only.
pub fn from_ascii(s: &str) -> Option<Vec<u8>> {
    let s = s.strip_suffix('.').unwrap_or(s);
    let mut out = Vec::with_capacity(s.len() + 2);
    if !s.is_empty() {
        for label in s.split('.') {
            if label.is_empty() || label.len() > 63 {
                return None;
            }
            out.push(label.len() as u8);
            out.extend_from_slice(label.as_bytes());
        }
    }
    out.push(0);
    (out.len() <= 255).then_some(out)
}

/// Dotted ASCII (trailing dot) of an uncompressed wire name built by [`from_ascii`]: labels are
/// joined verbatim, non-UTF-8 octets replaced.
pub fn to_ascii(wire: &[u8]) -> String {
    let mut out = String::with_capacity(wire.len());
    let mut i = 0;
    while let Some(&l) = wire.get(i) {
        if l == 0 {
            break;
        }
        let end = (i + 1 + usize::from(l)).min(wire.len());
        out.push_str(&String::from_utf8_lossy(&wire[i + 1..end]));
        out.push('.');
        i = end;
    }
    if out.is_empty() {
        out.push('.');
    }
    out
}
