# Nexora dev toolbox: the Linux environment the engine, management plane, GUI
# and end-to-end suite are built and tested in. Runs as a pod on kw (arm64).
FROM rust:1.97-trixie

ARG GO_VERSION=1.27.1
ARG NODE_VERSION=26.7.0
ARG BUF_VERSION=1.73.0
ARG OTELCOL_VERSION=0.160.0
ARG HELM_VERSION=4.3.0

RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      postgresql postgresql-contrib \
      dnsperf bind9 bind9-dnsutils knot-dnsutils ldnsutils \
      softhsm2 opensc libsofthsm2 \
      protobuf-compiler libprotobuf-dev clang lld cmake pkg-config libssl-dev \
      rsync jq curl ca-certificates git procps iproute2 less vim-tiny sudo \
    && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL https://go.dev/dl/go${GO_VERSION}.linux-arm64.tar.gz | tar -C /usr/local -xz \
 && curl -fsSL https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-arm64.tar.xz | tar -C /usr/local --strip-components=1 -xJ \
 && npm install -g pnpm@10 \
 && curl -fsSL -o /usr/local/bin/buf https://github.com/bufbuild/buf/releases/download/v${BUF_VERSION}/buf-Linux-aarch64 && chmod +x /usr/local/bin/buf \
 && curl -fsSL https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v${OTELCOL_VERSION}/otelcol-contrib_${OTELCOL_VERSION}_linux_arm64.tar.gz | tar -C /usr/local/bin -xz otelcol-contrib \
 && curl -fsSL https://get.helm.sh/helm-v${HELM_VERSION}-linux-arm64.tar.gz | tar -C /usr/local/bin --strip-components=1 -xz linux-arm64/helm

ENV PATH=/usr/local/go/bin:/root/go/bin:/usr/local/cargo/bin:/usr/lib/postgresql/17/bin:$PATH \
    CARGO_TARGET_DIR=/work/target \
    GOCACHE=/work/cache/go-build \
    GOMODCACHE=/work/cache/gomod \
    PNPM_HOME=/work/cache/pnpm

RUN rustup component add clippy rustfmt \
 && rustup toolchain install nightly --profile minimal \
 && cargo install cargo-fuzz --locked \
 && go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12 \
 && go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2 \
 && go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 \
 && npx -y playwright@1.63.0 install --with-deps chromium \
 && useradd -m -s /bin/bash dev

WORKDIR /work/nexora
CMD ["sleep", "infinity"]
