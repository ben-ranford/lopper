package js

import (
	"runtime"
	"strings"
	"testing"
)

func TestLargeNonIdentifierCalleeIsNotMaterialized(t *testing.T) {
	const calleeBytes = 1 << 20
	source := []byte("(" + strings.Repeat("x", calleeBytes) + ")();\n")
	call := parseJSNodeByType(t, source, "call_expression")

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if _, ok := parseUncertainDynamicImport(call, source, "index.js"); ok {
		t.Fatal("large non-identifier callee was treated as a dynamic import")
	}
	runtime.ReadMemStats(&after)
	copied := after.TotalAlloc - before.TotalAlloc
	if copied > calleeBytes/2 {
		t.Fatalf("large callee text was copied: %d bytes allocated", copied)
	}
}
