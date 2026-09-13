#[derive(clap::Parser)]
struct Args {
    #[arg(long, default_value = "/etc/nexora/engine.toml")]
    config: std::path::PathBuf,
}

fn main() {
    let _args = <Args as clap::Parser>::parse();
}
