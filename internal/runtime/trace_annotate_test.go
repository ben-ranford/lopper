package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestAnnotateRuntimeOnly(t *testing.T) {
	rep := report.Report{
		RepoPath: "/repo",
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

	trace := Trace{
		DependencyLoads:       map[string]int{"alpha": 2, "beta": 1},
		DependencyParents:     map[string]map[string]int{"alpha": map[string]int{"src/index.js": 2}},
		DependencyEntrypoints: map[string]map[string]int{"alpha": map[string]int{"src/main.js": 2}},
	}
	annotated := Annotate(rep, trace, AnnotateOptions{})

	if annotated.Dependencies[0].RuntimeUsage == nil || !annotated.Dependencies[0].RuntimeUsage.RuntimeOnly {
		t.Fatalf("expected alpha to be runtime-only annotated")
	}
	if annotated.Dependencies[0].RuntimeUsage.Correlation != report.RuntimeCorrelationRuntimeOnly {
		t.Fatalf("expected alpha runtime-only correlation, got %#v", annotated.Dependencies[0].RuntimeUsage)
	}
	if len(annotated.Dependencies[0].RuntimeUsage.Modules) != 0 {
		t.Fatalf("did not expect modules for alpha runtime usage")
	}
	if len(annotated.Dependencies[0].RuntimeUsage.ParentModules) != 1 || annotated.Dependencies[0].RuntimeUsage.ParentModules[0].Module != "src/index.js" {
		t.Fatalf("expected alpha parent module provenance, got %#v", annotated.Dependencies[0].RuntimeUsage.ParentModules)
	}
	if len(annotated.Dependencies[0].RuntimeUsage.Entrypoints) != 1 || annotated.Dependencies[0].RuntimeUsage.Entrypoints[0].Module != "src/main.js" {
		t.Fatalf("expected alpha entrypoint provenance, got %#v", annotated.Dependencies[0].RuntimeUsage.Entrypoints)
	}
	if annotated.Dependencies[1].RuntimeUsage == nil || annotated.Dependencies[1].RuntimeUsage.RuntimeOnly {
		t.Fatalf("expected beta to be runtime annotated but not runtime-only")
	}
	if annotated.Dependencies[1].RuntimeUsage.Correlation != report.RuntimeCorrelationOverlap {
		t.Fatalf("expected beta overlap correlation, got %#v", annotated.Dependencies[1].RuntimeUsage)
	}
}

func TestAnnotateRuntimeContextRedactsUnsafeVisiblePaths(t *testing.T) {
	repoPath := t.TempDir()
	repoParent := filepath.Join(repoPath, "src", "index.js")
	repoRootParent := filepath.Join(repoPath, "outside.js")
	repoReservedParent := filepath.Join(repoPath, "external:outside.js")
	repoEntrypoint := filepath.Join(repoPath, "cmd", "main.js")
	outsideParent := filepath.Join(t.TempDir(), "outside.js")
	outsideEntrypoint := filepath.Join(t.TempDir(), "launch.js")
	rep := report.Report{
		RepoPath: repoPath,
		Dependencies: []report.DependencyReport{
			{Name: "alpha"},
		},
	}

	trace := Trace{
		DependencyLoads: map[string]int{"alpha": 4},
		DependencyParents: map[string]map[string]int{
			"alpha": {
				fileURLPrefix + filepath.ToSlash(repoParent): 1,
				repoParent:                         2,
				repoRootParent:                     4,
				repoReservedParent:                 5,
				"../outside.js":                    1,
				outsideParent:                      1,
				`C:\Users\alice\project\secret.js`: 1,
				"file:///C:/Users/alice/project/secret.js": 2,
				`D:private\drive.js`:                       1,
				"node:fs":                                  1,
				"@scope/pkg":                               1,
				"literal:marker":                           1,
			},
		},
		DependencyEntrypoints: map[string]map[string]int{
			"alpha": {
				fileURLPrefix + filepath.ToSlash(repoEntrypoint): 3,
				outsideEntrypoint: 1,
				"file:///C:/Users/alice/project/win-launch.js": 1,
			},
		},
	}

	annotated := Annotate(rep, trace, AnnotateOptions{})
	usage := annotated.Dependencies[0].RuntimeUsage
	if usage == nil {
		t.Fatalf("expected runtime usage annotation")
	}
	assertRuntimeContextCounts(t, usage.ParentModules, map[string]int{
		"src/index.js":                3,
		"outside.js":                  4,
		"literal:external:outside.js": 5,
		"external:outside.js":         2,
		"external:secret.js":          3,
		"external:drive.js":           1,
		"node:fs":                     1,
		"@scope/pkg":                  1,
		"literal:literal:marker":      1,
	})
	assertRuntimeContextCounts(t, usage.Entrypoints, map[string]int{
		"cmd/main.js":            3,
		"external:launch.js":     1,
		"external:win-launch.js": 1,
	})
}

func TestAnnotateRuntimeContextPreservesIdentityThroughSymlinkedRepoRoot(t *testing.T) {
	tempRoot := t.TempDir()
	physicalRepo := filepath.Join(tempRoot, "physical")
	physicalParent := filepath.Join(physicalRepo, "src", "index.js")
	if err := os.MkdirAll(filepath.Dir(physicalParent), 0o750); err != nil {
		t.Fatalf("create physical repo: %v", err)
	}
	if err := os.WriteFile(physicalParent, nil, 0o600); err != nil {
		t.Fatalf("write physical parent module: %v", err)
	}
	linkedRepo := filepath.Join(tempRoot, "linked")
	if err := os.Symlink(physicalRepo, linkedRepo); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	rep := report.Report{
		RepoPath: linkedRepo,
		Dependencies: []report.DependencyReport{
			{Name: "alpha"},
		},
	}
	trace := Trace{
		DependencyLoads: map[string]int{"alpha": 3},
		DependencyParents: map[string]map[string]int{
			"alpha": {
				physicalParent: 1,
				filepath.Join(linkedRepo, "src", "index.js"): 2,
			},
		},
	}

	annotated := Annotate(rep, trace, AnnotateOptions{})
	usage := annotated.Dependencies[0].RuntimeUsage
	if usage == nil {
		t.Fatalf("expected runtime usage annotation")
	}
	assertRuntimeContextCounts(t, usage.ParentModules, map[string]int{"src/index.js": 3})
}

func TestAnnotateRuntimeContextEdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		value    string
		repoPath string
		want     string
	}{
		{name: "blank", value: "  "},
		{name: "empty file URL", value: "file://"},
		{name: "external root", value: "/"},
		{name: "non-file URL", value: "https://example.test/entry.js", want: "https://example.test/entry.js"},
		{name: "reserved URL prefix", value: "external://example.test/entry.js", want: "literal:external://example.test/entry.js"},
		{name: "relative path without repo", value: "src/index.js", want: "src/index.js"},
		{name: "relative repo root rejected", value: "/repo/main.js", repoPath: "repo", want: "external:main.js"},
		{name: "localhost Windows file URL", value: "file://localhost/C:/Users/alice/main.js", want: "external:main.js"},
		{name: "UNC file URL", value: "file://server/share/main.js", want: "external:main.js"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := annotateRuntimeParentModules(t, tt.value, tt.repoPath)
			if tt.want == "" {
				if len(got) != 0 {
					t.Fatalf("expected %q to be omitted, got %#v", tt.value, got)
				}
				return
			}
			if len(got) != 1 || got[0].Module != tt.want || got[0].Count != 1 {
				t.Fatalf("expected %q to render as %q, got %#v", tt.value, tt.want, got)
			}
		})
	}
}

func annotateRuntimeParentModules(t *testing.T, value, repoPath string) []report.RuntimeModuleUsage {
	t.Helper()
	rep := report.Report{
		RepoPath: repoPath,
		Dependencies: []report.DependencyReport{
			{Name: "alpha"},
		},
	}
	trace := Trace{
		DependencyLoads: map[string]int{"alpha": 1},
		DependencyParents: map[string]map[string]int{
			"alpha": {value: 1},
		},
	}
	usage := Annotate(rep, trace, AnnotateOptions{}).Dependencies[0].RuntimeUsage
	if usage == nil {
		t.Fatalf("expected runtime usage for %q", value)
	}
	return usage.ParentModules
}

func assertRuntimeContextCounts(t *testing.T, got []report.RuntimeModuleUsage, want map[string]int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("expected runtime context %#v, got %#v", want, got)
	}
	for _, item := range got {
		if count, ok := want[item.Module]; !ok || count != item.Count {
			t.Fatalf("expected runtime context %#v, got %#v", want, got)
		}
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

func TestAnnotatePythonRuntimeUsageWhenSupported(t *testing.T) {
	rep := report.Report{
		Dependencies: []report.DependencyReport{
			{
				Name:     "requests",
				Language: runtimeLanguagePython,
				UsedImports: []report.ImportUse{
					{Name: "get", Module: "requests"},
				},
			},
			{
				Name:     "requests",
				Language: runtimeLanguageJSTS,
				UsedImports: []report.ImportUse{
					{Name: "request", Module: "requests"},
				},
			},
		},
	}
	requestsKey := DependencyKey{Language: runtimeLanguagePython, Name: "requests"}
	httpxKey := DependencyKey{Language: runtimeLanguagePython, Name: "httpx"}
	trace := Trace{
		DependencyLoadsByLanguage: map[DependencyKey]int{
			requestsKey: 2,
			httpxKey:    1,
		},
		DependencyModulesByLanguage: map[DependencyKey]map[string]int{
			requestsKey: {"requests.sessions": 2},
			httpxKey:    {"httpx._client": 1},
		},
		DependencyParentsByLanguage: map[DependencyKey]map[string]int{
			requestsKey: {"app.py": 2},
		},
		DependencyEntrypointsByLanguage: map[DependencyKey]map[string]int{
			requestsKey: {"app.py": 2},
		},
		DependencySymbolsByLanguage: map[DependencyKey]map[string]int{
			requestsKey: {"requests.sessions\x00sessions": 2},
			httpxKey:    {"httpx._client\x00_client": 1},
		},
	}

	annotated := Annotate(rep, trace, AnnotateOptions{
		IncludeRuntimeOnlyRows: true,
		SupportedLanguages:     []string{runtimeLanguagePython},
	})

	if len(annotated.Dependencies) != 3 {
		t.Fatalf("expected python runtime-only row to be added, got %d dependencies", len(annotated.Dependencies))
	}
	dependencies := make(map[DependencyKey]report.DependencyReport, len(annotated.Dependencies))
	for _, dependency := range annotated.Dependencies {
		dependencies[DependencyKey{Language: dependency.Language, Name: dependency.Name}] = dependency
	}

	pythonRequests, ok := dependencies[requestsKey]
	if !ok {
		t.Fatalf("python requests dependency not found in %#v", annotated.Dependencies)
	}
	if pythonRequests.RuntimeUsage == nil {
		t.Fatalf("expected python requests runtime usage, got %#v", pythonRequests)
	}
	if pythonRequests.RuntimeUsage.Correlation != report.RuntimeCorrelationOverlap || pythonRequests.RuntimeUsage.LoadCount != 2 {
		t.Fatalf("expected python requests overlap with two loads, got %#v", pythonRequests.RuntimeUsage)
	}
	if len(pythonRequests.RuntimeUsage.Modules) != 1 || pythonRequests.RuntimeUsage.Modules[0].Module != "requests.sessions" {
		t.Fatalf("expected python runtime module context, got %#v", pythonRequests.RuntimeUsage.Modules)
	}

	jsRequests, ok := dependencies[DependencyKey{Language: runtimeLanguageJSTS, Name: "requests"}]
	if !ok {
		t.Fatalf("JS requests dependency not found in %#v", annotated.Dependencies)
	}
	if jsRequests.RuntimeUsage != nil {
		t.Fatalf("did not expect python runtime trace to annotate JS dependency, got %#v", jsRequests.RuntimeUsage)
	}
	httpx, ok := dependencies[httpxKey]
	if !ok {
		t.Fatalf("python httpx dependency not found in %#v", annotated.Dependencies)
	}
	if httpx.RuntimeUsage == nil || !httpx.RuntimeUsage.RuntimeOnly {
		t.Fatalf("expected python runtime-only httpx row, got %#v", httpx)
	}
}

func TestAnnotatePythonRuntimeUsesCanonicalStaticDependencyKeyWithoutDuplicateRuntimeOnlyRow(t *testing.T) {
	trace, err := loadTraceFromContent(t, `{"language":"python","dependency":"My__Package","module":"My__Package.client","parent":"/repo/app.py","entrypoint":"/repo/app.py"}`+"\n"+`{"language":"python","dependency":"my_.package","module":"my_.package.api","parent":"/repo/app.py","entrypoint":"/repo/app.py"}`+"\n")
	if err != nil {
		t.Fatalf(loadTraceErrFmt, err)
	}

	rep := report.Report{
		Dependencies: []report.DependencyReport{
			{
				Name:     "my-package",
				Language: runtimeLanguagePython,
				UsedImports: []report.ImportUse{
					{Name: "client", Module: "my_package"},
				},
			},
		},
	}

	annotated := Annotate(rep, trace, AnnotateOptions{
		IncludeRuntimeOnlyRows: true,
		SupportedLanguages:     []string{runtimeLanguagePython},
	})
	if len(annotated.Dependencies) != 1 {
		t.Fatalf("expected canonical static dependency to absorb runtime evidence without duplicate row, got %#v", annotated.Dependencies)
	}

	dependency := annotated.Dependencies[0]
	if dependency.Name != "my-package" || dependency.Language != runtimeLanguagePython {
		t.Fatalf("unexpected annotated dependency identity: %#v", dependency)
	}
	if dependency.RuntimeUsage == nil {
		t.Fatalf("expected runtime usage on canonical static dependency")
	}
	if dependency.RuntimeUsage.Correlation != report.RuntimeCorrelationOverlap || dependency.RuntimeUsage.LoadCount != 2 {
		t.Fatalf("expected overlap runtime usage with two loads, got %#v", dependency.RuntimeUsage)
	}
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
		DependencyParents: map[string]map[string]int{
			"chalk": {"src/index.js": 2},
		},
		DependencyEntrypoints: map[string]map[string]int{
			"chalk": {"src/main.js": 2},
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
	if len(chalk.RuntimeUsage.ParentModules) == 0 || chalk.RuntimeUsage.ParentModules[0].Module != "src/index.js" {
		t.Fatalf("expected runtime parent modules on runtime-only row, got %#v", chalk.RuntimeUsage.ParentModules)
	}
	if len(chalk.RuntimeUsage.Entrypoints) == 0 || chalk.RuntimeUsage.Entrypoints[0].Module != "src/main.js" {
		t.Fatalf("expected runtime entrypoints on runtime-only row, got %#v", chalk.RuntimeUsage.Entrypoints)
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
		DependencyParents: map[string]map[string]int{
			"new": {"src/index.js": 2},
		},
		DependencyEntrypoints: map[string]map[string]int{
			"new": {"src/main.js": 2},
		},
		DependencySymbols: map[string]map[string]int{
			"new": {"new/index.js\x00index": 2},
		},
	}

	seen := map[DependencyKey]struct{}{{Language: runtimeLanguageJSTS, Name: "seen"}: {}}
	supported := map[string]struct{}{runtimeLanguageJSTS: {}}
	appendRuntimeOnlyDependencies(&rep, trace, seen, supported)
	if len(rep.Dependencies) != 1 || rep.Dependencies[0].Name != "new" {
		t.Fatalf("expected only unseen runtime dependency to be appended, got %#v", rep.Dependencies)
	}
}

func TestRuntimeDependencyKeysSkipsZeroAndUnsupportedLanguages(t *testing.T) {
	trace := Trace{
		DependencyLoads: map[string]int{
			"lodash": 1,
			"zero":   0,
		},
		DependencyLoadsByLanguage: map[DependencyKey]int{
			{Language: runtimeLanguagePython, Name: "zero"}: 0,
			{Language: "ruby", Name: "rake"}:                1,
		},
	}
	supported := map[string]struct{}{runtimeLanguageJSTS: {}, runtimeLanguagePython: {}}

	keys := runtimeDependencyKeys(trace, supported)
	if len(keys) != 1 || keys[0].Language != runtimeLanguageJSTS || keys[0].Name != "lodash" {
		t.Fatalf("expected only supported nonzero JS key, got %#v", keys)
	}
	if got := runtimeLoadCount(Trace{}, DependencyKey{Language: runtimeLanguagePython, Name: "missing"}); got != 0 {
		t.Fatalf("expected missing non-JS runtime load count 0, got %d", got)
	}
}
