package e2e

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/piwi3910/nexora/e2e/harness"
)

type kwFilterIndex struct {
	At                time.Time `json:"at"`
	Entries           int64     `json:"entries"`
	Bytes             int64     `json:"bytes"`
	MaxBytes          int64     `json:"max_bytes"`
	BuildSeconds      float64   `json:"build_seconds"`
	DecisionNSBlocked float64   `json:"decision_ns_blocked"`
	DecisionNSClean   float64   `json:"decision_ns_clean"`
	CPU               string    `json:"cpu"`
}

var kwListName = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?(\.[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?)+$`)

// kwSourceNames downloads a catalog source (e2e cannot import mgmt/internal, so this repeats the
// domain, hosts and wildcard formats) and returns up to n names from the middle of the list.
func kwSourceNames(t *testing.T, src harness.CategorySource, member string, n int) []string {
	t.Helper()
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Get(src.URL)
	if err != nil {
		t.Fatalf("download %s: %v", src.Key, err)
	}
	defer resp.Body.Close()
	var body io.Reader = resp.Body
	if member != "" {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			t.Fatalf("%s archive: %v", src.Key, err)
		}
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				t.Fatalf("%s: archive member %s not found", src.Key, member)
			}
			if err != nil {
				t.Fatalf("%s archive: %v", src.Key, err)
			}
			if strings.TrimPrefix(h.Name, "./") == member {
				body = tr
				break
			}
		}
	}
	var names []string
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		f := strings.Fields(strings.ToLower(sc.Text()))
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		name := f[0]
		if (name == "0.0.0.0" || name == "127.0.0.1") && len(f) > 1 {
			name = f[1]
		}
		name = strings.TrimPrefix(name, "*.")
		if kwListName.MatchString(name) && name != "0.0.0.0" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		t.Fatalf("%s: no names (%v)", src.Key, sc.Err())
	}
	start := len(names) / 2
	return names[start:min(start+n, len(names))]
}

func TestKwFilterCategories(t *testing.T) {
	env := loadKwEnv(t)
	expected, err := kwExpectedFleet(os.Getenv("NEXORA_KW_EXPECTED_ENGINES"), env.engines)
	if err != nil {
		t.Fatal(err)
	}
	api := kwLogin(t, env)
	var initial []kwFilterEngine
	api.Must(http.MethodGet, "/engines", nil, &initial, http.StatusOK)
	if err := kwValidateFleet(expected, initial, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	var cats []harness.CategoryView
	api.Must(http.MethodGet, "/filter-categories", nil, &cats, http.StatusOK)
	if len(cats) == 0 {
		t.Fatal("no filter categories returned")
	}
	t.Cleanup(func() {
		for _, c := range cats {
			if code, reason := api.SetFilterCategory(c.Key, c.Enabled, nil, false); code != http.StatusOK {
				t.Errorf("restore %s -> %d %s", c.Key, code, reason)
			}
		}
		kwWaitApplied(t, api, env.engines)
	})

	for _, c := range cats {
		if code, reason := api.SetFilterCategory(c.Key, true, nil, true); code != http.StatusOK {
			t.Fatalf("enable %s -> %d %s", c.Key, code, reason)
		}
		api.RefreshCategory(c.Key)
	}
	kwWaitApplied(t, api, env.engines)
	var engines []kwFilterEngine
	api.Must(http.MethodGet, "/engines", nil, &engines, http.StatusOK)
	if err := kwValidateFleet(expected, engines, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	versions := make(map[string]uint64, len(expected))
	for _, e := range engines {
		versions[e.ID] = e.TargetVersion
	}
	after := time.Now()

	t.Run("every-category-blocks-real-names", func(t *testing.T) {
		for _, cat := range cats {
			view := api.FilterCategory(cat.Key)
			var source *harness.CategorySource
			for i, s := range view.Sources {
				if s.Enabled && s.EntryCount > 0 && s.LastError == "" {
					source = &view.Sources[i]
					break
				}
			}
			if source == nil {
				t.Errorf("category %s has no source with entries: %+v", cat.Key, view.Sources)
				continue
			}
			for _, s := range view.Sources {
				if s.Enabled && s.LastError != "" {
					t.Logf("source %s of %s failed its refresh (other sources still apply): %s", s.Key, cat.Key, s.LastError)
				}
			}
			member := kwArchiveMember(source.Key)
			blocked := 0
			names := kwSourceNames(t, *source, member, 3)
			for _, name := range names {
				addrs, _, err := kwQueryA(env.dnsAddr, name)
				if err == nil && len(addrs) == 1 && addrs[0] == "0.0.0.0" {
					blocked++
				}
			}
			if blocked < 2 {
				t.Errorf("category %s (source %s): %d of %v blocked on %s", cat.Key, source.Key, blocked, names, env.dnsAddr)
			}
		}
	})

	t.Run("per-engine-memory-and-decision-time", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		boundedAPI := acceptanceAPI(ctx, api)
		var report map[string]kwFilterIndex
		if err := acceptancePoll(ctx, 200*time.Millisecond, func() error {
			var err error
			report, err = kwCollectFilterReport(boundedAPI, expected, versions, after)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		for _, engine := range expected {
			node := engine.NodeName + " (" + engine.ID + ")"
			fi := report[engine.ID]
			scale := max(1, float64(fi.Entries)/5.1e6)
			t.Logf("%s: %d names, %.1f MiB (cap %.0f MiB), build %.2f s, %.0f ns blocked, %.0f ns clean on %s",
				node, fi.Entries, float64(fi.Bytes)/(1<<20), float64(fi.MaxBytes)/(1<<20), fi.BuildSeconds, fi.DecisionNSBlocked, fi.DecisionNSClean, fi.CPU)
			if float64(fi.Bytes) >= 120e6*scale {
				t.Errorf("%s: filter index %d bytes for %d names, budget %.0f", node, fi.Bytes, fi.Entries, 120e6*scale)
			}
			// Spec budgets (revision of 2026-09-14): build < 2 s, clean < 150 ns, cold blocked < 300 ns
			// (calibration decides uncached names, so blocked names pay the cold block read).
			if fi.BuildSeconds >= 2*scale {
				t.Errorf("%s: build %.2f s, budget %.2f s", node, fi.BuildSeconds, 2*scale)
			}
			if fi.CPU == "cortex-a76" && (fi.DecisionNSBlocked >= 300 || fi.DecisionNSClean >= 150) {
				t.Errorf("%s: %.0f ns blocked / %.0f ns clean on a Cortex-A76, budget 300 ns blocked, 150 ns clean", node, fi.DecisionNSBlocked, fi.DecisionNSClean)
			}
		}
		if path := os.Getenv("NEXORA_KW_FILTER_REPORT"); path != "" {
			raw, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	})
}

// kwArchiveMember is the UT1 member of a catalog source key, empty for plain lists.
func kwArchiveMember(sourceKey string) string {
	members := map[string]string{
		"ut1-malware": "blacklists/malware/domains", "ut1-phishing": "blacklists/phishing/domains",
		"ut1-adult": "blacklists/adult/domains", "ut1-gambling": "blacklists/gambling/domains",
		"ut1-social-networks": "blacklists/social_networks/domains", "ut1-cryptojacking": "blacklists/cryptojacking/domains",
		"ut1-warez": "blacklists/warez/domains", "ut1-drogue": "blacklists/drogue/domains",
		"ut1-fakenews": "blacklists/fakenews/domains", "ut1-dating": "blacklists/dating/domains", "ut1-games": "blacklists/games/domains",
	}
	return members[sourceKey]
}
