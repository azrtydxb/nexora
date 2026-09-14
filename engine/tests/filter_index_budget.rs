//! Filter index budgets on a synthetic corpus shaped like the default catalog selection.
use nexora_engine::filter::index::{FilterIndex, IndexOptions, ListInput, ListKind};
use nexora_engine::filter::synth::synthetic_lists;
use std::time::Instant;

/// 120 MB for the 5.1M-name corpus.
const BYTES_PER_NAME: f64 = 120e6 / 5.1e6;
/// 1.5 s for the 5.1M-name corpus.
const SECONDS_PER_NAME: f64 = 1.5 / 5.1e6;

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
