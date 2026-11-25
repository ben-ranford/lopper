package js

import "testing"

func TestCollectDependencyImportUsageExcludesNestedNamedObjectUsage(t *testing.T) {
	usage := collectDependencyImportUsageFromSource(t, "config-kit", `import { settings } from "config-kit"
function configure(settings) {
	settings.enabled
}
`)
	if _, ok := usage.UsedExports["settings"]; ok {
		t.Fatalf("expected nested binding to remain unused, got %#v", usage.UsedExports)
	}
}

func TestCollectDependencyImportUsageCountsComputedNamedObjectUse(t *testing.T) {
	usage := collectDependencyImportUsageFromSource(t, "config-kit", `import { settings } from "config-kit"
const property = "enabled"
settings[property]
`)
	if _, ok := usage.UsedExports["settings"]; !ok {
		t.Fatalf("expected computed object use to retain settings, got %#v", usage.UsedExports)
	}
	if got := usage.Counts["settings"]; got != 1 {
		t.Fatalf("expected computed object usage count 1, got %d", got)
	}
}
