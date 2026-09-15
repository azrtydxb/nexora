//go:build tools

// Package tools pins modules the operator's packages import in later tasks.
package tools

import (
	_ "github.com/oapi-codegen/runtime"
	_ "helm.sh/helm/v3/pkg/chart/loader"
	_ "helm.sh/helm/v3/pkg/chartutil"
	_ "helm.sh/helm/v3/pkg/engine"
	_ "helm.sh/helm/v3/pkg/releaseutil"
	_ "sigs.k8s.io/controller-runtime/pkg/envtest"
)
