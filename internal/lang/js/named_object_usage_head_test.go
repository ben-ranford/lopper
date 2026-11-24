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
