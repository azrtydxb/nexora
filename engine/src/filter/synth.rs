//! Deterministic synthetic block lists shaped like the default catalog selection: 77% two-label,
//! 19% three-label and 4% four-label names, about 18 characters on average; 10% of the names are
//! also in a second list.

const TLDS: [&str; 16] = [
    "com", "net", "org", "de", "ru", "info", "xyz", "top", "io", "nl", "fr", "uk", "cn", "br",
    "online", "site",
];
const EDGE: &[u8] = b"abcdefghijklmnopqrstuvwxyz0123456789";
const INNER: &[u8] = b"abcdefghijklmnopqrstuvwxyz0123456789-";

struct Rng(u64);

impl Rng {
    fn next(&mut self) -> u64 {
        self.0 ^= self.0 << 13;
        self.0 ^= self.0 >> 7;
        self.0 ^= self.0 << 17;
        self.0
    }
    fn below(&mut self, n: usize) -> usize {
        (self.next() % n as u64) as usize
    }
    fn label(&mut self, len: usize, out: &mut Vec<u8>) {
        for i in 0..len {
            let set = if i == 0 || i + 1 == len { EDGE } else { INNER };
            out.push(set[self.below(set.len())]);
        }
    }
}

pub fn synthetic_lists(names: usize, lists: usize, seed: u64) -> Vec<Vec<u8>> {
    let mut rng = Rng(seed.wrapping_mul(0x9E37_79B9_7F4A_7C15) | 1);
    let mut out = vec![Vec::with_capacity(names * 20 / lists.max(1)); lists.max(1)];
    let mut name = Vec::with_capacity(64);
    for _ in 0..names {
        name.clear();
        let labels = match rng.below(100) {
            0..=76 => 2,
            77..=95 => 3,
            _ => 4,
        };
        for _ in 2..labels {
            let len = 2 + rng.below(7);
            rng.label(len, &mut name);
            name.push(b'.');
        }
        let len = 6 + rng.below(14);
        rng.label(len, &mut name);
        name.push(b'.');
        name.extend_from_slice(TLDS[rng.below(TLDS.len())].as_bytes());
        name.push(b'\n');
        let first = rng.below(out.len());
        out[first].extend_from_slice(&name);
        if rng.below(10) == 0 {
            let second = rng.below(out.len());
            out[second].extend_from_slice(&name);
        }
    }
    out
}
