-- +goose Up
create table instances (
  id text primary key,
  started_at timestamptz not null default now(),
  heartbeat_at timestamptz not null default now()
);

create table users (
  id uuid primary key default gen_random_uuid(),
  username text not null unique check (username ~ '^[A-Za-z0-9._@-]{1,64}$'),
  email text not null default '',
  password_hash text,
  role text not null check (role in ('viewer', 'operator', 'admin')),
  source text not null default 'local' check (source in ('local', 'oidc')),
  oidc_subject text unique,
  disabled boolean not null default false,
  revision bigint not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table sessions (
  token_hash bytea primary key,
  user_id uuid not null references users(id) on delete cascade,
  created_at timestamptz not null default now(),
  expires_at timestamptz not null,
  last_seen_at timestamptz not null default now()
);
create index sessions_user on sessions(user_id);

create table api_tokens (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users(id) on delete cascade,
  name text not null,
  prefix text not null,
  token_hash bytea not null unique,
  role text not null check (role in ('viewer', 'operator', 'admin')),
  created_at timestamptz not null default now(),
  expires_at timestamptz,
  last_used_at timestamptz,
  revoked_at timestamptz
);

create table setup_tokens (
  singleton boolean primary key default true check (singleton),
  token_hash bytea not null,
  created_by_instance text not null,
  created_at timestamptz not null default now()
);

create table oidc_login_states (
  state text primary key,
  nonce text not null,
  code_verifier text not null,
  return_to text not null default '/',
  expires_at timestamptz not null
);

create table audit_log (
  id bigserial primary key,
  at timestamptz not null default now(),
  actor_type text not null check (actor_type in ('user', 'api_token', 'system')),
  actor_id text not null,
  actor_name text not null,
  action text not null,
  target_type text not null,
  target_id text not null,
  diff jsonb not null,
  config_version bigint
);
create index audit_log_at on audit_log(at desc);

create table config_versions (
  version bigint primary key,
  created_at timestamptz not null default now(),
  created_by text not null,
  summary text not null default '',
  snapshot bytea not null
);

create table resolver_settings (
  singleton boolean primary key default true check (singleton),
  strategy text not null default 'ordered' check (strategy in ('ordered', 'fastest')),
  cache_max_bytes bigint not null default 268435456 check (cache_max_bytes >= 1048576),
  cache_min_ttl integer not null default 0 check (cache_min_ttl >= 0),
  cache_max_ttl integer not null default 86400 check (cache_max_ttl >= cache_min_ttl),
  cache_negative_max_ttl integer not null default 3600 check (cache_negative_max_ttl >= 0),
  cache_stale_window integer not null default 86400 check (cache_stale_window >= 0),
  block_mode text not null default 'null_ip' check (block_mode in ('null_ip', 'nxdomain', 'refused')),
  block_ttl integer not null default 60 check (block_ttl >= 0),
  otlp_endpoint text not null default '',
  trace_sample_one_in integer not null default 0 check (trace_sample_one_in >= 0),
  trace_slow_threshold_us integer not null default 100000 check (trace_slow_threshold_us >= 0),
  revision bigint not null default 1,
  updated_at timestamptz not null default now()
);
insert into resolver_settings default values;

create table upstreams (
  id uuid primary key default gen_random_uuid(),
  name text not null unique,
  protocol text not null check (protocol in ('udp', 'tcp', 'dot', 'doh')),
  address text not null default '',
  tls_server_name text not null default '',
  doh_url text not null default '',
  timeout_ms integer not null default 250 check (timeout_ms between 50 and 5000),
  ca_certificate_pem text not null default '',
  position integer not null,
  enabled boolean not null default true,
  revision bigint not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table access_control (
  singleton boolean primary key default true check (singleton),
  allow_cidrs cidr[] not null default array[
    '127.0.0.0/8', '::1/128', '10.0.0.0/8', '172.16.0.0/12',
    '192.168.0.0/16', '100.64.0.0/10', 'fc00::/7', 'fe80::/10']::cidr[],
  revision bigint not null default 1,
  updated_at timestamptz not null default now()
);
insert into access_control default values;

create table blobs (
  sha256 text primary key check (sha256 ~ '^[0-9a-f]{64}$'),
  size bigint not null,
  data bytea not null,
  created_at timestamptz not null default now()
);

create table filter_lists (
  id uuid primary key default gen_random_uuid(),
  name text not null unique,
  kind text not null check (kind in ('block', 'allow')),
  url text not null check (url ~ '^https?://'),
  refresh_interval_seconds integer not null default 86400 check (refresh_interval_seconds >= 300),
  enabled boolean not null default true,
  current_blob_sha256 text references blobs(sha256),
  entry_count integer not null default 0,
  invalid_line_count integer not null default 0,
  last_success_at timestamptz,
  last_attempt_at timestamptz,
  last_error text not null default '',
  revision bigint not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table allowlist (
  singleton boolean primary key default true check (singleton),
  domains text[] not null default '{}',
  revision bigint not null default 1,
  updated_at timestamptz not null default now()
);
insert into allowlist default values;

create table join_tokens (
  id uuid primary key default gen_random_uuid(),
  name text not null,
  secret_hash bytea not null unique,
  created_by text not null,
  created_at timestamptz not null default now(),
  expires_at timestamptz not null,
  revoked_at timestamptz,
  uses integer not null default 0
);

create table engines (
  id uuid primary key default gen_random_uuid(),
  node_name text not null,
  join_token_id uuid references join_tokens(id) on delete set null,
  certificate_serial text not null,
  engine_version text not null default '',
  enrolled_at timestamptz not null default now(),
  last_seen_at timestamptz,
  connected_instance text references instances(id) on delete set null,
  applied_version bigint not null default 0,
  rejected_version bigint,
  rejected_reason text not null default '',
  persist_error text not null default '',
  version_ahead boolean not null default false,
  deleted_at timestamptz
);

create table engine_stats (
  engine_id uuid not null references engines(id) on delete cascade,
  at timestamptz not null,
  stats bytea not null,
  primary key (engine_id, at)
);

-- +goose Down
drop table engine_stats, engines, join_tokens, allowlist, filter_lists, blobs, access_control,
  upstreams, resolver_settings, config_versions, audit_log, oidc_login_states, setup_tokens,
  api_tokens, sessions, users, instances;
