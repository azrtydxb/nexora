package e2e

import (
	"testing"

	"github.com/piwi3910/nexora/e2e/harness"
)

// guiSeedEnv is what TestGUICoverage hands each registered seed before the screen specs run.
type guiSeedEnv struct {
	T      *testing.T
	Env    *harness.Env
	Mgmt   *harness.Mgmt
	Admin  *harness.API
	Engine *harness.Engine
	Web    *harness.HTTPFixture
	DNS    string
	Vars   map[string]string
	AI     *harness.OpenAIFixture // the scripted fake model mgmt's AI points at
	PGURL  string                 // mgmt's database, for AI seeds that insert rows with harness.PGExec
}

var guiSeeds []func(guiSeedEnv)

// registerGUISeed adds a seed; seeds run in file-name order (Go runs init functions by file name).
func registerGUISeed(f func(guiSeedEnv)) { guiSeeds = append(guiSeeds, f) }

func runGUISeeds(s guiSeedEnv) {
	for _, f := range guiSeeds {
		f(s)
	}
}

func TestGUISeedRegistryRunsInOrder(t *testing.T) {
	saved := guiSeeds
	defer func() { guiSeeds = saved }()
	guiSeeds = nil
	var got []int
	registerGUISeed(func(guiSeedEnv) { got = append(got, 1) })
	registerGUISeed(func(guiSeedEnv) { got = append(got, 2) })
	runGUISeeds(guiSeedEnv{T: t, Vars: map[string]string{}})
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("seeds ran as %v", got)
	}
}
