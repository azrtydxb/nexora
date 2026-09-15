package installation

import (
	"context"
	"net/http"

	"github.com/piwi3910/nexora/operator/internal/mgmtapi"
)

// HealthResult is what the management plane reports about itself.
type HealthResult struct{ Healthy, SetupRequired bool }

// Health checks a management plane with the operator token.
type Health interface {
	// Check returns an error when the management plane is unreachable or refuses the token (401).
	Check(ctx context.Context, managementURL, token string) (HealthResult, error)
}

// HTTPHealth checks /api/v1/health and /api/v1/setup over HTTP.
type HTTPHealth struct{ HC *http.Client }

// Check implements Health.
func (h HTTPHealth) Check(ctx context.Context, managementURL, token string) (HealthResult, error) {
	c, err := mgmtapi.New(managementURL, token, h.HC)
	if err != nil {
		return HealthResult{}, err
	}
	if err := c.Health(ctx); err != nil {
		return HealthResult{}, err
	}
	setup, err := c.SetupRequired(ctx)
	if err != nil {
		return HealthResult{}, err
	}
	return HealthResult{Healthy: true, SetupRequired: setup}, nil
}
