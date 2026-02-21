package golang

import (
	"context"
	"go/ast"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

const (
	errDetectFmt        = "detect: %v"
	depUUID             = "github.com/google/uuid"
	depLo               = "github.com/samber/lo"
	fileGoMod           = "go.mod"
	fileMainGo          = "main.go"
	fileGoWork          = "go.work"
	fileLargeGo         = "large.go"
	moduleDemo          = "example.com/demo"
	modulePrefix        = "module "
	moduleDemoLine      = "module example.com/demo"
	moduleOriginal      = "example.com/original"
	requirePrefix       = "require "
	versionV160         = " v1.6.0"
	go125Block          = "\n\ngo 1.25\n"
	errSymlinkFmt       = "symlink not supported: %v"
	importLoLine        = "import \"github.com/samber/lo\""
	packageMainLine     = "package main"
	exampleModuleA      = "example.com/a"
	exampleModuleX      = "example.com/x"
	goModDemo           = moduleDemoLine + go125Block
	goModDemoWithUUID   = moduleDemoLine + "\n\n" + requirePrefix + depUUID + versionV160 + "\n"
	mainNoopProgram     = packageMainLine + "\n\nfunc main() {}\n"
	mainUUIDNoopProgram = packageMainLine + "\n\nimport \"" + depUUID + "\"\n\nfunc main() { _ = uuid.NewString() }\n"
)

func TestAdapterDetectWithGoModAndSource(t *testing.T) {
	repo := t.TempDir()
	writeRepoGoMod(t, repo, goModDemoWithUUID)
	writeRepoMain(t, repo, mainUUIDNoopProgram)

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf(errDetectFmt, err)
	}
	if !detection.Matched {
		t.Fatalf("expected go detection to match")
	}
	if detection.Confidence <= 0 {
		t.Fatalf("expected confidence > 0, got %d", detection.Confidence)
	}
	if !slices.Contains(detection.Roots, repo) {
		t.Fatalf("expected root %q in %#v", repo, detection.Roots)
	}
}

func TestAdapterIdentityAndDetectWrapper(t *testing.T) {
	repo := t.TempDir()
	writeRepoGoMod(t, repo, goModDemo)

	adapter := NewAdapter()
	if adapter.ID() != "go" {
		t.Fatalf("expected adapter id go, got %q", adapter.ID())
	}
	if !slices.Equal(adapter.Aliases(), []string{"golang"}) {
		t.Fatalf("unexpected aliases %#v", adapter.Aliases())
	}

	ok, err := adapter.Detect(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect wrapper: %v", err)
	}
	if !ok {
		t.Fatalf("expected detect wrapper to match")
	}
}

func TestAdapterDetectWrapperError(t *testing.T) {
	adapter := NewAdapter()
	_, err := adapter.Detect(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatalf("expected detect wrapper error for missing repo path")
	}
}

func TestDetectWithConfidenceRepoIsFileErrors(t *testing.T) {
	repo := t.TempDir()
	filePath := writeTempFile(t, repo, "plain-file", "content")
	_, err := NewAdapter().DetectWithConfidence(context.Background(), filePath)
	if err == nil {
		t.Fatalf("expected detect error when repo path is a file")
	}
}

func TestApplyGoRootSignalsGoWorkError(t *testing.T) {
	repo := t.TempDir()
	writeRepoGoMod(t, repo, moduleDemoLine+"\n")
	mkdirGoWorkDir(t, repo)

	detection := language.Detection{}
	roots := map[string]struct{}{}
	if applyGoRootSignals(repo, &detection, roots) == nil {
		t.Fatalf("expected applyGoRootSignals error when go.work is unreadable")
	}
}

func TestAdapterAnalyseDependency(t *testing.T) {
	repo := t.TempDir()
	writeRepoGoMod(t, repo, goModDemoWithUUID)
	writeRepoMain(t, repo, packageMainLine+"\n\nimport (\n\t\"fmt\"\n\t\""+depUUID+"\"\n)\n\nfunc main() {\n\tfmt.Println(uuid.NewString())\n}\n")

	reportData := analyseReport(t, language.Request{
		RepoPath:   repo,
		Dependency: depUUID,
	})
	requireDependencyCount(t, reportData, 1)
	dep := reportData.Dependencies[0]
	if dep.Language != "go" {
		t.Fatalf("expected language go, got %q", dep.Language)
	}
	if dep.UsedExportsCount == 0 {
		t.Fatalf("expected used exports > 0")
	}
	if dep.Name != depUUID {
		t.Fatalf("expected dependency %s, got %q", depUUID, dep.Name)
	}
}

func TestAdapterAnalyseErrorPathsAndDefaultRequest(t *testing.T) {
	repo := t.TempDir()
	adapter := NewAdapter()

	// loadGoModuleInfo failure path via unreadable go.mod (directory)
	if err := os.Mkdir(filepath.Join(repo, fileGoMod), 0o755); err != nil {
		t.Fatalf("mkdir go.mod dir: %v", err)
	}
	_, err := adapter.Analyse(context.Background(), language.Request{RepoPath: repo})
	if err == nil {
		t.Fatalf("expected analyse error for unreadable go.mod")
	}

	// scanRepo failure path via canceled context
	repo2 := t.TempDir()
	writeRepoGoMod(t, repo2, goModDemo)
	writeRepoMain(t, repo2, packageMainLine+"\n\nfunc main(){}\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = adapter.Analyse(ctx, language.Request{RepoPath: repo2})
	if err == nil {
		t.Fatalf("expected analyse cancellation error")
	}

	// default request path (no dep/top) should still return warning payload
	reportData, err := adapter.Analyse(context.Background(), language.Request{RepoPath: repo2})
	if err != nil {
		t.Fatalf("analyse default request: %v", err)
	}
	warningsText := strings.ToLower(strings.Join(reportData.Warnings, "\n"))
	if !strings.Contains(warningsText, "no dependency or top-n target provided") {
		t.Fatalf("expected default-input warning, got %#v", reportData.Warnings)
	}
}

func TestAdapterAnalyseTopN(t *testing.T) {
	repo := t.TempDir()
	writeRepoGoMod(t, repo, moduleDemoLine+"\n\n"+requirePrefix+"(\n\t"+depUUID+versionV160+"\n\t"+depLo+" v1.47.0\n)\n")
	writeRepoMain(t, repo, packageMainLine+"\n\nimport (\n\tu \""+depUUID+"\"\n\t\""+depLo+"\"\n)\n\nfunc main() {\n\t_ = u.NewString()\n\t_ = lo.Contains([]int{1,2}, 2)\n}\n")

	reportData := analyseReport(t, language.Request{
		RepoPath: repo,
		TopN:     2,
	})
	requireDependencyCount(t, reportData, 2)
	names := []string{reportData.Dependencies[0].Name, reportData.Dependencies[1].Name}
	for _, dependency := range []string{depUUID, depLo} {
		if !slices.Contains(names, dependency) {
			t.Fatalf("expected dependency %q in %#v", dependency, names)
		}
	}
}

func TestParseImportsSkipsStdlibAndLocal(t *testing.T) {
	content := []byte(packageMainLine + "\n\nimport (\n\t\"fmt\"\n\t\"" + moduleDemo + "/internal/foo\"\n\t\"" + depUUID + "\"\n)\n")
	imports, _ := parseImports(content, fileMainGo, moduleInfo{
		ModulePath:           moduleDemo,
		LocalModulePaths:     []string{moduleDemo},
		DeclaredDependencies: []string{depUUID},
	})
	if len(imports) != 1 {
		t.Fatalf("expected one external dependency import, got %d", len(imports))
	}
	if imports[0].Dependency != depUUID {
		t.Fatalf("unexpected dependency %q", imports[0].Dependency)
	}
}

func TestAdapterDetectWithGoWorkRoots(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, fileGoWork), "go 1.25\n\nuse (\n\t./services/api\n)\n")
	writeFile(t, filepath.Join(repo, "services", "api", fileGoMod), "module example.com/api"+go125Block)
	writeFile(t, filepath.Join(repo, "services", "api", fileMainGo), mainNoopProgram)

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf(errDetectFmt, err)
	}
	expectedRoot := filepath.Join(repo, "services", "api")
	if !slices.Contains(detection.Roots, expectedRoot) {
		t.Fatalf("expected go.work root %q in %#v", expectedRoot, detection.Roots)
	}
}

func TestAdapterDetectWithNoSignals(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "README.md"), "no go files here\n")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf(errDetectFmt, err)
	}
	if detection.Matched {
		t.Fatalf("expected no match, got %#v", detection)
	}
	if detection.Confidence != 0 {
		t.Fatalf("expected zero confidence, got %d", detection.Confidence)
	}
}

func TestAdapterAnalyseUndeclaredDependencySignals(t *testing.T) {
	repo := t.TempDir()
	writeRepoGoMod(t, repo, goModDemo)
	writeRepoMain(t, repo, strings.Join([]string{
		packageMainLine,
		"",
		"import (",
		"\t_ \"github.com/lib/pq\"",
		"\t\"github.com/lib/pq\"",
		")",
		"",
		"func main() { _ = pq.Error{} }",
		"",
	}, "\n"))

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "github.com/lib/pq",
	})
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected one dependency report, got %d", len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]

	riskCodes := make([]string, 0, len(dep.RiskCues))
	for _, cue := range dep.RiskCues {
		riskCodes = append(riskCodes, cue.Code)
	}
	if !slices.Contains(riskCodes, "side-effect-import") {
		t.Fatalf("expected side-effect-import risk cue, got %#v", riskCodes)
	}
	if !slices.Contains(riskCodes, "undeclared-module-path") {
		t.Fatalf("expected undeclared-module-path risk cue, got %#v", riskCodes)
	}

	recCodes := make([]string, 0, len(dep.Recommendations))
	for _, rec := range dep.Recommendations {
		recCodes = append(recCodes, rec.Code)
	}
	if !slices.Contains(recCodes, "declare-go-module-requirement") {
		t.Fatalf("expected declare-go-module-requirement recommendation, got %#v", recCodes)
	}
}

func TestReplacementImportMapsToDependency(t *testing.T) {
	modulePath, dependencies, replacements := parseGoMod([]byte(strings.Join([]string{
		moduleDemoLine,
		"",
		requirePrefix + moduleOriginal + " v1.0.0",
		"replace " + moduleOriginal + " => github.com/fork/original v1.0.1",
		"",
	}, "\n")))
	if modulePath != moduleDemo {
		t.Fatalf("expected module path %s, got %q", moduleDemo, modulePath)
	}
	if len(dependencies) != 1 || dependencies[0] != moduleOriginal {
		t.Fatalf("unexpected dependencies: %#v", dependencies)
	}

	dependency := dependencyFromImport("github.com/fork/original/pkg", moduleInfo{
		ModulePath:           modulePath,
		LocalModulePaths:     []string{modulePath},
		DeclaredDependencies: dependencies,
		ReplacementImports:   replacements,
	})
	if dependency != moduleOriginal {
		t.Fatalf("expected replacement mapping to %s, got %q", moduleOriginal, dependency)
	}
}

func TestAdapterAnalyseSkipsGeneratedAndBuildTaggedFiles(t *testing.T) {
	repo := t.TempDir()
	writeRepoGoModLines(t, repo,
		moduleDemoLine,
		"",
		requirePrefix+"(",
		"\t"+depUUID+versionV160,
		"\t"+depLo+" v1.47.0",
		")",
		"",
	)

	writeRepoMainLines(t, repo,
		packageMainLine,
		"",
		"import \""+depUUID+"\"",
		"",
		"func main() { _ = uuid.NewString() }",
		"",
	)
	writeFile(t, filepath.Join(repo, "generated_lo.go"), strings.Join([]string{
		"// Code generated by mockgen. DO NOT EDIT.",
		packageMainLine,
		"",
		importLoLine,
		"",
		"var _ = lo.Contains([]int{1,2}, 2)",
		"",
	}, "\n"))
	writeFile(t, filepath.Join(repo, "tagged_lo.go"), strings.Join([]string{
		"//go:build never",
		packageMainLine,
		"",
		importLoLine,
		"",
		"var _ = lo.Contains([]int{1,2}, 2)",
		"",
	}, "\n"))

	reportData := analyseReport(t, language.Request{
		RepoPath: repo,
		TopN:     5,
	})
	names := dependencyNames(reportData.Dependencies)
	if !slices.Contains(names, depUUID) {
		t.Fatalf("expected uuid dependency in %#v", names)
	}
	if slices.Contains(names, depLo) {
		t.Fatalf("did not expect lo dependency from skipped files in %#v", names)
	}

	warningsText := strings.ToLower(strings.Join(reportData.Warnings, "\n"))
	if !strings.Contains(warningsText, "generated go file") {
		t.Fatalf("expected generated-file warning, got %#v", reportData.Warnings)
	}
	if !strings.Contains(warningsText, "build constraints") {
		t.Fatalf("expected build-constraint warning, got %#v", reportData.Warnings)
	}
}

func TestAdapterAnalyseSkipsNestedModulesFromRootScan(t *testing.T) {
	repo := t.TempDir()
	writeRepoGoModLines(t, repo,
		"module example.com/root",
		"",
		requirePrefix+depUUID+versionV160,
		"",
	)
	writeRepoMainLines(t, repo,
		packageMainLine,
		"",
		"import \""+depUUID+"\"",
		"",
		"func main() { _ = uuid.NewString() }",
		"",
	)

	writeFile(t, filepath.Join(repo, "services", "api", fileGoMod), strings.Join([]string{
		"module example.com/api",
		"",
		"require " + depLo + " v1.47.0",
		"",
	}, "\n"))
	writeFile(t, filepath.Join(repo, "services", "api", fileMainGo), strings.Join([]string{
		packageMainLine,
		"",
		importLoLine,
		"",
		"func main() { _ = lo.Contains([]int{1,2}, 2) }",
		"",
	}, "\n"))

	reportData := analyseReport(t, language.Request{
		RepoPath: repo,
		TopN:     10,
	})
	names := dependencyNames(reportData.Dependencies)
	if !slices.Contains(names, depUUID) {
		t.Fatalf("expected root dependency uuid in %#v", names)
	}
	if slices.Contains(names, depLo) {
		t.Fatalf("did not expect nested module dependency lo in root scan %#v", names)
	}

	warningsText := strings.ToLower(strings.Join(reportData.Warnings, "\n"))
	if !strings.Contains(warningsText, "nested module directories") {
		t.Fatalf("expected nested-module warning, got %#v", reportData.Warnings)
	}
}

func TestBuildRequestedGoDependenciesNoInputWarning(t *testing.T) {
	deps, warnings := buildRequestedGoDependencies(language.Request{}, scanResult{})
	if len(deps) != 0 {
		t.Fatalf("expected no deps, got %d", len(deps))
	}
	if len(warnings) == 0 || !strings.Contains(strings.ToLower(warnings[0]), "no dependency or top-n") {
		t.Fatalf("expected no-input warning, got %#v", warnings)
	}
}

func TestScanRepoInvalidRoot(t *testing.T) {
	_, err := scanRepo(context.Background(), "", moduleInfo{})
	if err == nil {
		t.Fatalf("expected invalid root error")
	}
}

func TestScanRepoCanceledContext(t *testing.T) {
	repo := t.TempDir()
	writeRepoGoMod(t, repo, goModDemo)
	writeRepoMain(t, repo, mainNoopProgram)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := scanRepo(ctx, repo, moduleInfo{})
	if err == nil {
		t.Fatalf("expected canceled context error")
	}
}

func TestScanRepoMissingPathError(t *testing.T) {
	_, err := scanRepo(context.Background(), filepath.Join(t.TempDir(), "missing"), moduleInfo{})
	if err == nil {
		t.Fatalf("expected scanRepo error for missing root path")
	}
}

func TestLoadGoModuleInfoNoGoMod(t *testing.T) {
	repo := t.TempDir()
	info, err := loadGoModuleInfo(repo)
	if err != nil {
		t.Fatalf("load module info: %v", err)
	}
	if info.ModulePath != "" {
		t.Fatalf("expected empty module path, got %q", info.ModulePath)
	}
	if len(info.DeclaredDependencies) != 0 {
		t.Fatalf("expected no deps, got %#v", info.DeclaredDependencies)
	}
}

func TestLoadGoModuleInfoOrchestrationHelpers(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, fileGoMod), strings.Join([]string{
		moduleDemoLine,
		"",
		requirePrefix + depUUID + versionV160,
		"",
		"go 1.25",
	}, "\n"))
	writeFile(t, filepath.Join(repo, fileGoWork), strings.Join([]string{
		"go 1.25",
		"",
		"use ./svc/a",
	}, "\n"))
	writeFile(t, filepath.Join(repo, "svc", "a", fileGoMod), modulePrefix+exampleModuleA+go125Block)
	writeFile(t, filepath.Join(repo, "nested", "x", fileGoMod), strings.Join([]string{
		modulePrefix + exampleModuleX,
		"",
		"require github.com/pkg/errors v0.9.1",
		"",
		"go 1.25",
	}, "\n"))

	got, err := loadGoModuleInfo(repo)
	if err != nil {
		t.Fatalf("loadGoModuleInfo: %v", err)
	}

	helperInfo := moduleInfo{ReplacementImports: make(map[string]string)}
	if err := loadRootModuleInfo(repo, &helperInfo); err != nil {
		t.Fatalf("loadRootModuleInfo: %v", err)
	}
	if err := loadWorkspaceModules(repo, &helperInfo); err != nil {
		t.Fatalf("loadWorkspaceModules: %v", err)
	}
	if err := loadNestedModules(repo, &helperInfo); err != nil {
		t.Fatalf("loadNestedModules: %v", err)
	}
	finalizeGoModuleInfo(&helperInfo)

	if got.ModulePath != helperInfo.ModulePath {
		t.Fatalf("module path mismatch: got %q want %q", got.ModulePath, helperInfo.ModulePath)
	}
	if !slices.Equal(got.LocalModulePaths, helperInfo.LocalModulePaths) {
		t.Fatalf("local modules mismatch: got %#v want %#v", got.LocalModulePaths, helperInfo.LocalModulePaths)
	}
	if !slices.Equal(got.DeclaredDependencies, helperInfo.DeclaredDependencies) {
		t.Fatalf("declared deps mismatch: got %#v want %#v", got.DeclaredDependencies, helperInfo.DeclaredDependencies)
	}
	if len(got.ReplacementImports) != len(helperInfo.ReplacementImports) {
		t.Fatalf("replacement count mismatch: got %#v want %#v", got.ReplacementImports, helperInfo.ReplacementImports)
	}
	for replacementImport, dependency := range helperInfo.ReplacementImports {
		if got.ReplacementImports[replacementImport] != dependency {
			t.Fatalf("replacement mismatch for %q: got %q want %q", replacementImport, got.ReplacementImports[replacementImport], dependency)
		}
	}
}

func TestLoadGoModuleInfoReadError(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, fileGoMod), 0o755); err != nil {
		t.Fatalf("mkdir go.mod dir: %v", err)
	}
	_, err := loadGoModuleInfo(repo)
	if err == nil {
		t.Fatalf("expected read error when go.mod is a directory")
	}
}

func TestLoadGoModuleInfoMissingPathError(t *testing.T) {
	_, err := loadGoModuleInfo(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatalf("expected loadGoModuleInfo error for missing path")
	}
}

func TestLoadGoWorkLocalModulesNoFile(t *testing.T) {
	repo := t.TempDir()
	mods, err := loadGoWorkLocalModules(repo)
	if err != nil {
		t.Fatalf("load go.work modules: %v", err)
	}
	if len(mods) != 0 {
		t.Fatalf("expected no modules, got %#v", mods)
	}
}

func TestLoadGoWorkLocalModulesReadError(t *testing.T) {
	repo := t.TempDir()
	mkdirGoWorkDir(t, repo)
	_, err := loadGoWorkLocalModules(repo)
	if err == nil {
		t.Fatalf("expected read error when go.work is a directory")
	}
}

func TestLoadGoWorkLocalModulesHappyPathAndInvalidEntries(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, fileGoWork), strings.Join([]string{
		"go 1.25",
		"",
		"use (",
		"\t./svc/a",
		"\t./svc/missing",
		")",
		"use ./svc/b",
		"",
	}, "\n"))
	writeFile(t, filepath.Join(repo, "svc", "a", fileGoMod), modulePrefix+exampleModuleA+go125Block)
	writeFile(t, filepath.Join(repo, "svc", "b", fileGoMod), modulePrefix+"example.com/b"+go125Block)

	mods, err := loadGoWorkLocalModules(repo)
	if err != nil {
		t.Fatalf("load go.work modules: %v", err)
	}
	if !slices.Contains(mods, exampleModuleA) || !slices.Contains(mods, "example.com/b") {
		t.Fatalf("expected workspace modules in %#v", mods)
	}
}

func TestParseImportsParseError(t *testing.T) {
	imports, metadata := parseImports([]byte(packageMainLine+"\nimport ("), "broken.go", moduleInfo{})
	if len(imports) != 0 || len(metadata) != 0 {
		t.Fatalf("expected no imports on parse failure, got %#v %#v", imports, metadata)
	}
}

func TestTrimImportPathNil(t *testing.T) {
	if got := trimImportPath(nil); got != "" {
		t.Fatalf("expected empty import path for nil, got %q", got)
	}
}

func TestApplyImportMetadataNilScanResult(t *testing.T) {
	applyImportMetadata([]importMetadata{
		{Dependency: exampleModuleA, IsBlank: true, Undeclared: true},
	}, nil)
}

func TestImportBindingIdentityBranches(t *testing.T) {
	name, local, wildcard := importBindingIdentity(depUUID, nil)
	if name != "uuid" || local != "uuid" || wildcard {
		t.Fatalf("unexpected default import identity %q %q %v", name, local, wildcard)
	}
	name, local, wildcard = importBindingIdentity("gopkg.in/yaml.v3", nil)
	if name != "yaml" || local != "yaml" || wildcard {
		t.Fatalf("unexpected default import identity for versioned module %q %q %v", name, local, wildcard)
	}
	name, local, wildcard = importBindingIdentity(depUUID, &ast.Ident{Name: "."})
	if name != "uuid" || local != "" || !wildcard {
		t.Fatalf("unexpected dot import identity %q %q %v", name, local, wildcard)
	}
	name, local, wildcard = importBindingIdentity(depUUID, &ast.Ident{Name: "_"})
	if name != "_" || local != "" || wildcard {
		t.Fatalf("unexpected blank import identity %q %q %v", name, local, wildcard)
	}
}

func TestTrimModuleVersionSuffix(t *testing.T) {
	prefix, ok := trimModuleVersionSuffix("yaml.v3")
	if !ok || prefix != "yaml" {
		t.Fatalf("expected yaml.v3 suffix trim to yaml, got %q (%v)", prefix, ok)
	}
	if _, ok := trimModuleVersionSuffix("yaml.v3beta"); ok {
		t.Fatalf("expected yaml.v3beta not to be treated as a version suffix")
	}
	if _, ok := trimModuleVersionSuffix("yaml"); ok {
		t.Fatalf("expected yaml with no suffix not to be trimmed")
	}
}

func TestBuildConstraintHelpers(t *testing.T) {
	t.Run("matching_and_extraction", testBuildConstraintMatchingAndExtraction)
	t.Run("comment_parsing_and_scan_stop", testBuildConstraintCommentAndScanStop)
	t.Run("tag_helpers", testBuildConstraintTagHelpers)
}

func testBuildConstraintMatchingAndExtraction(t *testing.T) {
	content := []byte(strings.Join([]string{
		"//go:build " + runtime.GOOS,
		packageMainLine,
		"",
	}, "\n"))
	if !matchesActiveBuild(content) {
		t.Fatalf("expected go:build for current GOOS to match")
	}

	plusBuildOnly := []byte(strings.Join([]string{
		"// +build " + runtime.GOOS,
		packageMainLine,
		"",
	}, "\n"))
	if !matchesActiveBuild(plusBuildOnly) {
		t.Fatalf("expected +build for current GOOS to match")
	}
	goBuild, plusBuild := extractBuildConstraintExpressions(plusBuildOnly)
	if goBuild != nil {
		t.Fatalf("expected nil go:build expr")
	}
	if len(plusBuild) == 0 {
		t.Fatalf("expected plus-build expr")
	}
}

func testBuildConstraintCommentAndScanStop(t *testing.T) {
	if expr, kind := parseBuildConstraintComment("//go:build ("); expr != nil || kind != "go" {
		t.Fatalf("unexpected malformed go:build parse result: %v %q", expr, kind)
	}
	if _, kind := parseBuildConstraintComment("// +build [invalid"); kind != "plus" {
		t.Fatalf("unexpected malformed +build parse kind: %q", kind)
	}
	if expr, kind := parseBuildConstraintComment("// random"); expr != nil || kind != "" {
		t.Fatalf("unexpected non-build comment parse result: %v %q", expr, kind)
	}
	if !shouldStopBuildConstraintScan(packageMainLine) {
		t.Fatalf("expected package line to stop scan")
	}
	if !shouldStopBuildConstraintScan("var x = 1") {
		t.Fatalf("expected non-comment line to stop scan")
	}
	if shouldStopBuildConstraintScan("// comment") {
		t.Fatalf("expected comment line not to stop scan")
	}
}

func testBuildConstraintTagHelpers(t *testing.T) {
	if isActiveBuildTag("definitely-not-a-real-tag") {
		t.Fatalf("expected unknown tag to be inactive")
	}
	if !isActiveBuildTag(runtime.GOARCH) {
		t.Fatalf("expected current GOARCH to be active")
	}
	if parseIntDefault("123", 0) != 123 {
		t.Fatalf("expected integer parse to work")
	}
	if parseIntDefault("abc", 9) != 9 {
		t.Fatalf("expected fallback for invalid integer")
	}
	if minInt(3, 8) != 3 || minInt(8, 3) != 3 {
		t.Fatalf("unexpected minInt")
	}
	if runtime.GOOS != "windows" && !isActiveBuildTag("unix") {
		t.Fatalf("expected unix tag active on non-windows")
	}
	if runtime.GOOS == "windows" && isActiveBuildTag("unix") {
		t.Fatalf("expected unix tag inactive on windows")
	}
	t.Setenv("CGO_ENABLED", "1")
	if !isActiveBuildTag("cgo") {
		t.Fatalf("expected cgo tag active when CGO_ENABLED=1")
	}
	if !isActiveBuildTag("go1.1") {
		t.Fatalf("expected go1.1 tag active")
	}
}

func TestBuildTagGoVersionHelper(t *testing.T) {
	if !isSupportedGoReleaseTag("go1.1") {
		t.Fatalf("expected old go1.x tag to be supported")
	}
	if minor, ok := goVersionMinor("devel go1.26-abc123"); !ok || minor < 1 {
		t.Fatalf("expected to parse devel runtime version, got %d %v", minor, ok)
	}
	if minor, ok := goVersionMinor("go1.25rc2"); !ok || minor != 25 {
		t.Fatalf("expected to parse rc runtime version, got %d %v", minor, ok)
	}
	if _, ok := goVersionMinor("invalid-version"); ok {
		t.Fatalf("expected invalid runtime version to fail parse")
	}
	if minor, ok := leadingInt("25rc2"); !ok || minor != 25 {
		t.Fatalf("expected leadingInt to parse numeric prefix, got %d %v", minor, ok)
	}
	if _, ok := leadingInt("rc2"); ok {
		t.Fatalf("expected leadingInt to reject non-numeric prefix")
	}
}

func TestGoModReplacementAndTokensEdgeCases(t *testing.T) {
	replaceSet := make(map[string]string)
	addGoModReplacement("invalid replacement line", replaceSet)
	addGoModReplacement("example.com/left => ../local", replaceSet)
	if len(replaceSet) != 0 {
		t.Fatalf("expected local replacement target to be skipped, got %#v", replaceSet)
	}

	addGoModDependency("", nil)
	addGoModReplacement("", nil)
	addGoModReplacement("=> github.com/new/path", replaceSet)
	addGoModReplacement("example.com/old =>", replaceSet)
	if firstToken("   ") != "" {
		t.Fatalf("expected empty token")
	}
	if !isLocalReplaceTarget("/absolute/path") {
		t.Fatalf("expected absolute path to be local replace target")
	}
	if !isLocalReplaceTarget("C:\\\\tmp\\\\x") {
		t.Fatalf("expected Windows-style path to be local replace target")
	}
	if isLocalReplaceTarget("github.com/user/repo") {
		t.Fatalf("expected import path not to be local replace target")
	}
}

func TestUseEntriesAndPathNormalization(t *testing.T) {
	entries := parseGoWorkUseEntries([]byte(strings.Join([]string{
		"use (",
		"  ./services/api",
		"  \"./services/api\"",
		")",
		"use ./services/worker",
		"",
	}, "\n")))
	if len(entries) < 2 {
		t.Fatalf("expected parsed entries, got %#v", entries)
	}
	if normalizeGoWorkPath("\"./x\"") != filepath.Clean("./x") {
		t.Fatalf("unexpected normalized go.work path")
	}
}

func TestNestedModuleDiscoveryAndSkipDir(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, fileGoMod), "module example.com/root\n\ngo 1.25\n")
	writeFile(t, filepath.Join(repo, "sub", fileGoMod), "module example.com/sub\n\nrequire "+depUUID+" v1.6.0\n")

	dirs, err := nestedModuleDirs(repo)
	if err != nil {
		t.Fatalf("nested module dirs: %v", err)
	}
	if _, ok := dirs[filepath.Join(repo, "sub")]; !ok {
		t.Fatalf("expected nested module dir to be discovered")
	}

	mods, deps, replacements, err := discoverNestedModules(repo)
	if err != nil {
		t.Fatalf("discover nested modules: %v", err)
	}
	if !slices.Contains(mods, "example.com/sub") {
		t.Fatalf("expected nested module path in %#v", mods)
	}
	if !slices.Contains(deps, depUUID) {
		t.Fatalf("expected nested dependency in %#v", deps)
	}
	if len(replacements) != 0 {
		t.Fatalf("expected no replacements, got %#v", replacements)
	}

	if !shouldSkipDir("vendor") || !shouldSkipDir("bin") || shouldSkipDir("src") {
		t.Fatalf("unexpected shouldSkipDir behavior")
	}

	if _, _, _, err = discoverNestedModules(filepath.Join(repo, "missing")); err == nil {
		t.Fatalf("expected discoverNestedModules error for missing repo")
	}
}

func TestGoRootAndDetectionHelpers(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, fileGoWork), "go 1.25\n\nuse ./svc/a\n")
	writeFile(t, filepath.Join(repo, "svc", "a", fileGoMod), "module "+exampleModuleA+"\n\ngo 1.25\n")

	roots := map[string]struct{}{}
	if err := addGoWorkRoots(repo, roots); err != nil {
		t.Fatalf("addGoWorkRoots: %v", err)
	}
	if _, ok := roots[filepath.Join(repo, "svc", "a")]; !ok {
		t.Fatalf("expected go.work root in %#v", roots)
	}

	repoEscape := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(repoEscape, fileGoWork), strings.Join([]string{
		"go 1.25",
		"",
		"use (",
		"\t./svc/a",
		"\t../outside",
		"\t" + outside,
		")",
		"",
	}, "\n"))
	writeFile(t, filepath.Join(repoEscape, "svc", "a", fileGoMod), modulePrefix+exampleModuleA+go125Block)
	escapeRoots := map[string]struct{}{}
	if err := addGoWorkRoots(repoEscape, escapeRoots); err != nil {
		t.Fatalf("addGoWorkRoots with escape entries: %v", err)
	}
	if _, ok := escapeRoots[filepath.Join(repoEscape, "svc", "a")]; !ok {
		t.Fatalf("expected in-repo go.work root in %#v", escapeRoots)
	}
	if _, ok := escapeRoots[outside]; ok {
		t.Fatalf("did not expect out-of-repo absolute root in %#v", escapeRoots)
	}
	if _, ok := escapeRoots[filepath.Clean(filepath.Join(repoEscape, "..", "outside"))]; ok {
		t.Fatalf("did not expect parent escape root in %#v", escapeRoots)
	}

	// no go.work should not error
	if err := addGoWorkRoots(t.TempDir(), map[string]struct{}{}); err != nil {
		t.Fatalf("unexpected addGoWorkRoots no-file error: %v", err)
	}

	// go.work as directory should error
	repoErr := t.TempDir()
	mkdirGoWorkDir(t, repoErr)
	if addGoWorkRoots(repoErr, map[string]struct{}{}) == nil {
		t.Fatalf("expected addGoWorkRoots read error")
	}

	update := language.Detection{}
	updateGoDetection(filepath.Join(repo, fileMainGo), mustDirEntry(t, writeTempFile(t, repo, fileMainGo, packageMainLine)), map[string]struct{}{}, &update)
	if !update.Matched {
		t.Fatalf("expected updateGoDetection to match")
	}

	// walk entry helpers
	visited := 0
	roots2 := map[string]struct{}{}
	detection := language.Detection{}
	if err := os.MkdirAll(filepath.Join(repo, "vendor"), 0o755); err != nil {
		t.Fatalf("mkdir vendor: %v", err)
	}
	if err := walkGoDetectionEntry(filepath.Join(repo, "vendor"), mustDirEntry(t, filepath.Join(repo, "vendor")), roots2, &detection, &visited, 5); err != filepath.SkipDir {
		t.Fatalf("expected skip dir from walk helper, got %v", err)
	}
	filePath := writeTempFile(t, repo, "tiny.go", packageMainLine)
	visited = 6
	if err := walkGoDetectionEntry(filePath, mustDirEntry(t, filePath), roots2, &detection, &visited, 5); err != fs.SkipAll {
		t.Fatalf("expected fs.SkipAll from max file bound, got %v", err)
	}
}

func TestBuildRecommendationsMatrix(t *testing.T) {
	none := buildRecommendations(reportDependency("dep", nil, nil), false)
	if len(none) != 0 {
		t.Fatalf("expected no recs, got %#v", none)
	}
	remove := buildRecommendations(reportDependency("dep", nil, []string{"x"}), false)
	if len(remove) == 0 {
		t.Fatalf("expected remove-unused recommendation")
	}
	wild := buildRecommendations(reportDependency("dep", []string{"*"}, nil), false)
	if len(wild) == 0 {
		t.Fatalf("expected dot-import recommendation")
	}
	decl := buildRecommendations(reportDependency("dep", nil, nil), true)
	if len(decl) == 0 {
		t.Fatalf("expected declare-go-module recommendation")
	}
}

func reportDependency(name string, used []string, unused []string) report.DependencyReport {
	dep := report.DependencyReport{Name: name}
	for _, item := range used {
		dep.UsedImports = append(dep.UsedImports, report.ImportUse{Name: item, Module: name})
	}
	for _, item := range unused {
		dep.UnusedImports = append(dep.UnusedImports, report.ImportUse{Name: item, Module: name})
	}
	return dep
}

func mustDirEntry(t *testing.T, path string) fs.DirEntry {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fs.FileInfoToDirEntry(info)
}

func writeTempFile(t *testing.T, root, name, content string) string {
	t.Helper()
	path := filepath.Join(root, name)
	writeFile(t, path, content)
	return path
}

func writeRepoGoMod(t *testing.T, repo, content string) {
	t.Helper()
	writeFile(t, filepath.Join(repo, fileGoMod), content)
}

func writeRepoMain(t *testing.T, repo, content string) {
	t.Helper()
	writeFile(t, filepath.Join(repo, fileMainGo), content)
}

func writeRepoGoModLines(t *testing.T, repo string, lines ...string) {
	t.Helper()
	writeRepoGoMod(t, repo, strings.Join(lines, "\n"))
}

func writeRepoMainLines(t *testing.T, repo string, lines ...string) {
	t.Helper()
	writeRepoMain(t, repo, strings.Join(lines, "\n"))
}

func mkdirGoWorkDir(t *testing.T, repo string) {
	t.Helper()
	if err := os.Mkdir(filepath.Join(repo, fileGoWork), 0o755); err != nil {
		t.Fatalf("mkdir go.work dir: %v", err)
	}
}

func analyseReport(t *testing.T, req language.Request) report.Report {
	t.Helper()
	reportData, err := NewAdapter().Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	return reportData
}

func requireDependencyCount(t *testing.T, reportData report.Report, expected int) {
	t.Helper()
	if len(reportData.Dependencies) != expected {
		t.Fatalf("expected %d dependency report(s), got %d", expected, len(reportData.Dependencies))
	}
}

func dependencyNames(dependencies []report.DependencyReport) []string {
	names := make([]string, 0, len(dependencies))
	for _, dep := range dependencies {
		names = append(names, dep.Name)
	}
	return names
}

func TestUtilityCoverageBranches(t *testing.T) {
	if stripInlineComment("value // comment") != "value " {
		t.Fatalf("expected inline comment to be stripped")
	}
	if normalizeDependencyID(" Example.COM/X ") != exampleModuleX {
		t.Fatalf("unexpected normalized dependency ID")
	}

	values := uniqueStrings([]string{"a", "a", " ", "b"})
	if !slices.Equal(values, []string{"a", "b"}) {
		t.Fatalf("unexpected unique values %#v", values)
	}

	if inferDependency(exampleModuleX+"/y/z") != exampleModuleX+"/y" {
		t.Fatalf("unexpected inferred dependency")
	}
	if inferDependency("stdlib/path") != "" {
		t.Fatalf("expected no inferred dep for non-external path")
	}

	if !looksExternalImport(exampleModuleA) || looksExternalImport("internal/a") {
		t.Fatalf("unexpected external import detection")
	}
	if !isLocalModuleImport("example.com/mod/x", []string{"example.com/mod"}) {
		t.Fatalf("expected local module import match")
	}
	if longestDeclaredDependency(exampleModuleA+"/x", []string{exampleModuleA, "example.com"}) != exampleModuleA {
		t.Fatalf("expected longest declared dependency")
	}
	if longestReplacementDependency(exampleModuleA+"/x", map[string]string{exampleModuleA: moduleOriginal}) != moduleOriginal {
		t.Fatalf("expected longest replacement dependency")
	}
	if longestReplacementDependency("example.com/none/x", map[string]string{exampleModuleA: moduleOriginal}) != "" {
		t.Fatalf("expected no replacement dependency match")
	}
	if parseIntDefault("", 7) != 7 {
		t.Fatalf("expected parseIntDefault empty fallback")
	}
}

func TestHandleScanDirAndWarningHelpers(t *testing.T) {
	repo := t.TempDir()
	child := filepath.Join(repo, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}
	vendor := filepath.Join(repo, "vendor")
	if err := os.MkdirAll(vendor, 0o755); err != nil {
		t.Fatalf("mkdir vendor: %v", err)
	}
	info, err := os.Stat(child)
	if err != nil {
		t.Fatalf("stat child: %v", err)
	}
	entry := fs.FileInfoToDirEntry(info)

	if err := handleScanDirEntry(repo, repo, entry, nil, nil); err != nil {
		t.Fatalf("expected nil scan dir err for repo root, got %v", err)
	}
	vendorInfo, err := os.Stat(vendor)
	if err != nil {
		t.Fatalf("stat vendor: %v", err)
	}
	vendorEntry := fs.FileInfoToDirEntry(vendorInfo)
	if err := handleScanDirEntry(vendor, repo, vendorEntry, nil, nil); err != filepath.SkipDir {
		t.Fatalf("expected vendor skip dir, got %v", err)
	}
	nested := map[string]struct{}{child: {}}
	result := newScanResult()
	if err := handleScanDirEntry(child, repo, entry, nested, &result); err != filepath.SkipDir {
		t.Fatalf("expected nested module skip, got %v", err)
	}
	if err := handleScanDirEntry(child, repo, entry, nested, nil); err != filepath.SkipDir {
		t.Fatalf("expected nested module skip with nil result, got %v", err)
	}

	appendScanWarnings(nil, moduleInfo{})
	appendSkipWarnings(nil)
	appendUndeclaredDependencyWarnings(nil)

	result.UndeclaredImportsByDependency[exampleModuleX] = 2
	appendUndeclaredDependencyWarnings(&result)
	if len(result.Warnings) == 0 {
		t.Fatalf("expected undeclared dependency warning")
	}
}

func TestDetectWithConfidenceCapAndDefaultRepoPath(t *testing.T) {
	repo := t.TempDir()
	writeRepoGoMod(t, repo, goModDemo)
	writeFile(t, filepath.Join(repo, fileGoWork), "go 1.25\n\nuse ./\n")
	for i := 0; i < 30; i++ {
		writeFile(t, filepath.Join(repo, "pkg", "f"+string(rune('a'+(i%26)))+".go"), "package pkg\n")
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), "")
	if err != nil {
		t.Fatalf("detect with empty repo path: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected detection to match")
	}
	if detection.Confidence > 95 {
		t.Fatalf("expected confidence clamp <=95, got %d", detection.Confidence)
	}
}

func TestScanGoSourceFileErrorAndSkipCounters(t *testing.T) {
	repo := t.TempDir()
	result := newScanResult()
	err := scanGoSourceFile(repo, filepath.Join(repo, "missing.go"), moduleInfo{}, &result)
	if err == nil {
		t.Fatalf("expected read error for missing go file")
	}

	large := strings.Repeat("a", maxScannableGoFile+1)
	writeFile(t, filepath.Join(repo, fileLargeGo), large)
	if err := scanGoSourceFile(repo, filepath.Join(repo, fileLargeGo), moduleInfo{}, &result); err != nil {
		t.Fatalf("scan large go file: %v", err)
	}
	if result.SkippedLargeFiles == 0 {
		t.Fatalf("expected skipped large files increment")
	}

	// nil result should be safe through skip paths
	if err := scanGoSourceFile(repo, filepath.Join(repo, fileLargeGo), moduleInfo{}, nil); err != nil {
		t.Fatalf("scan large go file with nil result: %v", err)
	}
}

func TestDiscoverNestedModulesContinueOnUnreadableGoMod(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, fileGoMod), "module example.com/root\n")
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.Mkdir(filepath.Join(repo, "sub", fileGoMod), 0o755); err != nil {
		t.Fatalf("mkdir sub/go.mod dir: %v", err)
	}

	mods, deps, replacements, err := discoverNestedModules(repo)
	if err != nil {
		t.Fatalf("discover nested modules: %v", err)
	}
	if len(mods) != 0 || len(deps) != 0 || len(replacements) != 0 {
		t.Fatalf("expected unreadable nested module to be skipped, got %#v %#v %#v", mods, deps, replacements)
	}
}

func TestMiscCoverageBranches(t *testing.T) {
	// nil state and empty line branch
	processGoModLine("", nil)

	if normalizeGoWorkPath("   ") != "" {
		t.Fatalf("expected empty normalized go.work path")
	}
	if isLocalModuleImport(exampleModuleX, []string{""}) {
		t.Fatalf("expected empty local module path to be ignored")
	}
	if dependencyFromImport("C", moduleInfo{}) != "" {
		t.Fatalf("expected cgo import to be ignored")
	}
	if _, warnings := buildDependencyReport("example.com/none", newScanResult()); len(warnings) == 0 {
		t.Fatalf("expected warning when no imports found")
	}
	if isSupportedGoReleaseTag("go1.999") {
		t.Fatalf("expected future go version tag to be unsupported")
	}
	if isSupportedGoReleaseTag("go1.bad") {
		t.Fatalf("expected malformed go version tag to be unsupported")
	}
	if matchesActiveBuild([]byte("// +build definitely_not_active\n" + packageMainLine + "\n")) {
		t.Fatalf("expected inactive plus-build expression to evaluate false")
	}

	applyImportMetadata([]importMetadata{{Dependency: ""}}, &scanResult{
		BlankImportsByDependency:      map[string]int{},
		UndeclaredImportsByDependency: map[string]int{},
	})
	addGoModDependency("   ", map[string]struct{}{})
	processGoModLine(moduleDemoLine, nil)
	processGoModLine("", &goModParseState{depSet: map[string]struct{}{}, replaceSet: map[string]string{}})
	if isLocalReplaceTarget("") {
		t.Fatalf("expected empty path not to be local replacement")
	}
}

func TestLoadGoModFromDirError(t *testing.T) {
	repo := t.TempDir()
	_, _, _, err := loadGoModFromDir(repo, filepath.Join(repo, "missing"))
	if err == nil {
		t.Fatalf("expected loadGoModFromDir error for missing file")
	}
}

func TestDetectWithConfidenceCanceledContext(t *testing.T) {
	repo := t.TempDir()
	writeRepoGoMod(t, repo, goModDemo)
	writeRepoMain(t, repo, mainNoopProgram)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := NewAdapter().DetectWithConfidence(ctx, repo)
	if err == nil {
		t.Fatalf("expected detect cancellation error")
	}
}

func TestSafeReadGuardsForGoModuleAndSource(t *testing.T) {
	repo := t.TempDir()
	outsideDir := t.TempDir()
	outsideGoMod := filepath.Join(outsideDir, "outside.mod")
	writeFile(t, outsideGoMod, "module example.com/outside\n")

	if err := os.Symlink(outsideGoMod, filepath.Join(repo, fileGoMod)); err != nil {
		t.Skipf(errSymlinkFmt, err)
	}
	if _, err := loadGoModuleInfo(repo); err == nil {
		t.Fatalf("expected guarded go.mod read to fail for escaping symlink")
	}

	outsideGoFile := filepath.Join(outsideDir, "outside.go")
	writeFile(t, outsideGoFile, packageMainLine+"\n")
	sourceRepo := t.TempDir()
	writeRepoGoMod(t, sourceRepo, goModDemo)
	sourceSymlink := filepath.Join(sourceRepo, "link.go")
	if err := os.Symlink(outsideGoFile, sourceSymlink); err != nil {
		t.Skipf(errSymlinkFmt, err)
	}
	result := newScanResult()
	if scanGoSourceFile(sourceRepo, sourceSymlink, moduleInfo{}, &result) == nil {
		t.Fatalf("expected guarded source read to fail for escaping symlink")
	}

	workRepo := t.TempDir()
	outsideGoWork := filepath.Join(outsideDir, "outside.work")
	writeFile(t, outsideGoWork, "go 1.25\n\nuse ./\n")
	if err := os.Symlink(outsideGoWork, filepath.Join(workRepo, fileGoWork)); err != nil {
		t.Skipf(errSymlinkFmt, err)
	}
	if _, err := readGoWorkUseEntries(workRepo); err == nil {
		t.Fatalf("expected guarded go.work read to fail for escaping symlink")
	}

	nestedRepo := t.TempDir()
	nestedDir := filepath.Join(nestedRepo, "sub")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
	}
	if err := os.Symlink(outsideGoMod, filepath.Join(nestedDir, fileGoMod)); err != nil {
		t.Skipf(errSymlinkFmt, err)
	}
	if _, _, _, err := loadGoModFromDir(nestedRepo, nestedDir); err == nil {
		t.Fatalf("expected guarded nested go.mod read to fail for escaping symlink")
	}
}

func TestParseGoModReplaceControlBranches(t *testing.T) {
	state := &goModParseState{}
	if parseGoModReplaceBlockControl("replace (", state) != true || !state.inReplaceBlock {
		t.Fatalf("expected start replace block")
	}
	if parseGoModReplaceBlockControl(")", state) != true || state.inReplaceBlock {
		t.Fatalf("expected close replace block")
	}
	if parseGoModReplaceBlockControl("not replace", state) {
		t.Fatalf("expected no replace block control")
	}
}

func TestProcessGoModLineInBlockBranches(t *testing.T) {
	state := &goModParseState{
		depSet:         map[string]struct{}{},
		replaceSet:     map[string]string{},
		inRequireBlock: true,
	}
	processGoModLine(depUUID+" v1.6.0", state)
	if _, ok := state.depSet[depUUID]; !ok {
		t.Fatalf("expected dependency from require block")
	}

	state.inRequireBlock = false
	state.inReplaceBlock = true
	processGoModLine(moduleOriginal+" => github.com/fork/original v1.0.0", state)
	if got := state.replaceSet["github.com/fork/original"]; got != moduleOriginal {
		t.Fatalf("expected replacement mapping from replace block, got %q", got)
	}
}

func TestLoadGoModuleInfoReplacementCollisionBranch(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, fileGoMod), strings.Join([]string{
		"module example.com/root",
		"",
		"replace " + moduleOriginal + " => github.com/shared/fork v1.0.0",
		"",
	}, "\n"))
	writeFile(t, filepath.Join(repo, "sub", fileGoMod), strings.Join([]string{
		"module example.com/sub",
		"",
		"replace example.com/other => github.com/shared/fork v1.1.0",
		"",
	}, "\n"))

	info, err := loadGoModuleInfo(repo)
	if err != nil {
		t.Fatalf("loadGoModuleInfo: %v", err)
	}
	// root replacement should win when nested module has same replacement import target
	if got := info.ReplacementImports["github.com/shared/fork"]; got != moduleOriginal {
		t.Fatalf("expected root replacement to win, got %q", got)
	}
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
