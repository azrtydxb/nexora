use nexora_engine::control::{Identity, backoff, load_identity, parse_join_token, save_identity};
use std::time::Duration;

#[test]
fn join_token_parsing() {
    let fp = "ab".repeat(32);
    let t = parse_join_token(&format!("nxj1.MFRGGZDFMZTWQ2LK.{fp}")).unwrap();
    assert_eq!(t.secret, "MFRGGZDFMZTWQ2LK");
    assert_eq!(t.ca_fingerprint, fp);
    for bad in [
        "nxj2.AAAA.".to_string() + &fp,
        "nxj1..".into(),
        format!("nxj1.AAAA.{}", "zz".repeat(32)),
        "nxj1.AAAA".into(),
    ] {
        assert!(parse_join_token(&bad).is_err(), "accepted {bad}");
    }
    assert!(
        parse_join_token(&format!("  nxj1.AAAA.{fp}\n")).is_ok(),
        "whitespace from files is trimmed"
    );
}

#[test]
fn identity_round_trip_with_private_key_mode() {
    use std::os::unix::fs::PermissionsExt;
    let dir = tempfile::tempdir().unwrap();
    assert!(load_identity(dir.path()).unwrap().is_none());
    let id = Identity {
        engine_id: "e-1".into(),
        cert_pem: "CERT".into(),
        key_pem: "KEY".into(),
        ca_pem: "CA".into(),
    };
    save_identity(dir.path(), &id).unwrap();
    let back = load_identity(dir.path()).unwrap().unwrap();
    assert_eq!(back.engine_id, "e-1");
    assert_eq!(back.key_pem, "KEY");
    let mode = std::fs::metadata(dir.path().join("identity/key.pem"))
        .unwrap()
        .permissions()
        .mode()
        & 0o777;
    assert_eq!(mode, 0o600);
}

#[test]
fn backoff_grows_jitters_and_caps() {
    for _ in 0..50 {
        let b0 = backoff(0);
        assert!(
            b0 >= Duration::from_millis(400) && b0 <= Duration::from_millis(600),
            "{b0:?}"
        );
        assert!(backoff(20) <= Duration::from_secs(36));
    }
    assert!(backoff(3) > backoff(0));
}
