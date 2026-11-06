# syntax=docker/dockerfile:1.10
# Cross-build: every stage that runs commands is pinned to $BUILDPLATFORM; arm64 is
# cross-compiled (gcc-aarch64-linux-gnu), never emulated. The final stage has no RUN.
FROM --platform=$BUILDPLATFORM rust:1.97-trixie AS build
ARG TARGETARCH
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends protobuf-compiler cmake \
 && case "$TARGETARCH" in \
      amd64) echo x86_64-unknown-linux-gnu > /rust-target; echo x86_64-linux-gnu > /gnu-triple; cross_pkgs="gcc-x86-64-linux-gnu libc6-dev-amd64-cross" ;; \
      arm64) echo aarch64-unknown-linux-gnu > /rust-target; echo aarch64-linux-gnu > /gnu-triple; cross_pkgs="gcc-aarch64-linux-gnu libc6-dev-arm64-cross" ;; \
      *) echo "unsupported TARGETARCH=$TARGETARCH" >&2; exit 1 ;; \
    esac \
 && if [ "$(dpkg --print-architecture)" != "$TARGETARCH" ]; then DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends $cross_pkgs; fi \
 && rm -rf /var/lib/apt/lists/*
# The image tag, compiled into the binary (nexora_engine::VERSION); scripts/build-image.sh passes it.
ARG VERSION=dev
# The full commit hash the image was built from (empty outside a git checkout).
ARG COMMIT=
WORKDIR /src
COPY Cargo.toml Cargo.lock rust-toolchain.toml ./
COPY proto proto
COPY engine engine
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/src/target,id=nexora-engine-target-${TARGETARCH} \
    set -eux; \
    T="$(cat /rust-target)"; G="$(cat /gnu-triple)"; \
    rustup target add "$T"; \
    if [ "$(dpkg --print-architecture)" != "$TARGETARCH" ]; then \
      export "CARGO_TARGET_$(echo "$T" | tr 'a-z-' 'A-Z_')_LINKER=${G}-gcc" "CC_$(echo "$T" | tr - _)=${G}-gcc"; \
    fi; \
    NEXORA_VERSION="${VERSION}" NEXORA_COMMIT="${COMMIT}" cargo build --locked --release --target "$T" -p nexora-engine; \
    if [ "$(dpkg --print-architecture)" = "$TARGETARCH" ]; then "/src/target/$T/release/nexora-engine" --version; fi; \
    install -m 0755 "target/$T/release/nexora-engine" /nexora-engine

# Users, CA certificates and state dirs are prepared here (build platform), then copied.
FROM --platform=$BUILDPLATFORM debian:trixie-slim AS rootfs
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --home-dir /var/lib/nexora nexora \
 && mkdir -p /var/lib/nexora /etc/nexora && chown nexora:nexora /var/lib/nexora

FROM debian:trixie-slim
COPY --from=rootfs /etc/passwd /etc/group /etc/shadow /etc/gshadow /etc/
COPY --from=rootfs /etc/ssl/certs /etc/ssl/certs
COPY --from=rootfs /usr/share/ca-certificates /usr/share/ca-certificates
COPY --from=rootfs /etc/ca-certificates.conf /etc/ca-certificates.conf
COPY --from=rootfs --chown=10001:10001 /var/lib/nexora /var/lib/nexora
COPY --from=rootfs /etc/nexora /etc/nexora
COPY --from=build /nexora-engine /usr/local/bin/nexora-engine
USER 10001
EXPOSE 53/udp 53/tcp 853/tcp 853/udp 443/tcp 9153/tcp
ENTRYPOINT ["/usr/local/bin/nexora-engine"]
CMD ["--config", "/etc/nexora/engine.toml"]
