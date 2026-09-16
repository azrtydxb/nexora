package kwrollout

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
)

// enrollPairs adds only the availability-pair label to the observed anchor pods.
// Every patch has its own ownership check and server-side UID/resourceVersion
// preconditions. Neither a controller template nor a pod lifecycle is changed.
func (r *FleetReader) enrollPairs(ctx context.Context, snapshot FleetSnapshot, anchors []string, ownership func(context.Context) error) error {
	if len(snapshot.Pods) != 4 || !snapshot.LegacySelectors || len(anchors) != 2 || ownership == nil {
		return fmt.Errorf("enrolment requires four verified members and legacy selectors")
	}
	if err := validateFleetIsolation(snapshot.Pods); err != nil {
		return err
	}
	for _, pair := range KWPairTopology() {
		member := pair.Members[0]
		if !slices.Contains(anchors, member.Workload) {
			return fmt.Errorf("enrolment requires both known anchors")
		}
		index := slices.IndexFunc(snapshot.Pods, func(p ServingPod) bool { return p.Workload == member.Workload })
		if index < 0 {
			return fmt.Errorf("enrolment anchor is missing")
		}
		observed := snapshot.Pods[index]
		data, err := r.execute(ctx, nil, "get", "pod", observed.Name, "-o", "json")
		if err != nil {
			return err
		}
		var pod enginePod
		if err := json.Unmarshal(data, &pod); err != nil {
			return err
		}
		if pod.Metadata.UID != observed.UID || pod.Metadata.ResourceVersion != observed.ResourceVersion || pod.Metadata.DeletionTimestamp != nil || pod.Metadata.Labels == nil {
			return fmt.Errorf("anchor changed before enrolment; discard observation")
		}
		if label := pod.Metadata.Labels["nexora.io/failover-pair"]; label != "" && label != pair.Name {
			return fmt.Errorf("anchor is labelled for another pair")
		}
		if err := ownership(ctx); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		patch, err := json.Marshal([]map[string]string{
			{"op": "test", "path": "/metadata/uid", "value": observed.UID},
			{"op": "test", "path": "/metadata/resourceVersion", "value": observed.ResourceVersion},
			{"op": "add", "path": "/metadata/labels/nexora.io~1failover-pair", "value": pair.Name},
		})
		if err != nil {
			return err
		}
		data, err = r.execute(ctx, patch, "patch", "pod", observed.Name, "--type=json", "--patch-file=/dev/stdin", "-o", "json")
		if err != nil {
			return fmt.Errorf("anchor enrolment failed; retain lock and diagnose: %w", err)
		}
		var after enginePod
		if err := json.Unmarshal(data, &after); err != nil {
			return err
		}
		if after.Metadata.UID != observed.UID || after.Metadata.Labels["nexora.io/failover-pair"] != pair.Name || !reflect.DeepEqual(pod.Spec, after.Spec) || !reflect.DeepEqual(pod.Status, after.Status) {
			return fmt.Errorf("anchor changed beyond its pair label during enrolment")
		}
	}
	// Verify the exact candidate selector set before switching either Service.
	for _, pair := range KWPairTopology() {
		data, err := r.execute(ctx, nil, "get", "pods", "-l", "app.kubernetes.io/name=nexora-engine,app.kubernetes.io/instance=nexora,nexora.io/engine-group=default,nexora.io/failover-pair="+pair.Name, "-o", "json")
		if err != nil {
			return err
		}
		var pods struct{ Items []enginePod }
		if err := json.Unmarshal(data, &pods); err != nil {
			return err
		}
		if len(pods.Items) != 2 {
			return fmt.Errorf("pair selector does not select exactly two observed members")
		}
		seen := map[string]bool{}
		for _, pod := range pods.Items {
			index := slices.IndexFunc(snapshot.Pods, func(p ServingPod) bool { return p.UID == pod.Metadata.UID })
			if index < 0 || seen[pod.Metadata.UID] || pod.Metadata.DeletionTimestamp != nil {
				return fmt.Errorf("pair selector contains stale or duplicate pods")
			}
			p := snapshot.Pods[index]
			if (p.Workload != pair.Members[0].Workload && p.Workload != pair.Members[1].Workload) || pod.Metadata.Name != p.Name || pod.Spec.NodeName != p.Node || pod.Status.PodIP != p.IP {
				return fmt.Errorf("pair selector contains a foreign member")
			}
			ready := false
			for _, condition := range pod.Status.Conditions {
				ready = ready || condition.Type == "Ready" && condition.Status == "True"
			}
			if !ready {
				return fmt.Errorf("pair candidate is no longer Ready")
			}
			seen[pod.Metadata.UID] = true
		}
	}
	return ctx.Err()
}
