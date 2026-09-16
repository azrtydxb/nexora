package kwrollout

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// Exercise the real UDP/TCP probe adapter, not just callback failures.
func TestRunSampledTCPFailureStopsRollout(t *testing.T) {
	var rolling atomic.Bool
	p := dnsFixture(t, func(w dns.ResponseWriter, _ *dns.Msg, reply *dns.Msg) {
		if rolling.Load() && w.RemoteAddr().Network() == "tcp" {
			reply.Rcode = dns.RcodeServerFailure
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var events []string
	var samples []DNSSample // baseline, monitor and final rounds never overlap
	s := fakeSteps(&events, false)
	s.monitorInterval = time.Millisecond
	s.checkDNS = func(ctx context.Context) error {
		return CheckDNS(ctx, []DNSProbe{p}, func(sample DNSSample) { samples = append(samples, sample) })
	}
	s.apply = func(ctx context.Context, stage Stage) error {
		events = append(events, "apply:"+stage.Name)
		rolling.Store(true)
		<-ctx.Done()
		return ctx.Err()
	}
	err := run(ctx, testPairs(), s)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if len(samples) < 4 || samples[len(samples)-1].Transport != "tcp" || samples[len(samples)-1].Err == nil || events[len(events)-1] != "apply:frozen" {
		t.Fatalf("samples=%+v events=%v", samples, events)
	}
}

func TestRunRequiresDNSMonitoring(t *testing.T) {
	for _, missing := range []string{"callback", "interval"} {
		t.Run(missing, func(t *testing.T) {
			var events []string
			s := fakeSteps(&events, true)
			if missing == "callback" {
				s.checkDNS = nil
			} else {
				s.monitorInterval = 0
			}
			if err := run(context.Background(), testPairs(), s); err == nil || len(events) != 0 {
				t.Fatalf("err=%v events=%v", err, events)
			}
		})
	}
}

func TestRunDNSBaselineAndFinalFailuresRetainLock(t *testing.T) {
	for _, round := range []int{1, 2} {
		t.Run(map[int]string{1: "baseline", 2: "final"}[round], func(t *testing.T) {
			var events []string
			s := fakeSteps(&events, true)
			failure := errors.New("DNS failure")
			calls := 0
			s.checkDNS = func(context.Context) error {
				calls++
				if calls == round {
					return failure
				}
				return nil
			}
			if err := run(context.Background(), testPairs(), s); !errors.Is(err, failure) {
				t.Fatalf("err=%v", err)
			}
			if slices.Contains(events, "unlock") || (round == 1 && len(events) != 1) {
				t.Fatalf("advanced or unlocked: %v", events)
			}
		})
	}
}

func TestRunMonitorFailureCancelsActiveStage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var events []string
	s := fakeSteps(&events, true)
	s.monitorInterval = time.Millisecond
	stageStarted := make(chan struct{})
	failure := errors.New("sampled TCP failure")
	var calls atomic.Int32
	s.checkDNS = func(ctx context.Context) error {
		if calls.Add(1) == 1 {
			return nil
		}
		select {
		case <-stageStarted:
			return failure
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.apply = func(ctx context.Context, stage Stage) error {
		events = append(events, "apply:"+stage.Name)
		close(stageStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	if err := run(ctx, testPairs(), s); !errors.Is(err, failure) {
		t.Fatalf("lost monitor error: %v", err)
	}
	if events[len(events)-1] != "apply:frozen" {
		t.Fatalf("advanced or unlocked: %v", events)
	}
}

func TestRunJoinsMonitorBeforeReturn(t *testing.T) {
	for _, mode := range []string{"success", "stage failure", "shutdown failure", "external cancellation"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var events []string
			s := fakeSteps(&events, false)
			s.monitorInterval = time.Millisecond
			started, joined := make(chan struct{}), make(chan struct{})
			finishRound := make(chan struct{})
			failure := errors.New("failure during shutdown")
			var calls atomic.Int32
			s.checkDNS = func(ctx context.Context) error {
				if calls.Add(1) != 2 {
					return nil
				}
				close(started)
				defer close(joined)
				select {
				case <-finishRound:
					if mode == "shutdown failure" {
						return failure
					}
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			health := s.health
			s.health = func(ctx context.Context, g Gate) error {
				if g.Name == "final" {
					select {
					case <-started:
					case <-ctx.Done():
						return ctx.Err()
					}
					if mode == "stage failure" {
						return failure
					}
					if mode == "external cancellation" {
						cancel()
					} else {
						close(finishRound)
					}
				}
				return health(ctx, g)
			}
			unlock := s.unlock
			s.unlock = func(ctx context.Context) error {
				select {
				case <-joined:
				default:
					t.Error("unlock before monitor joined")
				}
				return unlock(ctx)
			}
			err := run(ctx, testPairs(), s)
			select {
			case <-joined:
			default:
				t.Fatal("return before monitor joined")
			}
			if mode == "success" {
				if err != nil || !slices.Contains(events, "unlock") || calls.Load() < 3 {
					t.Fatalf("err=%v calls=%d events=%v", err, calls.Load(), events)
				}
			} else if err == nil || slices.Contains(events, "unlock") {
				t.Fatalf("failure unlocked: err=%v events=%v", err, events)
			}
		})
	}
}

func TestRunCancellationDuringOwnershipPreventsMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events []string
	s := fakeSteps(&events, true)
	s.ownership = func(context.Context) error { cancel(); return nil }
	if err := run(ctx, testPairs(), s); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if slices.Contains(events, "apply:frozen") || slices.Contains(events, "unlock") {
		t.Fatalf("mutated after cancellation: %v", events)
	}
}
