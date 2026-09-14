//! Filter index benchmark: build time, index memory and single-thread decision time.
//!   filter_bench --synthetic 1000000 --json bench.json
//!   taskset -c 4,5 filter_bench --threads 2 --json kw.json /work/filter-corpus/*.txt
//! Decision time is the median over --rounds passes of up to 20,000 blocked names ("www." plus
//! every 97th listed name) and 20,000 clean names (cdnN.imgN.siteN.exampleN.com, unlisted ones only).
use clap::Parser;
use nexora_engine::filter::domain_to_wire;
use nexora_engine::filter::index::{
    FilterDecision, FilterIndex, IndexOptions, ListInput, ListKind,
};
use nexora_engine::filter::synth::synthetic_lists;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Instant;

#[derive(Parser)]
struct Args {
    /// Generate this many names instead of reading list files.
    #[arg(long)]
    synthetic: Option<usize>,
    #[arg(long, default_value_t = 21)]
    lists: usize,
    #[arg(long, default_value_t = 1)]
    seed: u64,
    #[arg(long, default_value_t = 4)]
    threads: usize,
    #[arg(long, default_value_t = nexora_engine::filter::index::BLOCK_FILL)]
    fill: f64,
    #[arg(long, default_value_t = 5)]
    rounds: usize,
    #[arg(long)]
    json: Option<PathBuf>,
    files: Vec<PathBuf>,
}

#[derive(serde::Serialize)]
struct Report {
    unique_names: u64,
    invalid_lines: u64,
    index_bytes: u64,
    bytes_per_name: f64,
    stash_entries: u64,
    build_seconds: f64,
    blocked_ns: f64,
    clean_ns: f64,
}

fn median(mut v: Vec<f64>) -> f64 {
    v.sort_by(f64::total_cmp);
    v[v.len() / 2]
}

fn main() {
    let args = Args::parse();
    let texts: Vec<Vec<u8>> = match args.synthetic {
        Some(n) => synthetic_lists(n, args.lists, args.seed),
        None => args
            .files
            .iter()
            .map(|f| std::fs::read(f).unwrap_or_else(|e| panic!("{}: {e}", f.display())))
            .collect(),
    };
    assert!(
        !texts.is_empty(),
        "no lists: pass --synthetic N or list files"
    );
    let ids: Vec<String> = (0..texts.len()).map(|i| format!("list-{i}")).collect();
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
        threads: args.threads,
        fill: args.fill,
        ..IndexOptions::new(4 << 30)
    };
    let mut builds = Vec::new();
    let mut last = None;
    for _ in 0..3 {
        let t = Instant::now();
        let built = FilterIndex::build(&inputs, &opts).expect("build");
        builds.push(t.elapsed().as_secs_f64());
        last = Some(built);
    }
    let index = Arc::new(last.expect("built"));
    let all: Vec<u16> = (0..texts.len() as u16).collect();
    let view = index.view(&all, &[]);
    let blocked: Vec<Box<[u8]>> = texts
        .iter()
        .flat_map(|t| t.split(|&b| b == b'\n'))
        .filter(|l| !l.is_empty())
        .step_by(97)
        .filter_map(|l| {
            let mut n = b"www.".to_vec();
            n.extend_from_slice(l);
            domain_to_wire(&n)
        })
        .filter(|q| matches!(view.decide(q), FilterDecision::Blocked(_)))
        .take(20_000)
        .collect();
    let mut state = 0x9E37_79B9_7F4A_7C15u64;
    let mut rnd = move || {
        state ^= state << 13;
        state ^= state >> 7;
        state ^= state << 17;
        state
    };
    let clean: Vec<Box<[u8]>> = (0..20_000)
        .map(|_| {
            format!(
                "cdn{}.img{}.site{}.example{}.com",
                rnd() % 1000,
                rnd() % 1000,
                rnd() % 100_000,
                rnd() % 1000
            )
        })
        .filter_map(|n| domain_to_wire(n.as_bytes()))
        .filter(|q| view.decide(q) == FilterDecision::None)
        .collect();
    assert!(
        blocked.len() >= 1_000 && clean.len() >= 19_000,
        "samples: {} blocked, {} clean",
        blocked.len(),
        clean.len()
    );
    let time = |names: &[Box<[u8]>]| {
        let per_round = (0..args.rounds.max(1))
            .map(|_| {
                let t = Instant::now();
                for q in names {
                    std::hint::black_box(view.decide(std::hint::black_box(q)));
                }
                t.elapsed().as_nanos() as f64 / names.len() as f64
            })
            .collect();
        median(per_round)
    };
    let report = Report {
        unique_names: index.entries(),
        invalid_lines: index.invalid_lines(),
        index_bytes: index.memory_bytes() + view.memory_bytes(),
        bytes_per_name: (index.memory_bytes() + view.memory_bytes()) as f64
            / index.entries().max(1) as f64,
        stash_entries: index.stash_entries(),
        build_seconds: median(builds),
        blocked_ns: time(&blocked),
        clean_ns: time(&clean),
    };
    let json = serde_json::to_string_pretty(&report).expect("json");
    println!("{json}");
    if let Some(p) = &args.json {
        std::fs::write(p, json).unwrap_or_else(|e| panic!("{}: {e}", p.display()));
    }
}
