package powershell

import (
	"reflect"
	"strings"
	"testing"
)

func TestRequiredModulesRejectsExcessiveArrayDepth(t *testing.T) {
	nested := strings.Repeat("@(", 256) + "'DeepModule'" + strings.Repeat(")", 256)
	modules, warnings := parseRequiredModules([]byte("RequiredModules = @('Pester', "+nested+")"), "module.psd1")
	if !reflect.DeepEqual(modules, []string{"pester"}) {
		t.Fatalf("expected valid sibling module only, got %v", modules)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "module.psd1:1") || !strings.Contains(warnings[0], "array nesting exceeds limit of 32") {
		t.Fatalf("expected located depth warning, got %v", warnings)
	}
}

func TestModuleArrayDepthBoundary(t *testing.T) {
	for _, depth := range []int{1, 31, 32, 33} {
		expr := strings.Repeat("@(", depth) + "'Pester'" + strings.Repeat(")", depth)
		modules, warnings := parseModuleExpression(expr)
		module, dynamic, warning := parseModuleExpressionItem(expr)
		if depth <= 32 {
			if !reflect.DeepEqual(modules, []string{"Pester"}) || len(warnings) != 0 || module != "Pester" || dynamic || warning != "" {
				t.Fatalf("depth %d should be supported: %v %v / %q %t %q", depth, modules, warnings, module, dynamic, warning)
			}
		} else if len(modules) != 0 || len(warnings) != 1 || module != "" || dynamic || warning == "" {
			t.Fatalf("depth %d should be rejected: %v %v / %q %t %q", depth, modules, warnings, module, dynamic, warning)
		}
	}
}

func TestRequiredModulesPreservesNestedSiblingAtDepthLimit(t *testing.T) {
	deepEmpty := strings.Repeat("@(", 33) + strings.Repeat(")", 33)
	modules, warnings := parseRequiredModules([]byte("RequiredModules = @('Root', @('Nested', "+deepEmpty+"))"), "module.psd1")
	if !reflect.DeepEqual(modules, []string{"nested", "root"}) {
		t.Fatalf("valid nested sibling was discarded: %v", modules)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "array nesting exceeds limit of 32") {
		t.Fatalf("expected depth warning alongside retained modules, got %v", warnings)
	}
}
