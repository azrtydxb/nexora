# syntax=docker/dockerfile:1.10
FROM node:26-trixie-slim AS web
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

FROM golang:1.27-trixie AS build
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
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -trimpath -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" -o /nexora-mgmt ./mgmt/cmd/nexora-mgmt \
 && /nexora-mgmt version

# No PKCS#11 module is installed: mount the HSM vendor's module (and its configuration) and point
# NEXORA_PKCS11_MODULE at it; without one, key storage uses the key-encryption key (NEXORA_KEK_FILE).
FROM debian:trixie-slim
# uid and gid 65532 as in the former distroless :nonroot image: a pod that sets runAsUser but not
# runAsGroup gets the gid of this passwd entry, which must match fsGroup to read 0440 secret files.
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --gid 65532 nonroot \
 && useradd --uid 65532 --gid 65532 --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin nonroot
COPY --from=build /nexora-mgmt /nexora-mgmt
USER 65532:65532
EXPOSE 8080 9443
ENTRYPOINT ["/nexora-mgmt"]
CMD ["serve"]
