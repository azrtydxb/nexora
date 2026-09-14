//! Per-engine filter decision timing: after an index build, listed and unlisted names are decided
//! on a thread pinned to the fastest core this process may use, so fleets of mixed hardware report
//! comparable numbers per core type.

use super::index::{FilterDecision, FilterIndex, ListKind};
use std::sync::Arc;
use std::time::Instant;

/// The CPU label when `/proc/cpuinfo` names no known core.
pub const ARCH_LABEL: &str = std::env::consts::ARCH;
const SAMPLES: usize = 4096;
const PASSES: usize = 5;

#[derive(Clone, Debug, Default, PartialEq)]
pub struct Calibration {
    /// Median nanoseconds per decision of a listed name.
    pub blocked_ns: f64,
    /// Median nanoseconds per decision of an unlisted name.
    pub clean_ns: f64,
    /// The core type measured on; empty when nothing was measured (an empty index).
    pub cpu: String,
}

/// The core name of an ARM `CPU part`.
pub fn cpu_label(part: &str) -> &'static str {
    match part {
        "0xd0b" => "cortex-a76",
        "0xd05" => "cortex-a55",
        "0xd08" => "cortex-a72",
        "0xd03" => "cortex-a53",
        "0xd0c" => "neoverse-n1",
        _ => ARCH_LABEL,
    }
}

fn rank(label: &str) -> u8 {
    match label {
        "cortex-a76" | "neoverse-n1" => 4,
        "cortex-a72" => 3,
        "cortex-a55" => 2,
        "cortex-a53" => 1,
        _ => 0,
    }
}

/// The `allowed` cores of the fastest core type in `cpuinfo` and that type's label; no labelled
/// cores gives no cores and [`ARCH_LABEL`].
pub fn fastest_cores(cpuinfo: &str, allowed: &[usize]) -> (Vec<usize>, &'static str) {
    let mut cores: Vec<(usize, &'static str)> = Vec::new();
    let mut processor = None;
    for line in cpuinfo.lines() {
        let Some((key, value)) = line.split_once(':') else {
            continue;
        };
        match key.trim() {
            "processor" => processor = value.trim().parse::<usize>().ok(),
            "CPU part" => {
                if let Some(p) = processor.filter(|p| allowed.contains(p)) {
                    cores.push((p, cpu_label(value.trim())));
                }
            }
            _ => {}
        }
    }
    let Some(best) = cores.iter().map(|&(_, l)| rank(l)).max() else {
        return (Vec::new(), ARCH_LABEL);
    };
    let label = cores
        .iter()
        .find(|&&(_, l)| rank(l) == best)
        .map_or(ARCH_LABEL, |&(_, l)| l);
    (
        cores
            .iter()
            .filter(|&&(_, l)| rank(l) == best)
            .map(|&(p, _)| p)
            .collect(),
        label,
    )
}

/// Pins the calling thread to the fastest allowed cores (Linux); returns their label.
#[cfg(target_os = "linux")]
fn pin_to_fastest() -> &'static str {
    let cpuinfo = std::fs::read_to_string("/proc/cpuinfo").unwrap_or_default();
    // SAFETY: a zeroed cpu_set_t filled by sched_getaffinity for the calling thread, then applied
    // to the calling thread; both only read or write the set passed.
    unsafe {
        let mut set: libc::cpu_set_t = std::mem::zeroed();
        if libc::sched_getaffinity(0, size_of::<libc::cpu_set_t>(), &mut set) != 0 {
            return fastest_cores(&cpuinfo, &[]).1;
        }
        let allowed: Vec<usize> = (0..libc::CPU_SETSIZE as usize)
            .filter(|&c| libc::CPU_ISSET(c, &set))
            .collect();
        let (cores, label) = fastest_cores(&cpuinfo, &allowed);
        if !cores.is_empty() {
            let mut pinned: libc::cpu_set_t = std::mem::zeroed();
            for &c in &cores {
                libc::CPU_SET(c, &mut pinned);
            }
            libc::sched_setaffinity(0, size_of::<libc::cpu_set_t>(), &pinned);
        }
        label
    }
}

#[cfg(not(target_os = "linux"))]
fn pin_to_fastest() -> &'static str {
    ARCH_LABEL
}

/// Decision times of `index` over its block lists; the default for an empty index. Runs on its
/// own pinned thread (joined before returning), off the query path.
pub fn measure(index: &Arc<FilterIndex>) -> Calibration {
    if index.entries() == 0 {
        return Calibration::default();
    }
    let block: Vec<u16> = (0..index.lists().len() as u16)
        .filter(|&i| index.lists()[usize::from(i)].kind == ListKind::Block)
        .collect();
    let view = index.view(&block, &[]);
    let blocked: Vec<Box<[u8]>> = index
        .sample_names(SAMPLES)
        .into_iter()
        .filter(|n| matches!(view.decide(n), FilterDecision::Blocked(_)))
        .collect();
    let clean: Vec<Box<[u8]>> = (0..SAMPLES)
        .filter_map(|i| {
            super::domain_to_wire(format!("cdn{i}.img{}.nexora-calibrate.com", i % 97).as_bytes())
        })
        .filter(|n| view.decide(n) == FilterDecision::None)
        .collect();
    let timed = std::thread::Builder::new()
        .name("nexora-filter-calibrate".into())
        .spawn(move || {
            let cpu = pin_to_fastest();
            let median = |names: &[Box<[u8]>]| {
                if names.is_empty() {
                    return 0.0;
                }
                let mut passes: Vec<f64> = (0..PASSES)
                    .map(|_| {
                        let t = Instant::now();
                        for n in names {
                            std::hint::black_box(view.decide(std::hint::black_box(n)));
                        }
                        t.elapsed().as_nanos() as f64 / names.len() as f64
                    })
                    .collect();
                passes.sort_by(f64::total_cmp);
                passes[PASSES / 2]
            };
            Calibration {
                blocked_ns: median(&blocked),
                clean_ns: median(&clean),
                cpu: cpu.to_owned(),
            }
        })
        .and_then(|h| {
            h.join()
                .map_err(|_| std::io::Error::other("calibration panicked"))
        });
    timed.unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::filter::index::{FilterIndex, IndexOptions, ListInput, ListKind};

    const RK3588: &str = "processor\t: 0\nCPU part\t: 0xd05\n\nprocessor\t: 1\nCPU part\t: 0xd05\n\nprocessor\t: 4\nCPU part\t: 0xd0b\n\nprocessor\t: 5\nCPU part\t: 0xd0b\n";

    #[test]
    fn picks_the_fastest_allowed_cores() {
        assert_eq!(
            fastest_cores(RK3588, &[0, 1, 4, 5]),
            (vec![4, 5], "cortex-a76")
        );
        assert_eq!(fastest_cores(RK3588, &[0, 1]), (vec![0, 1], "cortex-a55"));
        assert_eq!(fastest_cores("", &[0, 1]), (vec![], ARCH_LABEL));
        assert_eq!(cpu_label("0xd08"), "cortex-a72");
    }

    #[test]
    fn measures_listed_and_clean_names() {
        let text: String = (0..5_000).map(|i| format!("n{i}.calibrate.test\n")).collect();
        let input = [ListInput {
            id: "l",
            category: "",
            category_slot: 0,
            kind: ListKind::Block,
            text: text.as_bytes(),
        }];
        let index = Arc::new(FilterIndex::build(&input, &IndexOptions::new(16 << 20)).unwrap());
        let samples = index.sample_names(100);
        assert!(samples.len() >= 50, "{} samples", samples.len());
        let view = index.view(&[0], &[]);
        assert!(
            samples
                .iter()
                .all(|n| matches!(view.decide(n), FilterDecision::Blocked(_))),
            "samples decode to listed names"
        );
        let c = measure(&index);
        assert!(
            c.blocked_ns > 0.0 && c.clean_ns > 0.0 && !c.cpu.is_empty(),
            "{c:?}"
        );
        assert_eq!(
            measure(&Arc::new(FilterIndex::empty())),
            Calibration::default()
        );
    }
}
