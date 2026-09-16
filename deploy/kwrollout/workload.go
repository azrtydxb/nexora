package kwrollout

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"path"
	"reflect"
	"strings"

	"github.com/google/uuid"
)

// WorkloadExpectation comes from the intended chart configuration, not discovery.
// Image is enforced only when RequireUpdated is true; frozen partners may still
// serve the previous release while their desired pod template has been updated.
type WorkloadExpectation struct {
	Member         Member
	StatePath      string
	Image          string
	RequireUpdated bool
}

// ServingPod binds a stable Kubernetes pod/container observation to its persisted
// public engine UUID. No certificate, private key or join token is read.
type ServingPod struct {
	Workload, Name, UID, ResourceVersion string
	Node, IP, Image, StatePath           string
	EngineID, EngineNodeName             string
}

type kubeOwner struct {
	Kind, Name, UID string
	Controller      bool
}

type kubeMetadata struct {
	Name, Namespace, UID, ResourceVersion string
	Generation                            int64
	DeletionTimestamp                     *string
	Labels                                map[string]string
	OwnerReferences                       []kubeOwner
}

type engineContainer struct {
	Name, Image string
	Env         []struct {
		Name, Value string
		ValueFrom   *struct {
			FieldRef *struct{ FieldPath string }
		}
	}
	VolumeMounts []struct {
		Name, MountPath, SubPath string
		ReadOnly                 bool
	}
}

type enginePodSpec struct {
	NodeName    string
	HostNetwork bool
	Containers  []engineContainer
	Volumes     []struct {
		Name     string
		HostPath *struct{ Path string }
	}
}

type enginePod struct {
	Kind     string
	Metadata kubeMetadata
	Spec     enginePodSpec
	Status   struct {
		Phase, PodIP      string
		Conditions        []struct{ Type, Status string }
		ContainerStatuses []struct {
			Name, ImageID, ContainerID string
			Ready                      bool
			RestartCount               int
			State                      struct{ Running *struct{ StartedAt string } }
		}
	}
}

type engineDaemonSet struct {
	Kind     string
	Metadata kubeMetadata
	Spec     struct {
		Selector struct{ MatchLabels map[string]string }
		Template struct{ Spec enginePodSpec }
	}
	Status struct {
		ObservedGeneration     int64
		DesiredNumberScheduled int
		CurrentNumberScheduled int
		NumberReady            int
		NumberAvailable        int
		NumberMisscheduled     int
		UpdatedNumberScheduled int
	}
}

// ReadServingPod is read-only: get controller/pods, read only the public UUID in
// the engine container, then re-read the pod to reject replacement/restart races.
// This is one workload gate, not a complete fleet or Service/EndpointSlice gate.
func ReadServingPod(ctx context.Context, kubeContext, namespace, release string, expected WorkloadExpectation) (ServingPod, error) {
	return readServingPod(ctx, kubectlCommand(kubeContext, namespace), namespace, release, expected)
}

func readServingPod(ctx context.Context, execute func(context.Context, []byte, ...string) ([]byte, error), namespace, release string, expected WorkloadExpectation) (ServingPod, error) {
	var result ServingPod
	instance := strings.TrimPrefix(expected.Member.Workload, release+"-engine-")
	if namespace == "" || release == "" || instance == "" || instance == expected.Member.Workload || strings.ContainsAny(instance, ",=/ ") || expected.Member.Node == "" || expected.StatePath == "/" || !path.IsAbs(expected.StatePath) || path.Clean(expected.StatePath) != expected.StatePath || (expected.RequireUpdated && expected.Image == "") {
		return result, fmt.Errorf("invalid workload expectation")
	}
	get := func(out any, args ...string) error {
		data, err := execute(ctx, nil, args...)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("invalid Kubernetes workload response: %w", err)
		}
		return nil
	}
	var ds engineDaemonSet
	if err := get(&ds, "get", "daemonset", expected.Member.Workload, "-o", "json"); err != nil {
		return result, err
	}
	selector := map[string]string{"app.kubernetes.io/name": "nexora-engine", "app.kubernetes.io/instance": release, "nexora.io/engine-group": "default", "nexora.io/engine-instance": instance}
	if ds.Kind != "DaemonSet" || ds.Metadata.Name != expected.Member.Workload || ds.Metadata.Namespace != namespace || ds.Metadata.UID == "" || ds.Metadata.DeletionTimestamp != nil || ds.Metadata.Generation <= 0 || !reflect.DeepEqual(ds.Spec.Selector.MatchLabels, selector) {
		return result, fmt.Errorf("unexpected or deleting workload controller")
	}
	s := ds.Status
	if s.ObservedGeneration < ds.Metadata.Generation || s.DesiredNumberScheduled != 1 || s.CurrentNumberScheduled != 1 || s.NumberReady != 1 || s.NumberAvailable != 1 || s.NumberMisscheduled != 0 || (expected.RequireUpdated && s.UpdatedNumberScheduled != 1) {
		return result, fmt.Errorf("workload %s has not converged to one available member", ds.Metadata.Name)
	}
	var pods struct{ Items []enginePod }
	if err := get(&pods, "get", "pods", "-l", "app.kubernetes.io/instance="+release+",nexora.io/engine-instance="+instance, "-o", "json"); err != nil {
		return result, err
	}
	if len(pods.Items) != 1 {
		return result, fmt.Errorf("workload %s must have exactly one stable pod, got %d", ds.Metadata.Name, len(pods.Items))
	}
	p := pods.Items[0]
	if p.Kind != "Pod" || p.Metadata.Namespace != namespace || p.Metadata.Name == "" || p.Metadata.UID == "" || p.Metadata.ResourceVersion == "" || p.Metadata.DeletionTimestamp != nil || p.Status.Phase != "Running" || p.Spec.NodeName != expected.Member.Node || p.Spec.HostNetwork {
		return result, fmt.Errorf("workload pod identity, placement or lifecycle differs from expectation")
	}
	owners := 0
	for _, owner := range p.Metadata.OwnerReferences {
		if owner.Controller {
			owners++
			if owner.Kind != "DaemonSet" || owner.Name != ds.Metadata.Name || owner.UID != ds.Metadata.UID {
				return result, fmt.Errorf("pod belongs to a different controller")
			}
		}
	}
	if owners != 1 {
		return result, fmt.Errorf("pod lacks one verified controlling owner")
	}
	for key, value := range selector {
		if p.Metadata.Labels[key] != value {
			return result, fmt.Errorf("pod does not match the controller selector")
		}
	}
	ip, err := netip.ParseAddr(p.Status.PodIP)
	if err != nil || ip.IsUnspecified() || ip.IsMulticast() || ip.Zone() != "" {
		return result, fmt.Errorf("pod lacks a valid DNS probe address")
	}
	ready := false
	for _, condition := range p.Status.Conditions {
		if condition.Type == "Ready" && condition.Status == "True" {
			ready = true
		}
	}
	statuses := 0
	for _, status := range p.Status.ContainerStatuses {
		if status.Name == "engine" {
			statuses++
			if !status.Ready || status.ImageID == "" || status.ContainerID == "" || status.State.Running == nil {
				return result, fmt.Errorf("engine container is not running and ready")
			}
		}
	}
	if !ready || statuses != 1 {
		return result, fmt.Errorf("pod readiness or engine container status is missing")
	}
	container, nodeName, err := verifyEngineSpec(p.Spec, expected)
	if err != nil {
		return result, err
	}
	if _, _, err := verifyEngineSpec(ds.Spec.Template.Spec, expected); err != nil {
		return result, fmt.Errorf("controller template: %w", err)
	}
	identity, err := execute(ctx, nil, "exec", p.Metadata.Name, "-c", "engine", "--", "head", "-c", "128", "/var/lib/nexora/identity/engine_id")
	if err != nil {
		return result, fmt.Errorf("read public engine identity: %w", err)
	}
	id, err := uuid.Parse(strings.TrimSpace(string(identity)))
	if err != nil || id == uuid.Nil {
		return result, fmt.Errorf("pod has no valid persisted engine UUID")
	}
	var after enginePod
	if err := get(&after, "get", "pod", p.Metadata.Name, "-o", "json"); err != nil {
		return result, err
	}
	if !reflect.DeepEqual(p, after) {
		return result, fmt.Errorf("pod changed while reading engine identity; discard observation")
	}
	return ServingPod{Workload: ds.Metadata.Name, Name: p.Metadata.Name, UID: p.Metadata.UID, ResourceVersion: p.Metadata.ResourceVersion, Node: p.Spec.NodeName, IP: ip.String(), Image: container.Image, StatePath: expected.StatePath, EngineID: id.String(), EngineNodeName: nodeName}, nil
}

func verifyEngineSpec(spec enginePodSpec, expected WorkloadExpectation) (engineContainer, string, error) {
	var engine engineContainer
	count, states := 0, 0
	for _, c := range spec.Containers {
		if c.Name == "engine" {
			engine = c
			count++
		}
	}
	for _, v := range spec.Volumes {
		if v.Name == "state" {
			states++
			if v.HostPath == nil || v.HostPath.Path != expected.StatePath {
				return engine, "", fmt.Errorf("engine state path differs from preserved configuration")
			}
		}
	}
	if spec.HostNetwork || count != 1 || states != 1 || engine.Image == "" || (expected.RequireUpdated && engine.Image != expected.Image) {
		return engine, "", fmt.Errorf("engine image, container or state volume is unexpected")
	}
	mounts := 0
	for _, mount := range engine.VolumeMounts {
		if mount.MountPath == "/var/lib/nexora" {
			mounts++
			if mount.Name != "state" || mount.SubPath != "" || mount.ReadOnly {
				return engine, "", fmt.Errorf("engine identity is not mounted from its preserved state volume")
			}
		}
	}
	var nodeName string
	nodeFields, engineNames := 0, 0
	for _, env := range engine.Env {
		switch env.Name {
		case "K8S_NODE_NAME":
			nodeFields++
			if env.ValueFrom == nil || env.ValueFrom.FieldRef == nil || env.ValueFrom.FieldRef.FieldPath != "spec.nodeName" {
				return engine, "", fmt.Errorf("node identity is not derived from the actual pod node")
			}
		case "NEXORA_ENGINE_NODE_NAME":
			engineNames++
			if env.ValueFrom != nil || strings.Count(env.Value, "$(K8S_NODE_NAME)") != 1 {
				return engine, "", fmt.Errorf("engine node-name expansion is unexpected")
			}
			nodeName = strings.ReplaceAll(env.Value, "$(K8S_NODE_NAME)", expected.Member.Node)
		}
	}
	if mounts != 1 || nodeFields != 1 || engineNames != 1 || strings.Contains(nodeName, "$(") {
		return engine, "", fmt.Errorf("engine identity mount or node-name inputs are ambiguous")
	}
	return engine, nodeName, nil
}
