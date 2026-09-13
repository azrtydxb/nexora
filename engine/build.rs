fn main() -> Result<(), Box<dyn std::error::Error>> {
    println!("cargo:rerun-if-changed=../proto/nexora/control/v1/control.proto");
    // build_transport(false): the rpc `Connect` would otherwise collide with the
    // generated `EngineControlClient::connect(dst)` constructor; clients are built
    // with `EngineControlClient::new(channel)`.
    tonic_prost_build::configure()
        .build_server(true)
        .build_client(true)
        .build_transport(false)
        .compile_protos(&["../proto/nexora/control/v1/control.proto"], &["../proto"])?;
    Ok(())
}
