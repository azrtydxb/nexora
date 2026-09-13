# syntax=docker/dockerfile:1.10
FROM rust:1.97-trixie AS build
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends protobuf-compiler \
 && rm -rf /var/lib/apt/lists/*
# The image tag, compiled into the binary (nexora_engine::VERSION); scripts/build-image.sh passes it.
ARG VERSION=dev
WORKDIR /src
COPY Cargo.toml Cargo.lock rust-toolchain.toml ./
COPY proto proto
COPY engine engine
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/src/target \
    NEXORA_VERSION="${VERSION}" cargo build --locked --release -p nexora-engine \
 && /src/target/release/nexora-engine --version \
 && install -m 0755 target/release/nexora-engine /nexora-engine

FROM debian:trixie-slim
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --home-dir /var/lib/nexora nexora \
 && mkdir -p /var/lib/nexora /etc/nexora && chown nexora:nexora /var/lib/nexora
COPY --from=build /nexora-engine /usr/local/bin/nexora-engine
USER 10001
EXPOSE 53/udp 53/tcp 853/tcp 853/udp 443/tcp 9153/tcp
ENTRYPOINT ["/usr/local/bin/nexora-engine"]
CMD ["--config", "/etc/nexora/engine.toml"]
