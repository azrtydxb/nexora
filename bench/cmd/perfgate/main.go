// Command perfgate runs Nexora's dnsperf performance gate: a relative A/B regression check on
// every pull request and an absolute throughput/latency check on the reference box.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/miekg/dns"
	"google.golang.org/protobuf/proto"

	"github.com/piwi3910/nexora/bench/corpus"
	"github.com/piwi3910/nexora/bench/dnsperf"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
)

const usage = `usage:
  perfgate run --engine PATH --fixture PATH --names N --seconds S --workers N --out FILE [--clients C] [--threads T]
  perfgate serve --engine PATH --fixture PATH --listen ADDR --workers N
  perfgate load --target ADDR --names N --seconds S --clients C --threads T --out FILE
  perfgate compare --base A1.json,A2.json,... --head B1.json,B2.json,... --max-drop 0.05  (Ai and Bi: round i)
  perfgate absolute --result FILE --min-qps 1000000 --max-p99 500us`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch os.Args[1] {
	case "run":
		err = cmdRun(ctx, os.Args[2:])
	case "serve":
		err = cmdServe(ctx, os.Args[2:])
	case "load":
		err = cmdLoad(ctx, os.Args[2:])
	case "compare":
		err = cmdCompare(os.Args[2:])
	case "absolute":
		err = cmdAbsolute(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "perfgate %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
}

var errGateFailed = errors.New("gate failed")

func cmdRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	engine := fs.String("engine", "", "nexora-engine binary")
	fixture := fs.String("fixture", "", "nexora-fixture binary")
	names := fs.Int("names", 10000, "distinct cache-hit names")
	seconds := fs.Int("seconds", 20, "dnsperf duration")
	workers := fs.Int("workers", 2, "engine workers")
	clients := fs.Int("clients", 64, "dnsperf clients")
	threads := fs.Int("threads", 0, "dnsperf threads (0: the cores not given to engine workers)")
	out := fs.String("out", "", "result JSON file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *engine == "" || *fixture == "" || *out == "" || *names <= 0 || *seconds <= 0 || *workers <= 0 {
		return errors.New("--engine, --fixture, --out and positive --names, --seconds, --workers are required")
	}
	if *threads <= 0 {
		*threads = max(1, runtime.GOMAXPROCS(0)-*workers)
	}
	st, err := startStack(ctx, *engine, *fixture, "127.0.0.1:0", *workers)
	if err != nil {
		return err
	}
	defer st.stop()
	r, err := warmAndLoad(ctx, st.dnsAddr, corpus.Names(*names), *seconds, *clients, *threads)
	if err != nil {
		return err
	}
	return writeResult(*out, r)
}

func cmdServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	engine := fs.String("engine", "", "nexora-engine binary")
	fixture := fs.String("fixture", "", "nexora-fixture binary")
	listen := fs.String("listen", "", "UDP and TCP listen address (host:port)")
	workers := fs.Int("workers", 8, "engine workers")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *engine == "" || *fixture == "" || *listen == "" || *workers <= 0 {
		return errors.New("--engine, --fixture, --listen and a positive --workers are required")
	}
	if err := checkAddr(*listen); err != nil {
		return err
	}
	st, err := startStack(ctx, *engine, *fixture, *listen, *workers)
	if err != nil {
		return err
	}
	defer st.stop()
	fmt.Println("perfgate ready")
	select {
	case <-ctx.Done():
		return nil
	case <-st.engine.done:
		return fmt.Errorf("nexora-engine exited:\n%s", tail(st.engine.log, 30))
	}
}

func cmdLoad(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("load", flag.ContinueOnError)
	target := fs.String("target", "", "engine DNS address (host:port)")
	names := fs.Int("names", 10000, "distinct cache-hit names")
	seconds := fs.Int("seconds", 60, "dnsperf duration")
	clients := fs.Int("clients", 64, "dnsperf clients")
	threads := fs.Int("threads", 16, "dnsperf threads")
	out := fs.String("out", "", "result JSON file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *target == "" || *out == "" || *names <= 0 || *seconds <= 0 {
		return errors.New("--target, --out and positive --names, --seconds are required")
	}
	if err := checkAddr(*target); err != nil {
		return err
	}
	r, err := warmAndLoad(ctx, *target, corpus.Names(*names), *seconds, *clients, *threads)
	if err != nil {
		return err
	}
	return writeResult(*out, r)
}

func cmdCompare(args []string) error {
	fs := flag.NewFlagSet("compare", flag.ContinueOnError)
	base := fs.String("base", "", "comma-separated base result files")
	head := fs.String("head", "", "comma-separated head result files")
	maxDrop := fs.Float64("max-drop", 0.05, "largest tolerated QPS drop (fraction)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *maxDrop < 0 || *maxDrop >= 1 {
		return errors.New("--max-drop must be in [0, 1)")
	}
	b, err := readResults(*base)
	if err != nil {
		return err
	}
	h, err := readResults(*head)
	if err != nil {
		return err
	}
	v, err := Compare(b, h, *maxDrop)
	if err != nil {
		return err
	}
	for i, r := range v.Ratios {
		fmt.Printf("round %d: base %.0f QPS, head %.0f QPS, head/base %.4f\n", i+1, b[i].QPS, h[i].QPS, r)
	}
	fmt.Printf("base median %.0f QPS, head median %.0f QPS, median per-round drop %.2f%% (limit %.2f%%): %s\n",
		v.BaseQPS, v.HeadQPS, v.Drop*100, *maxDrop*100, passFail(v.Pass))
	if !v.Pass {
		return errGateFailed
	}
	return nil
}

func cmdAbsolute(args []string) error {
	fs := flag.NewFlagSet("absolute", flag.ContinueOnError)
	result := fs.String("result", "", "result JSON file")
	minQPS := fs.Float64("min-qps", 1_000_000, "minimum QPS")
	maxP99 := fs.Duration("max-p99", 500*time.Microsecond, "p99 latency must stay below this")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rs, err := readResults(*result)
	if err != nil {
		return err
	}
	if len(rs) != 1 {
		return errors.New("--result takes exactly one file")
	}
	v, err := Absolute(rs[0], *minQPS, *maxP99)
	if err != nil {
		return err
	}
	fmt.Printf("QPS %.0f, p99 %s: %s\n", v.QPS, v.P99, passFail(v.Pass))
	for _, r := range v.Reasons {
		fmt.Println("  " + r)
	}
	if !v.Pass {
		return errGateFailed
	}
	return nil
}

func passFail(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

func checkAddr(addr string) error {
	a, err := net.ResolveUDPAddr("udp", addr)
	if err != nil || a.Port == 0 {
		return fmt.Errorf("invalid address %q: want host:port", addr)
	}
	return nil
}

func readResults(list string) ([]dnsperf.Result, error) {
	if list == "" {
		return nil, errors.New("no result files given")
	}
	var out []dnsperf.Result
	for _, p := range strings.Split(list, ",") {
		raw, err := os.ReadFile(strings.TrimSpace(p))
		if err != nil {
			return nil, err
		}
		var r dnsperf.Result
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, r)
	}
	return out, nil
}

func writeResult(path string, r dnsperf.Result) error {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	fmt.Printf("QPS %.0f, completed %d of %d, lost %d, avg %.6fs, p99 %.6fs\n",
		r.QPS, r.Completed, r.Sent, r.Lost, r.LatencyAvgSeconds, r.P99Seconds)
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// warmAndLoad queries every name once so the measured run is all cache hits, then runs dnsperf.
func warmAndLoad(ctx context.Context, addr string, names []string, seconds, clients, threads int) (dnsperf.Result, error) {
	if err := warm(ctx, addr, names); err != nil {
		return dnsperf.Result{}, err
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return dnsperf.Result{}, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return dnsperf.Result{}, err
	}
	r, err := dnsperf.Run(ctx, dnsperf.Options{Server: host, Port: port, Names: names, Seconds: seconds, Clients: clients, Threads: threads})
	if err != nil {
		return r, err
	}
	if r.Completed == 0 {
		return r, errors.New("dnsperf completed no queries")
	}
	return r, nil
}

// warm sends one A query per name from 64 goroutines, retrying each name up to three times.
func warm(ctx context.Context, addr string, names []string) error {
	work := make(chan string)
	var failed atomic.Int64
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() {
			c := &dns.Client{Net: "udp", Timeout: 2 * time.Second}
			for name := range work {
				m := new(dns.Msg)
				m.SetQuestion(name, dns.TypeA)
				ok := false
				for range 3 {
					if r, _, err := c.ExchangeContext(ctx, m, addr); err == nil && r.Rcode == dns.RcodeSuccess && len(r.Answer) > 0 {
						ok = true
						break
					}
				}
				if !ok {
					failed.Add(1)
				}
			}
		})
	}
	for _, n := range names {
		select {
		case work <- n:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(work)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	if n := failed.Load(); n > 0 {
		return fmt.Errorf("warm-up: %d of %d names got no answer from %s", n, len(names), addr)
	}
	return nil
}

type proc struct {
	cmd  *exec.Cmd
	log  string
	done chan struct{}
}

type stack struct {
	dir             string
	fixture, engine *proc
	// dnsAddr is the engine's bound UDP and TCP address.
	dnsAddr string
}

// startStack runs `nexora-fixture dns` on loopback and a standalone nexora-engine serving dnsAddr
// (port 0: a kernel-chosen port) with one UDP upstream pointing at the fixture, and waits until
// the engine serves version 1. Both children listen on port 0 where they may and report the
// addresses they bound, so no port is picked ahead of time for another process to take.
func startStack(ctx context.Context, engineBin, fixtureBin, dnsAddr string, workers int) (*stack, error) {
	dir, err := os.MkdirTemp("", "perfgate-")
	if err != nil {
		return nil, err
	}
	st := &stack{dir: dir}
	fail := func(err error) (*stack, error) {
		st.stop()
		return nil, err
	}
	const port0 = "127.0.0.1:0"
	st.fixture, err = start(ctx, dir, "fixture", fixtureBin, "dns", "--udp", port0, "--tcp", port0,
		"--dot", port0, "--doh", port0, "--control", port0, "--cert-dir", filepath.Join(dir, "certs"))
	if err != nil {
		return fail(err)
	}
	fixtureReady, err := st.fixture.waitReady(10 * time.Second)
	if err != nil {
		return fail(err)
	}
	fixtureUDP := fixtureReady["udp"]

	snap := &controlv1.ConfigSnapshot{
		Version:  1,
		Resolver: &controlv1.ResolverConfig{Strategy: controlv1.UpstreamStrategy_UPSTREAM_STRATEGY_ORDERED},
		Cache:    &controlv1.CacheConfig{MaxBytes: 512 << 20, MaxTtl: 86400, NegativeMaxTtl: 3600, StaleWindow: 0},
		Upstreams: []*controlv1.Upstream{{Id: "fixture", Name: "fixture",
			Protocol: controlv1.UpstreamProtocol_UPSTREAM_PROTOCOL_UDP, Address: fixtureUDP, TimeoutMs: 1000}},
		AclAllowCidrs: []string{"0.0.0.0/0", "::/0"},
		Filter:        &controlv1.FilterConfig{BlockMode: controlv1.BlockMode_BLOCK_MODE_NULL_IP, BlockTtl: 60},
		Telemetry:     &controlv1.TelemetryConfig{},
	}
	raw, err := proto.Marshal(snap)
	if err != nil {
		return fail(err)
	}
	snapPath, blobDir, stateDir := filepath.Join(dir, "snapshot.binpb"), filepath.Join(dir, "blobs"), filepath.Join(dir, "state")
	for _, d := range []string{blobDir, stateDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fail(err)
		}
	}
	if err := os.WriteFile(snapPath, raw, 0o600); err != nil {
		return fail(err)
	}
	cfg := fmt.Sprintf(`node_name = "perfgate"
state_dir = %q
listen_udp = [%q]
listen_tcp = [%q]
metrics_listen = %q
workers = %d
standalone_snapshot = %q
standalone_blob_dir = %q
`, stateDir, dnsAddr, dnsAddr, "127.0.0.1:0", workers, snapPath, blobDir)
	cfgPath := filepath.Join(dir, "engine.toml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		return fail(err)
	}
	st.engine, err = start(ctx, dir, "engine", engineBin, "--config", cfgPath)
	if err != nil {
		return fail(err)
	}
	if _, err := st.engine.waitLog(regexp.MustCompile(`(?m)^nexora-engine: serving version 1$`), 30*time.Second); err != nil {
		return fail(err)
	}
	engineReady, err := st.engine.waitReady(10 * time.Second)
	if err != nil {
		return fail(err)
	}
	st.dnsAddr = engineReady["udp"]
	return st, nil
}

func (st *stack) stop() {
	for _, p := range []*proc{st.engine, st.fixture} {
		if p != nil {
			p.stop()
		}
	}
	_ = os.RemoveAll(st.dir)
}

func start(ctx context.Context, dir, name, bin string, args ...string) (*proc, error) {
	logPath := filepath.Join(dir, name+".log")
	f, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// bin is an operator-supplied binary path (--engine/--fixture), not remote input.
	cmd := exec.Command(bin, args...) // nosemgrep: dangerous-exec-command
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	p := &proc{cmd: cmd, log: logPath, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	go func() {
		select {
		case <-ctx.Done():
			p.stop()
		case <-p.done:
		}
	}()
	return p, nil
}

func (p *proc) stop() {
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

// waitLog waits until the log matches re and returns the submatches of the first match.
func (p *proc) waitLog(re *regexp.Regexp, timeout time.Duration) ([]string, error) {
	deadline := time.Now().Add(timeout)
	for {
		if data, err := os.ReadFile(p.log); err == nil {
			if m := re.FindStringSubmatch(string(data)); m != nil {
				return m, nil
			}
		}
		select {
		case <-p.done:
			return nil, fmt.Errorf("%s exited before logging %q:\n%s", filepath.Base(p.cmd.Path), re, tail(p.log, 30))
		default:
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s did not log %q within %s:\n%s", filepath.Base(p.cmd.Path), re, timeout, tail(p.log, 30))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

var readyLine = regexp.MustCompile(`(?m)^READY (.*)$`)

// waitReady waits for the `READY key=addr ...` line that nexora-fixture and nexora-engine print
// once their listeners are bound, and returns the pairs; every value is a host:port with a
// non-zero port.
func (p *proc) waitReady(timeout time.Duration) (map[string]string, error) {
	m, err := p.waitLog(readyLine, timeout)
	if err != nil {
		return nil, err
	}
	return parseReady(m[1])
}

func parseReady(fields string) (map[string]string, error) {
	out := map[string]string{}
	for _, f := range strings.Fields(fields) {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return nil, fmt.Errorf("malformed READY field %q", f)
		}
		for _, a := range strings.Split(v, ",") {
			if err := checkAddr(a); err != nil {
				return nil, fmt.Errorf("READY %s: %w", k, err)
			}
		}
		out[k] = v
	}
	if out["udp"] == "" {
		return nil, fmt.Errorf("READY line %q has no udp address", fields)
	}
	return out, nil
}

func tail(path string, lines int) string {
	data, _ := os.ReadFile(path)
	all := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	return strings.Join(all[max(0, len(all)-lines):], "\n")
}
