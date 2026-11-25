package js

import "testing"

func TestBoundedCalleeNameRequiresSourceSpan(t *testing.T) {
	source := []byte("require(moduleName)")
	call := parseJSNodeByType(t, source, "call_expression")
	callee := call.ChildByFieldName("function")
	if callee == nil {
		t.Fatal("expected a callee")
	}

	if name, ok := boundedCalleeName(callee, source); !ok || name != "require" {
		t.Fatalf("expected the callee name from the source, got %q, %t", name, ok)
	}
	if name, ok := boundedCalleeName(callee, source[:1]); ok || name != "" {
		t.Fatalf("expected a callee outside the supplied source slice to be ignored, got %q, %t", name, ok)
	}
}
