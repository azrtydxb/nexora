//! Filter index benchmark: build time, index memory, single-thread decision time, and the per-worker
//! decision cache on a Zipf workload; with `--v1`, the v1 `FilterSet` on the same samples in the
//! same run.
//!   filter_bench --synthetic 1000000 --json bench.json
//!   taskset -c 4-7 filter_bench --threads 4 --pin 4 --v1 --json kw.json /work/lists/clean-*.txt
//! Cold decision time is the median over --rounds passes of up to 20,000 blocked names ("www." plus
//! every 97th listed name) and 20,000 clean names (cdnN.imgN.siteN.exampleN.com, unlisted ones
//! only). The Zipf workload ranks --zipf distinct names (a --zipf-blocked share of listed names,
//! the rest unlisted) in a random order, draws --zipf-queries queries with P(rank k) ∝ 1/k^s,
//! decides as many untimed warm-up queries, then reports the median ns per decision over --rounds
//! passes: through a DecisionCache (`zipf_ns`), without it (`zipf_uncached_ns`), and for the
//! queries of names ranked within the cache's first 1/16 of slots (`zipf_repeated_ns`).
use clap::Parser;
use nexora_engine::filter::decisions::{DEFAULT_SLOTS, DecisionCache};
use nexora_engine::filter::domain_to_wire;
use nexora_engine::filter::index::{
    FilterDecision, FilterIndex, FilterView, IndexOptions, ListInput, ListKind,
};
use nexora_engine::filter::oracle::{Decision, FilterSet};
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
    /// Pin the decision timing to this CPU (Linux) after the build.
    #[arg(long)]
    pin: Option<usize>,
    /// Also build and time the v1 FilterSet (RSS growth as its memory).
    #[arg(long)]
    v1: bool,
    /// Distinct names of the Zipf workload (0 skips it).
    #[arg(long, default_value_t = 1_000_000)]
    zipf: usize,
    #[arg(long, default_value_t = 4_000_000)]
    zipf_queries: usize,
    #[arg(long, default_value_t = 1.0)]
    zipf_s: f64,
    /// Share of listed (blocked) names among the Zipf names.
    #[arg(long, default_value_t = 0.2)]
    zipf_blocked: f64,
    #[arg(long, default_value_t = DEFAULT_SLOTS)]
    cache_slots: usize,
    #[arg(long)]
    json: Option<PathBuf>,
    files: Vec<PathBuf>,
}

#[derive(Default, serde::Serialize)]
struct Report {
    unique_names: u64,
    invalid_lines: u64,
    index_bytes: u64,
    bytes_per_name: f64,
    stash_entries: u64,
    build_threads: usize,
    build_seconds: f64,
    blocked_ns: f64,
    clean_ns: f64,
    cache_slots: usize,
    cache_bytes: usize,
    zipf_names: usize,
    zipf_queries: usize,
    zipf_s: f64,
    zipf_blocked_queries: f64,
    zipf_ns: f64,
    zipf_hit_rate: f64,
    zipf_uncached_ns: f64,
    zipf_repeated_ns: f64,
    zipf_repeated_hit_rate: f64,
    v1_rss_bytes: Option<u64>,
    v1_build_seconds: Option<f64>,
    v1_blocked_ns: Option<f64>,
    v1_clean_ns: Option<f64>,
    v1_zipf_ns: Option<f64>,
}

fn median(mut v: Vec<f64>) -> f64 {
    v.sort_by(f64::total_cmp);
    v[v.len() / 2]
}

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
    fn unit(&mut self) -> f64 {
        (self.next() >> 11) as f64 / (1u64 << 53) as f64
    }
}

/// Wire names laid out one after another in query order, so walking them reads memory
/// sequentially (as a worker reads packets it already holds).
#[derive(Default)]
struct Queries {
    bytes: Vec<u8>,
    lens: Vec<u8>,
}

impl Queries {
    fn push(&mut self, name: &[u8]) {
        self.bytes.extend_from_slice(name);
        self.lens.push(name.len() as u8);
    }
    fn len(&self) -> usize {
        self.lens.len()
    }
    /// Calls `f` for every name; returns ns per name.
    fn time(&self, mut f: impl FnMut(&[u8])) -> f64 {
        let t = Instant::now();
        let mut at = 0;
        for &n in &self.lens {
            let n = usize::from(n);
            f(std::hint::black_box(&self.bytes[at..at + n]));
            at += n;
        }
        t.elapsed().as_nanos() as f64 / self.lens.len().max(1) as f64
    }
}

#[cfg(target_os = "linux")]
fn rss_bytes() -> u64 {
    let statm = std::fs::read_to_string("/proc/self/statm").unwrap_or_default();
    let pages: u64 = statm
        .split_whitespace()
        .nth(1)
        .and_then(|p| p.parse().ok())
        .unwrap_or(0);
    // SAFETY: sysconf has no preconditions.
    pages * unsafe { libc::sysconf(libc::_SC_PAGESIZE) } as u64
}

#[cfg(not(target_os = "linux"))]
fn rss_bytes() -> u64 {
    0
}

#[cfg(target_os = "linux")]
fn pin(cpu: usize) {
    // SAFETY: a zeroed cpu_set_t with one bit set, applied to the calling thread.
    unsafe {
        let mut set: libc::cpu_set_t = std::mem::zeroed();
        libc::CPU_SET(cpu, &mut set);
        let rc = libc::sched_setaffinity(0, size_of::<libc::cpu_set_t>(), &set);
        assert_eq!(rc, 0, "sched_setaffinity to CPU {cpu} failed");
    }
}

#[cfg(not(target_os = "linux"))]
fn pin(_cpu: usize) {}

/// Up to `n` names the view blocks from every `step`th listed name: "www." plus the name, or for
/// odd samples with `mixed` the listed name itself.
fn blocked_names(
    texts: &[Vec<u8>],
    view: &FilterView,
    step: usize,
    n: usize,
    mixed: bool,
) -> Vec<Box<[u8]>> {
    texts
        .iter()
        .flat_map(|t| t.split(|&b| b == b'\n'))
        .filter(|l| !l.is_empty())
        .step_by(step.max(1))
        .enumerate()
        .filter_map(|(i, l)| {
            let mut name = if mixed && i % 2 == 1 {
                Vec::new()
            } else {
                b"www.".to_vec()
            };
            name.extend_from_slice(l);
            domain_to_wire(&name)
        })
        .filter(|q| matches!(view.decide(q), FilterDecision::Blocked(_)))
        .take(n)
        .collect()
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
        drop(last.take());
        let t = Instant::now();
        let built = FilterIndex::build(&inputs, &opts).expect("build");
        builds.push(t.elapsed().as_secs_f64());
        last = Some(built);
    }
    let index = Arc::new(last.expect("built"));
    let all: Vec<u16> = (0..texts.len() as u16).collect();
    let view = index.view(&all, &[]);

    let v1 = args.v1.then(|| {
        let before = rss_bytes();
        let t = Instant::now();
        let (set, _) = FilterSet::build(
            &texts,
            &[],
            nexora_engine::filter::BlockMode::NullIp,
            60,
        );
        let seconds = t.elapsed().as_secs_f64();
        (set, rss_bytes().saturating_sub(before), seconds)
    });

    if let Some(cpu) = args.pin {
        pin(cpu);
    }
    let blocked = blocked_names(&texts, &view, 97, 20_000, false);
    let mut rng = Rng(0x9E37_79B9_7F4A_7C15);
    let mut cold_blocked = Queries::default();
    for q in &blocked {
        cold_blocked.push(q);
    }
    let mut cold_clean = Queries::default();
    for _ in 0..20_000 {
        let n = format!(
            "cdn{}.img{}.site{}.example{}.com",
            rng.below(1000),
            rng.below(1000),
            rng.below(100_000),
            rng.below(1000)
        );
        if let Some(q) = domain_to_wire(n.as_bytes())
            && view.decide(&q) == FilterDecision::None
        {
            cold_clean.push(&q);
        }
    }
    assert!(
        cold_blocked.len() >= 1_000 && cold_clean.len() >= 19_000,
        "samples: {} blocked, {} clean",
        cold_blocked.len(),
        cold_clean.len()
    );
    let rounds = args.rounds.max(1);
    let per_round = |mut f: Box<dyn FnMut() -> f64 + '_>| median((0..rounds).map(|_| f()).collect());
    let mut report = Report {
        unique_names: index.entries(),
        invalid_lines: index.invalid_lines(),
        index_bytes: index.memory_bytes() + view.memory_bytes(),
        bytes_per_name: (index.memory_bytes() + view.memory_bytes()) as f64
            / index.entries().max(1) as f64,
        stash_entries: index.stash_entries(),
        build_threads: args.threads,
        build_seconds: median(builds),
        blocked_ns: per_round(Box::new(|| {
            cold_blocked.time(|q| {
                std::hint::black_box(view.decide(q));
            })
        })),
        clean_ns: per_round(Box::new(|| {
            cold_clean.time(|q| {
                std::hint::black_box(view.decide(q));
            })
        })),
        ..Report::default()
    };
    if let Some((set, rss, seconds)) = &v1 {
        let decide = |q: &[u8]| {
            std::hint::black_box(set.decide(q));
        };
        report.v1_rss_bytes = Some(*rss);
        report.v1_build_seconds = Some(*seconds);
        report.v1_blocked_ns = Some(per_round(Box::new(|| cold_blocked.time(decide))));
        report.v1_clean_ns = Some(per_round(Box::new(|| cold_clean.time(decide))));
        assert!(
            set.decide(&blocked[0]) == Decision::Blocked,
            "v1 blocks the samples"
        );
    }

    if args.zipf > 0 {
        let n = args.zipf;
        let want_blocked = (n as f64 * args.zipf_blocked) as usize;
        let listed: usize = texts.iter().map(|t| t.len() / 20).sum();
        let mut names = blocked_names(
            &texts,
            &view,
            listed / want_blocked.max(1) / 2,
            want_blocked,
            true,
        );
        names.sort_unstable();
        names.dedup();
        let mut i = 0u64;
        while names.len() < n {
            i += 1;
            let r = rng.next();
            let text = match r % 3 {
                0 => format!("www.site{i}.com"),
                1 => format!("cdn{}.img.site{i}.net", r % 97),
                _ => format!("api.v{}.service{i}.example.org", r % 7),
            };
            if let Some(q) = domain_to_wire(text.as_bytes())
                && view.decide(&q) == FilterDecision::None
            {
                names.push(q);
            }
        }
        let is_blocked: Vec<bool> = names
            .iter()
            .map(|q| matches!(view.decide(q), FilterDecision::Blocked(_)))
            .collect();
        // Random rank order.
        let mut order: Vec<u32> = (0..n as u32).collect();
        for k in (1..n).rev() {
            order.swap(k, rng.below(k + 1));
        }
        let mut cdf = Vec::with_capacity(n);
        let mut sum = 0.0;
        for k in 0..n {
            sum += 1.0 / ((k + 1) as f64).powf(args.zipf_s);
            cdf.push(sum);
        }
        let repeated_ranks = (args.cache_slots.next_power_of_two() / 16).max(1);
        let (mut warm, mut timed, mut repeated) =
            (Queries::default(), Queries::default(), Queries::default());
        let mut blocked_queries = 0usize;
        for q in 0..2 * args.zipf_queries {
            let u = rng.unit() * sum;
            let rank = cdf.partition_point(|&c| c < u).min(n - 1);
            let name = &names[order[rank] as usize];
            if q < args.zipf_queries {
                warm.push(name);
                continue;
            }
            timed.push(name);
            blocked_queries += usize::from(is_blocked[order[rank] as usize]);
            if rank < repeated_ranks {
                repeated.push(name);
            }
        }
        drop(cdf);
        let cache = DecisionCache::new(args.cache_slots);
        warm.time(|q| {
            std::hint::black_box(cache.decide(&view, q));
        });
        let hits_before = cache.hits();
        report.zipf_ns = per_round(Box::new(|| {
            timed.time(|q| {
                std::hint::black_box(cache.decide(&view, q));
            })
        }));
        report.zipf_hit_rate =
            (cache.hits() - hits_before) as f64 / (rounds * timed.len()) as f64;
        let hits_before = cache.hits();
        report.zipf_repeated_ns = per_round(Box::new(|| {
            repeated.time(|q| {
                std::hint::black_box(cache.decide(&view, q));
            })
        }));
        report.zipf_repeated_hit_rate =
            (cache.hits() - hits_before) as f64 / (rounds * repeated.len()).max(1) as f64;
        report.zipf_uncached_ns = per_round(Box::new(|| {
            timed.time(|q| {
                std::hint::black_box(view.decide(q));
            })
        }));
        if let Some((set, _, _)) = &v1 {
            report.v1_zipf_ns = Some(per_round(Box::new(|| {
                timed.time(|q| {
                    std::hint::black_box(set.decide(q));
                })
            })));
        }
        report.cache_slots = args.cache_slots.next_power_of_two();
        report.cache_bytes = cache.memory_bytes();
        report.zipf_names = n;
        report.zipf_queries = timed.len();
        report.zipf_s = args.zipf_s;
        report.zipf_blocked_queries = blocked_queries as f64 / timed.len() as f64;
    }
    let json = serde_json::to_string_pretty(&report).expect("json");
    println!("{json}");
    if let Some(p) = &args.json {
        std::fs::write(p, json).unwrap_or_else(|e| panic!("{}: {e}", p.display()));
    }
}
