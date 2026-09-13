package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/control"
	"github.com/piwi3910/nexora/mgmt/internal/fleet"
	"github.com/piwi3910/nexora/mgmt/internal/pki"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
)

var cliActor = auth.Actor{Type: "system", ID: "cli", Name: "cli"}

// cliBuildConfig derives the snapshot build inputs from the same environment `serve` reads, so a
// publish from the CLI builds the snapshots an instance would.
func cliBuildConfig() snapshot.BuildConfig {
	backend := os.Getenv("NEXORA_QUERYLOG_BACKEND")
	return snapshot.BuildConfig{QueryLogToManagement: backend == "" || backend == "builtin", DefaultOTLPEndpoint: os.Getenv("NEXORA_OTLP_ENDPOINT")}
}

// labelFlags collects repeated --label k=v flags.
type labelFlags map[string]string

func (l labelFlags) String() string { return fmt.Sprint(map[string]string(l)) }

func (l labelFlags) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok || k == "" {
		return fmt.Errorf("label %q must be key=value", v)
	}
	l[k] = val
	return nil
}

const (
	engineGroupCreateUsage = "usage: nexora-mgmt engine-group create --name N [--description D] [--if-missing]"
	joinTokenCreateUsage   = "usage: nexora-mgmt join-token create --engine-group G [--name N] [--ttl 24h] [--max-uses N] [--label k=v]..."
)

// engineGroupCreate creates an engine group (published with its first snapshot) and prints its id.
func engineGroupCreate(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("engine-group create", flag.ContinueOnError)
	name := fs.String("name", "", "engine group name")
	description := fs.String("description", "", "description")
	ifMissing := fs.Bool("if-missing", false, "print the id of an existing group of that name instead of failing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || fs.NArg() != 0 {
		return errors.New(engineGroupCreateUsage)
	}
	if !fleet.EngineGroupNameRE.MatchString(*name) || len(*description) > 1024 {
		return fmt.Errorf("engine group name must match %s and the description be at most 1024 characters", fleet.EngineGroupNameRE)
	}
	st, err := openMigrated(ctx)
	if err != nil {
		return err
	}
	defer st.Close()
	var id uuid.UUID
	err = st.Pool.QueryRow(ctx, "select id from engine_groups where name = $1", *name).Scan(&id)
	switch {
	case err == nil && *ifMissing:
		fmt.Fprintln(stdout, id)
		return nil
	case err == nil:
		return fmt.Errorf("engine group %q already exists", *name)
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	g := fleet.EngineGroup{Name: *name, Description: *description, UpstreamMode: "inherit", RolloutStrategy: "all_at_once",
		AckTimeoutSeconds: 60, HealthWindowSeconds: 30, MaxServfailRatio: 0.05, MinHealthQueries: 100}
	_, err = snapshot.Mutate(ctx, st, cliBuildConfig(), cliActor, func(tx pgx.Tx) (auth.Change, error) {
		created, err := fleet.CreateEngineGroup(ctx, tx, g)
		id = created.ID
		return auth.Change{Action: "createEngineGroup", TargetType: "engine_group", TargetID: id.String(), After: created}, err
	})
	if errors.Is(err, fleet.ErrEngineGroupNameTaken) {
		return fmt.Errorf("engine group %q already exists", *name)
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, id)
	return nil
}

// joinTokenCreate creates a join token for an engine group and prints only the token.
func joinTokenCreate(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("join-token create", flag.ContinueOnError)
	group := fs.String("engine-group", "", "engine group name")
	name := fs.String("name", "cli", "join token name")
	ttl := fs.Duration("ttl", 24*time.Hour, "validity (1m..8760h)")
	maxUses := fs.Int("max-uses", 0, "maximum enrollments (0 = unlimited)")
	labels := labelFlags{}
	fs.Var(labels, "label", "engine label k=v (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *group == "" || fs.NArg() != 0 {
		return errors.New(joinTokenCreateUsage)
	}
	switch {
	case len(*name) < 1 || len(*name) > 64:
		return errors.New("--name must be 1-64 characters")
	case *ttl < time.Minute || *ttl > 365*24*time.Hour:
		return errors.New("--ttl must be between 1m and 8760h")
	case *maxUses < 0 || *maxUses > 100000:
		return errors.New("--max-uses must be between 0 (unlimited) and 100000")
	}
	if err := fleet.ValidateLabels(labels); err != nil {
		return err
	}
	st, err := openMigrated(ctx)
	if err != nil {
		return err
	}
	defer st.Close()
	var groupID uuid.UUID
	if err := st.Pool.QueryRow(ctx, "select id from engine_groups where name = $1", *group).Scan(&groupID); errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("engine group %q not found", *group)
	} else if err != nil {
		return err
	}
	fingerprint, err := pki.FingerprintFile(os.Getenv("NEXORA_CA_CERT_FILE"))
	if err != nil {
		return fmt.Errorf("NEXORA_CA_CERT_FILE: %w", err)
	}
	spec := control.JoinTokenSpec{Name: *name, CreatedBy: cliActor.Name, TTL: *ttl, EngineGroupID: groupID, Labels: labels}
	if *maxUses > 0 {
		spec.MaxUses = maxUses
	}
	var token string
	// A join token is not configuration: audited, no config version.
	err = st.InTx(ctx, func(tx pgx.Tx) error {
		jt, t, err := control.InsertJoinToken(ctx, tx, fingerprint, spec)
		if err != nil {
			return err
		}
		token = t
		jt.Labels = spec.Labels
		return auth.WriteAudit(ctx, tx, cliActor, auth.Change{Action: "createJoinToken", TargetType: "join_token", TargetID: jt.ID, After: jt}, nil)
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, token)
	return nil
}

// caFilesPresent reports whether ca.crt and ca.key exist in dir; exactly one is an error.
func caFilesPresent(dir string) (bool, error) {
	var present int
	for _, f := range []string{"ca.crt", "ca.key"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			present++
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	if present == 1 {
		return false, errors.New("ca.crt and ca.key must both exist or both be absent")
	}
	return present == 2, nil
}
