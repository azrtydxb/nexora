package threat_test

import (
	"context"
	"fmt"
	"hash/crc32"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/ai/aifake"
	"github.com/piwi3910/nexora/mgmt/internal/ai/threat"
	"github.com/piwi3910/nexora/mgmt/internal/blocklist"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
)

const blobNames = 10000

func listNames() []string {
	names := make([]string, blobNames)
	for i := range names {
		names[i] = fmt.Sprintf("n%05d.ait.test", i)
	}
	return names
}

// TestSampleIsUniformAndStable catches a sample that repeats names, changes for the same seed, or
// favours one part of the list.
func TestSampleIsUniformAndStable(t *testing.T) {
	names := listNames()
	got := threat.Sample(names, 200, 42)
	if len(got) != 200 {
		t.Fatalf("sample size = %d, want 200", len(got))
	}
	seen := map[string]bool{}
	buckets := make([]float64, 10)
	for _, n := range got {
		if seen[n] {
			t.Fatalf("%q sampled twice", n)
		}
		seen[n] = true
		i := slices.Index(names, n)
		if i < 0 {
			t.Fatalf("%q is not in the list", n)
		}
		buckets[i*10/blobNames]++
	}
	if again := threat.Sample(names, 200, 42); !slices.Equal(again, got) {
		t.Fatal("the same seed sampled different names")
	}
	if other := threat.Sample(names, 200, 43); slices.Equal(other, got) {
		t.Fatal("another seed sampled the same names in the same order")
	}
	// Chi-square over 10 equal buckets, 9 degrees of freedom, p = 0.01.
	expected := 20.0
	chi := 0.0
	for _, b := range buckets {
		chi += (b - expected) * (b - expected) / expected
	}
	if chi >= 21.67 {
		t.Fatalf("chi-square %.2f over buckets %v, want below 21.67", chi, buckets)
	}
	if n := len(threat.Sample(names[:100], 200, 1)); n != 100 {
		t.Fatalf("sample of a shorter list = %d names, want all 100", n)
	}
}

// seedList stores a 10,000-name block list and returns its id and blob SHA-256.
func seedList(t *testing.T, st *store.Store, names []string) (uuid.UUID, string) {
	t.Helper()
	ctx := context.Background()
	data, _, err := blocklist.Compress(blocklist.Normalize(names))
	if err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	var sha string
	if err := st.InTx(ctx, func(tx pgx.Tx) error {
		sha, _, err = store.PutBlob(ctx, tx, data)
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `insert into filter_lists(name, kind, url, enabled, current_blob_sha256, entry_count)
			values ('ait-block', 'block', 'https://lists.ait.test/block', true, $1, $2) returning id`, sha, len(names)).Scan(&id)
	}); err != nil {
		t.Fatal(err)
	}
	return id, sha
}

// TestListClassificationSampling catches batches that are not 50 names, estimated counts that are not the
// sampled share of the entry count, and a list whose unchanged blob is classified again.
func TestListClassificationSampling(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	names := listNames()
	id, sha := seedList(t, st, names)

	sample := threat.Sample(names, 200, int64(crc32.ChecksumIEEE([]byte(sha))))
	var answers []any
	for i, batch := range slices.Collect(slices.Chunk(sample, 50)) {
		if len(batch) != 50 {
			t.Fatalf("batch %d has %d names, want 50", i, len(batch))
		}
		category := "malware"
		if i >= 2 {
			category = "none"
		}
		items := make([]map[string]any, 0, len(batch))
		for _, n := range batch {
			items = append(items, map[string]any{"name": n, "category": category})
		}
		answers = append(answers, map[string]any{"items": items})
	}
	m := aifake.Model(aifake.JSON(answers[0]), aifake.JSON(answers[1]), aifake.JSON(answers[2]), aifake.JSON(answers[3]))
	agent := &threat.ClassifyAgent{Store: st, Service: aifake.Service(t, st, m, nil), Now: func() time.Time { return now }}
	if agent.Name() != "threat_classification" {
		t.Fatalf("agent name = %q", agent.Name())
	}
	run := &ai.Run{Agent: agent.Name(), Started: now, Outcome: "ok", Detail: map[string]any{}}
	if err := agent.Run(ctx, run); err != nil {
		t.Fatal(err)
	}
	if n := len(m.RecordedCalls()); n != 4 {
		t.Fatalf("model calls = %d, want 4 batches of 50", n)
	}

	c, err := threat.GetClassification(ctx, st.Pool, id)
	if err != nil {
		t.Fatal(err)
	}
	if c.SampleSize != 200 || c.EntryCount != blobNames || c.BlobSHA256 != sha || c.ClassifiedAt == nil || !c.ClassifiedAt.Equal(now) {
		t.Fatalf("classification = %+v", c)
	}
	var malware, none bool
	for _, b := range c.Breakdown {
		switch b.Category {
		case "malware":
			malware = b.Sampled == 100 && b.Estimated == 5000
		case "none":
			none = b.Sampled == 100 && b.Estimated == 5000
		}
	}
	if !malware || !none {
		t.Fatalf("breakdown = %+v", c.Breakdown)
	}

	// The blob did not change, so the second run classifies nothing.
	run = &ai.Run{Agent: agent.Name(), Started: now, Outcome: "ok", Detail: map[string]any{}}
	if err := agent.Run(ctx, run); err != nil {
		t.Fatal(err)
	}
	if n := len(m.RecordedCalls()); n != 4 {
		t.Fatalf("model calls after an unchanged run = %d, want 4", n)
	}
	if run.Outcome != "no_change" {
		t.Fatalf("outcome of a run without work = %q", run.Outcome)
	}
}
