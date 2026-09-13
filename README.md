# Nexora

A fast DNS server: a Rust engine (forwarding, recursion, DNSSEC validation and
signing, authoritative zones with transfers and dynamic updates, blocklists,
per-client policy, RPZ, DoT/DoH/DoQ) managed by a stateless Go management plane
with a React GUI, from one host to a fleet of engines in engine groups with
staged, health-gated configuration rollouts.

- `nexora-engine` answers DNS, dials out to the management plane over mTLS and
  keeps serving its last configuration when the management plane is away.
- `nexora-mgmt` serves the GUI, the HTTP API (`/api/v1`, described by
  `mgmt/api/openapi.yaml`) and the engine control service. Run as many replicas
  as you like; all state is in PostgreSQL.

## Quick start

Docker Compose (single host): `deploy/compose` and "Install with Docker
Compose" in `docs/operations.md`.

```sh
cd deploy/compose && cp .env.example .env    # set NEXORA_TAG
openssl rand -hex 24 > secrets/postgres-password
printf 'postgres:5432:nexora:nexora:%s\n' "$(cat secrets/postgres-password)" > secrets/pgpass
chmod 0644 secrets/postgres-password secrets/pgpass
docker compose up -d && docker compose logs mgmt | grep "setup token"   # then open http://localhost:8080/setup
docker compose run --rm -T mgmt join-token create --engine-group default --ttl 1h > secrets/join-token
docker compose --profile engine up -d
# add an upstream under /upstreams (a new install forwards and has none), then:
dig @127.0.0.1 example.com
```

Kubernetes: the Helm chart in `deploy/helm/nexora`; see "Install with Helm" in
`docs/operations.md`.

## Documentation

- `docs/operations.md`: install, first-run setup, enrolling engines, TLS, key
  storage, upgrade, backup and restore, rollouts, engine lifecycle, monitoring,
  performance, known limitations, troubleshooting
- `docs/architecture.md`: how Nexora is built
- `deploy/kw/README.md`: the lab deployment on the kw cluster

## Development

The engine is Linux-only; builds and tests run in the kw dev pod:
`scripts/dev-exec.sh make build`, `scripts/dev-exec.sh make e2e`. Other
targets: `make lint`, `make engine-test`, `make mgmt-test`, `make web-test`,
`make bench`. Generated sources (`gen/go/`, `mgmt/internal/api/gen.go`,
`web/src/api/schema.d.ts`) are regenerated on a workstation with `make proto`.
Release images are built by `.github/workflows/images.yml`.
