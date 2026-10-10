package main

import (
	"errors"
	"strings"
	"testing"
)

func TestDiagnosticOverflowIsBoundedAndExplicit(t *testing.T) {
	exact := strings.Repeat("x", maxErrorBytes)
	if boundedErrorMessage(errors.New(exact)) != exact {
		t.Fatal("exact diagnostic changed")
	}
	got := boundedErrorMessage(errors.New(exact + "overflow"))
	if len(got) != maxErrorBytes || !strings.HasSuffix(got, " [diagnostic overflow]") {
		t.Fatalf("overflow was not bounded and explicit: bytes=%d", len(got))
	}
}
