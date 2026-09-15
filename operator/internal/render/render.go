package render

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	"helm.sh/helm/v3/pkg/releaseutil"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// FieldManager is the release service and server-side apply field owner of rendered objects.
const FieldManager = "nexora-operator"

// Chart is a loaded Nexora chart.
type Chart struct{ c *chart.Chart }

// Target is the cluster and release a chart renders for.
type Target struct {
	Name, Namespace string
	APIVersions     []string // "group/version" and "group/version/Kind" from discovery
	KubeVersion     string   // e.g. "v1.34.4"
}

// LoadChart loads the chart directory (or archive) at dir.
func LoadChart(dir string) (*Chart, error) {
	c, err := loader.Load(dir)
	if err != nil {
		return nil, fmt.Errorf("load chart %s: %w", dir, err)
	}
	return &Chart{c: c}, nil
}

// kindRank is the apply order; unlisted kinds go last.
var kindRank = map[string]int{
	"ConfigMap": 0, "Service": 1, "Cluster": 2, "ScheduledBackup": 3, "PodDisruptionBudget": 4,
	"Deployment": 5, "DaemonSet": 6, "Ingress": 7, "ServiceMonitor": 8, "PrometheusRule": 9,
}

func rank(kind string) int {
	if r, ok := kindRank[kind]; ok {
		return r
	}
	return len(kindRank)
}

// Render returns the chart's objects in apply order (see Architecture), namespaced to t.Namespace where unset.
func (c *Chart) Render(t Target, values map[string]any) ([]*unstructured.Unstructured, error) {
	caps := chartutil.DefaultCapabilities.Copy()
	caps.APIVersions = append(slices.Clone(caps.APIVersions), t.APIVersions...)
	if t.KubeVersion != "" {
		kv, err := chartutil.ParseKubeVersion(t.KubeVersion)
		if err != nil {
			return nil, fmt.Errorf("kube version %q: %w", t.KubeVersion, err)
		}
		caps.KubeVersion = *kv
	}
	vals, err := chartutil.ToRenderValues(c.c, values,
		chartutil.ReleaseOptions{Name: t.Name, Namespace: t.Namespace, Revision: 1, IsInstall: true}, caps)
	if err != nil {
		return nil, fmt.Errorf("chart values: %w", err)
	}
	vals["Release"].(map[string]any)["Service"] = FieldManager
	files, err := engine.Render(c.c, vals)
	if err != nil {
		return nil, fmt.Errorf("render chart: %w", err)
	}
	names := make([]string, 0, len(files))
	for name := range files {
		base := path.Base(name)
		if strings.HasPrefix(base, "_") || strings.HasSuffix(base, "NOTES.txt") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var objs []*unstructured.Unstructured
	for _, name := range names {
		docs := releaseutil.SplitManifests(files[name])
		keys := make([]string, 0, len(docs))
		for k := range docs {
			keys = append(keys, k)
		}
		// SplitManifests keys are "manifest-<index>".
		sort.Slice(keys, func(i, j int) bool { return manifestIndex(keys[i]) < manifestIndex(keys[j]) })
		for _, k := range keys {
			var m map[string]any
			// SplitManifests trims each document; Helm writes it back followed by a newline, which a
			// trailing block scalar (a ConfigMap's data) keeps.
			if err := yaml.Unmarshal([]byte(docs[k]+"\n"), &m); err != nil {
				return nil, fmt.Errorf("parse %s: %w", name, err)
			}
			if len(m) == 0 {
				continue
			}
			o := &unstructured.Unstructured{Object: m}
			if o.GetKind() == "" || o.GetName() == "" {
				return nil, fmt.Errorf("parse %s: document without kind or name", name)
			}
			if o.GetNamespace() == "" {
				o.SetNamespace(t.Namespace)
			}
			objs = append(objs, o)
		}
	}
	sort.SliceStable(objs, func(i, j int) bool {
		ri, rj := rank(objs[i].GetKind()), rank(objs[j].GetKind())
		if ri != rj {
			return ri < rj
		}
		return objs[i].GetName() < objs[j].GetName()
	})
	return objs, nil
}

func manifestIndex(key string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(key, "manifest-"))
	return n
}
