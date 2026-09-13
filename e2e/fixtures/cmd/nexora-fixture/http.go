package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// maxListBody caps an uploaded blocklist; e2e lists are far smaller.
const maxListBody = 64 << 20

// listServer serves blocklist files that tests upload, can be told to fail per list, and counts
// fetches.
type listServer struct {
	mu      sync.Mutex
	lists   map[string][]byte
	failing map[string]bool
	hits    map[string]int
}

func runHTTP(args []string) (func(), error) {
	fs := flag.NewFlagSet("http", flag.ContinueOnError)
	listen := fs.String("listen", "", "listen address")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if *listen == "" {
		return nil, errors.New("--listen is required")
	}
	s := &listServer{lists: map[string][]byte{}, failing: map[string]bool{}, hits: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /lists/{name}", s.put)
	mux.HandleFunc("GET /lists/{name}", s.get)
	mux.HandleFunc("POST /lists/{name}/fail", s.fail)
	mux.HandleFunc("GET /hits/{name}", s.hitCount)
	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(lis) }()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}, nil
}

func (s *listServer) put(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxListBody))
	if err != nil {
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	s.mu.Lock()
	s.lists[r.PathValue("name")] = body
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *listServer) get(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.mu.Lock()
	s.hits[name]++
	body, ok := s.lists[name]
	failing := s.failing[name]
	s.mu.Unlock()
	switch {
	case failing:
		http.Error(w, "fixture failure", http.StatusInternalServerError)
	case !ok:
		http.NotFound(w, r)
	default:
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(body)
	}
}

func (s *listServer) fail(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Failing *bool `json:"failing"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil || body.Failing == nil {
		http.Error(w, "body must be {\"failing\": bool}", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.failing[r.PathValue("name")] = *body.Failing
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *listServer) hitCount(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	n := s.hits[r.PathValue("name")]
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]int{"hits": n})
}
