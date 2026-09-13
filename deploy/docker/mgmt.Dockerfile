# syntax=docker/dockerfile:1.10
FROM node:26-trixie-slim AS web
RUN npm install -g pnpm@10
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml ./
RUN --mount=type=cache,target=/root/.local/share/pnpm/store pnpm install --frozen-lockfile
COPY web ./
COPY mgmt/api/openapi.yaml /src/mgmt/api/openapi.yaml
RUN pnpm run build

FROM golang:1.27-trixie AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY gen gen
COPY mgmt mgmt
COPY --from=web /src/web/dist mgmt/internal/webui/dist
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /nexora-mgmt ./mgmt/cmd/nexora-mgmt

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /nexora-mgmt /nexora-mgmt
# The distroless :nonroot image already defaults to this user; stated so it cannot silently change.
USER nonroot:nonroot
EXPOSE 8080 9443
ENTRYPOINT ["/nexora-mgmt"]
CMD ["serve"]
