package kwrollout

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"regexp"
)

const lockProtocol = "nexora-kw-rollout-v1"

// DeploymentLock is a non-expiring, release-scoped Kubernetes ConfigMap lock.
// Use one instance per invocation; its methods must not be called concurrently.
// Failed or interrupted deployments must not Release: remote mutations may still
// finish. Recovery requires operator diagnosis, not a timeout-based takeover.
type DeploymentLock struct {
	name, namespace, owner, uid string
	execute                     func(context.Context, []byte, ...string) ([]byte, error)
}

type lockObject struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name            string `json:"name"`
		Namespace       string `json:"namespace"`
		UID             string `json:"uid,omitempty"`
		ResourceVersion string `json:"resourceVersion,omitempty"`
	} `json:"metadata"`
	Data map[string]string `json:"data"`
}

// NewDeploymentLock creates a client, not a Kubernetes object. Acquire performs
// the first write. Explicit context and namespace prevent current-context drift.
func NewDeploymentLock(kubeContext, namespace, release string) (*DeploymentLock, error) {
	label := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	if kubeContext == "" || len(namespace) > 63 || !label.MatchString(namespace) || len(release) > 40 || !label.MatchString(release) {
		return nil, fmt.Errorf("explicit context and valid namespace/release names required")
	}
	l := &DeploymentLock{name: release + "-deploy-lock", namespace: namespace}
	l.execute = kubectlCommand(kubeContext, namespace)
	return l, nil
}

// Acquire creates the lock or atomically claims a previously released lock.
// An ambiguous command failure is not retried or treated as successful ownership.
func (l *DeploymentLock) Acquire(ctx context.Context) error {
	if l.uid != "" {
		return fmt.Errorf("lock already acquired by this invocation")
	}
	// A reused client must not resurrect an earlier ownership token.
	l.owner = rand.Text()
	obj, err := l.read(ctx)
	if err != nil {
		return err
	}
	var out []byte
	if obj == nil {
		obj = &lockObject{APIVersion: "v1", Kind: "ConfigMap", Data: map[string]string{"protocol": lockProtocol, "owner": l.owner}}
		obj.Metadata.Name, obj.Metadata.Namespace = l.name, l.namespace
		body, err := json.Marshal(obj)
		if err != nil {
			return err
		}
		out, err = l.execute(ctx, body, "create", "-f", "-", "-o", "json")
		if err != nil {
			return fmt.Errorf("create deployment lock (inspect before recovery): %w", err)
		}
	} else {
		if obj.Data["owner"] != "" {
			return fmt.Errorf("deployment lock is held; automatic takeover is forbidden")
		}
		out, err = l.changeOwner(ctx, obj, l.owner)
		if err != nil {
			return fmt.Errorf("claim deployment lock: %w", err)
		}
	}
	obj, err = l.decode(out)
	if err != nil {
		return err
	}
	if obj.Data["owner"] != l.owner {
		return fmt.Errorf("lock acquisition did not confirm ownership")
	}
	l.uid = obj.Metadata.UID
	return nil
}

// Check verifies that this invocation still owns the same Kubernetes object.
// Call before each mutation; this is not a server-side fence for Helm itself.
func (l *DeploymentLock) Check(ctx context.Context) error {
	_, err := l.owned(ctx)
	return err
}

// Release clears ownership with UID, resourceVersion and owner preconditions.
// It never deletes an object, so stale cleanup cannot delete a successor's lock.
func (l *DeploymentLock) Release(ctx context.Context) error {
	obj, err := l.owned(ctx)
	if err != nil {
		return err
	}
	out, err := l.changeOwner(ctx, obj, "")
	if err != nil {
		return fmt.Errorf("release deployment lock: %w", err)
	}
	updated, err := l.decode(out)
	if err != nil {
		return err
	}
	if updated.Metadata.UID != l.uid || updated.Data["owner"] != "" {
		return fmt.Errorf("lock release was not confirmed")
	}
	l.uid = ""
	return nil
}

func (l *DeploymentLock) owned(ctx context.Context) (*lockObject, error) {
	if l.uid == "" {
		return nil, fmt.Errorf("this invocation has not acquired the deployment lock")
	}
	obj, err := l.read(ctx)
	if err != nil {
		return nil, err
	}
	if obj == nil || obj.Metadata.UID != l.uid || obj.Data["owner"] != l.owner {
		return nil, fmt.Errorf("deployment lock ownership lost; halt all mutations")
	}
	return obj, nil
}

func (l *DeploymentLock) read(ctx context.Context) (*lockObject, error) {
	out, err := l.execute(ctx, nil, "get", "configmap", l.name, "--ignore-not-found", "-o", "json")
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, nil
	}
	return l.decode(out)
}

func (l *DeploymentLock) decode(out []byte) (*lockObject, error) {
	var obj lockObject
	if err := json.Unmarshal(out, &obj); err != nil {
		return nil, fmt.Errorf("invalid deployment lock response: %w", err)
	}
	_, hasOwner := obj.Data["owner"]
	if obj.APIVersion != "v1" || obj.Kind != "ConfigMap" || obj.Metadata.Name != l.name || obj.Metadata.Namespace != l.namespace || obj.Metadata.UID == "" || obj.Metadata.ResourceVersion == "" || obj.Data["protocol"] != lockProtocol || !hasOwner {
		return nil, fmt.Errorf("unrecognized deployment lock object; refusing to modify it")
	}
	return &obj, nil
}

func (l *DeploymentLock) changeOwner(ctx context.Context, obj *lockObject, owner string) ([]byte, error) {
	patch := []map[string]string{
		{"op": "test", "path": "/metadata/uid", "value": obj.Metadata.UID},
		{"op": "test", "path": "/metadata/resourceVersion", "value": obj.Metadata.ResourceVersion},
		{"op": "test", "path": "/data/protocol", "value": lockProtocol},
		{"op": "test", "path": "/data/owner", "value": obj.Data["owner"]},
		{"op": "replace", "path": "/data/owner", "value": owner},
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}
	return l.execute(ctx, body, "patch", "configmap", l.name, "--type=json", "--patch-file=/dev/stdin", "-o", "json")
}
