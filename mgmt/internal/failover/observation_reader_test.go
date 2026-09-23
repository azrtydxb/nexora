package failover

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type observationTransport func(*http.Request) (*http.Response, error)

func (f observationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type observationFixture struct {
	reader   *ObservationReader
	group    Group
	doc      TrustedObservations
	now      time.Time
	objects  map[string]observationObject
	calls    []string
	hook     func(*http.Request, int)
	response func(*http.Request) (*http.Response, error)
}

const observationInputPath = "/api/v1/namespaces/dns/configmaps/eligibility"

func observationPodPath(i int) string  { return fmt.Sprintf("/api/v1/namespaces/dns/pods/engine-%d", i) }
func observationNodePath(i int) string { return fmt.Sprintf("/api/v1/nodes/worker-%d", i) }

func newObservationFixture(t *testing.T) *observationFixture {
	t.Helper()
	f := &observationFixture{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC), objects: map[string]observationObject{}}
	f.group = Group{Name: "dns136", FrontendIP: "192.168.10.136", Members: [2]uuid.UUID{uuid.New(), uuid.New()}}
	f.doc.Version = 1
	policy := uuid.New()
	for i, id := range f.group.Members {
		podUID, nodeUID := uuid.NewString(), uuid.NewString()
		no := false
		check := EligibilityCheck{OK: true, ObservedAt: f.now}
		snap := EligibilitySnapshot{Version: 3, Digest: strings.Repeat("a", 64)}
		f.doc.Members = append(f.doc.Members, TrustedObservation{
			EngineID: id, Namespace: "dns", PodName: fmt.Sprintf("engine-%d", i), PodUID: podUID, NodeUID: nodeUID,
			ContainerID: fmt.Sprintf("containerd://engine-%d", i), BackendIP: fmt.Sprintf("10.0.0.%d", i+1),
			BindingObservedAt: f.now, InventoryObservedAt: f.now, PolicyGroupID: policy, Revoked: &no, Deleted: &no,
			Management: check, DirectDNS: check, Applied: snap, Target: snap, SnapshotObservedAt: f.now,
		})
		// Kubernetes wire objects, including creation/start boundaries.
		raw := fmt.Sprintf(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"engine-%d","namespace":"dns","uid":%q,"resourceVersion":"12","creationTimestamp":"2026-09-17T11:00:00Z"},"spec":{"nodeName":"worker-%d"},"status":{"phase":"Running","podIP":"10.0.0.%d","conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"name":"engine","containerID":"containerd://engine-%d","ready":true,"state":{"running":{"startedAt":"2026-09-17T11:00:01Z"}}}]}}`, i, podUID, i, i+1, i)
		var pod observationObject
		if err := json.Unmarshal([]byte(raw), &pod); err != nil {
			t.Fatal(err)
		}
		f.objects[observationPodPath(i)] = pod
		f.objects[observationNodePath(i)] = observationObject{APIVersion: "v1", Kind: "Node", Metadata: observationMetadata{Name: fmt.Sprintf("worker-%d", i), UID: nodeUID, ResourceVersion: "7"}}
	}
	f.objects[observationInputPath] = observationObject{APIVersion: "v1", Kind: "ConfigMap", Metadata: observationMetadata{Name: "eligibility", Namespace: "dns", UID: uuid.NewString(), ResourceVersion: "20"}}
	client := &http.Client{Transport: observationTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Scheme != "https" || req.URL.Host != "kubernetes.test" || req.URL.RawQuery != "" {
			t.Fatalf("unexpected API request: %s %s", req.Method, req.URL)
		}
		f.calls = append(f.calls, req.URL.Path)
		if f.hook != nil {
			f.hook(req, len(f.calls))
		}
		if f.response != nil {
			return f.response(req)
		}
		obj, ok := f.objects[req.URL.Path]
		if !ok {
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		if req.URL.Path == observationInputPath {
			b, err := json.Marshal(f.doc)
			if err != nil {
				t.Fatal(err)
			}
			obj.Data = map[string]string{"observations.json": string(b)}
		}
		b, err := json.Marshal(obj)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
	})}
	var err error
	f.reader, err = NewObservationReader(client, "https://kubernetes.test", "dns", "eligibility")
	if err != nil {
		t.Fatal(err)
	}
	f.reader.now = func() time.Time { return f.now }
	return f
}

func TestObservationReaderHealthy(t *testing.T) {
	f := newObservationFixture(t)
	result, err := f.reader.Observe(context.Background(), f.group, 30*time.Second)
	if err != nil || !reflect.DeepEqual(result.Eligible, f.group.Members[:]) {
		t.Fatalf("%+v %v", result, err)
	}
	want := []string{observationInputPath, observationPodPath(0), observationNodePath(0), observationPodPath(1), observationNodePath(1), observationNodePath(1), observationPodPath(1), observationNodePath(0), observationPodPath(0), observationInputPath}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("reads: %v", f.calls)
	}
	// Reusing the reader never reuses an earlier observation.
	delete(f.objects, observationInputPath)
	result, err = f.reader.Observe(context.Background(), f.group, 30*time.Second)
	if err == nil || len(result.Eligible) != 0 {
		t.Fatalf("cached eligibility survived missing input: %+v %v", result, err)
	}
}

func TestObservationReaderBindingFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*observationFixture)
	}{
		{"replacement with reused name", func(f *observationFixture) {
			p := f.objects[observationPodPath(0)]
			p.Metadata.UID = uuid.NewString()
			f.objects[observationPodPath(0)] = p
		}},
		{"node name reused", func(f *observationFixture) {
			n := f.objects[observationNodePath(0)]
			n.Metadata.UID = uuid.NewString()
			f.objects[observationNodePath(0)] = n
		}},
		{"foreign input namespace", func(f *observationFixture) {
			o := f.objects[observationInputPath]
			o.Metadata.Namespace = "foreign"
			f.objects[observationInputPath] = o
		}},
		{"foreign member namespace", func(f *observationFixture) { f.doc.Members[0].Namespace = "foreign" }},
		{"foreign pod namespace", func(f *observationFixture) {
			o := f.objects[observationPodPath(0)]
			o.Metadata.Namespace = "foreign"
			f.objects[observationPodPath(0)] = o
		}},
		{"UID mismatch", func(f *observationFixture) { f.doc.Members[0].PodUID = uuid.NewString() }},
		{"unknown engine", func(f *observationFixture) { f.doc.Members[0].EngineID = uuid.New() }},
		{"duplicate engine", func(f *observationFixture) { f.doc.Members[1] = f.doc.Members[0] }},
		{"missing member", func(f *observationFixture) { f.doc.Members = f.doc.Members[:1] }},
		{"extra member", func(f *observationFixture) { f.doc.Members = append(f.doc.Members, f.doc.Members[0]) }},
		{"missing revocation state", func(f *observationFixture) { f.doc.Members[0].Revoked = nil }},
		{"missing deletion state", func(f *observationFixture) { f.doc.Members[0].Deleted = nil }},
		{"wrong backend", func(f *observationFixture) { f.doc.Members[0].BackendIP = "10.0.0.99" }},
		{"container restart", func(f *observationFixture) {
			p := f.objects[observationPodPath(0)]
			p.Status.ContainerStatuses[0].ContainerID = "containerd://new"
			f.objects[observationPodPath(0)] = p
		}},
		{"old container measurements", func(f *observationFixture) { f.doc.Members[0].Management.ObservedAt = f.now.Add(-2 * time.Hour) }},
		{"missing start", func(f *observationFixture) {
			p := f.objects[observationPodPath(0)]
			p.Status.ContainerStatuses[0].State.Running.StartedAt = time.Time{}
			f.objects[observationPodPath(0)] = p
		}},
		{"terminating pod", func(f *observationFixture) {
			p := f.objects[observationPodPath(0)]
			p.Metadata.DeletionTimestamp = &f.now
			f.objects[observationPodPath(0)] = p
		}},
		{"terminating node", func(f *observationFixture) {
			p := f.objects[observationNodePath(0)]
			p.Metadata.DeletionTimestamp = &f.now
			f.objects[observationNodePath(0)] = p
		}},
		{"path traversal", func(f *observationFixture) { f.doc.Members[0].PodName = "../secrets" }},
		{"version", func(f *observationFixture) { f.doc.Version = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newObservationFixture(t)
			tc.change(f)
			got, err := f.reader.Observe(context.Background(), f.group, 30*time.Second)
			if err == nil || len(got.Eligible) != 0 {
				t.Fatalf("accepted invalid binding: %+v %v", got, err)
			}
		})
	}
}

func TestObservationReaderMovingInputs(t *testing.T) {
	for _, kind := range []string{"pod replaced", "node replaced", "resource version only", "input recreated", "target moved", "revoked during read"} {
		t.Run(kind, func(t *testing.T) {
			f := newObservationFixture(t)
			f.hook = func(_ *http.Request, n int) {
				if n != 6 {
					return
				}
				switch kind {
				case "pod replaced":
					p := f.objects[observationPodPath(0)]
					p.Metadata.UID = uuid.NewString()
					f.objects[observationPodPath(0)] = p
				case "node replaced":
					p := f.objects[observationNodePath(0)]
					p.Metadata.UID = uuid.NewString()
					f.objects[observationNodePath(0)] = p
				case "resource version only":
					p := f.objects[observationPodPath(0)]
					p.Metadata.ResourceVersion = "13"
					f.objects[observationPodPath(0)] = p
				case "input recreated":
					p := f.objects[observationInputPath]
					p.Metadata.UID = uuid.NewString()
					f.objects[observationInputPath] = p
				case "target moved":
					f.doc.Members[0].Target.Version++
				case "revoked during read":
					yes := true
					f.doc.Members[0].Revoked = &yes
				}
			}
			got, err := f.reader.Observe(context.Background(), f.group, 30*time.Second)
			if err == nil || len(got.Eligible) != 0 {
				t.Fatalf("moving input accepted: %+v %v", got, err)
			}
		})
	}
}

func TestObservationReaderEligibility(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*observationFixture)
		pool   int
		reason EligibilityReason
	}{
		{"revoked", func(f *observationFixture) { yes := true; f.doc.Members[0].Revoked = &yes }, 1, ReasonRevoked},
		{"deleted", func(f *observationFixture) { yes := true; f.doc.Members[0].Deleted = &yes }, 1, ReasonDeleted},
		{"stale inventory", func(f *observationFixture) { f.doc.Members[0].InventoryObservedAt = f.now.Add(-31 * time.Second) }, 1, ReasonInventoryTime},
		{"future inventory", func(f *observationFixture) { f.doc.Members[0].InventoryObservedAt = f.now.Add(time.Nanosecond) }, 1, ReasonInventoryTime},
		{"stale binding", func(f *observationFixture) { f.doc.Members[0].BindingObservedAt = f.now.Add(-31 * time.Second) }, 0, ReasonFailureDomain},
		{"future binding", func(f *observationFixture) { f.doc.Members[0].BindingObservedAt = f.now.Add(time.Nanosecond) }, 0, ReasonFailureDomain},
		{"management stale", func(f *observationFixture) { f.doc.Members[0].Management.ObservedAt = f.now.Add(-31 * time.Second) }, 1, ReasonManagement},
		{"DNS stale", func(f *observationFixture) { f.doc.Members[0].DirectDNS.ObservedAt = f.now.Add(-31 * time.Second) }, 1, ReasonDirectDNS},
		{"DNS failed", func(f *observationFixture) { f.doc.Members[0].DirectDNS.OK = false }, 1, ReasonDirectDNS},
		{"applied stale", func(f *observationFixture) { f.doc.Members[0].SnapshotObservedAt = f.now.Add(-31 * time.Second) }, 1, ReasonSnapshot},
		{"unapplied target", func(f *observationFixture) { f.doc.Members[0].Applied.Version-- }, 1, ReasonSnapshot},
		{"wrong applied content", func(f *observationFixture) { f.doc.Members[0].Applied.Digest = strings.Repeat("b", 64) }, 1, ReasonSnapshot},
		{"effective config differs", func(f *observationFixture) {
			f.doc.Members[0].Target.Digest = strings.Repeat("b", 64)
			f.doc.Members[0].Applied = f.doc.Members[0].Target
		}, 0, ReasonTargetMismatch},
		{"policy differs", func(f *observationFixture) { f.doc.Members[0].PolicyGroupID = uuid.New() }, 0, ReasonPolicyMismatch},
		{"pod unready", func(f *observationFixture) {
			p := f.objects[observationPodPath(0)]
			p.Status.Conditions[0].Status = "False"
			f.objects[observationPodPath(0)] = p
		}, 1, ReasonNotReady},
		{"same real node", func(f *observationFixture) {
			p := f.objects[observationPodPath(1)]
			p.Spec.NodeName = "worker-0"
			f.objects[observationPodPath(1)] = p
			f.doc.Members[1].NodeUID = f.doc.Members[0].NodeUID
		}, 0, ReasonFailureDomain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newObservationFixture(t)
			tc.change(f)
			got, err := f.reader.Observe(context.Background(), f.group, 30*time.Second)
			if err != nil || len(got.Eligible) != tc.pool || !slices.Contains(got.Members[0].Reasons, tc.reason) {
				t.Fatalf("%+v %v", got, err)
			}
			if tc.pool == 1 && got.Eligible[0] != f.group.Members[1] {
				t.Fatalf("wrong surviving member: %+v", got)
			}
		})
	}
}

func TestObservationReaderTimeBounds(t *testing.T) {
	for _, elapsed := range []time.Duration{30 * time.Second, 30*time.Second + time.Nanosecond, -time.Nanosecond} {
		t.Run(elapsed.String(), func(t *testing.T) {
			f := newObservationFixture(t)
			f.hook = func(_ *http.Request, n int) {
				if n == 10 {
					f.now = f.now.Add(elapsed)
				}
			}
			got, err := f.reader.Observe(context.Background(), f.group, 30*time.Second)
			if elapsed == 30*time.Second {
				if err != nil || len(got.Eligible) != 2 {
					t.Fatalf("inclusive boundary rejected: %+v %v", got, err)
				}
			} else if err == nil || len(got.Eligible) != 0 {
				t.Fatalf("bad elapsed time accepted: %+v %v", got, err)
			}
		})
	}
	f := newObservationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.hook = func(_ *http.Request, n int) {
		if n == 10 {
			cancel()
		}
	}
	got, err := f.reader.Observe(ctx, f.group, 30*time.Second)
	if err != context.Canceled || len(got.Eligible) != 0 {
		t.Fatalf("cancellation: %+v %v", got, err)
	}
}

func TestObservationReaderHTTPFailures(t *testing.T) {
	for _, mode := range []string{"not found", "forbidden", "unavailable", "redirect", "network", "malformed", "too large", "unknown input field", "trailing input"} {
		t.Run(mode, func(t *testing.T) {
			f := newObservationFixture(t)
			f.response = func(*http.Request) (*http.Response, error) {
				code, body := 200, "{}"
				switch mode {
				case "not found":
					code = 404
				case "forbidden":
					code = 403
				case "unavailable":
					code = 503
				case "redirect":
					return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://foreign.test/"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
				case "network":
					return nil, fmt.Errorf("unreachable")
				case "malformed":
					body = "{"
				case "too large":
					body = strings.Repeat("x", (1<<20)+1)
				case "unknown input field", "trailing input":
					b, e := json.Marshal(f.doc)
					if e != nil {
						t.Fatal(e)
					}
					raw := string(b)
					if mode == "unknown input field" {
						raw = `{"engineNodeName":"pretend",` + raw[1:]
					} else {
						raw += " {}"
					}
					obj := f.objects[observationInputPath]
					obj.Data = map[string]string{"observations.json": raw}
					b, e = json.Marshal(obj)
					if e != nil {
						t.Fatal(e)
					}
					body = string(b)
				}
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body))}, nil
			}
			got, err := f.reader.Observe(context.Background(), f.group, 30*time.Second)
			if err == nil || len(got.Eligible) != 0 || len(f.calls) != 1 {
				t.Fatalf("%+v %v calls=%v", got, err, f.calls)
			}
		})
	}
}

func TestObservationReaderConfiguration(t *testing.T) {
	for _, server := range []string{"http://kubernetes.test", "https://user:password@kubernetes.test", "https://kubernetes.test/path", "https://kubernetes.test?token=x", "https://kubernetes.test#fragment", ""} {
		if _, err := NewObservationReader(&http.Client{}, server, "dns", "eligibility"); err == nil {
			t.Fatalf("accepted %s", server)
		}
	}
	if _, err := NewObservationReader(nil, "https://kubernetes.test", "dns", "eligibility"); err == nil {
		t.Fatal("nil client accepted")
	}
	for _, name := range []string{"", "../other", "UPPER"} {
		if _, err := NewObservationReader(&http.Client{}, "https://kubernetes.test", name, "eligibility"); err == nil {
			t.Fatalf("namespace %q accepted", name)
		}
	}
	client := &http.Client{}
	reader, err := NewObservationReader(client, "https://kubernetes.test", "dns", "eligibility")
	if err != nil || reader.client.Timeout != 10*time.Second || client.Timeout != 0 || client.CheckRedirect != nil {
		t.Fatalf("constructor changed caller client or lacks timeout: %v", err)
	}
}

func TestObservationReaderRebinding(t *testing.T) {
	f := newObservationFixture(t)
	o := &f.doc.Members[0]
	p := f.objects[observationPodPath(0)]
	p.Metadata.UID = uuid.NewString()
	p.Metadata.CreationTimestamp = f.now.Add(-2 * time.Second)
	p.Status.ContainerStatuses[0].ContainerID = "containerd://replacement"
	p.Status.ContainerStatuses[0].State.Running.StartedAt = f.now.Add(-time.Second)
	f.objects[observationPodPath(0)] = p
	o.PodUID = p.Metadata.UID
	o.ContainerID = p.Status.ContainerStatuses[0].ContainerID
	got, err := f.reader.Observe(context.Background(), f.group, 30*time.Second)
	if err != nil || !reflect.DeepEqual(got.Eligible, f.group.Members[:]) {
		t.Fatalf("fresh authoritative rebind failed: %+v %v", got, err)
	}
}

func TestObservationReaderBackendCannotBeVIPOrShared(t *testing.T) {
	for _, mode := range []string{"frontend", "shared", "link-local"} {
		t.Run(mode, func(t *testing.T) {
			f := newObservationFixture(t)
			ip := f.group.FrontendIP
			if mode == "shared" {
				ip = f.doc.Members[1].BackendIP
			}
			if mode == "link-local" {
				ip = "169.254.0.1"
			}
			f.doc.Members[0].BackendIP = ip
			p := f.objects[observationPodPath(0)]
			p.Status.PodIP = ip
			f.objects[observationPodPath(0)] = p
			got, err := f.reader.Observe(context.Background(), f.group, 30*time.Second)
			if err == nil || len(got.Eligible) != 0 {
				t.Fatalf("invalid direct backend accepted: %+v %v", got, err)
			}
		})
	}
}

func TestObservationReaderDeadline(t *testing.T) {
	f := newObservationFixture(t)
	f.response = func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok || time.Until(deadline) > time.Second {
			t.Fatal("HTTP read lacks bounded deadline")
		}
		<-req.Context().Done()
		return nil, req.Context().Err()
	}
	got, err := f.reader.Observe(context.Background(), f.group, 10*time.Millisecond)
	if err == nil || len(got.Eligible) != 0 || len(f.calls) != 1 {
		t.Fatalf("deadline was not fail closed without retry: %+v %v reads=%d", got, err, len(f.calls))
	}
}
