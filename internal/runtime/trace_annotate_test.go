package runtime

import (
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

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

	annotated := Annotate(rep, Trace{DependencyLoads: map[string]int{"alpha": 2, "beta": 1}}, AnnotateOptions{})

	if annotated.Dependencies[0].RuntimeUsage == nil || !annotated.Dependencies[0].RuntimeUsage.RuntimeOnly {
		t.Fatalf("expected alpha to be runtime-only annotated")
	}
	if annotated.Dependencies[0].RuntimeUsage.Correlation != report.RuntimeCorrelationRuntimeOnly {
		t.Fatalf("expected alpha runtime-only correlation, got %#v", annotated.Dependencies[0].RuntimeUsage)
	}
	if len(annotated.Dependencies[0].RuntimeUsage.Modules) != 0 {
		t.Fatalf("did not expect modules for alpha runtime usage")
	}
	if annotated.Dependencies[1].RuntimeUsage == nil || annotated.Dependencies[1].RuntimeUsage.RuntimeOnly {
		t.Fatalf("expected beta to be runtime annotated but not runtime-only")
	}
	if annotated.Dependencies[1].RuntimeUsage.Correlation != report.RuntimeCorrelationOverlap {
		t.Fatalf("expected beta overlap correlation, got %#v", annotated.Dependencies[1].RuntimeUsage)
	}
}

func TestAnnotateStaticOnly(t *testing.T) {
	rep := report.Report{
		Dependencies: []report.DependencyReport{
			{
				Name: "alpha",
				UsedImports: []report.ImportUse{
					{Name: "map", Module: "alpha"},
				},
			},
		},
	}

	annotated := Annotate(rep, Trace{DependencyLoads: map[string]int{}}, AnnotateOptions{})
	if annotated.Dependencies[0].RuntimeUsage == nil {
		t.Fatalf("expected static-only runtime usage annotation")
	}
	if annotated.Dependencies[0].RuntimeUsage.Correlation != report.RuntimeCorrelationStaticOnly {
		t.Fatalf("expected static-only correlation, got %#v", annotated.Dependencies[0].RuntimeUsage)
	}
	if annotated.Dependencies[0].RuntimeUsage.LoadCount != 0 {
		t.Fatalf("expected zero load count for static-only annotation, got %d", annotated.Dependencies[0].RuntimeUsage.LoadCount)
	}
	if annotated.Dependencies[0].RuntimeUsage.RuntimeOnly {
		t.Fatalf("did not expect runtime-only=true for static-only annotation")
	}
}

func TestAnnotateSkipsUnsupportedLanguageAndZeroLoads(t *testing.T) {
	rep := report.Report{
		Dependencies: []report.DependencyReport{
			{Name: "java-dep", Language: "jvm"},
			{Name: "js-dep", Language: "js-ts"},
		},
	}

	annotated := Annotate(rep, Trace{DependencyLoads: map[string]int{"java-dep": 3}}, AnnotateOptions{})
	if annotated.Dependencies[0].RuntimeUsage != nil {
		t.Fatalf("did not expect runtime usage for unsupported language")
	}
	if annotated.Dependencies[1].RuntimeUsage == nil {
		return
	}
	t.Fatalf("did not expect runtime usage when js dependency has no static imports and no runtime loads")
}

func TestAnnotateAddsRuntimeOnlyDependencyRows(t *testing.T) {
	rep := report.Report{
		Dependencies: []report.DependencyReport{
			{Name: "lodash", Language: "js-ts"},
		},
	}
	trace := Trace{
		DependencyLoads: map[string]int{
			"lodash": 1,
			"chalk":  2,
		},
		DependencyModules: map[string]map[string]int{
			"chalk": {"chalk/index.js": 2},
		},
		DependencySymbols: map[string]map[string]int{
			"chalk": {"index": 2},
		},
	}

	annotated := Annotate(rep, trace, AnnotateOptions{IncludeRuntimeOnlyRows: true})
	if len(annotated.Dependencies) != 2 {
		t.Fatalf("expected runtime-only row to be added, got %d dependencies", len(annotated.Dependencies))
	}

	var chalk *report.DependencyReport
	for i := range annotated.Dependencies {
		if annotated.Dependencies[i].Name == "chalk" {
			chalk = &annotated.Dependencies[i]
			break
		}
	}
	if chalk == nil || chalk.RuntimeUsage == nil {
		t.Fatalf("expected runtime-only chalk dependency row")
	}
	if chalk.RuntimeUsage.Correlation != report.RuntimeCorrelationRuntimeOnly {
		t.Fatalf("expected runtime-only correlation, got %#v", chalk.RuntimeUsage)
	}
	if len(chalk.RuntimeUsage.Modules) == 0 || chalk.RuntimeUsage.Modules[0].Module != "chalk/index.js" {
		t.Fatalf("expected runtime modules on runtime-only row, got %#v", chalk.RuntimeUsage.Modules)
	}
}

func TestAppendRuntimeOnlyDependenciesSkipsSeenAndZeroLoads(t *testing.T) {
	rep := report.Report{}
	trace := Trace{
		DependencyLoads: map[string]int{
			"seen": 1,
			"zero": 0,
			"new":  2,
		},
		DependencyModules: map[string]map[string]int{
			"new": {"new/index.js": 2},
		},
		DependencySymbols: map[string]map[string]int{
			"new": {"new/index.js\x00index": 2},
		},
	}

	appendRuntimeOnlyDependencies(&rep, trace, map[string]struct{}{"seen": {}})
	if len(rep.Dependencies) != 1 || rep.Dependencies[0].Name != "new" {
		t.Fatalf("expected only unseen runtime dependency to be appended, got %#v", rep.Dependencies)
	}
}
