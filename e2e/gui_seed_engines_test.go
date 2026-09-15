package e2e

import (
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

func init() { registerGUISeed(seedEngines) }

// seedEngines starts the engines 32-engine-modal.spec.ts opens: spec 05 deletes gui-engine-2 and
// spec 20 revokes gui-engine before it runs. One query on gui-engine-3 fills its Queries tab.
func seedEngines(s guiSeedEnv) {
	e3 := s.Env.StartManagedEngine("gui-engine-3", []string{s.Mgmt.GRPCURL}, s.Admin.CreateJoinToken())
	s.Env.StartManagedEngine("gui-engine-4", []string{s.Mgmt.GRPCURL}, s.Admin.CreateJoinToken())
	v := s.Admin.LatestVersion()
	for _, n := range []string{"gui-engine-3", "gui-engine-4"} {
		s.Admin.WaitEngine(n, 15*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion == v })
	}
	harness.MustQuery(s.T, e3.DNS, harness.UniqueName("modal"), dns.TypeA, harness.QueryOpts{})
}
