use nexora_engine::bootstrap::{self, Bootstrap};
use nexora_engine::clock;
use nexora_engine::server::{self, Shared};
use nexora_engine::snapshot::{self, ApplyOutcome, DirBlobs};
use std::path::{Path, PathBuf};
use std::process::ExitCode;
use std::sync::Arc;
use std::sync::atomic::Ordering;

#[derive(clap::Parser)]
struct Args {
    #[arg(long, default_value = "/etc/nexora/engine.toml")]
    config: PathBuf,
}

fn main() -> ExitCode {
    let args = <Args as clap::Parser>::parse();
    let boot = match bootstrap::load(&args.config) {
        Ok(b) => b,
        Err(e) => {
            eprintln!("nexora-engine: {e:#}");
            return ExitCode::FAILURE;
        }
    };
    clock::start_ticker();
    let shared = Shared::new(boot.worker_count());

    if boot.is_standalone() {
        if !apply_standalone(&shared, &boot) {
            return ExitCode::FAILURE;
        }
    } else {
        match snapshot::load(&boot.state_dir) {
            Ok(Some(s)) => {
                let blobs = DirBlobs {
                    dir: boot.state_dir.join("blobs"),
                };
                report(&shared, snapshot::apply(&shared.runtime, s, &blobs, None));
            }
            Ok(None) => {}
            Err(e) => eprintln!("nexora-engine: persisted snapshot unreadable: {e}"),
        }
    }

    let workers = match server::spawn_workers(shared.clone(), &boot) {
        Ok(h) => h,
        Err(e) => {
            eprintln!("nexora-engine: listen: {e}");
            return ExitCode::FAILURE;
        }
    };

    let control = match tokio::runtime::Builder::new_multi_thread()
        .worker_threads(2)
        .thread_name("nexora-control")
        .enable_all()
        .build()
    {
        Ok(rt) => rt,
        Err(e) => {
            eprintln!("nexora-engine: control runtime: {e}");
            return ExitCode::FAILURE;
        }
    };
    if boot.is_standalone() {
        // Registered before workers serve long, so a SIGHUP never takes the default action.
        let hangup = {
            let _entered = control.enter();
            tokio::signal::unix::signal(tokio::signal::unix::SignalKind::hangup())
        };
        match hangup {
            Ok(mut hangup) => {
                control.spawn(async move {
                    while hangup.recv().await.is_some() {
                        let (shared, boot) = (shared.clone(), boot.clone());
                        let _ =
                            tokio::task::spawn_blocking(move || apply_standalone(&shared, &boot))
                                .await;
                    }
                });
            }
            Err(e) => eprintln!("nexora-engine: SIGHUP handler: {e}"),
        }
    }

    for w in workers {
        let _ = w.join();
    }
    ExitCode::SUCCESS
}

fn apply_standalone(shared: &Arc<Shared>, boot: &Bootstrap) -> bool {
    let outcome = match snapshot::load_file(Path::new(&boot.standalone_snapshot)) {
        Ok(s) => {
            let blobs = DirBlobs {
                dir: PathBuf::from(&boot.standalone_blob_dir),
            };
            snapshot::apply(&shared.runtime, s, &blobs, None)
        }
        Err(e) => ApplyOutcome::Rejected {
            version: 0,
            reason: e.to_string(),
        },
    };
    report(shared, outcome)
}

fn report(shared: &Shared, outcome: ApplyOutcome) -> bool {
    match outcome {
        ApplyOutcome::Applied {
            version,
            persist_error,
        } => {
            shared
                .metrics
                .config_version
                .store(version, Ordering::Relaxed);
            eprintln!("nexora-engine: serving version {version}");
            if let Some(e) = persist_error {
                eprintln!("nexora-engine: snapshot not persisted: {e}");
            }
            true
        }
        ApplyOutcome::Rejected { reason, .. } => {
            eprintln!("nexora-engine: snapshot rejected: {reason}");
            false
        }
    }
}
