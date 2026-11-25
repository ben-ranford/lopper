package js

import (
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestNamedImportStaticDestructureRequiresModuleScope(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		wantCount  int
		wantUnused int
	}{
		{
			name: "module alias",
			source: `import { config as settings } from "library";
let port;
({ port } = (settings));
void port;
`,
			wantCount: 1,
		},
		{
			name: "module require",
			source: `const { config } = require("library");
const { port, host } = config;
void [port, host];
`,
			wantCount: 1,
		},
		{
			name: "local parameter",
			source: `import { config } from "library";
function display(config) {
  const { title } = config;
  return title;
}
`,
			wantUnused: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			usage := collectDependencyImportUsageFromSource(t, "library", test.source)
			if got := usage.Counts["config"]; got != test.wantCount {
				t.Errorf("config usage count = %d, want %d", got, test.wantCount)
			}
			if got := len(usage.UnusedImports); got != test.wantUnused {
				t.Errorf("unused imports = %d, want %d", got, test.wantUnused)
			}
		})
	}
}

func TestNamedImportStaticDestructureKeepsDistinctDeclarationLocations(t *testing.T) {
	const source = `import { layout } from "catalog";
function preview() {
  const { layout } = require("local-settings");
  const { title } = layout;
  return title;
}
const { columns } = layout;
void columns;
`
	moduleUsage := collectDependencyImportUsageFromSource(t, "catalog", source)
	if moduleUsage.Counts["layout"] != 1 || len(moduleUsage.UnusedImports) != 0 {
		t.Fatalf("unexpected module import usage: %#v", moduleUsage)
	}
	localUsage := collectDependencyImportUsageFromSource(t, "local-settings", source)
	if localUsage.Counts["layout"] != 0 || len(localUsage.UnusedImports) != 1 {
		t.Fatalf("unexpected local binding usage: %#v", localUsage)
	}
}

func TestUniqueModuleNamedImportsLeavesDuplicateDeclarationsUnattributed(t *testing.T) {
	imports := []ImportBinding{
		{Kind: ImportNamed, LocalName: "layout", Location: report.Location{Line: 1}, moduleScope: true},
		{Kind: ImportNamed, LocalName: "layout", Location: report.Location{Line: 2}, moduleScope: true},
		{Kind: ImportNamed, LocalName: "theme", Location: report.Location{Line: 3}, moduleScope: true},
	}
	names := uniqueModuleNamedImports(imports)
	if got := names["layout"]; got.Line != 0 {
		t.Errorf("selected a duplicate declaration at line %d", got.Line)
	}
	if got := names["theme"]; got.Line != 3 {
		t.Errorf("theme declaration line = %d, want 3", got.Line)
	}
}
