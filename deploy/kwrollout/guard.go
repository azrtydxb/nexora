package kwrollout

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// mutationGuard lets supporting shell scripts check the *same* lock owner before
// each Kubernetes/API mutation without exposing an owner token or allowing a
// second process to acquire/release the lock. The private Unix socket is local.
type mutationGuard struct {
	path   string
	dir    string
	server *http.Server
	mu     sync.Mutex
	ctx    context.Context
	check  func(context.Context) error
}

func newMutationGuard(ctx context.Context, check func(context.Context) error) (*mutationGuard, error) {
	dir, err := os.MkdirTemp("/tmp", "nexora-guard-")
	if err != nil {
		return nil, err
	}
	g := &mutationGuard{path: filepath.Join(dir, "check.sock"), dir: dir, ctx: ctx, check: check}
	listener, err := net.Listen("unix", g.path)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	if err := os.Chmod(g.path, 0600); err != nil {
		listener.Close()
		os.RemoveAll(dir)
		return nil, err
	}
	g.server = &http.Server{ReadHeaderTimeout: time.Second, WriteTimeout: 20 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/check" {
			http.Error(w, "invalid guard request", http.StatusNotFound)
			return
		}
		g.mu.Lock()
		active := g.ctx
		g.mu.Unlock()
		if err := active.Err(); err != nil {
			http.Error(w, "rollout cancelled", http.StatusConflict)
			return
		}
		if err := g.check(active); err != nil {
			http.Error(w, "deployment ownership not confirmed", http.StatusConflict)
			return
		}
		if active.Err() != nil {
			http.Error(w, "rollout cancelled", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})}
	go func() { _ = g.server.Serve(listener) }()
	return g, nil
}

func (g *mutationGuard) setContext(ctx context.Context) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ctx = ctx
}

func (g *mutationGuard) close() {
	_ = g.server.Close()
	_ = os.RemoveAll(g.dir)
}

// guardEnvironment replaces inherited controls instead of appending duplicates
// whose interpretation differs between shells and operating systems.
func guardEnvironment(base []string, values map[string]string) []string {
	var out []string
	for _, entry := range base {
		keep := true
		for key := range values {
			if len(entry) > len(key) && entry[:len(key)+1] == key+"=" {
				keep = false
			}
		}
		if keep {
			out = append(out, entry)
		}
	}
	for key, value := range values {
		out = append(out, fmt.Sprintf("%s=%s", key, value))
	}
	return out
}
