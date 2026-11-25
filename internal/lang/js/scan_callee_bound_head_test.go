package js

import "testing"

func TestDynamicImportCalleeNameRequiresSourceSpan(t *testing.T) {
	source := []byte("require(moduleName)")
	call := parseJSNodeByType(t, source, "call_expression")
	callee := call.ChildByFieldName("function")
	if callee == nil {
		t.Fatal("expected a callee")
	}

	if name := dynamicImportCalleeName(callee, source); name != "require" {
		t.Fatalf("expected the callee name from the source, got %q", name)
	}
	if name := dynamicImportCalleeName(callee, source[:1]); name != "" {
		t.Fatalf("expected a callee outside the supplied source slice to be ignored, got %q", name)
	}
}
