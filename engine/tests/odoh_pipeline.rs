//! ODoH through the DoH handler: configs, target, rejections, proxy statuses.
use bytes::Bytes;
use http::{Method, Request, StatusCode};
use nexora_engine::proto;
use nexora_engine::server::doh;
use nexora_engine::server::odoh::{
    self, CONFIGS_PATH, CONTENT_TYPE, MAX_BODY, OdohRuntime, OdohState,
};

mod common;

fn post(uri: &str, ct: &str, body: Vec<u8>) -> Request<http_body_util::Full<Bytes>> {
    Request::builder()
        .method(Method::POST)
        .uri(uri)
        .header("content-type", ct)
        .body(http_body_util::Full::new(Bytes::from(body)))
        .unwrap()
}

#[tokio::test(flavor = "current_thread")]
async fn odoh_paths_through_the_doh_handler() {
    let answerer = common::StaticAnswerer::a("www.example.test.", "192.0.2.10");
    let keys = OdohState::default();
    keys.set_keys(&proto::OdohKeys {
        keys: vec![proto::OdohKey {
            seed: vec![9; 32],
            publish_after_unix: 0,
            not_after_unix: i64::MAX,
        }],
    });
    let off = OdohRuntime::off();
    let target = OdohRuntime::build(Some(&proto::OdohConfig {
        target_enabled: true,
        ..Default::default()
    }))
    .unwrap();
    let client = common::client_info("192.0.2.50:5000");

    // Target off: configs 404, an ODoH POST 415 as before M8.
    let r = doh::handle(
        &answerer,
        client,
        "/dns-query",
        &off,
        &keys,
        common::get(CONFIGS_PATH),
    )
    .await;
    assert_eq!(r.status(), StatusCode::NOT_FOUND);
    let r = doh::handle(
        &answerer,
        client,
        "/dns-query",
        &off,
        &keys,
        post("/dns-query", CONTENT_TYPE, vec![1, 2, 3]),
    )
    .await;
    assert_eq!(r.status(), StatusCode::UNSUPPORTED_MEDIA_TYPE);

    // Target on: configs, a round trip, rejections.
    let r = doh::handle(
        &answerer,
        client,
        "/dns-query",
        &target,
        &keys,
        common::get(CONFIGS_PATH),
    )
    .await;
    assert_eq!(r.status(), StatusCode::OK);
    assert_eq!(r.headers()["content-type"], "application/octet-stream");
    assert_eq!(r.headers()["cache-control"], "max-age=300");
    let configs = common::body(r).await;
    let (body, plain, secret) =
        common::odoh_client_query(&configs, &common::query("www.example.test.", 1));
    let r = doh::handle(
        &answerer,
        client,
        "/dns-query",
        &target,
        &keys,
        post("/dns-query", CONTENT_TYPE, body.clone()),
    )
    .await;
    assert_eq!(r.status(), StatusCode::OK);
    assert_eq!(r.headers()["content-type"], CONTENT_TYPE);
    assert_eq!(r.headers()["cache-control"], "no-store");
    let answer = common::odoh_client_open(&plain, secret, &common::body(r).await);
    assert!(common::has_a(&answer, "192.0.2.10"));
    let mut garbled = body.clone();
    let n = garbled.len() - 1;
    garbled[n] ^= 1;
    let r = doh::handle(
        &answerer,
        client,
        "/dns-query",
        &target,
        &keys,
        post("/dns-query", CONTENT_TYPE, garbled),
    )
    .await;
    assert_eq!(r.status(), StatusCode::BAD_REQUEST);
    let r = doh::handle(
        &answerer,
        client,
        "/dns-query",
        &target,
        &keys,
        post("/dns-query", CONTENT_TYPE, vec![0; MAX_BODY + 1]),
    )
    .await;
    assert_eq!(r.status(), StatusCode::PAYLOAD_TOO_LARGE);
    keys.set_keys(&proto::OdohKeys {
        keys: vec![proto::OdohKey {
            seed: vec![10; 32],
            publish_after_unix: 0,
            not_after_unix: i64::MAX,
        }],
    });
    let r = doh::handle(
        &answerer,
        client,
        "/dns-query",
        &target,
        &keys,
        post("/dns-query", CONTENT_TYPE, body),
    )
    .await;
    assert_eq!(
        r.status(),
        StatusCode::UNAUTHORIZED,
        "a rotated-away key is unknown"
    );
    let r = doh::handle(
        &answerer,
        client,
        "/dns-query",
        &target,
        &keys,
        post("/dns-query", "application/json", vec![0; 20]),
    )
    .await;
    assert_eq!(r.status(), StatusCode::UNSUPPORTED_MEDIA_TYPE);

    // Target on without a published key: configs 503.
    keys.set_keys(&proto::OdohKeys {
        keys: vec![proto::OdohKey {
            seed: vec![11; 32],
            publish_after_unix: i64::MAX - 1,
            not_after_unix: i64::MAX,
        }],
    });
    let r = doh::handle(
        &answerer,
        client,
        "/dns-query",
        &target,
        &keys,
        common::get(CONFIGS_PATH),
    )
    .await;
    assert_eq!(r.status(), StatusCode::SERVICE_UNAVAILABLE);

    // Proxy off: a targethost request is denied; proxy on: unlisted 403, missing targetpath 400, unreachable 502.
    let r = doh::handle(
        &answerer,
        client,
        "/dns-query",
        &target,
        &keys,
        post(
            "/dns-query?targethost=a.test&targetpath=%2Fq",
            CONTENT_TYPE,
            vec![1],
        ),
    )
    .await;
    assert_eq!(r.status(), StatusCode::FORBIDDEN);
    let proxy = OdohRuntime::build(Some(&proto::OdohConfig {
        proxy_enabled: true,
        proxy_targets: vec![proto::OdohProxyTarget {
            host: format!("127.0.0.1:{}", common::closed_port()),
            ca_pem: String::new(),
        }],
        proxy_timeout_ms: 500,
        ..Default::default()
    }))
    .unwrap();
    let listed = format!(
        "/dns-query?targethost=127.0.0.1:{}&targetpath=%2Fdns-query",
        common::closed_port()
    );
    let r = doh::handle(
        &answerer,
        client,
        "/dns-query",
        &proxy,
        &keys,
        post(
            "/dns-query?targethost=evil.test&targetpath=%2Fq",
            CONTENT_TYPE,
            vec![1],
        ),
    )
    .await;
    assert_eq!(
        (r.status(), r.headers()["proxy-status"].to_str().unwrap()),
        (StatusCode::FORBIDDEN, "nexora; error=http_request_denied")
    );
    let r = doh::handle(
        &answerer,
        client,
        "/dns-query",
        &proxy,
        &keys,
        post("/dns-query?targethost=evil.test", CONTENT_TYPE, vec![1]),
    )
    .await;
    assert_eq!(
        (r.status(), r.headers()["proxy-status"].to_str().unwrap()),
        (StatusCode::BAD_REQUEST, "nexora; error=http_request_error")
    );
    let r = doh::handle(
        &answerer,
        client,
        "/dns-query",
        &proxy,
        &keys,
        post(&listed, CONTENT_TYPE, vec![1]),
    )
    .await;
    assert_eq!(
        (r.status(), r.headers()["proxy-status"].to_str().unwrap()),
        (
            StatusCode::BAD_GATEWAY,
            "nexora; error=destination_unavailable"
        )
    );

    // The same listed request: GET 405, another content type 415, too large 413, and a client
    // outside the recursion ACL 403.
    let r = doh::handle(
        &answerer,
        client,
        "/dns-query",
        &proxy,
        &keys,
        common::get(&listed),
    )
    .await;
    assert_eq!(r.status(), StatusCode::METHOD_NOT_ALLOWED);
    let r = doh::handle(
        &answerer,
        client,
        "/dns-query",
        &proxy,
        &keys,
        post(&listed, "application/dns-message", vec![1]),
    )
    .await;
    assert_eq!(r.status(), StatusCode::UNSUPPORTED_MEDIA_TYPE);
    let r = doh::handle(
        &answerer,
        client,
        "/dns-query",
        &proxy,
        &keys,
        post(&listed, CONTENT_TYPE, vec![0; MAX_BODY + 1]),
    )
    .await;
    assert_eq!(r.status(), StatusCode::PAYLOAD_TOO_LARGE);
    let refused = common::StaticAnswerer {
        recursion: false,
        ..common::StaticAnswerer::a("www.example.test.", "192.0.2.10")
    };
    let r = doh::handle(
        &refused,
        client,
        "/dns-query",
        &proxy,
        &keys,
        post(&listed, CONTENT_TYPE, vec![1]),
    )
    .await;
    assert_eq!(
        (r.status(), r.headers()["proxy-status"].to_str().unwrap()),
        (StatusCode::FORBIDDEN, "nexora; error=http_request_denied")
    );

    // Outcomes are counted by role and status; disabled roles are not ODoH requests.
    let mut text = String::new();
    odoh::COUNTERS.render(&mut text);
    for (role, status, n) in [
        ("target", "200", 2),
        ("target", "400", 1),
        ("target", "401", 1),
        ("target", "404", 0),
        ("target", "413", 1),
        ("target", "503", 1),
        ("proxy", "403", 2),
        ("proxy", "400", 1),
        ("proxy", "405", 1),
        ("proxy", "413", 1),
        ("proxy", "415", 1),
        ("proxy", "502", 1),
    ] {
        let line =
            format!("nexora_odoh_requests_total{{role=\"{role}\",status=\"{status}\"}} {n}\n");
        assert!(text.contains(&line), "want {line:?} in\n{text}");
    }
}

#[test]
fn snapshot_odoh_config_is_validated_and_built() {
    use nexora_engine::runtime::Runtime;
    use nexora_engine::snapshot::{self, SnapshotError};
    struct NoBlobs;
    impl snapshot::BlobSource for NoBlobs {
        fn read(&self, r: &proto::BlobRef) -> Result<Vec<u8>, SnapshotError> {
            Err(SnapshotError::Blob {
                sha256: r.sha256.clone(),
                reason: "none".into(),
            })
        }
    }
    let base = proto::ConfigSnapshot {
        version: 1,
        cache: Some(proto::CacheConfig {
            max_bytes: 1 << 20,
            ..Default::default()
        }),
        ..Default::default()
    };
    let on = proto::ConfigSnapshot {
        odoh: Some(proto::OdohConfig {
            target_enabled: true,
            ..Default::default()
        }),
        ..base.clone()
    };
    snapshot::validate(&on, 0).expect("a target-only config is valid");
    let rt = Runtime::build(&on, &NoBlobs, None).unwrap();
    assert!(rt.odoh.target_enabled && rt.odoh.proxy.is_none());
    assert!(
        !Runtime::build(&base, &NoBlobs, None)
            .unwrap()
            .odoh
            .target_enabled,
        "absent config: off"
    );
    assert!(!Runtime::initial().odoh.target_enabled);

    let bad = proto::ConfigSnapshot {
        odoh: Some(proto::OdohConfig {
            proxy_enabled: true,
            ..Default::default()
        }),
        ..base
    };
    match snapshot::validate(&bad, 0) {
        Err(SnapshotError::Invalid(m)) => assert!(m.starts_with("odoh: "), "{m}"),
        other => panic!("proxy without targets must be invalid, got {other:?}"),
    }
}

#[test]
fn engine_metrics_include_odoh_requests() {
    let shared = nexora_engine::server::Shared::new(1);
    let text = shared
        .metrics
        .render(&shared.runtime.load(), &shared.recursor);
    assert!(
        text.contains("# TYPE nexora_odoh_requests counter\n"),
        "{text}"
    );
    assert!(
        text.ends_with("# EOF\n"),
        "the family comes before the OpenMetrics EOF marker"
    );
}
