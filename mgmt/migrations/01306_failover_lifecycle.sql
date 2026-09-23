-- +goose Up
-- No runtime source is authorized by migration. Dedicated LOGIN roles and their
-- narrowly scoped grants are operator provisioning, never desired-state API input.
CREATE TABLE failover_sources (
    role_oid oid PRIMARY KEY,
    incarnation uuid NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    role_name name NOT NULL UNIQUE,
    -- Grant UPDATE only on this inert column when FOR SHARE needs lock privilege.
    lock_marker boolean NOT NULL DEFAULT false,
    purpose text NOT NULL CHECK (purpose IN ('collector','controller','withdrawal'))
);
REVOKE ALL ON failover_sources FROM PUBLIC;
-- Each reconfiguration creates a new authorization incarnation. The inert lock
-- column is the only update privilege that a source LOGIN should receive.
-- +goose StatementBegin
CREATE FUNCTION failover_source_incarnation() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
    NEW.incarnation := gen_random_uuid();
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER failover_source_incarnation_trigger BEFORE UPDATE OF role_oid,role_name,purpose,incarnation ON failover_sources
    FOR EACH ROW EXECUTE FUNCTION failover_source_incarnation();


ALTER TABLE failover_groups ADD COLUMN lifecycle text NOT NULL DEFAULT 'active'
    CHECK (lifecycle IN ('active','draining','withdrawn','deleting','deleted'));
ALTER TABLE failover_groups DROP CONSTRAINT failover_groups_frontend_ip_key;
ALTER TABLE failover_groups DROP CONSTRAINT failover_groups_name_key;
CREATE UNIQUE INDEX failover_groups_live_name ON failover_groups(name) WHERE lifecycle <> 'deleted';
ALTER TABLE failover_members DROP CONSTRAINT failover_members_engine_id_key;
CREATE TABLE failover_ip_reservations (
    frontend_ip inet PRIMARY KEY,
    group_id uuid NOT NULL UNIQUE REFERENCES failover_groups(id) ON DELETE RESTRICT
);
CREATE TABLE failover_engine_reservations (
    engine_id uuid PRIMARY KEY REFERENCES engines(id) ON DELETE RESTRICT,
    group_id uuid NOT NULL REFERENCES failover_groups(id) ON DELETE RESTRICT
);
INSERT INTO public.failover_ip_reservations SELECT frontend_ip,id FROM public.failover_groups;
INSERT INTO public.failover_engine_reservations SELECT engine_id,group_id FROM public.failover_members;

-- Reservations are acquired transactionally even for direct desired SQL writes.
-- Historical members remain reserved when membership changes. No TTL releases them.
-- +goose StatementBegin
CREATE FUNCTION failover_reserve() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'failover history must be retained' USING ERRCODE = '23514';
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF OLD.lifecycle = 'deleted' OR NEW.frontend_ip <> OLD.frontend_ip OR NEW.id <> OLD.id THEN
            RAISE EXCEPTION 'immutable failover identity or tombstone' USING ERRCODE = '23514';
        END IF;
    END IF;
    IF NEW.lifecycle <> 'deleted' THEN
        INSERT INTO public.failover_ip_reservations VALUES (NEW.frontend_ip, NEW.id)
            ON CONFLICT (frontend_ip) DO UPDATE SET group_id=EXCLUDED.group_id
            WHERE failover_ip_reservations.group_id=EXCLUDED.group_id;
        IF NOT FOUND THEN RAISE EXCEPTION 'frontend reserved' USING ERRCODE = '23505'; END IF;
        INSERT INTO public.failover_engine_reservations VALUES (NEW.member_a,NEW.id)
            ON CONFLICT (engine_id) DO UPDATE SET group_id=EXCLUDED.group_id
            WHERE failover_engine_reservations.group_id=EXCLUDED.group_id;
        IF NOT FOUND THEN RAISE EXCEPTION 'member reserved' USING ERRCODE = '23505'; END IF;
        INSERT INTO public.failover_engine_reservations VALUES (NEW.member_b,NEW.id)
            ON CONFLICT (engine_id) DO UPDATE SET group_id=EXCLUDED.group_id
            WHERE failover_engine_reservations.group_id=EXCLUDED.group_id;
        IF NOT FOUND THEN RAISE EXCEPTION 'member reserved' USING ERRCODE = '23505'; END IF;
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER failover_reserve_trigger AFTER INSERT OR UPDATE OR DELETE ON failover_groups
    FOR EACH ROW EXECUTE FUNCTION failover_reserve();

CREATE TABLE failover_publishers (
    group_id uuid PRIMARY KEY REFERENCES failover_groups(id),
    generation bigint NOT NULL CHECK (generation > 0),
    epoch bigint NOT NULL CHECK (epoch > 0),
    owner uuid NOT NULL,
    session uuid NOT NULL UNIQUE,
    sequence bigint NOT NULL DEFAULT 0 CHECK (sequence >= 0)
);
CREATE TABLE failover_publisher_history (
    group_id uuid NOT NULL REFERENCES failover_groups(id),
    generation bigint NOT NULL CHECK (generation > 0),
    epoch bigint NOT NULL,
    owner uuid NOT NULL,
    session uuid NOT NULL UNIQUE,
    PRIMARY KEY(group_id,epoch)
);
CREATE TABLE failover_publications (
    group_id uuid PRIMARY KEY REFERENCES failover_groups(id),
    generation bigint NOT NULL,
    source_incarnation uuid NOT NULL,
    max_age_ms bigint NOT NULL CHECK (max_age_ms BETWEEN 1 AND 30000),
    epoch bigint NOT NULL,
    sequence bigint NOT NULL,
    evidence jsonb NOT NULL,
    collected_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK (expires_at > collected_at)
);
CREATE TABLE failover_withdrawals (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id uuid NOT NULL REFERENCES failover_groups(id),
    generation bigint NOT NULL,
    epoch bigint NOT NULL,
    proof_sha256 text NOT NULL CHECK (length(proof_sha256)=64),
    source name NOT NULL DEFAULT session_user,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(group_id,generation)
);

-- Neither the reservation key nor its owning group may be reassigned in place.
-- +goose StatementBegin
CREATE FUNCTION failover_reservation_immutable() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
    IF NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'reservation identity is immutable' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER failover_ip_immutable BEFORE UPDATE ON failover_ip_reservations
    FOR EACH ROW EXECUTE FUNCTION failover_reservation_immutable();
CREATE TRIGGER failover_engine_immutable BEFORE UPDATE ON failover_engine_reservations
    FOR EACH ROW EXECUTE FUNCTION failover_reservation_immutable();

-- Reservations can only be removed by the dedicated withdrawal identity after
-- an evidence receipt and terminal transition in the same transaction.
-- +goose StatementBegin
CREATE FUNCTION failover_release_guard() RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM public.failover_sources s JOIN pg_catalog.pg_roles r ON r.oid=s.role_oid AND r.rolname=s.role_name
                   WHERE s.role_name=session_user AND s.purpose='withdrawal') OR
       NOT EXISTS (SELECT 1 FROM public.failover_groups g JOIN public.failover_withdrawals w ON w.group_id=g.id
                   AND w.generation=g.generation-1 WHERE g.id=OLD.group_id AND g.lifecycle IN ('withdrawn','deleted')) THEN
        RAISE EXCEPTION 'trusted withdrawal receipt required' USING ERRCODE = '42501';
    END IF;
    RETURN OLD;
END $$;
-- +goose StatementEnd
CREATE TRIGGER failover_ip_release_guard BEFORE DELETE ON failover_ip_reservations
    FOR EACH ROW EXECUTE FUNCTION failover_release_guard();
CREATE TRIGGER failover_engine_release_guard BEFORE DELETE ON failover_engine_reservations
    FOR EACH ROW EXECUTE FUNCTION failover_release_guard();

-- Downgrade is allowed only on an unused installation, never by deleting history.
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM public.failover_groups) OR EXISTS (SELECT 1 FROM public.failover_sources) THEN
        RAISE EXCEPTION '01306 requires an inspected offline rollback; lifecycle history is retained';
    END IF;
END $$;
-- +goose StatementEnd
DROP TRIGGER failover_reserve_trigger ON failover_groups;
DROP FUNCTION failover_reserve();
DROP TABLE failover_ip_reservations, failover_engine_reservations;
DROP FUNCTION failover_release_guard();
DROP FUNCTION failover_reservation_immutable();
DROP TABLE failover_withdrawals, failover_publications, failover_publisher_history, failover_publishers, failover_sources;
DROP FUNCTION failover_source_incarnation();
DROP INDEX failover_groups_live_name;
ALTER TABLE failover_groups DROP COLUMN lifecycle;
ALTER TABLE failover_groups ADD UNIQUE(frontend_ip), ADD UNIQUE(name);
ALTER TABLE failover_members ADD UNIQUE(engine_id);
