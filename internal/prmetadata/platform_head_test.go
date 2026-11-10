package prmetadata

import (
	"strings"
	"testing"
)

func TestRegressionPlatformMetadata(t *testing.T) {
	for _, platform := range []string{"linux", "windows", "darwin"} {
		body := "Regression-Test: ./pkg::TestBehavior [" + platform + "]"
		metadata, err := ParseRegressionProof(body)
		if err != nil || len(metadata.Declarations) != 1 {
			t.Fatalf("%s: %v", platform, err)
		}
		if got := metadata.Declarations[0]; got.TargetOS != platform || got.PackagePath != "./pkg" || got.TestName != "TestBehavior" {
			t.Fatalf("metadata: %#v", got)
		}
		if err := ValidateRegressionRequirements("fix(ci): platform proof", body, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, platform := range []string{"freebsd", "Darwin", "darwin/amd64", "darwin;echo", ""} {
		_, err := ParseRegressionProof("Regression-Test: ./pkg::TestBehavior [" + platform + "]")
		if err == nil {
			t.Fatalf("accepted %q", platform)
		}
		if platform != "" && !strings.Contains(err.Error(), "unsupported regression proof target") {
			t.Fatalf("unclear unsupported target: %v", err)
		}
	}
}

func TestRegressionPlatformRejectsDuplicateIdentity(t *testing.T) {
	for _, suffixes := range [][2]string{{"", " [darwin]"}, {" [linux]", " [darwin]"}, {" [darwin]", " [darwin]"}, {" [darwin]", ""}} {
		body := "Regression-Test: ./pkg::TestBehavior" + suffixes[0] + "\nRegression-Test: ./pkg::TestBehavior" + suffixes[1]
		_, err := ParseRegressionProof(body)
		if err == nil || !strings.Contains(err.Error(), "duplicate regression-test declaration") {
			t.Fatalf("conflicting body accepted: %q: %v", body, err)
		}
		if err := ValidateRegressionRequirements("fix(ci): prove native platform", body, false); err == nil || !strings.Contains(err.Error(), "duplicate regression-test declaration") {
			t.Fatalf("body validation accepted conflicts: %v", err)
		}
	}
}
