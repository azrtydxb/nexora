# syntax=docker/dockerfile:1.10
# Cross-build: Go stages run on $BUILDPLATFORM (CGO off), never emulated; the final stage has no RUN.
FROM --platform=$BUILDPLATFORM golang:1.27-trixie AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=
ARG BUILD_DATE=
WORKDIR /src/operator
COPY operator/go.mod operator/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY operator ./
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build,id=nexora-operator-gobuild-${TARGETARCH} \
    set -eux; \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath -ldflags "-s -w -X github.com/piwi3910/nexora/operator/internal/version.Version=${VERSION} -X github.com/piwi3910/nexora/operator/internal/version.Commit=${COMMIT} -X github.com/piwi3910/nexora/operator/internal/version.BuildDate=${BUILD_DATE}" -o /nexora-operator ./cmd/nexora-operator; \
    if [ "$(dpkg --print-architecture)" = "$TARGETARCH" ]; then /nexora-operator version; fi

# The nonroot user is created on the build platform, then copied.
FROM --platform=$BUILDPLATFORM debian:trixie-slim AS rootfs
RUN groupadd --gid 65532 nonroot \
 && useradd --uid 65532 --gid 65532 --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin nonroot

FROM debian:trixie-slim
COPY --from=rootfs /etc/passwd /etc/group /etc/shadow /etc/gshadow /etc/
# The Nexora chart the operator renders; it ships with the operator of the same commit.
COPY deploy/helm/nexora /charts/nexora
COPY --from=build /nexora-operator /nexora-operator
USER 65532:65532
ENTRYPOINT ["/nexora-operator"]
