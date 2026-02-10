package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestLoadTrace(t *testing.T) {
	tmp := t.TempDir()
	tracePath := filepath.Join(tmp, "runtime.ndjson")
	content := []byte(
		`{"kind":"resolve","module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js"}` + "\n" +
			`{"kind":"require","module":"@scope/pkg/lib","resolved":"/repo/node_modules/@scope/pkg/lib/index.js"}` + "\n",
	)
	if err := os.WriteFile(tracePath, content, 0o644); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	trace, err := Load(tracePath)
	if err != nil {
		t.Fatalf("load trace: %v", err)
	}
	if trace.DependencyLoads["lodash"] != 1 {
		t.Fatalf("expected lodash load count=1, got %d", trace.DependencyLoads["lodash"])
	}
	if trace.DependencyLoads["@scope/pkg"] != 1 {
		t.Fatalf("expected @scope/pkg load count=1, got %d", trace.DependencyLoads["@scope/pkg"])
	}
}

func TestAnnotateRuntimeOnly(t *testing.T) {
	rep := report.Report{
		Dependencies: []report.DependencyReport{
			{Name: "alpha"},
			{
				Name: "beta",
				UsedImports: []report.ImportUse{
					{Name: "map", Module: "beta"},
				},
			},
		},
	}

	annotated := Annotate(rep, Trace{
		DependencyLoads: map[string]int{
			"alpha": 2,
			"beta":  1,
		},
	})

	if annotated.Dependencies[0].RuntimeUsage == nil || !annotated.Dependencies[0].RuntimeUsage.RuntimeOnly {
		t.Fatalf("expected alpha to be runtime-only annotated")
	}
	if annotated.Dependencies[1].RuntimeUsage == nil || annotated.Dependencies[1].RuntimeUsage.RuntimeOnly {
		t.Fatalf("expected beta to be runtime annotated but not runtime-only")
	}
}
