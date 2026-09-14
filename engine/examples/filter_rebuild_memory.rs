//! Peak memory of a filter index rebuild on the engine's snapshot path: the lists are stored as
//! zstd blobs (sized frames, as mgmt writes them), a first runtime builds the old index, then a
//! second snapshot (other categories, so the index is rebuilt) builds next to it. Reports process
//! RSS at every build phase (`BuildMemory` reservations), the peak RSS since the previous phase,
//! and the peak of the whole rebuild beyond the RSS with the old index live (Linux).
//!   taskset -c 4-5 filter_rebuild_memory --json rebuild.json /work/lists/clean-*.txt  # 2 build threads
use clap::Parser;
use nexora_engine::filter::memory::BuildMemory;
use nexora_engine::proto::*;
use nexora_engine::runtime::Runtime;
use nexora_engine::snapshot::DirBlobs;
use sha2::{Digest, Sha256};
use std::path::PathBuf;
use std::sync::{Arc, Mutex};

#[derive(Parser)]
struct Args {
    /// Generate this many names instead of reading list files.
    #[arg(long)]
    synthetic: Option<usize>,
    #[arg(long)]
    json: Option<PathBuf>,
    files: Vec<PathBuf>,
}

#[derive(serde::Serialize)]
struct Phase {
    phase: &'static str,
    reserved_bytes: u64,
    rss_bytes: u64,
    /// Peak RSS between the previous phase and this one.
    peak_before_bytes: u64,
}

#[derive(serde::Serialize)]
struct Report {
    text_bytes: u64,
    unique_names: u64,
    index_bytes: u64,
    baseline_rss_bytes: u64,
    old_index_rss_bytes: u64,
    rebuild_peak_rss_bytes: u64,
    after_rss_bytes: u64,
    /// Rebuild peak beyond the RSS with the old index live, in new index sizes.
    overhead_ratio: f64,
    build_seconds: f64,
    phases: Vec<Phase>,
}

/// (VmRSS, VmHWM) in bytes.
fn rss() -> (u64, u64) {
    let status = std::fs::read_to_string("/proc/self/status").unwrap_or_default();
    let field = |name: &str| {
        status
            .lines()
            .find_map(|l| l.strip_prefix(name))
            .and_then(|v| v.split_whitespace().next()?.parse::<u64>().ok())
            .unwrap_or(0)
            * 1024
    };
    (field("VmRSS:"), field("VmHWM:"))
}

/// Resets VmHWM to the current RSS.
fn reset_peak() {
    let _ = std::fs::write("/proc/self/clear_refs", "5");
}

fn main() {
    let args = Args::parse();
    let dir = tempfile::tempdir().expect("tempdir");
    let mut text_bytes = 0u64;
    let blobs: Vec<BlobRef> = {
        let texts: Vec<Vec<u8>> = match args.synthetic {
            Some(n) => nexora_engine::filter::synth::synthetic_lists(n, 21, 1),
            None => args
                .files
                .iter()
                .map(|f| std::fs::read(f).unwrap_or_else(|e| panic!("{}: {e}", f.display())))
                .collect(),
        };
        assert!(!texts.is_empty(), "no lists: pass --synthetic N or files");
        texts
            .iter()
            .enumerate()
            .map(|(i, text)| {
                text_bytes += text.len() as u64;
                let z = zstd::bulk::compress(text, 3).expect("zstd");
                let sha = hex::encode(Sha256::digest(&z));
                std::fs::write(dir.path().join(&sha), &z).expect("write blob");
                BlobRef {
                    sha256: sha,
                    size: z.len() as u64,
                    name: format!("list-{i}"),
                }
            })
            .collect()
    };
    let snapshot = |version: u64, category: &str| ConfigSnapshot {
        version,
        cache: Some(CacheConfig {
            max_bytes: 1 << 20,
            ..Default::default()
        }),
        filter: Some(FilterConfig {
            block_mode: BlockMode::NullIp as i32,
            block_ttl: 60,
            blocklist_refs: blobs
                .iter()
                .enumerate()
                .map(|(i, b)| FilterListRef {
                    list_id: format!("list-{i}"),
                    category: category.into(),
                    position: i as u32 + 1,
                    blob: Some(b.clone()),
                })
                .collect(),
            ..Default::default()
        }),
        ..Default::default()
    };
    let source = DirBlobs {
        dir: dir.path().into(),
    };
    nexora_engine::filter::lists::release_freed_memory();
    let baseline = rss().0;
    let old = Runtime::build_with(
        &snapshot(1, "old"),
        &source,
        None,
        &BuildMemory::unlimited(),
    )
    .expect("old index");
    nexora_engine::filter::lists::release_freed_memory();
    let with_old = rss().0;

    let phases = Arc::new(Mutex::new(Vec::new()));
    let seen = phases.clone();
    let memory = BuildMemory::unlimited().observe(move |phase, bytes| {
        let (now, peak) = rss();
        reset_peak();
        seen.lock().expect("phases").push(Phase {
            phase,
            reserved_bytes: bytes,
            rss_bytes: now,
            peak_before_bytes: peak,
        });
    });
    reset_peak();
    let started = std::time::Instant::now();
    let new =
        Runtime::build_with(&snapshot(2, "new"), &source, Some(&old), &memory).expect("new index");
    let build_seconds = started.elapsed().as_secs_f64();
    let (_, last_peak) = rss();
    let mut phases = std::mem::take(&mut *phases.lock().expect("phases"));
    let peak = phases
        .iter()
        .map(|p| p.peak_before_bytes)
        .chain([last_peak])
        .max()
        .unwrap_or(0);
    phases.push(Phase {
        phase: "runtime",
        reserved_bytes: 0,
        rss_bytes: rss().0,
        peak_before_bytes: last_peak,
    });
    drop(old);
    nexora_engine::filter::lists::release_freed_memory();
    let index_bytes = new.filter_index.memory_bytes();
    let report = Report {
        text_bytes,
        unique_names: new.filter_index.entries(),
        index_bytes,
        baseline_rss_bytes: baseline,
        old_index_rss_bytes: with_old,
        rebuild_peak_rss_bytes: peak,
        after_rss_bytes: rss().0,
        overhead_ratio: peak.saturating_sub(with_old) as f64 / index_bytes.max(1) as f64,
        build_seconds,
        phases,
    };
    let json = serde_json::to_string_pretty(&report).expect("json");
    println!("{json}");
    if let Some(p) = &args.json {
        std::fs::write(p, json).unwrap_or_else(|e| panic!("{}: {e}", p.display()));
    }
}
