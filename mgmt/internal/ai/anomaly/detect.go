// Package anomaly is the query-log anomaly agent (M11 S-4): deterministic detectors over the query-log
// window since the previous run produce candidates, and the model only explains the ones it confirms.
package anomaly

import (
	"cmp"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/piwi3910/nexora/mgmt/internal/ai/finding"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
)

// Window is the query-log slice one run inspects.
type Window struct {
	Records      []querylog.Record  // newest first, at most MaxRecords
	Sampled      bool               // true when the window held more than MaxRecords records
	Baseline     map[string]float64 // client -> QPS over 24 h; nil skips the flood detector
	PriorThreats map[string]bool    // "client:<ip>" or "group:<id>" with threat-category blocks in the prior 24 h
	From, To     time.Time
}

// MaxRecords caps the records one run reads.
const MaxRecords = 5000 // debt: newest 5,000 records per run; revisit when a backend offers server-side aggregation for these detectors

// Detector thresholds (fixed by the M11 spec).
const (
	tunnelMinQueries   = 50
	tunnelMinEntropy   = 3.5
	tunnelMinLength    = 20
	tunnelTXTMinShare  = 0.4
	tunnelTXTMinCount  = 30
	nxMinQueries       = 50
	nxMinRatio         = 0.5
	floodFactor        = 10
	floodMinQPS        = 20
	beaconMinQueries   = 20
	beaconMaxCV        = 0.1
	maxSampleDomains   = 5
	maxAffectedClients = 20
)

// ThreatCategories are the filter categories whose first block raises category_escalation.
var ThreatCategories = []string{"malware", "phishing", "cryptomining"}

var multiPartSuffixes = map[string]bool{"co": true, "com": true, "net": true, "org": true, "gov": true, "ac": true, "edu": true}

// Entropy is the Shannon entropy of label in bits per character.
func Entropy(label string) float64 {
	if label == "" {
		return 0
	}
	counts := map[rune]int{}
	n := 0
	for _, r := range label {
		counts[r]++
		n++
	}
	var h float64
	for _, c := range counts {
		p := float64(c) / float64(n)
		h -= p * math.Log2(p)
	}
	return h
}

// RegistrableParent returns the lower-case last two labels of name, or three when the second-level label
// is one of co, com, net, org, gov, ac, edu.
func RegistrableParent(name string) string {
	labels := strings.Split(strings.ToLower(strings.TrimSuffix(name, ".")), ".")
	n := 2
	if len(labels) >= 3 && multiPartSuffixes[labels[len(labels)-2]] {
		n = 3
	}
	return strings.Join(labels[max(len(labels)-n, 0):], ".")
}

func leftmostLabel(name string) string {
	label, _, _ := strings.Cut(name, ".")
	return label
}

// Detect runs every detector over w and returns the candidates ordered by id.
func Detect(w Window) []finding.Candidate {
	byClient := map[string][]querylog.Record{}
	for _, r := range w.Records {
		byClient[r.Client] = append(byClient[r.Client], r)
	}
	var out []finding.Candidate
	for _, client := range slices.Sorted(maps.Keys(byClient)) {
		recs := byClient[client]
		if c, ok := tunneling(client, recs); ok {
			out = append(out, c)
		}
		if c, ok := nxdomainBurst(client, recs); ok {
			out = append(out, c)
		}
		if c, ok := queryFlood(client, recs, w); ok {
			out = append(out, c)
		}
		out = append(out, beacons(client, recs)...)
	}
	out = append(out, escalations(w)...)
	slices.SortFunc(out, func(a, b finding.Candidate) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

func candidate(typ, subject, severity, title, description string, detail map[string]any) finding.Candidate {
	return finding.Candidate{ID: typ + ":" + subject, Kind: "anomaly", Type: typ, Severity: severity,
		Title: title, Description: description, Detail: detail}
}

// sampleDomains returns up to maxSampleDomains distinct names of recs in order of appearance.
func sampleDomains(recs []querylog.Record) []string {
	var out []string
	for _, r := range recs {
		if len(out) == maxSampleDomains {
			break
		}
		if !slices.Contains(out, r.Name) {
			out = append(out, r.Name)
		}
	}
	return out
}

func tunneling(client string, recs []querylog.Record) (finding.Candidate, bool) {
	byParent := map[string][]querylog.Record{}
	special := 0
	for _, r := range recs {
		p := RegistrableParent(r.Name)
		byParent[p] = append(byParent[p], r)
		if r.QType == "TXT" || r.QType == "NULL" {
			special++
		}
	}
	for _, parent := range slices.Sorted(maps.Keys(byParent)) {
		prs := byParent[parent]
		if len(prs) < tunnelMinQueries {
			continue
		}
		var entropy, length float64
		for _, r := range prs {
			label := leftmostLabel(r.Name)
			entropy += Entropy(label)
			length += float64(len(label))
		}
		entropy /= float64(len(prs))
		length /= float64(len(prs))
		if entropy >= tunnelMinEntropy && length >= tunnelMinLength {
			return candidate("dns_tunneling", client, "critical", "Possible DNS tunnelling from "+client,
				fmt.Sprintf("%s sent %d queries under %s with random-looking labels (mean entropy %.2f bits/char, mean length %.1f).",
					client, len(prs), parent, entropy, length),
				map[string]any{"affected_clients": []string{client}, "sample_domains": sampleDomains(prs),
					"metrics": map[string]any{"queries": len(prs), "parent": parent, "mean_entropy": entropy, "mean_label_length": length}}), true
		}
	}
	share := float64(special) / float64(len(recs))
	if special >= tunnelTXTMinCount && share >= tunnelTXTMinShare {
		var txt []querylog.Record
		for _, r := range recs {
			if r.QType == "TXT" || r.QType == "NULL" {
				txt = append(txt, r)
			}
		}
		return candidate("dns_tunneling", client, "critical", "Possible DNS tunnelling from "+client,
			fmt.Sprintf("%d of %s's %d queries (%.0f%%) are TXT or NULL.", special, client, len(recs), 100*share),
			map[string]any{"affected_clients": []string{client}, "sample_domains": sampleDomains(txt),
				"metrics": map[string]any{"queries": len(recs), "txt_null_queries": special, "txt_null_share": share}}), true
	}
	return finding.Candidate{}, false
}

func nxdomainBurst(client string, recs []querylog.Record) (finding.Candidate, bool) {
	if len(recs) < nxMinQueries {
		return finding.Candidate{}, false
	}
	var nx []querylog.Record
	for _, r := range recs {
		if r.RCode == "NXDOMAIN" {
			nx = append(nx, r)
		}
	}
	ratio := float64(len(nx)) / float64(len(recs))
	if ratio < nxMinRatio {
		return finding.Candidate{}, false
	}
	return candidate("nxdomain_burst", client, "warning", "NXDOMAIN burst from "+client,
		fmt.Sprintf("%d of %s's %d queries (%.0f%%) answered NXDOMAIN.", len(nx), client, len(recs), 100*ratio),
		map[string]any{"affected_clients": []string{client}, "sample_domains": sampleDomains(nx),
			"metrics": map[string]any{"queries": len(recs), "nxdomain": len(nx), "nxdomain_ratio": ratio}}), true
}

func queryFlood(client string, recs []querylog.Record, w Window) (finding.Candidate, bool) {
	if w.Baseline == nil {
		return finding.Candidate{}, false
	}
	from := w.From
	if w.Sampled && len(w.Records) > 0 {
		from = w.Records[len(w.Records)-1].Time // the cap cut the window: measure over what was read
	}
	seconds := w.To.Sub(from).Seconds()
	if seconds <= 0 {
		return finding.Candidate{}, false
	}
	qps := float64(len(recs)) / seconds
	baseline := w.Baseline[client]
	if qps < floodMinQPS || qps < floodFactor*baseline {
		return finding.Candidate{}, false
	}
	return candidate("query_flood", client, "warning", "Query flood from "+client,
		fmt.Sprintf("%s sent %.1f queries per second against a 24 h baseline of %.2f.", client, qps, baseline),
		map[string]any{"affected_clients": []string{client}, "sample_domains": sampleDomains(recs),
			"metrics": map[string]any{"queries": len(recs), "qps": qps, "baseline_qps": baseline}}), true
}

func beacons(client string, recs []querylog.Record) []finding.Candidate {
	byName := map[string][]time.Time{}
	for _, r := range recs {
		byName[r.Name] = append(byName[r.Name], r.Time)
	}
	var out []finding.Candidate
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		times := byName[name]
		if len(times) < beaconMinQueries {
			continue
		}
		slices.SortFunc(times, func(a, b time.Time) int { return a.Compare(b) })
		gaps := make([]float64, len(times)-1)
		var mean float64
		for i := range gaps {
			gaps[i] = times[i+1].Sub(times[i]).Seconds()
			mean += gaps[i]
		}
		mean /= float64(len(gaps))
		if mean <= 0 {
			continue
		}
		var variance float64
		for _, g := range gaps {
			variance += (g - mean) * (g - mean)
		}
		cv := math.Sqrt(variance/float64(len(gaps))) / mean
		if cv > beaconMaxCV {
			continue
		}
		out = append(out, candidate("periodic_beacon", client+"|"+name, "warning", "Periodic beacon from "+client,
			fmt.Sprintf("%s queried %s %d times every %.0f s (coefficient of variation %.3f).", client, name, len(times), mean, cv),
			map[string]any{"affected_clients": []string{client}, "sample_domains": []string{name},
				"metrics": map[string]any{"queries": len(times), "mean_interval_seconds": mean, "coefficient_of_variation": cv}}))
	}
	return out
}

func escalations(w Window) []finding.Candidate {
	type hits struct {
		clients, domains []string
		categories       []string
		count            int
	}
	byKey := map[string]*hits{}
	for _, r := range w.Records {
		if r.Filter != "blocked" || !slices.Contains(ThreatCategories, r.Category) {
			continue
		}
		keys := []string{"client:" + r.Client}
		if r.PolicyGroupID != "" {
			keys = append(keys, "group:"+r.PolicyGroupID)
		}
		for _, k := range keys {
			if w.PriorThreats[k] {
				continue
			}
			h := byKey[k]
			if h == nil {
				h = &hits{}
				byKey[k] = h
			}
			h.count++
			if !slices.Contains(h.clients, r.Client) && len(h.clients) < maxAffectedClients {
				h.clients = append(h.clients, r.Client)
			}
			if !slices.Contains(h.domains, r.Name) && len(h.domains) < maxSampleDomains {
				h.domains = append(h.domains, r.Name)
			}
			if !slices.Contains(h.categories, r.Category) {
				h.categories = append(h.categories, r.Category)
			}
		}
	}
	var out []finding.Candidate
	for _, k := range slices.Sorted(maps.Keys(byKey)) {
		h := byKey[k]
		kind, subject, _ := strings.Cut(k, ":")
		who := kind + " " + subject
		if kind == "group" {
			who = "policy group " + subject
		}
		out = append(out, candidate("category_escalation", k, "critical", "New "+strings.Join(h.categories, "/")+" blocks for "+who,
			fmt.Sprintf("%d queries from %s were blocked as %s; it had no such blocks in the previous 24 h.", h.count, who, strings.Join(h.categories, ", ")),
			map[string]any{"affected_clients": h.clients, "sample_domains": h.domains,
				"metrics": map[string]any{"blocked_queries": h.count, "categories": h.categories}}))
	}
	return out
}
