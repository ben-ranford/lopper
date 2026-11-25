package js

import "testing"

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
