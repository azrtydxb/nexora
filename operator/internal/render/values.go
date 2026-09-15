// Package render turns a NexoraInstallation into the objects of the Nexora chart.
package render

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/piwi3910/nexora/operator/api/v1alpha1"
)

var (
	ErrImageTagRequired = errors.New("image tag required: set spec.image.tag or run a stamped operator")
	ErrForeignNamespace = errors.New("object outside the installation namespace")
)

// Injected is what the operator adds to the spec: the image tag and the Secrets it manages.
type Injected struct {
	Tag                                       string // operator version; "" or "dev" means none
	CASecret, KEKSecret, BootstrapTokenSecret string
	JoinTokenSecrets                          map[string]string // NexoraEngineGroup name -> join token Secret
}

// Values are the chart values of one installation.
type Values struct {
	Map           map[string]any
	PendingGroups []string // spec group names left out because their engine group has no Secret yet
}

// BuildValues maps the spec onto chart values: the spec's JSON is the values document, minus every
// engineGroupRef, plus the injected tag and Secrets. Groups whose engine group has no join token
// Secret are left out and reported in PendingGroups.
func BuildValues(spec v1alpha1.NexoraInstallationSpec, in Injected) (Values, error) {
	tag := spec.Image.Tag
	if tag == "" && in.Tag != "dev" {
		tag = in.Tag
	}
	if tag == "" {
		return Values{}, ErrImageTagRequired
	}
	m, err := toMap(spec)
	if err != nil {
		return Values{}, err
	}
	groups := []any{}
	var pending []string
	for _, g := range spec.Engine.Groups {
		ref := g.EngineGroupRef
		if ref == "" {
			ref = g.Name
		}
		secret, ok := in.JoinTokenSecrets[ref]
		if !ok || secret == "" {
			pending = append(pending, g.Name)
			continue
		}
		gm, err := toMap(g)
		if err != nil {
			return Values{}, err
		}
		delete(gm, "engineGroupRef")
		gm["joinTokenSecret"] = secret
		groups = append(groups, gm)
	}
	sub(m, "image")["tag"] = tag
	sub(m, "engine")["groups"] = groups
	mgmt := sub(m, "mgmt")
	sub(mgmt, "ca")["existingSecret"] = in.CASecret
	sub(mgmt, "kek")["existingSecret"] = in.KEKSecret
	sub(mgmt, "bootstrapToken")["existingSecret"] = in.BootstrapTokenSecret
	metrics := sub(m, "metrics")
	sub(metrics, "serviceMonitor")["namespace"] = ""
	sub(metrics, "prometheusRule")["namespace"] = ""
	return Values{Map: m, PendingGroups: pending}, nil
}

func toMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal values: %w", err)
	}
	out := map[string]any{}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("unmarshal values: %w", err)
	}
	pruneEmpty(out)
	return out, nil
}

// pruneEmpty removes empty maps, which unset struct fields marshal to despite omitempty. An empty
// map merges into the chart default unchanged, so removing it keeps the default.
func pruneEmpty(m map[string]any) {
	for k, v := range m {
		switch x := v.(type) {
		case map[string]any:
			pruneEmpty(x)
			if len(x) == 0 {
				delete(m, k)
			}
		case []any:
			for _, e := range x {
				if em, ok := e.(map[string]any); ok {
					pruneEmpty(em)
				}
			}
		}
	}
}

// sub returns m[key] as a map, creating it when absent.
func sub(m map[string]any, key string) map[string]any {
	if s, ok := m[key].(map[string]any); ok {
		return s
	}
	s := map[string]any{}
	m[key] = s
	return s
}
