package thresholds

import (
	"slices"
	"strings"
	"testing"
)

func TestValuesValidateAdditionalBranches(t *testing.T) {
	values := Defaults()
	values.LicenseDenyList = []string{"gpl-3.0-only", "  ", "GPL-3.0-only", "Apache-2.0"}
	if err := values.Validate(); err != nil {
		t.Fatalf("validate values with deny list: %v", err)
	}
	if !slices.Equal(values.LicenseDenyList, []string{"APACHE-2.0", "GPL-3.0-ONLY"}) {
		t.Fatalf("unexpected deny list normalization: %#v", values.LicenseDenyList)
	}

	values = Defaults()
	values.LockfileDriftPolicy = "broken"
	err := values.Validate()
	if err == nil || !strings.Contains(err.Error(), "lockfile_drift_policy") {
		t.Fatalf("expected invalid lockfile drift policy error, got %v", err)
	}
}
