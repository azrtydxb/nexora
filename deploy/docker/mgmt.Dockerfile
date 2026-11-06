# syntax=docker/dockerfile:1.10
# Cross-build: every stage that runs commands is pinned to $BUILDPLATFORM; arm64 is
# cross-compiled (gcc-aarch64-linux-gnu for cgo), never emulated. The final stage has no RUN.
FROM --platform=$BUILDPLATFORM node:26-trixie-slim AS web
RUN npm install -g pnpm@10
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml ./
RUN --mount=type=cache,target=/root/.local/share/pnpm/store pnpm install --frozen-lockfile
COPY web ./
COPY mgmt/api/openapi.yaml /src/mgmt/api/openapi.yaml
# The build stamp shown in the GUI footer (__NEXORA_VERSION__ and friends in web/vite.config.ts).
ARG VERSION=dev
ARG COMMIT=
ARG BUILD_DATE=
RUN NEXORA_VERSION=${VERSION} NEXORA_COMMIT=${COMMIT} NEXORA_BUILD_DATE=${BUILD_DATE} pnpm run build

FROM --platform=$BUILDPLATFORM golang:1.27-trixie AS build
ARG TARGETOS
ARG TARGETARCH
RUN case "$TARGETARCH" in \
      amd64) echo x86_64-linux-gnu > /gnu-triple; cross_pkgs="gcc-x86-64-linux-gnu libc6-dev-amd64-cross" ;; \
      arm64) echo aarch64-linux-gnu > /gnu-triple; cross_pkgs="gcc-aarch64-linux-gnu libc6-dev-arm64-cross" ;; \
      *) echo "unsupported TARGETARCH=$TARGETARCH" >&2; exit 1 ;; \
    esac \
 && if [ "$(dpkg --print-architecture)" != "$TARGETARCH" ]; then apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends $cross_pkgs && rm -rf /var/lib/apt/lists/*; fi
ARG VERSION=dev
ARG COMMIT=
ARG BUILD_DATE=
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY gen gen
COPY mgmt mgmt
COPY --from=web /src/web/dist mgmt/internal/webui/dist
# cgo: PKCS#11 modules (NEXORA_PKCS11_MODULE) are C shared libraries loaded with dlopen, so the binary
# links glibc dynamically and the runtime image is Debian (same trixie glibc as the build stage).
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build,id=nexora-mgmt-gobuild-${TARGETARCH} \
    set -eux; \
    if [ "$(dpkg --print-architecture)" != "$TARGETARCH" ]; then export CC="$(cat /gnu-triple)-gcc"; fi; \
    CGO_ENABLED=1 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" -o /nexora-mgmt ./mgmt/cmd/nexora-mgmt; \
    if [ "$(dpkg --print-architecture)" = "$TARGETARCH" ]; then /nexora-mgmt version; fi

# No PKCS#11 module is installed: mount the HSM vendor's module (and its configuration) and point
# NEXORA_PKCS11_MODULE at it; without one, key storage uses the key-encryption key (NEXORA_KEK_FILE).
# Users and CA certificates are prepared on the build platform, then copied.
FROM --platform=$BUILDPLATFORM debian:trixie-slim AS rootfs
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --gid 65532 nonroot \
 && useradd --uid 65532 --gid 65532 --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin nonroot

# uid and gid 65532 as in the former distroless :nonroot image: a pod that sets runAsUser but not
# runAsGroup gets the gid of this passwd entry, which must match fsGroup to read 0440 secret files.
FROM debian:trixie-slim
COPY --from=rootfs /etc/passwd /etc/group /etc/shadow /etc/gshadow /etc/
COPY --from=rootfs /etc/ssl/certs /etc/ssl/certs
COPY --from=rootfs /usr/share/ca-certificates /usr/share/ca-certificates
COPY --from=rootfs /etc/ca-certificates.conf /etc/ca-certificates.conf
COPY --from=build /nexora-mgmt /nexora-mgmt
USER 65532:65532
EXPOSE 8080 9443
ENTRYPOINT ["/nexora-mgmt"]
CMD ["serve"]
