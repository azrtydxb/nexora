package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/piwi3910/nexora/e2e/fixtures/authhier"
)

// runAuthhier serves authhier.DefaultSpec on a kernel-chosen shared port and writes its Ready
// (port, root DS, root hints, forwarder, stats URL) as JSON to the ready file.
func runAuthhier(args []string) (func(), string, error) {
	fs := flag.NewFlagSet("authhier", flag.ContinueOnError)
	readyFile := fs.String("ready-file", "", "file receiving the hierarchy's Ready JSON")
	if err := fs.Parse(args); err != nil {
		return nil, "", err
	}
	if *readyFile == "" {
		return nil, "", errors.New("--ready-file is required")
	}
	h, r, err := authhier.Start(context.Background(), authhier.DefaultSpec(0), time.Now())
	if err != nil {
		return nil, "", err
	}
	data, err := json.Marshal(r)
	if err == nil {
		// Written aside and renamed so a reader never sees a partial file.
		tmp := *readyFile + ".tmp"
		if err = os.WriteFile(tmp, data, 0o600); err == nil {
			err = os.Rename(tmp, *readyFile)
		}
	}
	if err != nil {
		h.Close()
		return nil, "", err
	}
	return h.Close, fmt.Sprintf("port=%d stats=%s", r.Port, strings.TrimPrefix(r.StatsURL, "http://")), nil
}
