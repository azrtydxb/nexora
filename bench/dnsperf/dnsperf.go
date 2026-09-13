// Package dnsperf runs dnsperf 2.14 and parses its statistics, including the p99 latency from
// the `-O latency-histogram` buckets. It is shared by the end-to-end tests and perfgate.
package dnsperf

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Options selects the target, the query corpus and the load shape.
type Options struct {
	Server                    string
	Port                      int
	Names                     []string
	QType                     string // default "A"
	Seconds, Clients, Threads int
	MaxQPS                    int // 0 means unlimited
}

// Result is the parsed dnsperf summary.
type Result struct {
	QPS                   float64
	Sent, Completed, Lost uint64
	LatencyAvgSeconds     float64
	P99Seconds            float64
}

// Run writes a data file of `<name> <qtype>` lines and runs dnsperf against o.Server:o.Port.
func Run(ctx context.Context, o Options) (Result, error) {
	if o.Server == "" || o.Port <= 0 || len(o.Names) == 0 || o.Seconds <= 0 {
		return Result{}, errors.New("dnsperf: server, port, names and seconds are required")
	}
	qtype := o.QType
	if qtype == "" {
		qtype = "A"
	}
	clients, threads := max(o.Clients, 1), max(o.Threads, 1)
	dir, err := os.MkdirTemp("", "dnsperf-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)
	var data bytes.Buffer
	for _, n := range o.Names {
		fmt.Fprintf(&data, "%s %s\n", n, qtype)
	}
	dataFile := filepath.Join(dir, "queries.txt")
	if err := os.WriteFile(dataFile, data.Bytes(), 0o600); err != nil {
		return Result{}, err
	}
	args := []string{
		"-s", o.Server, "-p", strconv.Itoa(o.Port), "-d", dataFile,
		"-l", strconv.Itoa(o.Seconds), "-c", strconv.Itoa(clients), "-T", strconv.Itoa(threads),
		"-O", "latency-histogram",
	}
	// debt: dnsperf's default of 100 outstanding queries is kept — larger values measured more lost
	// (in flight at the time limit) and higher latency on the dev pod. Revisit if the reference box
	// saturates dnsperf rather than the engine.
	if o.MaxQPS > 0 {
		args = append(args, "-Q", strconv.Itoa(o.MaxQPS))
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "dnsperf", args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return Result{}, fmt.Errorf("dnsperf: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return Parse(stdout.String())
}

// Parse extracts the summary from dnsperf output. The p99 is the upper bound of the histogram
// bucket in which the cumulative answer count reaches 99%.
func Parse(out string) (Result, error) {
	var r Result
	var seen struct{ sent, completed, qps, avg bool }
	type bucket struct {
		hi    float64
		count uint64
	}
	var buckets []bucket
	var total uint64
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(val)
		if len(fields) == 0 {
			continue
		}
		var err error
		switch key {
		case "Queries sent":
			r.Sent, err = strconv.ParseUint(fields[0], 10, 64)
			seen.sent = true
		case "Queries completed":
			r.Completed, err = strconv.ParseUint(fields[0], 10, 64)
			seen.completed = true
		case "Queries lost":
			r.Lost, err = strconv.ParseUint(fields[0], 10, 64)
		case "Queries per second":
			r.QPS, err = strconv.ParseFloat(fields[0], 64)
			seen.qps = true
		case "Average Latency (s)":
			r.LatencyAvgSeconds, err = strconv.ParseFloat(fields[0], 64)
			seen.avg = true
		default:
			// Histogram bucket: "0.000320 - 0.000327:  1".
			lo, hi, isRange := strings.Cut(key, " - ")
			if !isRange || len(fields) != 1 {
				continue
			}
			if _, err = strconv.ParseFloat(lo, 64); err != nil {
				continue
			}
			var b bucket
			if b.hi, err = strconv.ParseFloat(hi, 64); err != nil {
				continue
			}
			if b.count, err = strconv.ParseUint(fields[0], 10, 64); err != nil {
				continue
			}
			buckets = append(buckets, b)
			total += b.count
		}
		if err != nil {
			return Result{}, fmt.Errorf("dnsperf: parse %q: %w", line, err)
		}
	}
	if !seen.sent || !seen.completed || !seen.qps || !seen.avg {
		return Result{}, errors.New("dnsperf: output has no statistics summary")
	}
	if total > 0 {
		threshold := (total*99 + 99) / 100
		var cum uint64
		for _, b := range buckets {
			cum += b.count
			if cum >= threshold {
				r.P99Seconds = b.hi
				break
			}
		}
	}
	return r, nil
}
