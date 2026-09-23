package catzone

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

// Service manages catalog zones: producers regenerate their records when membership changes,
// consumers create and delete secondary zones after each refresh of the catalog.
type Service struct {
	Store *store.Store
	Zones *zone.Service
	Build snapshot.BuildConfig
	Now   func() time.Time
}

// MemberView is one member of a catalog: a zone it lists ("configured") or, for consumers, a
// listed member that was not applied ("clash", with the reason in Issue).
type MemberView struct {
	ZoneID                    *uuid.UUID
	Name, Label, State, Issue string
}

// View is a catalog zone with its members.
type View struct {
	ID, ZoneID      uuid.UUID
	Name, Role      string
	EngineGroupID   *uuid.UUID
	BrokenReason    string
	ProcessedSerial *int64
	ProcessedAt     *time.Time
	Members         []MemberView
	CreatedAt       time.Time
}

// CreateInput creates a producer (Transfer, Notify) or consumer (Primaries) catalog zone.
type CreateInput struct {
	Name, Role    string
	EngineGroupID *uuid.UUID
	Primaries     []zone.Endpoint
	Transfer      zone.TransferInput
	Notify        []zone.Endpoint
}

// systemActor runs consumer reconciliations.
var systemActor = auth.Actor{Type: "system", ID: "catzone", Name: "system:catzone"}

// errPublish rolls back a reconciliation that changes zones, so it runs again inside a publish.
var errPublish = errors.New("catalog reconciliation changes zones")

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func invalid(msg string) error { return &zone.ValidationError{Code: "invalid_zone", Message: msg} }

func lock(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('catalog:' || $1::text))", id)
	return err
}

// Create creates the catalog zone and its catalog_zones row in one published version.
func (s *Service) Create(ctx context.Context, actor auth.Actor, in CreateInput) (View, error) {
	zin := zone.CreateZoneInput{Name: in.Name, EngineGroupID: in.EngineGroupID, Primaries: in.Primaries,
		Transfer: in.Transfer, Notify: in.Notify}
	switch in.Role {
	case "producer":
		if len(in.Transfer.AllowCIDRs) == 0 {
			return View{}, invalid("transfer.allow_cidrs needs at least one CIDR for a producer catalog")
		}
		zin.Kind = "primary"
		zin.SOA = zone.SOA{MName: "invalid.", RName: "hostmaster.invalid."}
		zin.Nameservers = []string{"invalid."}
		zin.AllowQueryCIDRs = []string{"127.0.0.1/32", "::1/128"}
	case "consumer":
		if len(in.Primaries) == 0 {
			return View{}, invalid("primaries needs at least one primary for a consumer catalog")
		}
		zin.Kind = "secondary" // CreateZoneInTx requests the first refresh
	default:
		return View{}, invalid(fmt.Sprintf("role %q must be producer or consumer", in.Role))
	}
	var out View
	_, err := snapshot.Mutate(ctx, s.Store, s.Build, actor, func(tx pgx.Tx) (auth.Change, error) {
		z, err := s.Zones.CreateZoneInTx(ctx, tx, actor, zin)
		if err != nil {
			return auth.Change{}, err
		}
		var id uuid.UUID
		if err := tx.QueryRow(ctx, "INSERT INTO catalog_zones (zone_id, role) VALUES ($1, $2) RETURNING id", z.ID, in.Role).Scan(&id); err != nil {
			return auth.Change{}, err
		}
		if in.Role == "producer" {
			if err := s.Regenerate(ctx, tx, []uuid.UUID{id}); err != nil {
				return auth.Change{}, err
			}
		}
		if out, err = get(ctx, tx, id); err != nil {
			return auth.Change{}, err
		}
		return auth.Change{Action: "createCatalogZone", TargetType: "catalog_zone", TargetID: id.String(), After: out}, nil
	})
	return out, err
}

// List returns every catalog zone ordered by name.
func (s *Service) List(ctx context.Context) ([]View, error) {
	rows, err := s.Store.Pool.Query(ctx, "SELECT c.id FROM catalog_zones c JOIN zones z ON z.id = c.zone_id ORDER BY z.name")
	if err != nil {
		return nil, store.MapError(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, store.MapError(err)
	}
	out := make([]View, 0, len(ids))
	// debt: two queries per catalog; revisit when an installation has hundreds of catalogs.
	for _, id := range ids {
		v, err := get(ctx, s.Store.Pool, id)
		if errors.Is(err, store.ErrNotFound) {
			continue // deleted meanwhile
		}
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Get returns one catalog zone with its members.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (View, error) {
	return get(ctx, s.Store.Pool, id)
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func get(ctx context.Context, q querier, id uuid.UUID) (View, error) {
	v := View{ID: id}
	err := q.QueryRow(ctx, `SELECT c.zone_id, z.name, c.role, z.engine_group_id, c.broken_reason, c.processed_serial, c.processed_at, c.created_at
		FROM catalog_zones c JOIN zones z ON z.id = c.zone_id WHERE c.id = $1`, id).
		Scan(&v.ZoneID, &v.Name, &v.Role, &v.EngineGroupID, &v.BrokenReason, &v.ProcessedSerial, &v.ProcessedAt, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return View{}, fmt.Errorf("catalog zone %s: %w", id, store.ErrNotFound)
	}
	if err != nil {
		return View{}, store.MapError(err)
	}
	rows, err := q.Query(ctx, `SELECT id, name, catalog_member_label, 'configured', '' FROM zones WHERE catalog_zone_id = $1
		UNION ALL SELECT NULL, member_name, label, 'clash', issue FROM catalog_member_issues WHERE catalog_zone_id = $1
		ORDER BY 2`, id)
	if err != nil {
		return View{}, store.MapError(err)
	}
	v.Members, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (MemberView, error) {
		var m MemberView
		err := row.Scan(&m.ZoneID, &m.Name, &m.Label, &m.State, &m.Issue)
		if v.Role == "producer" && m.ZoneID != nil {
			m.Label = Label(*m.ZoneID)
		}
		return m, err
	})
	return v, store.MapError(err)
}

// Delete removes the catalog zone. Its members stay as ordinary zones outside any catalog.
func (s *Service) Delete(ctx context.Context, actor auth.Actor, id uuid.UUID) error {
	_, err := snapshot.Mutate(ctx, s.Store, s.Build, actor, func(tx pgx.Tx) (auth.Change, error) {
		if err := lock(ctx, tx, id); err != nil {
			return auth.Change{}, err
		}
		before, err := get(ctx, tx, id)
		if err != nil {
			return auth.Change{}, err
		}
		if _, err := tx.Exec(ctx, "UPDATE zones SET catalog_zone_id = NULL, catalog_member_label = '' WHERE catalog_zone_id = $1", id); err != nil {
			return auth.Change{}, err
		}
		if err := s.Zones.DeleteZoneInTx(ctx, tx, actor, before.ZoneID); err != nil {
			return auth.Change{}, err
		}
		return auth.Change{Action: "deleteCatalogZone", TargetType: "catalog_zone", TargetID: id.String(), Before: before}, nil
	})
	return err
}

// Regenerate implements zone.Service.CatalogChanged: rewrites and rebuilds each producer catalog zone.
func (s *Service) Regenerate(ctx context.Context, tx pgx.Tx, catalogZoneIDs []uuid.UUID) error {
	ids := slices.Clone(catalogZoneIDs)
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	for _, id := range slices.Compact(ids) {
		if err := lock(ctx, tx, id); err != nil {
			return err
		}
		var zoneID uuid.UUID
		err := tx.QueryRow(ctx, "SELECT zone_id FROM catalog_zones WHERE id = $1 AND role = 'producer'", id).Scan(&zoneID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT id, name FROM zones WHERE catalog_zone_id = $1 ORDER BY id", id)
		if err != nil {
			return err
		}
		members, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Member, error) {
			var mid uuid.UUID
			var name string
			err := row.Scan(&mid, &name)
			return Member{Label: Label(mid), Zone: name}, err
		})
		if err != nil {
			return err
		}
		z, err := zone.LoadZoneTx(ctx, tx, zoneID, true)
		if err != nil {
			return err
		}
		if err := zone.SetRecords(ctx, tx, zoneID, Build(z.Name, members)); err != nil {
			return err
		}
		if _, err := zone.Rebuild(ctx, tx, s.Zones.Signer, z, zone.RebuildOptions{}, s.now()); err != nil {
			return err
		}
	}
	return nil
}

// reconciliation is the audited outcome of processing a consumer catalog.
type reconciliation struct {
	Created   []string `json:"created"`
	Deleted   []string `json:"deleted"`
	Recreated []string `json:"recreated"`
	Clashes   []string `json:"clashes"`
}

func (r reconciliation) changesZones() bool {
	return len(r.Created)+len(r.Deleted)+len(r.Recreated) > 0
}

// Reconcile processes a consumer catalog after its zone refreshed; other zone ids are ignored.
func (s *Service) Reconcile(ctx context.Context, zoneID uuid.UUID) error {
	var id uuid.UUID
	err := s.Store.Pool.QueryRow(ctx, "SELECT id FROM catalog_zones WHERE zone_id = $1 AND role = 'consumer'", zoneID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return store.MapError(err)
	}
	// A pass that only records clashes, a broken reason or the processed serial commits without a
	// config version; one that creates or deletes zones rolls back and runs again in a publish.
	err = s.Store.InTx(ctx, func(tx pgx.Tx) error {
		r, err := s.reconcile(ctx, tx, id)
		if err == nil && r.changesZones() {
			return errPublish
		}
		return err
	})
	if !errors.Is(err, errPublish) {
		return err
	}
	_, err = snapshot.Mutate(ctx, s.Store, s.Build, systemActor, func(tx pgx.Tx) (auth.Change, error) {
		r, err := s.reconcile(ctx, tx, id)
		return auth.Change{Action: "reconcileCatalogZone", TargetType: "catalog_zone", TargetID: id.String(), After: r}, err
	})
	return err
}

func (s *Service) reconcile(ctx context.Context, tx pgx.Tx, id uuid.UUID) (reconciliation, error) {
	r := reconciliation{Created: []string{}, Deleted: []string{}, Recreated: []string{}, Clashes: []string{}}
	if err := lock(ctx, tx, id); err != nil {
		return r, err
	}
	var zoneID uuid.UUID
	var broken string
	var processed *int64
	err := tx.QueryRow(ctx, "SELECT zone_id, broken_reason, processed_serial FROM catalog_zones WHERE id = $1", id).Scan(&zoneID, &broken, &processed)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, nil // deleted meanwhile
	}
	if err != nil {
		return r, err
	}
	cat, err := zone.LoadZoneTx(ctx, tx, zoneID, false)
	if err != nil {
		return r, err
	}
	if cat.Expired || !cat.Loaded || (processed != nil && *processed == int64(cat.Serial) && broken == "") {
		return r, nil
	}
	served, err := zone.LoadServed(ctx, tx, cat)
	if err != nil {
		return r, err
	}
	processedNow := func(reason string) error {
		_, err := tx.Exec(ctx, "UPDATE catalog_zones SET broken_reason = $2, processed_serial = $3, processed_at = $4 WHERE id = $1",
			id, reason, int64(cat.Serial), s.now())
		return err
	}
	members, err := Parse(cat.Name, served)
	if be := (*BrokenError)(nil); errors.As(err, &be) {
		return r, processedNow(be.Reason)
	}
	if err != nil {
		return r, err
	}

	type owned struct {
		id    uuid.UUID
		label string
	}
	current := map[string]owned{} // member name -> zone this catalog created
	var o owned
	var name string
	rows, err := tx.Query(ctx, "SELECT id, name, catalog_member_label FROM zones WHERE catalog_zone_id = $1", id)
	if err != nil {
		return r, err
	}
	if _, err := pgx.ForEachRow(rows, []any{&o.id, &name, &o.label}, func() error {
		current[name] = o
		return nil
	}); err != nil {
		return r, err
	}

	listed := map[string]bool{}
	var create []Member
	type clash struct{ name, label, issue string }
	var clashes []clash
	for _, m := range members {
		listed[m.Zone] = true
		o, ok := current[m.Zone]
		switch {
		case ok && o.label == m.Label:
		case ok:
			if err := s.Zones.DeleteZoneInTx(ctx, tx, systemActor, o.id); err != nil {
				return r, err
			}
			r.Recreated = append(r.Recreated, m.Zone)
			create = append(create, m)
		case m.Zone == cat.Name:
			clashes = append(clashes, clash{m.Zone, m.Label, "the member is the catalog zone itself"})
		default:
			var taken bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM zones WHERE name = $1)", m.Zone).Scan(&taken); err != nil {
				return r, err
			}
			if taken {
				clashes = append(clashes, clash{m.Zone, m.Label, "a zone with this name exists and was not created by this catalog"})
				continue
			}
			r.Created = append(r.Created, m.Zone)
			create = append(create, m)
		}
	}
	for name, o := range current {
		if !listed[name] {
			if err := s.Zones.DeleteZoneInTx(ctx, tx, systemActor, o.id); err != nil {
				return r, err
			}
			r.Deleted = append(r.Deleted, name)
		}
	}
	slices.Sort(r.Deleted)
	for _, m := range create {
		// CreateZoneInTx requests the first refresh of the secondary (trigger "create").
		_, err := s.Zones.CreateZoneInTx(ctx, tx, systemActor, zone.CreateZoneInput{Name: m.Zone, Kind: "secondary",
			Primaries: cat.Primaries, EngineGroupID: cat.EngineGroupID, CatalogZoneID: &id, CatalogMemberLabel: m.Label})
		var verr *zone.ValidationError
		if errors.As(err, &verr) {
			// Validation runs before any statement, so the transaction is still usable.
			clashes = append(clashes, clash{m.Zone, m.Label, verr.Message})
			r.Created = slices.DeleteFunc(r.Created, func(n string) bool { return n == m.Zone })
			continue
		}
		if err != nil {
			return r, err
		}
	}
	if _, err := tx.Exec(ctx, "DELETE FROM catalog_member_issues WHERE catalog_zone_id = $1", id); err != nil {
		return r, err
	}
	for _, c := range clashes {
		if _, err := tx.Exec(ctx, "INSERT INTO catalog_member_issues (catalog_zone_id, member_name, label, issue) VALUES ($1, $2, $3, $4)",
			id, c.name, c.label, c.issue); err != nil {
			return r, err
		}
		r.Clashes = append(r.Clashes, c.name)
	}
	return r, processedNow("")
}
