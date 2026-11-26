package js

import "testing"

func TestCollectDependencyImportUsageExcludesNestedNamedObjectUsage(t *testing.T) {
	usage := collectDependencyImportUsageFromSource(t, "config-kit", `import { settings } from "config-kit"
function configure(settings) {
	settings.enabled
}
if (ready) {
	const settings = defaults()
	settings.enabled && run()
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

func TestCollectDependencyImportUsageCountsNamedObjectUseInModuleExpressions(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{name: "if condition", source: "if (settings.enabled) {}"},
		{name: "logical expression", source: "settings.enabled && run()"},
		{name: "conditional expression", source: "settings.enabled ? run() : skip()"},
		{name: "chained access", source: "settings.enabled.value"},
		{name: "template expression", source: "const label = `Enabled: ${settings.enabled}`"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			usage := collectDependencyImportUsageFromSource(t, "config-kit", "import { settings } from \"config-kit\"\n"+test.source)
			if _, ok := usage.UsedExports["settings"]; !ok {
				t.Fatalf("expected module object use to retain settings, got %#v", usage.UsedExports)
			}
			if got := usage.Counts["settings"]; got != 1 {
				t.Fatalf("expected module object usage count 1, got %d", got)
			}
		})
	}
}

func TestCollectDependencyImportUsageKeepsConditionalRequireBindingOutOfModuleScope(t *testing.T) {
	usage := collectDependencyImportUsageFromSource(t, "config-kit", `if (ready) var { settings } = require("config-kit")
settings.enabled
`)
	if len(usage.UnusedImports) != 1 || len(usage.UsedImports) != 0 {
		t.Fatalf("expected one unclaimed conditional binding, got %d unused and %d used", len(usage.UnusedImports), len(usage.UsedImports))
	}
}
