package failover

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"reflect"
	"time"

	"github.com/google/uuid"
)

// TrustedObservations is the versioned payload of a dedicated, operator-controlled
// ConfigMap's observations.json key. It contains no credentials. Only a trusted
// provisioning/management observer may write it; engines MUST NOT have write access.
// No such publisher is currently wired in the deployment. A missing input denies
// the pool. See observation_reader.md for the publisher's provenance obligations.
type TrustedObservations struct {
	Version int                  `json:"version"`
	Members []TrustedObservation `json:"members"`
}

// TrustedObservation binds every measurement to one persisted identity and running
// container. Digests must come from trusted effective snapshot storage, with Applied
// resolved using the acknowledged version, never copied from Target.
type TrustedObservation struct {
	ConnectionSession   uuid.UUID           `json:"connectionSession"`
	EngineID            uuid.UUID           `json:"engineID"`
	Namespace           string              `json:"namespace"`
	PodName             string              `json:"podName"`
	PodUID              string              `json:"podUID"`
	NodeUID             string              `json:"nodeUID"`
	ContainerID         string              `json:"containerID"`
	BackendIP           string              `json:"backendIP"`
	BindingObservedAt   time.Time           `json:"bindingObservedAt"`
	InventoryObservedAt time.Time           `json:"inventoryObservedAt"`
	PolicyGroupID       uuid.UUID           `json:"policyGroupID"`
	Revoked             *bool               `json:"revoked"`
	Deleted             *bool               `json:"deleted"`
	Management          EligibilityCheck    `json:"management"`
	DirectDNS           EligibilityCheck    `json:"directDNS"`
	Applied             EligibilitySnapshot `json:"applied"`
	Target              EligibilitySnapshot `json:"target"`
	SnapshotObservedAt  time.Time           `json:"snapshotObservedAt"`
}

// ObservationReader reads Kubernetes core/v1 through an authenticated HTTPS client.
// Configure the client's transport with the cluster CA and read-only credentials;
// no kubeconfig discovery, credential reading, exec, LIST, or writes are performed.
// The API server, namespace, and dedicated input name are trusted configuration.
// Calls return an explicitly empty pool on any IO/coherence error. They do not
// cache evidence, retry failures, mutate membership, or confer frontend ownership.
type ObservationReader struct {
	client           *http.Client
	base             string
	namespace, input string
	now              func() time.Time
}

// NewObservationReader pins the API origin and trusted input location, preserving
// the caller's transport while bounding reads and refusing HTTP redirects.
func NewObservationReader(client *http.Client, apiServer, namespace, input string) (*ObservationReader, error) {
	u, err := url.Parse(apiServer)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || !nameRE.MatchString(namespace) || len(namespace) > 63 || !canonicalNodeName(input) {
		return nil, fmt.Errorf("invalid trusted observation reader configuration")
	}
	if client == nil {
		return nil, fmt.Errorf("authenticated Kubernetes client is required")
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if c.Timeout <= 0 || c.Timeout > 10*time.Second {
		c.Timeout = 10 * time.Second
	}
	return &ObservationReader{client: &c, base: "https://" + u.Host, namespace: namespace, input: input, now: time.Now}, nil
}

type observationMetadata struct {
	Name              string     `json:"name"`
	Namespace         string     `json:"namespace"`
	UID               string     `json:"uid"`
	ResourceVersion   string     `json:"resourceVersion"`
	CreationTimestamp time.Time  `json:"creationTimestamp"`
	DeletionTimestamp *time.Time `json:"deletionTimestamp"`
}

type observationObject struct {
	APIVersion string              `json:"apiVersion"`
	Kind       string              `json:"kind"`
	Metadata   observationMetadata `json:"metadata"`
	Data       map[string]string   `json:"data"`
	Spec       struct {
		NodeName string `json:"nodeName"`
	} `json:"spec"`
	Status struct {
		Phase      string `json:"phase"`
		PodIP      string `json:"podIP"`
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
		ContainerStatuses []struct {
			Name        string `json:"name"`
			ContainerID string `json:"containerID"`
			Ready       bool   `json:"ready"`
			State       struct {
				Running *struct {
					StartedAt time.Time `json:"startedAt"`
				} `json:"running"`
			} `json:"state"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

func (r *ObservationReader) get(ctx context.Context, path, kind, namespace, name string) (observationObject, error) {
	var obj observationObject
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+path, nil)
	if err != nil {
		return obj, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return obj, fmt.Errorf("Kubernetes %s read failed", kind)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return obj, fmt.Errorf("Kubernetes %s read returned HTTP %d", kind, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return obj, fmt.Errorf("invalid Kubernetes %s response size", kind)
	}
	if err := json.Unmarshal(body, &obj); err != nil {
		return obj, fmt.Errorf("invalid Kubernetes %s JSON", kind)
	}
	m := obj.Metadata
	if obj.APIVersion != "v1" || obj.Kind != kind || m.Name != name || m.Namespace != namespace || !canonicalUID(m.UID) || m.ResourceVersion == "" || m.DeletionTimestamp != nil {
		return obj, fmt.Errorf("Kubernetes %s identity or lifecycle mismatch", kind)
	}
	return obj, nil
}

// Collect brackets both members' reads with a second read of every object. A
// resourceVersion change (including target/revocation updates) rejects the entire
// observation. This detects moving inputs, not an atomic multi-object transaction:
// callers still need bounded reevaluation and expiry before using a decision.
func (r *ObservationReader) Collect(ctx context.Context, g Group, maxAge time.Duration) ([]MemberEvidence, error) {
	start := r.now()
	fail := func(err error) ([]MemberEvidence, error) { return nil, err }
	if g.Validate() != nil || start.IsZero() || maxAge <= 0 {
		return fail(fmt.Errorf("invalid group or observation time budget"))
	}
	// Bound the whole read, not just individual requests.
	budget := maxAge
	if budget > 10*time.Second {
		budget = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	type read struct {
		path, kind, namespace, name string
		object                      observationObject
	}
	var reads []read
	fetch := func(path, kind, namespace, name string) (observationObject, error) {
		obj, err := r.get(ctx, path, kind, namespace, name)
		if err == nil {
			reads = append(reads, read{path, kind, namespace, name, obj})
		}
		return obj, err
	}
	prefix := "/api/v1/namespaces/" + r.namespace
	input, err := fetch(prefix+"/configmaps/"+r.input, "ConfigMap", r.namespace, r.input)
	if err != nil {
		return fail(err)
	}
	var doc TrustedObservations
	dec := json.NewDecoder(bytes.NewBufferString(input.Data["observations.json"]))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return fail(fmt.Errorf("invalid trusted observations document"))
	}
	var trailing any
	if dec.Decode(&trailing) != io.EOF || doc.Version != 1 || len(doc.Members) != 2 {
		return fail(fmt.Errorf("unsupported or ambiguous trusted observations document"))
	}
	evidence := make([]MemberEvidence, 0, 2)
	seen := map[uuid.UUID]bool{}
	seenBackends := map[string]bool{}
	for _, o := range doc.Members {
		if (o.EngineID != g.Members[0] && o.EngineID != g.Members[1]) || seen[o.EngineID] || o.Namespace != r.namespace || !canonicalNodeName(o.PodName) || !canonicalUID(o.PodUID) || !canonicalUID(o.NodeUID) || o.ContainerID == "" || o.Revoked == nil || o.Deleted == nil {
			return fail(fmt.Errorf("invalid trusted member binding"))
		}
		seen[o.EngineID] = true
		pod, err := fetch(prefix+"/pods/"+o.PodName, "Pod", r.namespace, o.PodName)
		if err != nil {
			return fail(err)
		}
		ip, ipErr := netip.ParseAddr(o.BackendIP)
		if pod.Metadata.UID != o.PodUID || !canonicalNodeName(pod.Spec.NodeName) || ipErr != nil || !ip.IsGlobalUnicast() || ip.Zone() != "" || ip.String() != o.BackendIP || pod.Status.PodIP != o.BackendIP || o.BackendIP == g.FrontendIP || seenBackends[o.BackendIP] {
			return fail(fmt.Errorf("pod incarnation, placement or backend differs from trusted binding"))
		}
		seenBackends[o.BackendIP] = true
		node, err := fetch("/api/v1/nodes/"+pod.Spec.NodeName, "Node", "", pod.Spec.NodeName)
		if err != nil {
			return fail(err)
		}
		if node.Metadata.UID != o.NodeUID {
			return fail(fmt.Errorf("node incarnation differs from trusted binding"))
		}
		ready, readyCount := false, 0
		for _, c := range pod.Status.Conditions {
			if c.Type == "Ready" {
				readyCount++
				ready = c.Status == "True"
			}
		}
		containers := 0
		var started time.Time
		for _, c := range pod.Status.ContainerStatuses {
			if c.Name == "engine" {
				containers++
				if c.ContainerID != o.ContainerID || c.State.Running == nil {
					return fail(fmt.Errorf("engine container incarnation differs from trusted binding"))
				}
				started = c.State.Running.StartedAt
				ready = ready && c.Ready
			}
		}
		if containers != 1 || started.IsZero() || pod.Metadata.CreationTimestamp.IsZero() || started.Before(pod.Metadata.CreationTimestamp) || started.After(start) {
			return fail(fmt.Errorf("invalid engine container lifecycle"))
		}
		// Measurements from a previous container in the same Pod are not reusable.
		for _, at := range []time.Time{o.BindingObservedAt, o.InventoryObservedAt, o.Management.ObservedAt, o.DirectDNS.ObservedAt, o.SnapshotObservedAt} {
			if at.Before(started) {
				return fail(fmt.Errorf("observation predates bound engine container"))
			}
		}
		evidence = append(evidence, MemberEvidence{
			ConnectionSession: o.ConnectionSession, ContainerID: o.ContainerID,
			EngineID: o.EngineID, PolicyGroupID: o.PolicyGroupID, PodUID: o.PodUID,
			Placement:  TrustedPlacement{EngineID: o.EngineID, PodUID: o.PodUID, NodeUID: node.Metadata.UID, NodeName: pod.Spec.NodeName, ObservedAt: o.BindingObservedAt},
			ObservedAt: o.InventoryObservedAt, Revoked: *o.Revoked, Deleted: *o.Deleted,
			Ready:      EligibilityCheck{OK: ready && readyCount == 1 && pod.Status.Phase == "Running", ObservedAt: start},
			Management: o.Management, DirectDNS: o.DirectDNS, Applied: o.Applied, Target: o.Target, SnapshotObservedAt: o.SnapshotObservedAt,
		})
	}
	// Reverse order makes the trusted input the final read, so a new target,
	// revoked binding or replaced input during either member read is discarded.
	for i := len(reads) - 1; i >= 0; i-- {
		before := reads[i]
		after, err := r.get(ctx, before.path, before.kind, before.namespace, before.name)
		if err != nil {
			return fail(err)
		}
		if !reflect.DeepEqual(before.object, after) {
			return fail(fmt.Errorf("Kubernetes %s changed during observation", before.kind))
		}
	}
	end := r.now()
	if ctx.Err() != nil {
		return fail(ctx.Err())
	}
	if end.Before(start) || !freshEligibility(start, end, maxAge) {
		return fail(fmt.Errorf("observation exceeded time budget or clock moved backwards"))
	}
	return evidence, nil
}

// Observe preserves the original read-only eligibility API. Publication uses
// Collect and independently checks the management database before persistence.
func (r *ObservationReader) Observe(ctx context.Context, g Group, maxAge time.Duration) (Eligibility, error) {
	evidence, err := r.Collect(ctx, g, maxAge)
	if err != nil {
		return EvaluateEligibility(g, nil, r.now(), maxAge), err
	}
	return EvaluateEligibility(g, evidence, r.now(), maxAge), nil
}
