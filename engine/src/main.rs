use nexora_engine::authoritative;
use nexora_engine::bootstrap::{self, Bootstrap};
use nexora_engine::clock;
use nexora_engine::lifecycle;
use nexora_engine::recursor::{self, RecursorState};
use nexora_engine::server::tls::CertStore;
use nexora_engine::server::{self, Shared};
use nexora_engine::snapshot::{self, ApplyOutcome, DirBlobs};
use nexora_engine::telemetry::{metrics, otlp};
use std::net::SocketAddr;
use std::path::{Path, PathBuf};
use std::process::ExitCode;
use std::sync::Arc;
use std::sync::atomic::Ordering;

#[derive(clap::Parser)]
#[command(version = nexora_engine::VERSION)]
struct Args {
    #[arg(long, default_value = "/etc/nexora/engine.toml")]
    config: PathBuf,
}

/// `-V` prints `nexora-engine <VERSION>`; `--version` adds the commit of a stamped build.
fn parse_args() -> Args {
    use clap::{CommandFactory, FromArgMatches};
    let long = format!("{} {}", nexora_engine::VERSION, nexora_engine::COMMIT);
    // Leaked once at start: clap without its `string` feature takes a `&'static str`.
    let long: &'static str = Box::leak(long.trim_end().to_owned().into_boxed_str());
    let matches = Args::command().long_version(long).get_matches();
    Args::from_arg_matches(&matches).unwrap_or_else(|e| e.exit())
}

fn main() -> ExitCode {
    nexora_engine::telemetry::process::started_unix_ms();
    let args = parse_args();
    let boot = match bootstrap::load(&args.config) {
        Ok(b) => b,
        Err(e) => {
            eprintln!("nexora-engine: {e:#}");
            return ExitCode::FAILURE;
        }
    };
    clock::start_ticker();
    let shared = Shared::with_recursor(
        boot.worker_count(),
        RecursorState::new(Some(&boot.state_dir)),
    );
    shared.recursor.recursor.detect_ipv6();
    shared.node_name.store(Arc::new(boot.node_name.clone()));
    let cert_store = Arc::new(CertStore::new());

    if boot.is_standalone() {
        if !apply_standalone(&shared, &boot) {
            return ExitCode::FAILURE;
        }
        load_standalone_tls(&cert_store, &boot);
    } else {
        match snapshot::load(&boot.state_dir) {
            Ok(Some(s)) => {
                let blobs = DirBlobs {
                    dir: boot.state_dir.join("blobs"),
                };
                if report(&shared, snapshot::apply(&shared.runtime, s, &blobs, None)) {
                    shared.recursor.sync(&shared.runtime.load_full());
                    authoritative::after_apply(&shared, &shared.runtime.load());
                }
            }
            Ok(None) => {}
            Err(e) => eprintln!("nexora-engine: persisted snapshot unreadable: {e}"),
        }
    }

    let workers = match server::spawn_workers(shared.clone(), &boot, cert_store.clone()) {
        Ok(h) => h,
        Err(e) => {
            eprintln!("nexora-engine: listen: {e}");
            return ExitCode::FAILURE;
        }
    };

    if let Err(e) = recursor::spawn_background(shared.clone()) {
        eprintln!("nexora-engine: recursor thread: {e}");
        return ExitCode::FAILURE;
    }

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
    shared.auth.set_control_runtime(control.handle().clone());
    authoritative::after_apply(&shared, &shared.runtime.load());
    otlp::spawn_telemetry_thread(shared.clone());
    let metrics_addr = match bind_metrics(&control, boot.metrics_listen) {
        Ok(listener) => {
            let addr = listener.local_addr().ok();
            control.spawn(metrics::serve_metrics_on(listener, shared.clone()));
            addr
        }
        Err(e) => {
            eprintln!(
                "nexora-engine: metrics listener {}: {e}",
                boot.metrics_listen
            );
            None
        }
    };
    // Registered before the READY line, so SIGTERM and SIGINT always start the drain.
    let stop_signals = {
        use tokio::signal::unix::{SignalKind, signal};
        let _entered = control.enter();
        signal(SignalKind::terminate()).and_then(|t| Ok((t, signal(SignalKind::interrupt())?)))
    };
    let drain = std::time::Duration::from_secs(boot.shutdown_drain_seconds);
    println!("{}", ready_line(&workers, metrics_addr));
    if boot.is_standalone() {
        // Registered before workers serve long, so a SIGHUP never takes the default action.
        let hangup = {
            let _entered = control.enter();
            tokio::signal::unix::signal(tokio::signal::unix::SignalKind::hangup())
        };
        match hangup {
            Ok(mut hangup) => {
                let (shared, boot, cert_store) = (shared.clone(), boot.clone(), cert_store.clone());
                control.spawn(async move {
                    while hangup.recv().await.is_some() {
                        let (shared, boot, cert_store) =
                            (shared.clone(), boot.clone(), cert_store.clone());
                        let _ = tokio::task::spawn_blocking(move || {
                            apply_standalone(&shared, &boot);
                            load_standalone_tls(&cert_store, &boot);
                        })
                        .await;
                    }
                });
            }
            Err(e) => eprintln!("nexora-engine: SIGHUP handler: {e}"),
        }
    } else {
        // The persisted snapshot (if any) is already served; the control stream only updates it.
        control.spawn(nexora_engine::control::run(
            shared.clone(),
            boot.clone(),
            cert_store.clone(),
        ));
    }

    match stop_signals {
        Ok((mut term, mut int)) => control.block_on(async {
            tokio::select! {
                _ = term.recv() => {}
                _ = int.recv() => {}
            }
            lifecycle::drain(&shared, drain).await;
        }),
        Err(e) => {
            // Without signal handlers the engine serves until killed.
            eprintln!("nexora-engine: signal handlers: {e}");
            for w in workers.handles {
                let _ = w.join();
            }
        }
    }
    // Worker threads never return on their own; exiting the process ends them.
    std::process::exit(0)
}

fn bind_metrics(
    control: &tokio::runtime::Runtime,
    addr: SocketAddr,
) -> std::io::Result<tokio::net::TcpListener> {
    let listener = std::net::TcpListener::bind(addr)?;
    listener.set_nonblocking(true)?;
    let _entered = control.enter();
    tokio::net::TcpListener::from_std(listener)
}

/// The machine-readable line naming every bound address (ports chosen by the
/// kernel for port 0 included), e.g.
/// `READY udp=127.0.0.1:5353 tcp=127.0.0.1:5353 dot=127.0.0.1:853 metrics=127.0.0.1:9153`
/// (`dot`, `doh` and `doq` appear only when configured).
fn ready_line(workers: &server::Workers, metrics: Option<SocketAddr>) -> String {
    let join = |addrs: &[SocketAddr]| {
        addrs
            .iter()
            .map(ToString::to_string)
            .collect::<Vec<_>>()
            .join(",")
    };
    let mut line = format!(
        "READY udp={} tcp={}",
        join(&workers.udp),
        join(&workers.tcp)
    );
    for (name, addrs) in [
        ("dot", &workers.dot),
        ("doh", &workers.doh),
        ("doq", &workers.doq),
    ] {
        if !addrs.is_empty() {
            line.push_str(&format!(" {name}={}", join(addrs)));
        }
    }
    if let Some(m) = metrics {
        line.push_str(&format!(" metrics={m}"));
    }
    line
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
    let applied = report(shared, outcome);
    if applied {
        shared.recursor.sync(&shared.runtime.load_full());
        authoritative::after_apply(shared, &shared.runtime.load());
    }
    applied
}

/// Installs `tls_cert_file`/`tls_key_file` when set; a bad certificate leaves the
/// previous one (if any) serving and UDP/TCP unaffected.
fn load_standalone_tls(cert_store: &CertStore, boot: &Bootstrap) {
    if boot.tls_cert_file.is_empty() {
        return;
    }
    let installed = std::fs::read(&boot.tls_cert_file)
        .map_err(|e| format!("read {}: {e}", boot.tls_cert_file))
        .and_then(|chain| {
            let mut key = std::fs::read(&boot.tls_key_file)
                .map_err(|e| format!("read {}: {e}", boot.tls_key_file))?;
            let r = cert_store.install_pem(&chain, &key, clock::unix_now());
            zeroize::Zeroize::zeroize(&mut key);
            r
        });
    match installed {
        Ok(info) => eprintln!(
            "nexora-engine: standalone TLS certificate {} installed",
            info.fingerprint_sha256
        ),
        Err(e) => eprintln!("nexora-engine: standalone TLS certificate rejected: {e}"),
    }
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
