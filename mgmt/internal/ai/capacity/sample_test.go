package capacity

import (
	"testing"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"google.golang.org/protobuf/proto"
)

func TestRecursorCachePresence(t *testing.T) {
	field := (&controlv1.Stats{}).ProtoReflect().Descriptor().Fields().ByName("recursor_cache")
	if field == nil || field.Number() != 1200 || !field.HasPresence() {
		t.Fatal("recursor_cache must keep wire field 1200 and message presence")
	}
	for _, tc := range []struct {
		name   string
		report *controlv1.RecursorCacheStats
		want   bool
	}{
		{"old engine", nil, false},
		{"disabled", &controlv1.RecursorCacheStats{Bytes: 999, MaxBytes: 64 << 20}, false},
		{"missing limit", &controlv1.RecursorCacheStats{Enabled: true, Bytes: 123}, false},
		{"empty message", &controlv1.RecursorCacheStats{}, false},
		{"measured zero", &controlv1.RecursorCacheStats{Enabled: true, MaxBytes: 64 << 20}, true},
		{"populated", &controlv1.RecursorCacheStats{Enabled: true, Bytes: 123, MaxBytes: 128 << 20}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Exercise wire presence, including proto3's omission of zero scalar values.
			raw, err := proto.Marshal(&controlv1.Stats{RecursorCache: tc.report})
			if err != nil {
				t.Fatal(err)
			}
			var decoded controlv1.Stats
			if err := proto.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if (decoded.RecursorCache == nil) != (tc.report == nil) {
				t.Fatal("message presence lost")
			}
			samples := sampleSet{}
			samples.addEngine(&decoded, 256<<20)
			got, ok := samples["recursor_cache"]
			if ok != tc.want {
				t.Fatalf("sample present = %v, want %v", ok, tc.want)
			}
			if ok && (got.value != float64(tc.report.Bytes) || got.limit == nil || *got.limit != float64(tc.report.MaxBytes)) {
				t.Fatalf("sample = %+v, report = %v", got, tc.report)
			}
		})
	}
	// A pre-extension wire payload carrying cache_bytes (field 14) only.
	var old controlv1.Stats
	if err := proto.Unmarshal([]byte{0x70, 0x7b}, &old); err != nil {
		t.Fatal(err)
	}
	samples := sampleSet{}
	samples.addEngine(&old, 256<<20)
	if old.CacheBytes != 123 || old.RecursorCache != nil {
		t.Fatalf("old stats = %v", &old)
	}
	if _, ok := samples["recursor_cache"]; ok {
		t.Fatal("old wire payload invented a recursor measurement")
	}
}

func TestEngineResourceMaximaKeepTheirOwnLimits(t *testing.T) {
	a := &controlv1.Stats{
		CacheBytes:           200,
		RecursorCache:        &controlv1.RecursorCacheStats{Enabled: true, Bytes: 100, MaxBytes: 4 << 20},
		FilterIndex:          &controlv1.FilterIndexStats{Bytes: 800, MaxBytes: 900},
		ProcessResidentBytes: 300, MemoryLimitBytes: 500,
	}
	b := &controlv1.Stats{
		CacheBytes:           100,
		RecursorCache:        &controlv1.RecursorCacheStats{Enabled: true, Bytes: 200, MaxBytes: 128 << 20},
		FilterIndex:          &controlv1.FilterIndexStats{Bytes: 100, MaxBytes: 1000},
		ProcessResidentBytes: 400,
	}
	for _, engines := range [][]*controlv1.Stats{{a, b}, {b, a}} {
		samples := sampleSet{}
		for _, s := range engines {
			samples.addEngine(s, 256<<20)
		}
		// An old engine and a disabled engine with larger retained bytes must not overwrite b.
		samples.addEngine(&controlv1.Stats{}, 256<<20)
		samples.addEngine(&controlv1.Stats{RecursorCache: &controlv1.RecursorCacheStats{Bytes: 999, MaxBytes: 512 << 20}}, 256<<20)
		for resource, want := range map[string][2]float64{
			"cache": {200, 256 << 20}, "recursor_cache": {200, 128 << 20},
			"filter_index": {800, 900}, "engine_memory": {400, -1},
		} {
			got, ok := samples[resource]
			if !ok || got.value != want[0] || (got.limit == nil) != (want[1] < 0) || (got.limit != nil && *got.limit != want[1]) {
				t.Fatalf("%s = %+v, want %v", resource, got, want)
			}
		}
	}
}
