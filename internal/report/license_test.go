package report

import (
	"strings"
	"testing"
)

const reportTestGPL30OnlyLower = "GPL-3.0-only"

func TestNormalizeDependencyLicenses(t *testing.T) {
	deps := []DependencyReport{
		{Name: "a"},
		{Name: "b", License: &DependencyLicense{SPDX: "MIT", Source: "package.json"}},
	}
	NormalizeDependencyLicenses(deps)
	if deps[0].License == nil || !deps[0].License.Unknown || deps[0].License.Source != "unknown" {
		t.Fatalf("expected unknown placeholder license, got %#v", deps[0].License)
	}
	if deps[1].License == nil || deps[1].License.SPDX != "MIT" {
		t.Fatalf("expected existing license to remain unchanged, got %#v", deps[1].License)
	}
}

func TestApplyLicensePolicyAndCountDenied(t *testing.T) {
	deps := []DependencyReport{
		{Name: "a", License: &DependencyLicense{SPDX: "MIT OR Apache-2.0"}},
		{Name: "b", License: &DependencyLicense{SPDX: reportTestGPL30OnlyLower}},
		{Name: "c", License: &DependencyLicense{SPDX: "AGPL-3.0-only WITH GCC-exception-3.1"}},
	}

	ApplyLicensePolicy(deps, []string{reportTestGPL30OnlyLower, "agpl-3.0-only"})
	if deps[0].License.Denied {
		t.Fatalf("did not expect MIT/Apache to be denied")
	}
	if !deps[1].License.Denied || !deps[2].License.Denied {
		t.Fatalf("expected denied licenses to be flagged: %#v", deps)
	}
	if got := CountDeniedLicenses(deps); got != 2 {
		t.Fatalf("expected denied count 2, got %d", got)
	}
}

func TestApplyLicensePolicyClearsDeniedWhenNoDenyList(t *testing.T) {
	deps := []DependencyReport{
		{Name: "x", License: &DependencyLicense{SPDX: reportTestGPL30OnlyLower, Denied: true}},
		{Name: "y", License: nil},
	}
	ApplyLicensePolicy(deps, nil)
	if deps[0].License.Denied {
		t.Fatalf("expected denied flag to clear when denylist is empty")
	}
}

func TestNormalizeSPDXIDAndSortedDenyList(t *testing.T) {
	values := SortedDenyList([]string{" gpl-3.0-only ", "agpl-3.0-only", "GPL-3.0-only", "##"})
	if strings.Join(values, ",") != "AGPL-3.0-ONLY,GPL-3.0-ONLY" {
		t.Fatalf("unexpected sorted deny list: %#v", values)
	}
}

func TestSPDXExpressionContainsDeniedTokens(t *testing.T) {
	deny := map[string]struct{}{
		"GPL-2.0-ONLY": {},
	}
	if !spdxExpressionContainsDenied("(MIT OR GPL-2.0-only)", deny) {
		t.Fatalf("expected denied token match in expression")
	}
	if spdxExpressionContainsDenied("MIT OR Apache-2.0", deny) {
		t.Fatalf("did not expect denied token match")
	}
	if spdxExpressionContainsDenied("", nil) {
		t.Fatalf("did not expect denied match for empty expression/denylist")
	}
}
