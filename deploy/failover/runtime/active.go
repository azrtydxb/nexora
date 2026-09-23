package runtime

import (
	"errors"
	"path/filepath"
)

// ValidateActiveCode binds immutable root-owned code independently of caller
// UID. No setuid helper, file-capability escalation or unprivileged owner exists.
func ValidateActiveCode(c Config, manifest string) error {
	if c.Mode != "isolated-active-lab-v1" || c.Driver.UID != 0 || c.Stage.UID != 0 {
		return errors.New("root-only isolated active lab")
	}
	for _, p := range []string{c.Driver.DriverPath, c.Stage.Python} {
		if e := trusted(p, 0, true); e != nil {
			return e
		}
	}
	dir := filepath.Dir(c.Stage.Script)
	for _, p := range []string{c.Driver.ObjectPath, manifest, filepath.Join(dir, "active_probe.py"), filepath.Join(dir, "../../platform/active_lab.py")} {
		if e := trusted(filepath.Clean(p), 0, false); e != nil {
			return e
		}
	}
	return nil
}
