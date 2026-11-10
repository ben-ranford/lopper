package prmetadata

import "testing"

// This regression uses the existing parser API so the test compiles on base.
func TestParseRegressionProofAcceptsDarwinPlatform(t *testing.T) {
	metadata, err := ParseRegressionProof("Regression-Test: ./pkg::TestDarwinBehavior [darwin]")
	if err != nil {
		t.Fatalf("supported Darwin platform declaration rejected: %v", err)
	}
	if len(metadata.Declarations) != 1 || metadata.Declarations[0].PackagePath != "./pkg" || metadata.Declarations[0].TestName != "TestDarwinBehavior" {
		t.Fatalf("declaration identity lost: %#v", metadata)
	}
}
