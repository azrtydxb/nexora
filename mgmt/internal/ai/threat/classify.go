package threat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/klauspost/compress/zstd"

	"github.com/piwi3910/nexora/mgmt/internal/ai"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// ClassifyName is the background agent's name.
const ClassifyName = "threat_classification"

const (
	// SampleSize is how many names of a list one classification judges.
	SampleSize = 200
	// classifyBatch is how many sampled names one model call classifies.
	classifyBatch = 50
	// maxListsPerRun caps the lists one run classifies.
	maxListsPerRun = 20
	// maxBlobLine is the longest accepted line of a list blob.
	maxBlobLine = 1 << 16
	// noCategory is the category of a name that is no threat.
	noCategory = "none"
)

// Bucket is one category's share of a classified sample.
type Bucket struct {
	Category  string `json:"category"`
	Sampled   int    `json:"sampled"`
	Estimated int64  `json:"estimated"`
}

// Classification is a block list's sampled category breakdown (AiListClassification).
type Classification struct {
	ListID       uuid.UUID  `json:"list_id"`
	BlobSHA256   string     `json:"blob_sha256"`
	SampleSize   int        `json:"sample_size"`
	EntryCount   int64      `json:"entry_count"`
	ClassifiedAt *time.Time `json:"classified_at"`
	Breakdown    []Bucket   `json:"breakdown"`
}

// Sample returns n names drawn uniformly without replacement from names, the same ones for the same
// seed. Shorter lists are returned whole.
func Sample(names []string, n int, seed int64) []string {
	if n <= 0 {
		return []string{}
	}
	out := slices.Clone(names)
	if len(out) <= n {
		return out
	}
	// A partial Fisher-Yates shuffle: every name has the same chance of ending up in the first n.
	r := rand.New(rand.NewPCG(uint64(seed), 0x9e3779b97f4a7c15))
	for i := range n {
		j := i + r.IntN(len(out)-i)
		out[i], out[j] = out[j], out[i]
	}
	return out[:n:n]
}

// ClassifyAgent samples the enabled block lists whose content changed and has the model estimate what
// each list blocks. It never changes a list.
type ClassifyAgent struct {
	Store   *store.Store
	Service *ai.Service
	Now     func() time.Time // nil: time.Now
}

// Name returns "threat_classification".
func (a *ClassifyAgent) Name() string { return ClassifyName }

type item struct {
	Name     string `json:"name"`
	Category string `json:"category"`
}

type classifyOutput struct {
	Items []item `json:"items"`
}

var classifySystem = `You label DNS names taken from a DNS resolver block list, so its operator learns what the list blocks.
The data block holds a sample of the list's entries.
Answer with one JSON object: {"items":[{"name":"<the name, unchanged>","category":"<category>"}]}.
Label every name in the data block exactly once and no other name.
The category is one of: ` + categoryList + `, or "` + noCategory + `" when the name is no threat.
This is a description of the list; you never change any configuration.`

// pending is one list waiting to be classified.
type pending struct {
	id         uuid.UUID
	name, sha  string
	entryCount int64
	data       []byte
}

// Run classifies at most maxListsPerRun changed lists.
func (a *ClassifyAgent) Run(ctx context.Context, run *ai.Run) error {
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	lists, err := a.changedLists(ctx)
	if err != nil {
		return err
	}
	if len(lists) == 0 {
		run.Outcome = "no_change"
		return nil
	}
	classified := 0
	for _, l := range lists {
		names, err := blobNames(l.data)
		if err != nil {
			return fmt.Errorf("list %s blob: %w", l.name, err)
		}
		sample := Sample(names, SampleSize, int64(crc32.ChecksumIEEE([]byte(l.sha))))
		if len(sample) == 0 {
			continue
		}
		counts := map[string]int{}
		for batch := range slices.Chunk(sample, classifyBatch) {
			res, err := ai.Generate(ctx, a.Service, ai.Request[classifyOutput]{
				Feature: ClassifyName, Priority: ai.Background, System: classifySystem,
				Prompt:   "Label these names.\n" + ai.DataBlock(map[string]any{"list": l.name, "names": batch}),
				Validate: func(o *classifyOutput) error { return validateItems(o, batch) },
			})
			run.Usage.InputTokens += res.Usage.InputTokens
			run.Usage.OutputTokens += res.Usage.OutputTokens
			switch {
			case errors.Is(err, ai.ErrBudgetExhausted):
				run.Outcome = "skipped_budget"
				run.Detail["classified"] = classified
				return nil
			case ctx.Err() != nil:
				return ctx.Err()
			case err != nil:
				// The list keeps its old classification and is retried next run.
				run.Detail["llm_error"] = ai.Code(err)
				run.Detail["classified"] = classified
				return nil
			}
			for _, it := range res.Value.Items {
				counts[it.Category]++
			}
		}
		if err := a.save(ctx, l, breakdown(counts, len(sample), l.entryCount), len(sample), now); err != nil {
			return err
		}
		classified++
	}
	run.Detail["classified"] = classified
	if classified == 0 {
		run.Outcome = "no_change"
	}
	return nil
}

// changedLists reads the enabled block lists whose blob differs from their classification.
func (a *ClassifyAgent) changedLists(ctx context.Context) ([]pending, error) {
	rows, err := a.Store.Pool.Query(ctx, `select f.id, f.name, f.current_blob_sha256, f.entry_count, b.data
		from filter_lists f
		join blobs b on b.sha256 = f.current_blob_sha256
		left join ai_list_classifications c on c.list_id = f.id
		where f.enabled and f.kind = 'block' and (c.blob_sha256 is null or c.blob_sha256 <> f.current_blob_sha256)
		order by f.name limit $1`, maxListsPerRun)
	if err != nil {
		return nil, store.MapError(err)
	}
	var out []pending
	var p pending
	if _, err := pgx.ForEachRow(rows, []any{&p.id, &p.name, &p.sha, &p.entryCount, &p.data}, func() error {
		out = append(out, p)
		return nil
	}); err != nil {
		return nil, store.MapError(err)
	}
	return out, nil
}

// save upserts a list's breakdown.
func (a *ClassifyAgent) save(ctx context.Context, l pending, buckets []Bucket, sampled int, now time.Time) error {
	raw, err := json.Marshal(buckets)
	if err != nil {
		return err
	}
	_, err = a.Store.Pool.Exec(ctx, `insert into ai_list_classifications(list_id, blob_sha256, sample_size, breakdown, classified_at)
		values ($1, $2, $3, $4, $5)
		on conflict (list_id) do update set blob_sha256 = excluded.blob_sha256, sample_size = excluded.sample_size,
			breakdown = excluded.breakdown, classified_at = excluded.classified_at`,
		l.id, l.sha, sampled, raw, now)
	return store.MapError(err)
}

// blobNames decompresses a list blob into its names.
func blobNames(data []byte) ([]string, error) {
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	if err := dec.Reset(bytes.NewReader(data)); err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(dec)
	sc.Buffer(make([]byte, 0, 1024), maxBlobLine)
	var names []string
	for sc.Scan() {
		if name := strings.TrimSpace(sc.Text()); name != "" {
			names = append(names, name)
		}
	}
	return names, sc.Err()
}

// breakdown turns the counted categories into buckets, in descending sampled order, with each
// category's estimated share of the whole list.
func breakdown(counts map[string]int, sampled int, entryCount int64) []Bucket {
	out := make([]Bucket, 0, len(counts))
	for category, n := range counts {
		estimated := int64(0)
		if sampled > 0 {
			estimated = int64(math.Round(float64(n) / float64(sampled) * float64(entryCount)))
		}
		out = append(out, Bucket{Category: category, Sampled: n, Estimated: estimated})
	}
	slices.SortFunc(out, func(a, b Bucket) int {
		if a.Sampled != b.Sampled {
			return b.Sampled - a.Sampled
		}
		return strings.Compare(a.Category, b.Category)
	})
	return out
}

// validateItems checks a classification answer against its batch; its messages are fed back to the model.
func validateItems(o *classifyOutput, batch []string) error {
	var errs []error
	seen := map[string]bool{}
	for i, it := range o.Items {
		at := fmt.Sprintf("items[%d]", i)
		switch {
		case !slices.Contains(batch, it.Name):
			errs = append(errs, fmt.Errorf("%s: %q is not one of the names in the data block", at, it.Name))
			continue
		case seen[it.Name]:
			errs = append(errs, fmt.Errorf("%s: %q is labelled twice", at, it.Name))
			continue
		case it.Category != noCategory && !slices.Contains(Categories, it.Category):
			errs = append(errs, fmt.Errorf("%s: unknown category %q; known: %s, %s", at, it.Category, categoryList, noCategory))
			continue
		}
		seen[it.Name] = true
	}
	for _, name := range batch {
		if !seen[name] {
			errs = append(errs, fmt.Errorf("no category for %q", name))
		}
	}
	return errors.Join(errs...)
}

// GetClassification reads a list's classification. A list that was never classified answers with its
// current blob, no classification time and an empty breakdown; an unknown list is store.ErrNotFound.
func GetClassification(ctx context.Context, q store.PolicyQuerier, listID uuid.UUID) (Classification, error) {
	c := Classification{ListID: listID, Breakdown: []Bucket{}}
	var raw []byte
	err := q.QueryRow(ctx, `select coalesce(c.blob_sha256, f.current_blob_sha256, ''), coalesce(c.sample_size, 0),
			c.classified_at, coalesce(c.breakdown, '[]'), f.entry_count
		from filter_lists f left join ai_list_classifications c on c.list_id = f.id where f.id = $1`, listID).
		Scan(&c.BlobSHA256, &c.SampleSize, &c.ClassifiedAt, &raw, &c.EntryCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return Classification{}, store.ErrNotFound
	}
	if err != nil {
		return Classification{}, store.MapError(err)
	}
	if err := json.Unmarshal(raw, &c.Breakdown); err != nil {
		return Classification{}, fmt.Errorf("list classification breakdown: %w", err)
	}
	if c.Breakdown == nil {
		c.Breakdown = []Bucket{}
	}
	return c, nil
}
