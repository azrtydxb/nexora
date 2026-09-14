//! Filter index budgets on a synthetic corpus shaped like the default catalog selection.
use nexora_engine::filter::index::{FilterIndex, IndexOptions, ListInput, ListKind};
use nexora_engine::filter::synth::synthetic_lists;
use std::time::Instant;

/// 120 MB for the 5.1M-name corpus.
const BYTES_PER_NAME: f64 = 120e6 / 5.1e6;
/// 2 s for the 5.1M-name corpus.
const SECONDS_PER_NAME: f64 = 2.0 / 5.1e6;

#[allow(non_snake_case)]
#[test]
fn TestFilterIndexBudget() {
    let names = 1_000_000;
    let texts = synthetic_lists(names, 21, 1);
    let chars: usize = texts.iter().map(|t| t.len()).sum();
    let lines: usize = texts
        .iter()
        .map(|t| t.iter().filter(|&&b| b == b'\n').count())
        .sum();
    let mean = (chars - lines) as f64 / lines as f64;
    assert!(
        (17.0..19.5).contains(&mean),
        "synthetic names average {mean:.1} characters, the corpus 18.0"
    );
    let ids: Vec<String> = (0..texts.len()).map(|i| format!("synthetic-{i}")).collect();
    let inputs: Vec<ListInput<'_>> = texts
        .iter()
        .zip(&ids)
        .map(|(text, id)| ListInput {
            id,
            category: "",
            category_slot: 0,
            kind: ListKind::Block,
            text,
        })
        .collect();
    let opts = IndexOptions {
        threads: 2,
        ..IndexOptions::new(512 << 20)
    };
    let mut best = f64::MAX;
    let mut last = None;
    for _ in 0..3 {
        let t = Instant::now();
        let built = FilterIndex::build(&inputs, &opts).expect("build");
        best = best.min(t.elapsed().as_secs_f64());
        last = Some(built);
    }
    let index = last.expect("built");
    let unique = index.entries() as f64;
    assert!(
        unique > 0.99 * names as f64,
        "synthetic names are unique: {unique}"
    );
    let per_name = index.memory_bytes() as f64 / unique;
    assert!(
        per_name < BYTES_PER_NAME,
        "{per_name:.2} bytes per name, budget {BYTES_PER_NAME:.2}"
    );
    if cfg!(debug_assertions) {
        eprintln!("debug build: build time {best:.3} s is checked by the --release CI step");
        return;
    }
    let budget = SECONDS_PER_NAME * unique;
    assert!(best < budget, "build {best:.3} s, budget {budget:.3} s");
}

#[allow(non_snake_case)]
#[test]
fn TestFilterIndexSharedAcrossGroups() {
    use nexora_engine::proto::*;
    use nexora_engine::runtime::Runtime;
    use nexora_engine::snapshot::DirBlobs;
    use sha2::{Digest, Sha256};

    let dir = tempfile::tempdir().unwrap();
    let refs: Vec<FilterListRef> = synthetic_lists(200_000, 6, 9)
        .iter()
        .enumerate()
        .map(|(i, text)| {
            let z = zstd::encode_all(&text[..], 3).unwrap();
            let sha = hex::encode(Sha256::digest(&z));
            std::fs::write(dir.path().join(&sha), &z).unwrap();
            FilterListRef {
                list_id: format!("list-{i}"),
                category: "ads-tracking".into(),
                position: i as u32 + 1,
                blob: Some(BlobRef {
                    sha256: sha,
                    size: z.len() as u64,
                    name: format!("list-{i}"),
                }),
            }
        })
        .collect();
    let snapshot = |groups: Vec<PolicyGroup>| ConfigSnapshot {
        version: 1,
        cache: Some(CacheConfig {
            max_bytes: 1 << 20,
            ..Default::default()
        }),
        filter: Some(FilterConfig {
            block_mode: BlockMode::NullIp as i32,
            block_ttl: 60,
            blocklist_refs: refs.clone(),
            ..Default::default()
        }),
        policy_groups: groups,
        ..Default::default()
    };
    let group = |id: &str, cidr: &str, lists: &[usize]| PolicyGroup {
        id: id.into(),
        name: id.into(),
        cidrs: vec![cidr.into()],
        blocklist_refs: lists.iter().map(|&i| refs[i].clone()).collect(),
        ..Default::default()
    };
    let blobs = DirBlobs {
        dir: dir.path().into(),
    };
    let single = Runtime::build(&snapshot(vec![]), &blobs, None).unwrap();
    let three = Runtime::build(
        &snapshot(vec![
            group("g1", "10.1.0.0/16", &[0, 1]),
            group("g2", "10.2.0.0/16", &[2, 3, 4]),
            group("g3", "10.3.0.0/16", &[1, 5]),
        ]),
        &blobs,
        None,
    )
    .unwrap();
    assert!(
        single.filter_memory_bytes > 3_000_000,
        "the single-group index holds the lists: {}",
        single.filter_memory_bytes
    );
    assert_eq!(
        three.filter_index.entries(),
        single.filter_index.entries(),
        "groups add no names"
    );
    let ratio = three.filter_memory_bytes as f64 / single.filter_memory_bytes as f64;
    assert!(
        ratio < 1.10,
        "three policy groups use {ratio:.3}x the memory of one"
    );
}
