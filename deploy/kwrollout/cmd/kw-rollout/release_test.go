package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/piwi3910/nexora/deploy/kwrollout"
)

type fakeReleaser struct {
	calls   int
	owner   string
	confirm bool
}

func (f *fakeReleaser) ReleaseHeld(_ context.Context, owner string, confirm bool) (*kwrollout.HeldLock, error) {
	f.calls++
	f.owner, f.confirm = owner, confirm
	h := &kwrollout.HeldLock{Name: "nexora-deploy-lock", Owner: owner, Stage: "stage frozen", ResourceVersion: "7"}
	if !confirm {
		return h, kwrollout.ErrReleaseNotConfirmed
	}
	return h, nil
}

func TestReleaseLockFlags(t *testing.T) {
	for name, tc := range map[string]struct {
		args  []string
		calls int
		ok    bool
		out   string
	}{
		"missing owner":   {[]string{"--confirm"}, 0, false, ""},
		"extra argument":  {[]string{"--owner", "t", "--confirm", "x"}, 0, false, ""},
		"missing confirm": {[]string{"--owner", "t"}, 1, false, "found lock"},
		"confirmed":       {[]string{"--owner", "t", "--confirm"}, 1, true, "released lock nexora-deploy-lock: owner=t acquiredAt=unknown stage=stage frozen"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeReleaser{}
			var out bytes.Buffer
			err := releaseLock(context.Background(), tc.args, &out, func(string) (lockReleaser, error) { return f, nil })
			if (err == nil) != tc.ok || f.calls != tc.calls || !strings.Contains(out.String(), tc.out) {
				t.Fatalf("err=%v calls=%d out=%q", err, f.calls, out.String())
			}
		})
	}
}
