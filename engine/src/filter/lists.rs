//! Every filter list of a snapshot with its identity, the index key, category counter slots and the
//! index memory cap.

use super::index::{FilterIndex, IndexOptions, ListKind, ListMeta, MAX_LISTS, TextArena};
use super::memory::BuildMemory;
use crate::proto::{BlobRef, ConfigSnapshot, FilterListRef};
use crate::snapshot::{BlobSource, SnapshotError};
use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::sync::Mutex;

/// The cap when the engine has no cgroup memory limit.
pub const DEFAULT_MAX_BYTES: u64 = 512 << 20;
/// Per-category block counters: slot 0 is `custom`, 1..=62 named categories, 63 `other`.
pub const CATEGORY_SLOTS: usize = 64;
/// Attribution position of lists without one (M1 blobs): after every catalog list.
pub const CUSTOM_POSITION: u32 = 1_000_000;
const MAX_LIST_ID: usize = 128;
const MAX_CATEGORY: usize = 32;
const OTHER_SLOT: u8 = 63;

pub enum ListSource {
    Blob(BlobRef),
    /// A policy group's inline allowlist, one domain per line.
    Inline(Vec<u8>),
}

pub struct CollectedList {
    pub id: String,
    pub category: String,
    pub position: u32,
    pub kind: ListKind,
    pub sha256: String,
    pub source: ListSource,
}

/// The list ids one policy group selects.
pub struct GroupLists {
    pub block: Vec<String>,
    pub allow: Option<String>,
}

pub struct SnapshotLists {
    /// Distinct lists, sorted by `(position, id)`: the index's list order.
    pub lists: Vec<CollectedList>,
    pub global_block: Vec<String>,
    pub global_allow: Vec<String>,
    /// Per policy group, in snapshot order.
    pub groups: Vec<GroupLists>,
    /// Changes whenever the index would differ: every list's kind, id, content and category.
    pub key: String,
}

struct Collector {
    lists: Vec<CollectedList>,
    by_id: HashMap<String, usize>,
}

impl Collector {
    fn add(&mut self, list: CollectedList) -> Result<String, String> {
        let id = list.id.clone();
        match self.by_id.get(&id) {
            Some(&i) => {
                let seen = &self.lists[i];
                if seen.sha256 != list.sha256 {
                    return Err(format!(
                        "filter list {id}: two different blobs in one snapshot"
                    ));
                }
                if seen.kind != list.kind {
                    return Err(format!("filter list {id}: used as block and allow list"));
                }
            }
            None => {
                self.by_id.insert(id.clone(), self.lists.len());
                self.lists.push(list);
            }
        }
        Ok(id)
    }

    /// A list with identity.
    fn add_ref(&mut self, r: &FilterListRef, kind: ListKind) -> Result<String, String> {
        if r.list_id.is_empty() || r.list_id.len() > MAX_LIST_ID {
            return Err(format!("filter list id must be 1..={MAX_LIST_ID} bytes"));
        }
        let Some(blob) = &r.blob else {
            return Err(format!("filter list {}: blob missing", r.list_id));
        };
        let category_ok = r.category.len() <= MAX_CATEGORY
            && r.category
                .bytes()
                .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == b'-');
        if !category_ok {
            return Err(format!(
                "filter list {}: category {} must match [a-z0-9-]{{0,{MAX_CATEGORY}}}",
                r.list_id, r.category
            ));
        }
        self.add(CollectedList {
            id: r.list_id.clone(),
            category: r.category.clone(),
            position: r.position,
            kind,
            sha256: blob.sha256.clone(),
            source: ListSource::Blob(blob.clone()),
        })
    }

    /// The refs when there are any, else the M1 blobs as `blob:<sha256>` lists without category.
    fn add_all(
        &mut self,
        refs: &[FilterListRef],
        blobs: &[BlobRef],
        kind: ListKind,
    ) -> Result<Vec<String>, String> {
        if !refs.is_empty() {
            return refs.iter().map(|r| self.add_ref(r, kind)).collect();
        }
        blobs
            .iter()
            .enumerate()
            .map(|(i, b)| {
                self.add(CollectedList {
                    id: format!("blob:{}", b.sha256),
                    category: String::new(),
                    position: CUSTOM_POSITION.saturating_add(i as u32),
                    kind,
                    sha256: b.sha256.clone(),
                    source: ListSource::Blob(b.clone()),
                })
            })
            .collect()
    }
}

impl SnapshotLists {
    pub fn collect(snap: &ConfigSnapshot) -> Result<SnapshotLists, String> {
        let mut c = Collector {
            lists: Vec::new(),
            by_id: HashMap::new(),
        };
        let f = snap.filter.clone().unwrap_or_default();
        let global_block = c.add_all(&f.blocklist_refs, &f.blocklists, ListKind::Block)?;
        let global_allow = c.add_all(&f.allowlist_refs, &f.allowlists, ListKind::Allow)?;
        let mut groups = Vec::with_capacity(snap.policy_groups.len());
        for g in &snap.policy_groups {
            let block = c.add_all(&g.blocklist_refs, &g.blocklists, ListKind::Block)?;
            let allow = if g.allowlist.is_empty() {
                None
            } else {
                let text = format!("{}\n", g.allowlist.join("\n")).into_bytes();
                let sha256 = hex::encode(Sha256::digest(&text));
                Some(c.add(CollectedList {
                    id: format!("group-allow:{sha256}"),
                    category: String::new(),
                    position: u32::MAX,
                    kind: ListKind::Allow,
                    sha256,
                    source: ListSource::Inline(text),
                })?)
            };
            groups.push(GroupLists { block, allow });
        }
        let mut lists = c.lists;
        if lists.len() > MAX_LISTS {
            return Err(format!("{} filter lists exceed {MAX_LISTS}", lists.len()));
        }
        lists.sort_by(|a, b| (a.position, &a.id).cmp(&(b.position, &b.id)));
        let key = lists
            .iter()
            .map(|l| {
                let kind = if l.kind == ListKind::Block { 'b' } else { 'a' };
                format!("{kind}:{}@{}/{}", l.id, l.sha256, l.category)
            })
            .collect::<Vec<_>>()
            .join(";");
        Ok(SnapshotLists {
            lists,
            global_block,
            global_allow,
            groups,
            key,
        })
    }

    /// Decodes every list and builds the index in list order (off the query path).
    pub fn build_index(
        &self,
        blobs: &dyn BlobSource,
        max_bytes: u64,
        threads: usize,
        memory: &BuildMemory,
    ) -> Result<FilterIndex, SnapshotError> {
        map_large_allocations();
        let invalid = |e: super::index::IndexError| SnapshotError::Invalid(e.to_string());
        let zstd_error = |r: &BlobRef, reason: String| SnapshotError::Blob {
            sha256: r.sha256.clone(),
            reason: format!("zstd: {reason}"),
        };
        // One blob at a time: a frame that declares its content size (mgmt writes them so) is
        // decoded straight into its arena text, so the build holds one copy of every text and one
        // compressed blob.
        let mut arena = TextArena::new();
        for l in &self.lists {
            match &l.source {
                ListSource::Blob(r) => {
                    let data = blobs.read(r)?;
                    match super::blob_content_size(&data) {
                        Some(n) => {
                            memory.reserve("texts", n as u64).map_err(invalid)?;
                            arena.push_with(n, |text| {
                                match zstd::bulk::decompress_to_buffer(&data, text) {
                                    Ok(got) if got == n => Ok(()),
                                    Ok(got) => Err(zstd_error(
                                        r,
                                        format!("frame declares {n} bytes, holds {got}"),
                                    )),
                                    Err(e) => Err(zstd_error(r, e.to_string())),
                                }
                            })?;
                        }
                        None => {
                            let text = super::decode_blob(&data)
                                .map_err(|e| zstd_error(r, e.to_string()))?;
                            drop(data);
                            memory
                                .reserve("texts", text.len() as u64)
                                .map_err(invalid)?;
                            arena.push(&text);
                        }
                    }
                }
                ListSource::Inline(text) => arena.push(text),
            }
        }
        let metas = self
            .lists
            .iter()
            .map(|l| ListMeta {
                id: l.id.as_str().into(),
                category: l.category.as_str().into(),
                category_slot: category_slot(&l.category),
                kind: l.kind,
                invalid_lines: 0,
            })
            .collect();
        FilterIndex::build_in(
            arena,
            metas,
            &IndexOptions {
                threads,
                ..IndexOptions::new(max_bytes)
            },
            memory,
        )
        .map_err(invalid)
    }

    /// The index positions of `ids`.
    pub fn indexes(&self, index: &FilterIndex, ids: &[String]) -> Vec<u16> {
        ids.iter().filter_map(|id| index.list_index(id)).collect()
    }

    /// Equal for groups that select the same block lists and allowlist (their views and cache
    /// partitions are shared).
    pub fn group_key(&self, group: usize) -> String {
        let g = &self.groups[group];
        let sha = |id: &str| {
            self.lists
                .iter()
                .find(|l| l.id == id)
                .map_or("", |l| l.sha256.as_str())
        };
        let mut block: Vec<String> = g
            .block
            .iter()
            .map(|id| format!("{id}@{}", sha(id)))
            .collect();
        block.sort_unstable();
        block.dedup();
        format!("{}|{}", block.join(","), g.allow.as_deref().unwrap_or(""))
    }
}

/// Half the cgroup memory limit, else [`DEFAULT_MAX_BYTES`].
pub fn default_max_bytes(limit: Option<u64>) -> u64 {
    limit.map_or(DEFAULT_MAX_BYTES, |limit| limit / 2)
}

/// Returns the heap pages a build freed to the kernel. glibc keeps freed medium allocations in its
/// per-thread arenas, where the next build's large (mmap) buffers cannot reuse them, so without this
/// every build after the first adds that retained memory to its peak (kw: engines at a 1 GiB limit
/// were OOM-killed while categories were toggled). Off the query path; queries do not allocate.
pub fn release_freed_memory() {
    #[cfg(all(target_os = "linux", target_env = "gnu"))]
    // SAFETY: malloc_trim only releases free heap memory; it has no preconditions.
    unsafe {
        libc::malloc_trim(0);
    }
}

/// Fixes glibc's mmap threshold at 1 MiB (it otherwise grows with every large block freed), so
/// every large build buffer is its own mapping and returns to the kernel when it is freed instead
/// of staying in a per-thread arena and adding to the next buffer's peak. Off the query path.
pub fn map_large_allocations() {
    #[cfg(all(target_os = "linux", target_env = "gnu"))]
    // SAFETY: mallopt only changes allocator tuning; it has no preconditions.
    unsafe {
        libc::mallopt(libc::M_MMAP_THRESHOLD, 1 << 20);
    }
}

/// Index build threads: the CPUs this process may use (std honours the cgroup CPU quota), at
/// most four.
pub fn build_threads() -> usize {
    std::thread::available_parallelism()
        .map_or(1, std::num::NonZeroUsize::get)
        .min(4)
}

/// Category names of slots 1..=62 in first-use order. Only snapshot application and metrics
/// rendering take the lock, never the query path.
static CATEGORIES: Mutex<Vec<String>> = Mutex::new(Vec::new());
static OTHER_USED: std::sync::atomic::AtomicBool = std::sync::atomic::AtomicBool::new(false);

/// The counter slot of a category: 0 for custom lists, a stable slot per name while fewer than 62
/// are taken, else 63 (`other`).
pub fn category_slot(category: &str) -> u8 {
    if category.is_empty() {
        return 0;
    }
    let mut names = CATEGORIES.lock().unwrap_or_else(|e| e.into_inner());
    if let Some(i) = names.iter().position(|n| n == category) {
        return i as u8 + 1;
    }
    if names.len() < usize::from(OTHER_SLOT) - 1 {
        names.push(category.to_owned());
        return names.len() as u8;
    }
    OTHER_USED.store(true, std::sync::atomic::Ordering::Relaxed);
    OTHER_SLOT
}

/// Every slot in use with its label: `custom`, the named categories, and `other` once used.
pub fn category_names() -> Vec<(u8, String)> {
    let names = CATEGORIES.lock().unwrap_or_else(|e| e.into_inner());
    let mut out = vec![(0, "custom".to_owned())];
    out.extend(
        names
            .iter()
            .enumerate()
            .map(|(i, n)| (i as u8 + 1, n.clone())),
    );
    if OTHER_USED.load(std::sync::atomic::Ordering::Relaxed) {
        out.push((OTHER_SLOT, "other".to_owned()));
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::proto::{BlobRef, ConfigSnapshot, FilterConfig, FilterListRef, PolicyGroup};

    fn blob(sha: char) -> BlobRef {
        BlobRef {
            sha256: sha.to_string().repeat(64),
            size: 10,
            name: "n".into(),
        }
    }
    fn list_ref(id: &str, category: &str, position: u32, sha: char) -> FilterListRef {
        FilterListRef {
            list_id: id.into(),
            category: category.into(),
            position,
            blob: Some(blob(sha)),
        }
    }

    #[test]
    fn collect_prefers_refs_synthesizes_ids_and_orders_by_position() {
        let snap = ConfigSnapshot {
            filter: Some(FilterConfig {
                blocklists: vec![blob('a'), blob('b')],
                blocklist_refs: vec![
                    list_ref("custom-1", "", CUSTOM_POSITION, 'b'),
                    list_ref("hagezi-pro", "ads-tracking", 3, 'a'),
                ],
                allowlists: vec![blob('c')],
                ..Default::default()
            }),
            policy_groups: vec![PolicyGroup {
                id: "g1".into(),
                blocklists: vec![blob('d')],
                allowlist: vec!["ok.example".into()],
                ..Default::default()
            }],
            ..Default::default()
        };
        let l = SnapshotLists::collect(&snap).unwrap();
        let ids: Vec<&str> = l.lists.iter().map(|x| x.id.as_str()).collect();
        let (c, d) = (
            format!("blob:{}", "c".repeat(64)),
            format!("blob:{}", "d".repeat(64)),
        );
        assert_eq!(
            &ids[..4],
            &["hagezi-pro", c.as_str(), d.as_str(), "custom-1"],
            "by position, then id"
        );
        assert_eq!(
            l.global_block,
            vec!["custom-1".to_string(), "hagezi-pro".to_string()]
        );
        assert_eq!(l.global_allow, vec![format!("blob:{}", "c".repeat(64))]);
        assert_eq!(l.groups[0].block, vec![format!("blob:{}", "d".repeat(64))]);
        let allow = l.groups[0].allow.clone().unwrap();
        assert!(allow.starts_with("group-allow:") && allow.len() == "group-allow:".len() + 64);
        assert!(
            l.lists.iter().any(|x| x.id == allow
                && matches!(&x.source, ListSource::Inline(t) if t == b"ok.example\n"))
        );
        assert_eq!(
            l.lists
                .iter()
                .find(|x| x.id == "hagezi-pro")
                .unwrap()
                .category,
            "ads-tracking"
        );
    }

    struct MapBlobs(HashMap<String, Vec<u8>>);
    impl BlobSource for MapBlobs {
        fn read(&self, r: &BlobRef) -> Result<Vec<u8>, SnapshotError> {
            self.0.get(&r.sha256).cloned().ok_or(SnapshotError::Blob {
                sha256: r.sha256.clone(),
                reason: "missing".into(),
            })
        }
    }

    /// Catches: a frame decoded straight into the build arena at its declared content size (how
    /// mgmt's zstd EncodeAll writes blobs) losing or shifting text, a frame without a content size
    /// (streaming encoders) no longer decoding, inline allowlists dropped from the arena, and a
    /// frame whose content size lies being accepted.
    #[test]
    fn build_index_decodes_sized_and_unsized_frames_into_one_arena() {
        let sized_text = b"sized-one.test\nsized-two.test\n";
        let sized = zstd::bulk::compress(sized_text, 3).unwrap();
        assert_eq!(
            zstd::zstd_safe::get_frame_content_size(&sized).unwrap(),
            Some(sized_text.len() as u64),
            "fixture: bulk frames declare their content size"
        );
        let unsized_frame = zstd::encode_all(&b"unsized.test\n"[..], 3).unwrap();
        assert_eq!(
            zstd::zstd_safe::get_frame_content_size(&unsized_frame).unwrap(),
            None
        );
        let snap = ConfigSnapshot {
            filter: Some(FilterConfig {
                blocklist_refs: vec![
                    list_ref("sized", "malware", 1, 'a'),
                    list_ref("unsized", "phishing", 2, 'b'),
                ],
                ..Default::default()
            }),
            policy_groups: vec![PolicyGroup {
                id: "g1".into(),
                allowlist: vec!["sized-two.test".into()],
                ..Default::default()
            }],
            ..Default::default()
        };
        let lists = SnapshotLists::collect(&snap).unwrap();
        let blobs = MapBlobs(HashMap::from([
            ("a".repeat(64), sized.clone()),
            ("b".repeat(64), unsized_frame),
        ]));
        let index = std::sync::Arc::new(
            lists
                .build_index(&blobs, 16 << 20, 1, &BuildMemory::unlimited())
                .unwrap(),
        );
        assert_eq!(
            index.entries(),
            3,
            "sized-two.test is in a block list and the allowlist"
        );
        let block = lists.indexes(&index, &lists.global_block);
        let view = index.view(&block, &[]);
        for name in ["sized-one.test", "sized-two.test", "unsized.test"] {
            let wire = crate::filter::domain_to_wire(name.as_bytes()).unwrap();
            assert!(
                matches!(view.decide(&wire), super::super::FilterDecision::Blocked(_)),
                "{name} not blocked"
            );
        }
        let wire = crate::filter::domain_to_wire(b"other.test").unwrap();
        assert_eq!(view.decide(&wire), super::super::FilterDecision::None);

        // A declared content size larger than the frame's data is refused, not zero-padded.
        let mut lying = sized;
        // Magic (4 bytes), then the frame header descriptor: single segment, one-byte content
        // size, no dictionary id, so the content size is byte 5.
        assert_eq!(
            lying[4] & 0xE3,
            0x20,
            "fixture: single-segment frame, one-byte size"
        );
        lying[5] += 8;
        let blobs = MapBlobs(HashMap::from([
            ("a".repeat(64), lying),
            (
                "b".repeat(64),
                zstd::encode_all(&b"unsized.test\n"[..], 3).unwrap(),
            ),
        ]));
        assert!(
            lists
                .build_index(&blobs, 16 << 20, 1, &BuildMemory::unlimited())
                .is_err()
        );
    }

    #[test]
    fn identical_lists_are_shared_and_conflicts_are_rejected() {
        let g = |id: &str, refs: Vec<FilterListRef>, allow: &[&str]| PolicyGroup {
            id: id.into(),
            blocklist_refs: refs,
            allowlist: allow.iter().map(|s| s.to_string()).collect(),
            ..Default::default()
        };
        let mut snap = ConfigSnapshot {
            filter: Some(FilterConfig {
                blocklist_refs: vec![list_ref("l1", "gambling", 1, 'a')],
                ..Default::default()
            }),
            policy_groups: vec![
                g("g1", vec![list_ref("l1", "gambling", 1, 'a')], &["x.test"]),
                g("g2", vec![], &["x.test"]),
            ],
            ..Default::default()
        };
        let l = SnapshotLists::collect(&snap).unwrap();
        assert_eq!(
            l.lists.len(),
            2,
            "one shared block list and one shared inline allowlist"
        );
        assert_eq!(l.groups[0].allow, l.groups[1].allow);
        let before = l.key.clone();
        snap.policy_groups[1].allowlist = vec!["y.test".into()];
        assert_ne!(
            SnapshotLists::collect(&snap).unwrap().key,
            before,
            "an allowlist edit changes the key"
        );
        snap.policy_groups[0].blocklist_refs = vec![list_ref("l1", "gambling", 1, 'b')];
        assert_eq!(
            SnapshotLists::collect(&snap).err().unwrap(),
            "filter list l1: two different blobs in one snapshot"
        );
        snap.policy_groups[0].blocklist_refs = vec![list_ref("l2", "Bad Category", 1, 'a')];
        assert_eq!(
            SnapshotLists::collect(&snap).err().unwrap(),
            "filter list l2: category Bad Category must match [a-z0-9-]{0,32}"
        );
        snap.policy_groups[0].blocklist_refs = vec![FilterListRef {
            list_id: "l3".into(),
            blob: None,
            ..Default::default()
        }];
        assert_eq!(
            SnapshotLists::collect(&snap).err().unwrap(),
            "filter list l3: blob missing"
        );
        snap.policy_groups[0].blocklist_refs = vec![list_ref("", "", 1, 'a')];
        assert_eq!(
            SnapshotLists::collect(&snap).err().unwrap(),
            "filter list id must be 1..=128 bytes"
        );
    }

    #[test]
    fn default_max_bytes_reads_the_cgroup_v2_limit() {
        let dir = tempfile::tempdir().unwrap();
        let f = dir.path().join("memory.max");
        let limit = || BuildMemory::cgroup(dir.path()).limit();
        std::fs::write(&f, "1073741824\n").unwrap();
        assert_eq!(default_max_bytes(limit()), 536_870_912);
        std::fs::write(&f, "max\n").unwrap();
        assert_eq!(default_max_bytes(limit()), DEFAULT_MAX_BYTES);
        std::fs::write(&f, "garbage").unwrap();
        assert_eq!(default_max_bytes(limit()), DEFAULT_MAX_BYTES);
        std::fs::remove_file(&f).unwrap();
        assert_eq!(default_max_bytes(limit()), DEFAULT_MAX_BYTES);
        assert!((1..=4).contains(&build_threads()));
    }

    #[test]
    fn category_slots_are_stable_and_bounded() {
        assert_eq!(category_slot(""), 0);
        let a = category_slot("slot-test-a");
        assert!((1..63).contains(&a));
        assert_eq!(category_slot("slot-test-a"), a, "stable across calls");
        for i in 0..80 {
            assert!(category_slot(&format!("slot-overflow-{i}")) <= 63);
        }
        let names = category_names();
        assert!(names.contains(&(0, "custom".to_string())));
        assert!(names.contains(&(a, "slot-test-a".to_string())));
        assert!(names.contains(&(63, "other".to_string())));
    }
}
