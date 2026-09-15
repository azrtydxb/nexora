package api

import (
	"encoding/json"
	"testing"
)

// TestLicenseAcknowledgementOnlyForCategoryOperations catches apply adding acknowledge_license to an
// operation that does not declare it, or dropping it from the two that do.
func TestLicenseAcknowledgementOnlyForCategoryOperations(t *testing.T) {
	body := json.RawMessage(`{"revision":3}`)
	for op, want := range map[string]bool{"updateFilterCategory": true, "updatePolicyGroup": true, "updateGlobalSafeSearch": false,
		"updateResolverSettings": false, "updateAllowlist": false, "updateUpstream": false, "updateEngineGroup": false, "createPolicyGroup": true} {
		var m map[string]any
		if err := json.Unmarshal(withLicenseAcknowledged(op, body, true), &m); err != nil {
			t.Fatal(err)
		}
		if got := m["acknowledge_license"] == true; got != want || m["revision"] != float64(3) {
			t.Errorf("%s: body %v, want acknowledge_license %v", op, m, want)
		}
		if string(withLicenseAcknowledged(op, body, false)) != string(body) {
			t.Errorf("%s: changed without acknowledgement", op)
		}
	}
}
