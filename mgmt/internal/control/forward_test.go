package control_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/control"
)

func TestNotifyAndUpdateAreForwardedWithoutBlockingTheStream(t *testing.T) {
	notified := make(chan *controlv1.NotifyReceived, 1)
	release := make(chan struct{})
	f := setupServers(t, 1, nil, func(s *control.Server) {
		s.OnNotify = func(_ context.Context, _ pgx.Tx, _ string, ev *controlv1.NotifyReceived) error {
			notified <- ev
			return nil
		}
		s.OnUpdate = func(ctx context.Context, _ string, req *controlv1.UpdateRequest, _ func(pgx.Tx) error) *controlv1.UpdateResult {
			if req.Zone == "slow.test." {
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
			return &controlv1.UpdateResult{Rcode: dns.RcodeSuccess, Detail: req.Zone}
		}
	})
	client, id := f.enroll(t, f.addr[0])
	stream, err := client.Connect(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	send := func(m *controlv1.EngineMessage) {
		t.Helper()
		if err := stream.Send(m); err != nil {
			t.Fatal(err)
		}
	}
	send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: id, NodeName: "e1"}}})
	recvSnapshot(t, stream)

	send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_NotifyReceived{NotifyReceived: &controlv1.NotifyReceived{Zone: "up.test.", Source: "192.0.2.53:53"}}})
	select {
	case ev := <-notified:
		if ev.Zone != "up.test." || ev.Source != "192.0.2.53:53" {
			t.Fatalf("notify %v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OnNotify not called")
	}

	// maxInflightUpdates (16) slow updates occupy every slot; the next ones are refused at once.
	for i := range 20 {
		send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_UpdateRequest{UpdateRequest: &controlv1.UpdateRequest{
			RequestId: "slow-" + string(rune('a'+i)), Zone: "slow.test.", Message: []byte{0}}}})
	}
	send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_UpdateRequest{UpdateRequest: &controlv1.UpdateRequest{
		RequestId: "big", Zone: "fast.test.", Message: make([]byte, 70000)}}})
	// The receive loop keeps going while updates are being applied.
	send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Applied{Applied: &controlv1.Applied{Version: 1}}})
	harness.Eventually(t, 3*time.Second, func() error { return expectEngine(f, id, "applied_version", int64(1)) })

	results := map[string]*controlv1.UpdateResult{}
	recvResults := func(n int) {
		t.Helper()
		for len(results) < n {
			ch := make(chan *controlv1.ServerMessage, 1)
			go func() { m, _ := stream.Recv(); ch <- m }()
			select {
			case m := <-ch:
				if r := m.GetUpdateResult(); r != nil {
					results[r.RequestId] = r
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("results so far: %v", results)
			}
		}
	}
	recvResults(5)
	refused := 0
	for id, r := range results {
		switch {
		case id == "big":
			if r.Rcode != dns.RcodeFormatError {
				t.Fatalf("oversized update: %v", r)
			}
		case r.Rcode == dns.RcodeRefused && strings.Contains(r.Detail, "concurrent"):
			refused++
		default:
			t.Fatalf("unexpected early result %v", r)
		}
	}
	if refused != 4 {
		t.Fatalf("refused %d updates beyond the concurrency bound, want 4", refused)
	}
	close(release)
	recvResults(21)
	for id, r := range results {
		if id != "big" && r.Rcode == dns.RcodeSuccess && r.Detail != "slow.test." {
			t.Fatalf("result %v", r)
		}
	}
}
