# syntax=docker/dockerfile:1.10
FROM golang:1.27-trixie AS build
ARG VERSION=dev
ARG COMMIT=
ARG BUILD_DATE=
WORKDIR /src/operator
COPY operator/go.mod operator/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY operator ./
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/piwi3910/nexora/operator/internal/version.Version=${VERSION} -X github.com/piwi3910/nexora/operator/internal/version.Commit=${COMMIT} -X github.com/piwi3910/nexora/operator/internal/version.BuildDate=${BUILD_DATE}" -o /nexora-operator ./cmd/nexora-operator \
 && /nexora-operator version

FROM debian:trixie-slim
RUN groupadd --gid 65532 nonroot \
 && useradd --uid 65532 --gid 65532 --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin nonroot
# The Nexora chart the operator renders; it ships with the operator of the same commit.
COPY deploy/helm/nexora /charts/nexora
COPY --from=build /nexora-operator /nexora-operator
USER 65532:65532
ENTRYPOINT ["/nexora-operator"]
