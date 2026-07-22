package prmetadata

import (
	"strings"
	"testing"
)

func TestParseRegressionMetadataAcceptsMultipleDeclarations(t *testing.T) {
	t.Parallel()

	lines := []string{
		"## Validation",
		"",
		"Regression-Test: ./pkg/alpha::TestOne",
		"Regression-Test: ./pkg/beta::TestTwo",
	}
	body := strings.Join(lines, "\n")

	metadata, err := ParseRegressionProof(body)
	if err != nil {
		t.Fatalf("ParseRegressionProof returned error: %v", err)
	}
	if metadata.ExemptionReason != "" {
		t.Fatalf("ExemptionReason = %q, want empty", metadata.ExemptionReason)
	}
	if len(metadata.Declarations) != 2 {
		t.Fatalf("len(Declarations) = %d, want 2", len(metadata.Declarations))
	}
}

func TestParseRegressionProofRejectsInvalidMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{name: "malformed declaration", body: "Regression-Test: ./pkg::testLower", wantErr: "invalid Regression-Test declaration"},
		{name: "traversal", body: "Regression-Test: ./pkg/../evil::TestTraversal", wantErr: "canonical"},
		{name: "injection characters", body: "Regression-Test: ./pkg;rm-rf::TestInjection", wantErr: "invalid Regression-Test declaration"},
		{name: "duplicate declaration", body: "Regression-Test: ./pkg::TestOne\nRegression-Test: ./pkg::TestOne", wantErr: "duplicate regression-test declaration"},
		{name: "repeated exemption", body: "Regression-Test-Exemption: first reason\nRegression-Test-Exemption: second reason", wantErr: "at most once"},
		{name: "blank exemption", body: "Regression-Test-Exemption:   ", wantErr: "non-empty reason"},
		{name: "declaration and exemption", body: "Regression-Test: ./pkg::TestOne\nRegression-Test-Exemption: not needed", wantErr: "cannot be combined"},
		{name: "backslash path", body: `Regression-Test: .\pkg::TestOne`, wantErr: "invalid Regression-Test declaration"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertRegressionProofError(t, tt.body, tt.wantErr)
		})
	}
}

func assertRegressionProofError(t *testing.T, body, wantErr string) {
	t.Helper()
	_, err := ParseRegressionProof(body)
	if err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("ParseRegressionProof error = %v, want %q", err, wantErr)
	}
}

func TestParseRegressionDeclarationDirectBranches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		wantErr string
	}{
		{name: "backslash", value: `.\\pkg::TestOne`, wantErr: "invalid Regression-Test declaration"},
		{name: "root path", value: `./::TestOne`, wantErr: "invalid Regression-Test declaration"},
		{name: "parent traversal", value: `./..::TestOne`, wantErr: "stay under the repository root"},
		{name: "non canonical", value: `./pkg/../nested::TestOne`, wantErr: "canonical"},
		{name: "valid", value: `./pkg/nested::TestOne`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertRegressionDeclaration(t, tt.value, tt.wantErr)
		})
	}
}

func assertRegressionDeclaration(t *testing.T, value, wantErr string) {
	t.Helper()
	declaration, err := parseRegressionDeclaration(value)
	if wantErr != "" {
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Fatalf("parseRegressionDeclaration error = %v, want %q", err, wantErr)
		}
		return
	}
	if err != nil {
		t.Fatalf("parseRegressionDeclaration returned error: %v", err)
	}
	if declaration.PackagePath != "./pkg/nested" || declaration.TestName != "TestOne" {
		t.Fatalf("declaration = %#v", declaration)
	}
}

func TestParseRegressionExemptionLabelIsStrict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value   string
		want    bool
		wantErr bool
	}{
		{value: "true", want: true},
		{value: "false"},
		{value: "TRUE", wantErr: true},
		{value: "1", wantErr: true},
		{value: " true ", wantErr: true},
		{value: "$(touch injected)", wantErr: true},
	}

	for _, tt := range tests {
		got, err := ParseRegressionExemptionLabel(tt.value)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("ParseRegressionExemptionLabel(%q) succeeded", tt.value)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseRegressionExemptionLabel(%q) returned error: %v", tt.value, err)
		}
		if got != tt.want {
			t.Fatalf("ParseRegressionExemptionLabel(%q) = %t, want %t", tt.value, got, tt.want)
		}
	}
}

func TestValidateRegressionRequirementsRejectsFixWithoutProof(t *testing.T) {
	t.Parallel()

	err := ValidateRegressionRequirements("fix(ci): tighten validation", "## Validation\n\nNo regression proof\n", false)
	if err == nil {
		t.Fatal("ValidateRegressionRequirements succeeded without fix proof metadata")
	}
	if !strings.Contains(err.Error(), "must declare at least one Regression-Test") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRegressionRequirementsRequiresReasonAndMaintainerLabel(t *testing.T) {
	t.Parallel()

	title := "fix(ci): tighten validation"
	reason := "Regression-Test-Exemption: external dependency outage has no deterministic local reproducer"

	tests := []struct {
		name     string
		body     string
		hasLabel bool
		wantErr  string
	}{
		{name: "reason only", body: reason, wantErr: "maintainer-controlled regression-exempt label"},
		{name: "label only", body: "## Validation\n\nNo regression proof\n", hasLabel: true, wantErr: "requires a non-empty Regression-Test-Exemption reason"},
		{name: "reason and label", body: reason, hasLabel: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRegressionRequirements(title, tt.body, tt.hasLabel)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateRegressionRequirements returned error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateRegressionRequirements error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateRegressionRequirementsAcceptsFixDeclaration(t *testing.T) {
	t.Parallel()

	err := ValidateRegressionRequirements("fix(ci): tighten validation", "Regression-Test: ./pkg::TestThing", false)
	if err != nil {
		t.Fatalf("ValidateRegressionRequirements returned error: %v", err)
	}
}

func TestValidateRegressionRequirementsAcceptsNonFixWithoutMetadata(t *testing.T) {
	t.Parallel()

	err := ValidateRegressionRequirements("feat(ci): add gate", "## Validation\n\nNo regression metadata\n", false)
	if err != nil {
		t.Fatalf("ValidateRegressionRequirements returned error: %v", err)
	}
}

func TestValidateRegressionRequirementsRejectsMetadataOnNonFixPR(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		"Regression-Test: ./pkg::TestThing",
		"Regression-Test-Exemption: no deterministic reproducer",
	} {
		err := ValidateRegressionRequirements("feat(ci): add gate", body, true)
		if err == nil {
			t.Fatalf("ValidateRegressionRequirements succeeded for non-fix metadata %q", body)
		}
		if !strings.Contains(err.Error(), "only allowed on conventional fix") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

func TestValidateRegressionRequirementsReturnsParseError(t *testing.T) {
	t.Parallel()

	err := ValidateRegressionRequirements("fix(ci): add gate", "Regression-Test: invalid", false)
	if err == nil {
		t.Fatal("ValidateRegressionRequirements succeeded for malformed metadata")
	}
	if !strings.Contains(err.Error(), "invalid Regression-Test declaration") {
		t.Fatalf("unexpected error: %v", err)
	}
}
