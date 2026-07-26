package js

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	dynamicRequireToken = "require("
	bindingGypFile      = "binding.gyp"
	nodeBinaryFile      = "addon.node"
	packageJSONFile     = "package.json"
	scopedDependency    = "@scope/pkg"
	rootPackageMkdirErr = "mkdir root package root: %v"
)

func TestRiskHelperFunctions(t *testing.T) {
	if dependencyPath(scopedDependency) != filepath.Join("@scope", "pkg") {
		t.Fatalf("expected scoped dependency path")
	}
	if dependencyPath("lodash") != "lodash" {
		t.Fatalf("expected unscoped dependency path")
	}

	if !hasDynamicCall("const x = require(dep)", dynamicRequireToken) {
		t.Fatalf("expected dynamic require call detection")
	}
	if hasDynamicCall("const x = myrequire(dep)", dynamicRequireToken) {
		t.Fatalf("did not expect identifier-prefixed token to count as dynamic call")
	}
	if hasDynamicCall("const x = require('fixed')", dynamicRequireToken) {
		t.Fatalf("did not expect static require to be detected as dynamic")
	}
	if hasDynamicCall("// require(dep)", dynamicRequireToken) {
		t.Fatalf("did not expect commented token to be detected")
	}
	if !hasDynamicCall(`const url = "http://example.com//noop"; require(dep)`, dynamicRequireToken) {
		t.Fatalf("expected dynamic require after // inside string literal to be detected")
	}
	if !isCommented("abc // trailing") || isCommented("abc") {
		t.Fatalf("unexpected commented-line detection")
	}
	if isCommented(`const url = "http://example.com//noop";`) {
		t.Fatalf("did not expect // inside string literal to count as comment")
	}
	if firstNonSpaceByte("  \t\rX") != 'X' {
		t.Fatalf("expected first non-space byte detection")
	}
	if !isIdentifierByte('a') || isIdentifierByte('-') {
		t.Fatalf("unexpected identifier byte detection")
	}

	values := dedupeStrings([]string{"b", "a", "a"})
	if strings.Join(values, ",") != "a,b" {
		t.Fatalf("unexpected dedupe/sort result: %#v", values)
	}
}

func TestDetectNodeBinaryAndBindingGyp(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, nodeBinaryFile), []byte("bin"), 0o600); err != nil {
		t.Fatalf("write node binary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, bindingGypFile), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write binding.gyp: %v", err)
	}

	binary, err := detectNodeBinary(context.Background(), root)
	if err != nil {
		t.Fatalf("detect node binary: %v", err)
	}
	if binary != nodeBinaryFile {
		t.Fatalf("expected %s detection, got %q", nodeBinaryFile, binary)
	}
	binding, err := detectBindingGyp(root)
	if err != nil {
		t.Fatalf("detect binding.gyp: %v", err)
	}
	if len(binding) != 1 || binding[0] != bindingGypFile {
		t.Fatalf("unexpected binding.gyp detection: %#v", binding)
	}
}

func TestDetectNodeBinaryWithinRootPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	openCalls := 0
	root := &fakeJSRoot{
		open: func(string) (safeio.File, error) {
			openCalls++
			return nil, errors.New("unexpected open")
		},
	}
	if _, err := detectNodeBinaryWithinRoot(ctx, root, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled native walk to return context.Canceled, got %v", err)
	}
	if openCalls != 0 {
		t.Fatalf("expected canceled native walk not to open the root, got %d calls", openCalls)
	}
}

func TestAssessRiskCueWarningBranches(t *testing.T) {
	repo := t.TempDir()
	cues, warnings := assessRiskCues(context.Background(), repo, "", "", ExportSurface{})
	if len(cues) != 0 {
		t.Fatalf("expected no cues for invalid dependency root, got %#v", cues)
	}
	if len(warnings) == 0 {
		t.Fatalf("expected warning for invalid dependency root")
	}

	depRoot := filepath.Join(repo, "node_modules", "pkg")
	if err := os.MkdirAll(depRoot, 0o755); err != nil {
		t.Fatalf("mkdir dep root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, packageJSONFile), []byte("{"), 0o600); err != nil {
		t.Fatalf("write invalid package.json: %v", err)
	}
	_, warnings = assessRiskCues(context.Background(), repo, "pkg", "", ExportSurface{EntryPoints: []string{filepath.Join(depRoot, "missing.js")}})
	if len(warnings) == 0 {
		t.Fatalf("expected warnings for invalid metadata and missing entrypoint")
	}
}

func TestDetectDynamicLoaderUsageReadError(t *testing.T) {
	depRoot := t.TempDir()
	_, _, _, err := detectDynamicLoaderUsage(depRoot, []string{filepath.Join(depRoot, "missing.js")})
	if err == nil {
		t.Fatalf("expected read error for missing entrypoint")
	}
}

func TestAppendDynamicRiskCueSkipsOversizedEntrypointDuringSampling(t *testing.T) {
	depRoot := t.TempDir()
	entry := filepath.Join(depRoot, "index.js")
	dynamicSuffix := "\nmodule.exports = require(loader())\n"
	content := append(bytesOfLength(jsSourceReadMaxBytes-int64(len(dynamicSuffix))+1), []byte(dynamicSuffix)...)
	if err := os.WriteFile(entry, content, 0o600); err != nil {
		t.Fatalf("write oversized entrypoint: %v", err)
	}

	cues, warnings := appendDynamicRiskCue(nil, nil, "pkg", depRoot, []string{entry})
	if len(cues) != 0 {
		t.Fatalf("expected oversized entrypoint to be skipped during dynamic scan, got cues %#v", cues)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected one oversized-entrypoint warning, got %#v", warnings)
	}
	if !strings.Contains(warnings[0], "skipped 1 JS/TS file(s) above") {
		t.Fatalf("expected oversized-entrypoint warning, got %#v", warnings)
	}
	if strings.Contains(warnings[0], "dynamic loader scan failed") {
		t.Fatalf("expected oversized entrypoint skip warning, got %#v", warnings)
	}
}

func TestRiskRootOpenAndCloseBranches(t *testing.T) {
	originalOpen := openDependencyRootNoFollow
	t.Cleanup(func() {
		openDependencyRootNoFollow = originalOpen
	})

	missingRoot := filepath.Join(t.TempDir(), "missing")
	_, warnings := appendDynamicRiskCue(nil, nil, "pkg", missingRoot, nil)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "dynamic loader scan failed") {
		t.Fatalf("expected dynamic-root open warning, got %#v", warnings)
	}

	depRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(depRoot, packageJSONFile), []byte(`{"name":"pkg"}`), 0o600); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	closeErr := errors.New("close failed")
	openDependencyRootNoFollow = func(path string) (safeio.Root, error) {
		root, err := safeio.OpenRootNoFollow(path)
		if err != nil {
			return nil, err
		}
		return &jsCloseErrorRoot{Root: root, closeErr: closeErr}, nil
	}

	_, warnings = assessRiskCues(context.Background(), depRoot, "pkg", depRoot, ExportSurface{})
	if !warningsContain(warnings, "failed to close dependency root after risk analysis") || !warningsContain(warnings, "close failed") {
		t.Fatalf("expected risk-analysis close warning, got %#v", warnings)
	}

	_, warnings = appendDynamicRiskCue(nil, nil, "pkg", depRoot, nil)
	if !warningsContain(warnings, "failed to close dependency root after dynamic loader scan") || !warningsContain(warnings, "close failed") {
		t.Fatalf("expected dynamic-scan close warning, got %#v", warnings)
	}

	_, warnings = appendNativeRiskCue(context.Background(), nil, nil, "pkg", depRoot, packageJSON{})
	if !warningsContain(warnings, "failed to close dependency root after native module scan") || !warningsContain(warnings, "close failed") {
		t.Fatalf("expected native-scan close warning, got %#v", warnings)
	}
}

func TestNativeRiskConfinementErrorBranches(t *testing.T) {
	rootErr := errors.New("confined root failure")
	bindingErrRoot := &fakeJSRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return nil, rootErr
		},
	}
	if _, err := buildNativeModuleRiskCueWithinRoot(context.Background(), bindingErrRoot, t.TempDir(), packageJSON{}); !errors.Is(err, rootErr) {
		t.Fatalf("expected native-cue binding error, got %v", err)
	}
	cues, warnings := appendNativeRiskCueWithinRoot(context.Background(), nil, nil, "pkg", bindingErrRoot, t.TempDir(), packageJSON{})
	if len(cues) != 0 || !warningsContain(warnings, "native module scan failed") {
		t.Fatalf("expected confined native-cue warning, got cues=%#v warnings=%#v", cues, warnings)
	}

	nodeWalkErrRoot := &fakeJSRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return nil, os.ErrNotExist
		},
		open: func(string) (safeio.File, error) {
			return nil, rootErr
		},
	}
	if _, _, err := detectNativeModuleIndicatorsWithinRoot(context.Background(), nodeWalkErrRoot, t.TempDir(), packageJSON{}); !errors.Is(err, rootErr) {
		t.Fatalf("expected native indicator walk error, got %v", err)
	}

	if _, err := detectBindingGyp(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected missing binding.gyp root to fail")
	}
}

func TestNodeBinaryScannerWalkBranches(t *testing.T) {
	walkErr := errors.New("walk failed")
	scanner := &nodeBinaryScanner{maxVisited: 2}
	if err := scanner.walk("", nil, walkErr); !errors.Is(err, walkErr) {
		t.Fatalf("expected incoming walk error, got %v", err)
	}

	infoErr := errors.New("entry info failed")
	if err := scanner.walk("entry", &infoErrorDirEntry{err: infoErr}, nil); !errors.Is(err, infoErr) {
		t.Fatalf("expected entry info error, got %v", err)
	}

	dir := t.TempDir()
	filePath := filepath.Join(dir, "entry.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write scanner entry: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("read scanner entries: entries=%#v err=%v", entries, err)
	}

	capped := &nodeBinaryScanner{maxVisited: 0}
	if err := capped.walk(filePath, entries[0], nil); !errors.Is(err, fs.SkipAll) {
		t.Fatalf("expected scanner cap to stop walk, got %v", err)
	}

	uncapped := &nodeBinaryScanner{maxVisited: 2}
	if err := uncapped.walk(filePath, entries[0], nil); err != nil {
		t.Fatalf("expected regular entry walk to continue, got %v", err)
	}
}

type jsCloseErrorRoot struct {
	safeio.Root
	closeErr error
}

func (r *jsCloseErrorRoot) Close() error {
	return errors.Join(r.Root.Close(), r.closeErr)
}

type infoErrorDirEntry struct {
	err error
}

func (*infoErrorDirEntry) Name() string                 { return "entry" }
func (*infoErrorDirEntry) IsDir() bool                  { return false }
func (*infoErrorDirEntry) Type() fs.FileMode            { return 0 }
func (e *infoErrorDirEntry) Info() (fs.FileInfo, error) { return nil, e.err }

func warningsContain(warnings []string, fragment string) bool {
	return strings.Contains(strings.Join(warnings, "\n"), fragment)
}

func TestNativeMetadataAndDepthHelpers(t *testing.T) {
	repo := t.TempDir()
	depRoot := filepath.Join(repo, "node_modules", "pkg")
	if err := os.MkdirAll(depRoot, 0o755); err != nil {
		t.Fatalf("mkdir dep root: %v", err)
	}
	pkg := packageJSON{
		Gypfile: true,
		Scripts: map[string]string{
			"install":     "node-gyp rebuild",
			"postinstall": "echo noop",
		},
	}
	indicators := collectNativeMetadataIndicators(pkg)
	if len(indicators) == 0 {
		t.Fatalf("expected native metadata indicators")
	}

	if _, err := os.Stat(filepath.Join(depRoot, bindingGypFile)); !os.IsNotExist(err) {
		t.Fatalf("expected no binding.gyp in fixture")
	}
	native, details, err := detectNativeModuleIndicators(context.Background(), depRoot, pkg)
	if err != nil {
		t.Fatalf("detect native module indicators: %v", err)
	}
	if !native || len(details) == 0 {
		t.Fatalf("expected native indicators from package metadata")
	}

	if err := os.WriteFile(filepath.Join(depRoot, packageJSONFile), []byte(`{"name":"pkg","dependencies":{"a":"1.0.0"}}`), 0o600); err != nil {
		t.Fatalf("write root package.json: %v", err)
	}
	childRoot := filepath.Join(depRoot, "node_modules", "a")
	if err := os.MkdirAll(childRoot, 0o755); err != nil {
		t.Fatalf("mkdir child root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(childRoot, packageJSONFile), []byte(`{"name":"a"}`), 0o600); err != nil {
		t.Fatalf("write child package.json: %v", err)
	}
	depth, warnings := estimateTransitiveDepth(repo, depRoot, packageJSON{Dependencies: map[string]string{"a": "1.0.0"}})
	if len(warnings) != 0 {
		t.Fatalf("did not expect depth warnings, got %#v", warnings)
	}
	if depth < 2 {
		t.Fatalf("expected depth >= 2, got %d", depth)
	}
}

func TestTransitiveDepthBudgetAndCycleBranches(t *testing.T) {
	repo := t.TempDir()
	pkg := packageJSON{Dependencies: map[string]string{"missing": "1.0.0"}}

	memo := map[string]depthEvaluation{}
	visiting := map[string]struct{}{}
	depth := transitiveDepth(repo, filepath.Join(repo, "node_modules", "pkg"), pkg, memo, visiting, 0)
	if depth.depth != 1 {
		t.Fatalf("expected depth 1 when budget is exhausted, got %d", depth.depth)
	}

	visiting = map[string]struct{}{filepath.Join(repo, "node_modules", "pkg"): {}}
	depth = transitiveDepth(repo, filepath.Join(repo, "node_modules", "pkg"), pkg, memo, visiting, 10)
	if depth.depth != 1 {
		t.Fatalf("expected depth 1 for cycle detection branch, got %d", depth.depth)
	}
}

func TestDetectNodeBinaryMaxVisitedBranch(t *testing.T) {
	depRoot := t.TempDir()
	for i := 0; i < 650; i++ {
		name := "f-" + strconv.Itoa(i) + ".txt"
		if err := os.WriteFile(filepath.Join(depRoot, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write file %d: %v", i, err)
		}
	}

	found, err := detectNodeBinary(context.Background(), depRoot)
	if err != nil {
		t.Fatalf("detect node binary with max visited cap: %v", err)
	}
	if found != "" {
		t.Fatalf("expected no .node file found, got %q", found)
	}
}

func TestRiskHelperAdditionalBranches(t *testing.T) {
	if firstNonSpaceByte("   \t\r") != 0 {
		t.Fatalf("expected firstNonSpaceByte to return 0 for blank input")
	}

	depRoot := t.TempDir()
	native, details, err := detectNativeModuleIndicators(context.Background(), depRoot, packageJSON{})
	if err != nil {
		t.Fatalf("detect native indicators without metadata: %v", err)
	}
	if native || len(details) != 0 {
		t.Fatalf("expected no native indicators, got native=%v details=%#v", native, details)
	}
}

func TestRiskHelperErrorBranches(t *testing.T) {
	// Use a regular file path as depRoot to trigger filesystem errors
	// without changing directory permissions.
	depRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(depRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("write depRoot file: %v", err)
	}

	if _, _, err := detectNativeModuleIndicators(context.Background(), depRoot, packageJSON{}); err == nil {
		t.Fatalf("expected detectNativeModuleIndicators permission error")
	}

	if _, err := detectNodeBinary(context.Background(), filepath.Join(t.TempDir(), "missing-root")); err == nil {
		t.Fatalf("expected detectNodeBinary error for missing root")
	}
}

func TestIsCommentedBranches(t *testing.T) {
	if isCommented(`"not-a-comment"`) {
		// empty input without comment marker should not be flagged by this specific helper.
		t.Fatalf("expected no inline comment when no delimiter exists")
	}

	if isCommented("a"+"b`c // ignored` // comment") != true {
		t.Fatalf("expected comment after template literal to be detected")
	}

	if isCommented("'single-quoted // ignored'") {
		t.Fatalf("did not expect comment inside single-quoted string")
	}

	if isCommented("\"double-quoted \\\" // ignored\"") {
		t.Fatalf("did not expect comment after escaped quote in double-quoted string")
	}
}

func TestDetectNativeModuleIndicatorsNodeBinaryBranch(t *testing.T) {
	depRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(depRoot, packageJSONFile), []byte(`{"name":"pkg"}`), 0o600); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, nodeBinaryFile), []byte(""), 0o600); err != nil {
		t.Fatalf("write node binary: %v", err)
	}

	isNative, details, err := detectNativeModuleIndicators(context.Background(), depRoot, packageJSON{})
	if err != nil {
		t.Fatalf("detect native indicators with node binary: %v", err)
	}
	if !isNative {
		t.Fatal("expected package to be native due to .node binary")
	}
	if len(details) == 0 {
		t.Fatal("expected metadata detail for detected .node binary")
	}
	found := false
	for _, detail := range details {
		if detail == nodeBinaryFile {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected %s detail, got %#v", nodeBinaryFile, details)
	}
}

func TestAppendDepthRiskCueSeverityHeuristic(t *testing.T) {
	repoRoot := t.TempDir()
	pkgRoot := filepath.Join(repoRoot, "node_modules", "pkg")
	if err := os.MkdirAll(pkgRoot, 0o755); err != nil {
		t.Fatalf("mkdir pkg root: %v", err)
	}

	chain := []string{"a", "b", "c", "d", "e", "f", "g"}
	for i, depName := range chain {
		next := ""
		if i+1 < len(chain) {
			next = chain[i+1]
		}

		depJSON := `{"name":"` + depName + `"}`
		if next != "" {
			depJSON = `{"name":"` + depName + `","dependencies":{"` + next + `":"1.0.0"}}`
		}

		depRoot := filepath.Join(repoRoot, "node_modules", depName)
		if err := os.MkdirAll(depRoot, 0o755); err != nil {
			t.Fatalf("mkdir dependency root: %v", err)
		}
		if err := os.WriteFile(filepath.Join(depRoot, packageJSONFile), []byte(depJSON), 0o600); err != nil {
			t.Fatalf("write dependency package.json for %s: %v", depName, err)
		}
	}

	if err := os.WriteFile(filepath.Join(pkgRoot, packageJSONFile), []byte(`{"name":"pkg","dependencies":{"a":"1.0.0"}}`), 0o600); err != nil {
		t.Fatalf("write root package.json: %v", err)
	}

	rootPkg := packageJSON{Dependencies: map[string]string{"a": "1.0.0"}}
	cues, warnings := appendDepthRiskCue(nil, nil, repoRoot, pkgRoot, rootPkg)
	if len(warnings) != 0 {
		t.Fatalf("did not expect warnings: %#v", warnings)
	}
	if len(cues) != 1 {
		t.Fatalf("expected one deep graph cue, got %#v", cues)
	}
	if cues[0].Code != riskCodeDeepGraph {
		t.Fatalf("unexpected risk code: %q", cues[0].Code)
	}
	if cues[0].Severity != "high" {
		t.Fatalf("expected high severity for deep graph, got %q", cues[0].Severity)
	}
}

func TestTransitiveDepthChildWarningBranch(t *testing.T) {
	repoRoot := t.TempDir()
	rootPkgRoot := filepath.Join(repoRoot, "node_modules", "pkg")
	if err := os.MkdirAll(rootPkgRoot, 0o755); err != nil {
		t.Fatalf(rootPackageMkdirErr, err)
	}

	if err := os.WriteFile(filepath.Join(rootPkgRoot, packageJSONFile), []byte(`{"name":"pkg","dependencies":{"valid":"1.0.0","invalid":"1.0.0"}}`), 0o600); err != nil {
		t.Fatalf("write root package json: %v", err)
	}

	validRoot := filepath.Join(repoRoot, "node_modules", "valid")
	if err := os.MkdirAll(validRoot, 0o755); err != nil {
		t.Fatalf("mkdir valid dependency root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(validRoot, packageJSONFile), []byte(`{"name":"valid"}`), 0o600); err != nil {
		t.Fatalf("write valid package json: %v", err)
	}

	invalidRoot := filepath.Join(repoRoot, "node_modules", "invalid")
	if err := os.MkdirAll(invalidRoot, 0o755); err != nil {
		t.Fatalf("mkdir invalid dependency root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(invalidRoot, packageJSONFile), []byte(`{"name":"invalid"`), 0o600); err != nil {
		t.Fatalf("write invalid package json: %v", err)
	}

	rootPkg := packageJSON{Dependencies: map[string]string{"valid": "1.0.0", "invalid": "1.0.0"}}
	depth := transitiveDepth(repoRoot, rootPkgRoot, rootPkg, map[string]depthEvaluation{}, map[string]struct{}{}, 4)
	if depth.depth == 0 {
		t.Fatalf("expected positive depth for dependency graph")
	}
	if !warningsContain(depth.warnings, "transitive dependency depth is incomplete") {
		t.Fatalf("expected incomplete depth warning, got %#v", depth.warnings)
	}
}

func TestTransitiveDepthSkipsMissingDependencyRoot(t *testing.T) {
	repoRoot := t.TempDir()
	rootPkgRoot := filepath.Join(repoRoot, "node_modules", "pkg")
	if err := os.MkdirAll(rootPkgRoot, 0o755); err != nil {
		t.Fatalf(rootPackageMkdirErr, err)
	}
	if err := os.WriteFile(filepath.Join(rootPkgRoot, packageJSONFile), []byte(`{"name":"pkg","dependencies":{"missing":"1.0.0"}}`), 0o600); err != nil {
		t.Fatalf("write root package json: %v", err)
	}

	rootPkg := packageJSON{Dependencies: map[string]string{"missing": "1.0.0"}}
	depth := transitiveDepth(repoRoot, rootPkgRoot, rootPkg, map[string]depthEvaluation{}, map[string]struct{}{}, 4)
	if depth.depth != 1 {
		t.Fatalf("expected depth to remain 1 for missing child dep roots, got %d", depth.depth)
	}
}

func TestTransitiveDepthResolvesNormalAndScopedDependencyNames(t *testing.T) {
	repoRoot := t.TempDir()
	rootPkgRoot := filepath.Join(repoRoot, "node_modules", "pkg")
	if err := os.MkdirAll(rootPkgRoot, 0o755); err != nil {
		t.Fatalf(rootPackageMkdirErr, err)
	}

	normalRoot := filepath.Join(repoRoot, "node_modules", "dep")
	if err := os.MkdirAll(normalRoot, 0o755); err != nil {
		t.Fatalf("mkdir normal dependency root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(normalRoot, packageJSONFile), []byte(`{"name":"dep"}`), 0o600); err != nil {
		t.Fatalf("write normal package json: %v", err)
	}

	scopedRoot := filepath.Join(repoRoot, "node_modules", "@scope", "pkg")
	if err := os.MkdirAll(scopedRoot, 0o755); err != nil {
		t.Fatalf("mkdir scoped dependency root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scopedRoot, packageJSONFile), []byte(`{"name":"`+scopedDependency+`"}`), 0o600); err != nil {
		t.Fatalf("write scoped package json: %v", err)
	}

	if root, ok := resolveInstalledDependencyRoot(repoRoot, rootPkgRoot, "dep"); !ok || root != normalRoot {
		t.Fatalf("expected normal dependency root resolution, got root=%q ok=%v", root, ok)
	}
	if root, ok := resolveInstalledDependencyRoot(repoRoot, rootPkgRoot, scopedDependency); !ok || root != scopedRoot {
		t.Fatalf("expected scoped dependency root resolution, got root=%q ok=%v", root, ok)
	}

	rootPkg := packageJSON{
		Dependencies: map[string]string{
			"dep":            "1.0.0",
			scopedDependency: "1.0.0",
		},
	}
	depth := transitiveDepth(repoRoot, rootPkgRoot, rootPkg, map[string]depthEvaluation{}, map[string]struct{}{}, 4)
	if depth.depth != 2 {
		t.Fatalf("expected depth 2 for direct normal and scoped dependencies, got %d", depth.depth)
	}
}

func TestTransitiveDepthResolvesRepoHoistedChain(t *testing.T) {
	repoRoot := t.TempDir()
	rootPkgRoot := filepath.Join(repoRoot, "node_modules", "pkg")

	mustWritePackage(t, rootPkgRoot, `{"name":"pkg","main":"index.js","gypfile":true,"dependencies":{"deep-a":"1.0.0"}}`)
	mustWritePackage(t, filepath.Join(repoRoot, "node_modules", "deep-a"), `{"name":"deep-a","dependencies":{"deep-b":"1.0.0"}}`)
	mustWritePackage(t, filepath.Join(repoRoot, "node_modules", "deep-b"), `{"name":"deep-b","dependencies":{"deep-c":"1.0.0"}}`)
	mustWritePackage(t, filepath.Join(repoRoot, "node_modules", "deep-c"), `{"name":"deep-c"}`)

	rootPkg, warnings := loadDependencyPackageJSONWithinBoundary(rootPkgRoot, repoRoot)
	if len(warnings) != 0 {
		t.Fatalf("did not expect root package warnings for repo-hoisted chain, got %#v", warnings)
	}

	depth, warnings := estimateTransitiveDepth(repoRoot, rootPkgRoot, rootPkg)
	if len(warnings) != 0 {
		t.Fatalf("did not expect warnings for repo-hoisted chain, got %#v", warnings)
	}
	if depth != 4 {
		t.Fatalf("expected depth 4 for repo-hoisted chain, got %d", depth)
	}
}

func TestTransitiveDepthResolvesNestedInRepoSymlinkDependency(t *testing.T) {
	repoRoot := t.TempDir()
	rootPkgRoot := filepath.Join(repoRoot, "node_modules", "pkg")
	if err := os.MkdirAll(filepath.Join(rootPkgRoot, "node_modules"), 0o755); err != nil {
		t.Fatalf("mkdir pkg node_modules: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootPkgRoot, packageJSONFile), []byte(`{"name":"pkg","dependencies":{"dep":"1.0.0"}}`), 0o600); err != nil {
		t.Fatalf("write root package json: %v", err)
	}

	targetRoot := filepath.Join(repoRoot, "packages", "dep")
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		t.Fatalf("mkdir linked dependency root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetRoot, packageJSONFile), []byte(`{"name":"dep"}`), 0o600); err != nil {
		t.Fatalf("write linked dependency package json: %v", err)
	}

	linkRoot := filepath.Join(rootPkgRoot, "node_modules", "dep")
	if err := os.Symlink(targetRoot, linkRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	root, ok := resolveInstalledDependencyRoot(repoRoot, rootPkgRoot, "dep")
	if !ok || root != linkRoot {
		t.Fatalf("expected nested dependency symlink to resolve to %q, got root=%q ok=%v", linkRoot, root, ok)
	}

	depth, warnings := estimateTransitiveDepth(repoRoot, rootPkgRoot, packageJSON{Dependencies: map[string]string{"dep": "1.0.0"}})
	if len(warnings) != 0 {
		t.Fatalf("did not expect warnings for in-repo nested symlink, got %#v", warnings)
	}
	if depth != 2 {
		t.Fatalf("expected depth 2 for nested symlinked dependency, got %d", depth)
	}
}

func TestTransitiveDepthResolvesIntermediateHoistFromSymlinkedInRepoChild(t *testing.T) {
	repoRoot := t.TempDir()
	realPkgRoot := filepath.Join(repoRoot, "packages", "shared", "apps", "app")
	if err := os.MkdirAll(realPkgRoot, 0o755); err != nil {
		t.Fatalf("mkdir real package root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realPkgRoot, packageJSONFile), []byte(`{"name":"app","dependencies":{"deep-a":"1.0.0"}}`), 0o600); err != nil {
		t.Fatalf("write real package json: %v", err)
	}

	symlinkPkgRoot := filepath.Join(repoRoot, "apps", "app-link")
	if err := os.MkdirAll(filepath.Dir(symlinkPkgRoot), 0o755); err != nil {
		t.Fatalf("mkdir symlink parent: %v", err)
	}
	if err := os.Symlink(realPkgRoot, symlinkPkgRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	intermediateHoist := filepath.Join(repoRoot, "packages", "shared", "node_modules", "deep-a")
	if err := os.MkdirAll(intermediateHoist, 0o755); err != nil {
		t.Fatalf("mkdir intermediate hoist root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(intermediateHoist, packageJSONFile), []byte(`{"name":"deep-a","dependencies":{"deep-b":"1.0.0"}}`), 0o600); err != nil {
		t.Fatalf("write deep-a package json: %v", err)
	}

	deepBRoot := filepath.Join(repoRoot, "packages", "shared", "node_modules", "deep-b")
	if err := os.MkdirAll(deepBRoot, 0o755); err != nil {
		t.Fatalf("mkdir deep-b root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deepBRoot, packageJSONFile), []byte(`{"name":"deep-b"}`), 0o600); err != nil {
		t.Fatalf("write deep-b package json: %v", err)
	}

	canonicalIntermediateHoist, err := filepath.EvalSymlinks(intermediateHoist)
	if err != nil {
		t.Fatalf("canonicalize intermediate hoist: %v", err)
	}

	root, ok := resolveInstalledDependencyRoot(repoRoot, symlinkPkgRoot, "deep-a")
	if !ok || root != canonicalIntermediateHoist {
		t.Fatalf("expected symlinked package root to resolve canonical intermediate hoist %q, got root=%q ok=%v", canonicalIntermediateHoist, root, ok)
	}

	depth, warnings := estimateTransitiveDepth(repoRoot, symlinkPkgRoot, packageJSON{Dependencies: map[string]string{"deep-a": "1.0.0"}})
	if len(warnings) != 0 {
		t.Fatalf("did not expect warnings for canonical intermediate hoist resolution, got %#v", warnings)
	}
	if depth != 3 {
		t.Fatalf("expected depth 3 via canonical intermediate hoist chain, got %d", depth)
	}
}

func TestTransitiveDepthRejectsNestedEscapingSymlinkDependency(t *testing.T) {
	repoRoot := t.TempDir()
	rootPkgRoot := filepath.Join(repoRoot, "node_modules", "pkg")
	if err := os.MkdirAll(filepath.Join(rootPkgRoot, "node_modules"), 0o755); err != nil {
		t.Fatalf("mkdir pkg node_modules: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootPkgRoot, packageJSONFile), []byte(`{"name":"pkg","dependencies":{"dep":"1.0.0"}}`), 0o600); err != nil {
		t.Fatalf("write root package json: %v", err)
	}

	outsideRoot := filepath.Join(t.TempDir(), "dep")
	if err := os.MkdirAll(outsideRoot, 0o755); err != nil {
		t.Fatalf("mkdir outside dependency root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outsideRoot, packageJSONFile), []byte(`{"name":"dep"}`), 0o600); err != nil {
		t.Fatalf("write outside dependency package json: %v", err)
	}

	linkRoot := filepath.Join(rootPkgRoot, "node_modules", "dep")
	if err := os.Symlink(outsideRoot, linkRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if root, ok := resolveInstalledDependencyRoot(repoRoot, rootPkgRoot, "dep"); ok || root != "" {
		t.Fatalf("expected nested escaping dependency symlink to be rejected, got root=%q ok=%v", root, ok)
	}
}

func TestTransitiveDepthRejectsTraversalDependencyNames(t *testing.T) {
	repoRoot := t.TempDir()
	rootPkgRoot := filepath.Join(repoRoot, "node_modules", "pkg")
	if err := os.MkdirAll(rootPkgRoot, 0o755); err != nil {
		t.Fatalf(rootPackageMkdirErr, err)
	}

	outsideRoot := filepath.Join(filepath.Dir(repoRoot), "out1")
	if err := os.MkdirAll(outsideRoot, 0o755); err != nil {
		t.Fatalf("mkdir outside dependency root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outsideRoot, packageJSONFile), []byte(`{"name":"out1"}`), 0o600); err != nil {
		t.Fatalf("write outside package json: %v", err)
	}

	traversalName := "../../../../out1"
	if root, ok := resolveInstalledDependencyRoot(repoRoot, rootPkgRoot, traversalName); ok || root != "" {
		t.Fatalf("expected traversal-shaped dependency name to be rejected, got root=%q ok=%v", root, ok)
	}

	rootPkg := packageJSON{Dependencies: map[string]string{traversalName: "1.0.0"}}
	depth := transitiveDepth(repoRoot, rootPkgRoot, rootPkg, map[string]depthEvaluation{}, map[string]struct{}{}, 4)
	if depth.depth != 1 {
		t.Fatalf("expected depth 1 when traversal-shaped dependency is rejected, got %d", depth.depth)
	}
}

func TestAppendDepthRiskCueSurfacesIncompleteEvaluationWarnings(t *testing.T) {
	repoRoot := t.TempDir()
	rootPkgRoot := filepath.Join(repoRoot, "node_modules", "pkg")
	if err := os.MkdirAll(rootPkgRoot, 0o755); err != nil {
		t.Fatalf(rootPackageMkdirErr, err)
	}

	largeRoot := filepath.Join(repoRoot, "node_modules", "large")
	if err := os.MkdirAll(largeRoot, 0o755); err != nil {
		t.Fatalf("mkdir large dependency root: %v", err)
	}
	oversized := append([]byte(`{"name":"large","dependencies":{"deep":"1.0.0"},"padding":"`), bytesOfLength(jsPackageJSONReadMaxBytes)...)
	oversized = append(oversized, []byte(`"}`)...)
	if err := os.WriteFile(filepath.Join(largeRoot, packageJSONFile), oversized, 0o600); err != nil {
		t.Fatalf("write oversized package json: %v", err)
	}

	cues, warnings := appendDepthRiskCue(nil, nil, repoRoot, rootPkgRoot, packageJSON{Dependencies: map[string]string{"large": "1.0.0"}})
	if len(cues) != 0 {
		t.Fatalf("did not expect a deep-graph cue from incomplete evaluation, got %#v", cues)
	}
	if !warningsContain(warnings, "transitive dependency depth is incomplete") {
		t.Fatalf("expected incomplete depth warning, got %#v", warnings)
	}
	if !warningsContain(warnings, "unable to read dependency metadata") {
		t.Fatalf("expected oversized metadata warning to surface, got %#v", warnings)
	}
}

func TestAssessRiskCuesIncludesDeepGraphForRepoHoistedChain(t *testing.T) {
	repoRoot, rootPkgRoot := setupRepoHoistedRiskChain(t)
	root, validatedDepRoot, err := openValidatedRootNoFollow(rootPkgRoot)
	if err != nil {
		t.Fatalf("open validated root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close validated root: %v", closeErr)
		}
	}()

	rootPkg, warnings := loadDependencyPackageJSONFromRoot(root, validatedDepRoot)
	assertNoRiskWarnings(t, "root package", warnings)
	depthCues, depthWarnings := appendDepthRiskCue(nil, nil, repoRoot, validatedDepRoot, rootPkg)
	assertNoRiskWarnings(t, "standalone depth", depthWarnings)
	if len(depthCues) != 1 || depthCues[0].Code != riskCodeDeepGraph {
		t.Fatalf("expected standalone depth cue before aggregate risk assessment, got cues=%#v rawRoot=%q validatedRoot=%q", depthCues, rootPkgRoot, validatedDepRoot)
	}

	cues, warnings := assessRiskCues(context.Background(), repoRoot, "risky", rootPkgRoot, ExportSurface{EntryPoints: []string{filepath.Join(rootPkgRoot, "index.js")}})
	assertNoRiskWarnings(t, "aggregate risk", warnings)
	codes := make([]string, 0, len(cues))
	for _, cue := range cues {
		codes = append(codes, cue.Code)
	}
	if !slices.Contains(codes, riskCodeDeepGraph) {
		t.Fatalf("expected deep-graph cue, got %#v", cues)
	}
}

func setupRepoHoistedRiskChain(t *testing.T) (string, string) {
	t.Helper()

	repoRoot := t.TempDir()
	rootPkgRoot := filepath.Join(repoRoot, "node_modules", "risky")
	mustWritePackage(t, rootPkgRoot, `{"name":"risky","main":"index.js","gypfile":true,"dependencies":{"deep-a":"1.0.0"}}`)
	if err := os.WriteFile(filepath.Join(rootPkgRoot, "index.js"), []byte("const dep = process.env.DEP\nmodule.exports = require(dep)\n"), 0o600); err != nil {
		t.Fatalf("write root entrypoint: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootPkgRoot, bindingGypFile), []byte("{ }\n"), 0o600); err != nil {
		t.Fatalf("write binding.gyp: %v", err)
	}

	for depRoot, pkgJSON := range map[string]string{
		filepath.Join(repoRoot, "node_modules", "deep-a"): `{"name":"deep-a","dependencies":{"deep-b":"1.0.0"}}`,
		filepath.Join(repoRoot, "node_modules", "deep-b"): `{"name":"deep-b","dependencies":{"deep-c":"1.0.0"}}`,
		filepath.Join(repoRoot, "node_modules", "deep-c"): `{"name":"deep-c"}`,
	} {
		mustWritePackage(t, depRoot, pkgJSON)
	}

	return repoRoot, rootPkgRoot
}

func assertNoRiskWarnings(t *testing.T, context string, warnings []string) {
	t.Helper()
	if len(warnings) != 0 {
		t.Fatalf("did not expect %s warnings, got %#v", context, warnings)
	}
}

func TestLoadDependencyPackageJSONWithinBoundaryReturnsWarningWhenOpenFails(t *testing.T) {
	repoRoot := t.TempDir()
	depRoot := filepath.Join(repoRoot, "node_modules", "missing")

	pkg, warnings := loadDependencyPackageJSONWithinBoundary(depRoot, repoRoot)
	if pkg.Name != "" {
		t.Fatalf("expected missing dependency metadata to return empty package, got %#v", pkg)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unable to read dependency metadata") {
		t.Fatalf("expected missing dependency metadata warning, got %#v", warnings)
	}
}

func TestLoadDependencyPackageJSONWithinBoundaryResetsPackageWhenCloseFails(t *testing.T) {
	originalOpen := openDependencyRootNoFollow
	t.Cleanup(func() {
		openDependencyRootNoFollow = originalOpen
	})

	repoRoot := t.TempDir()
	depRoot := filepath.Join(repoRoot, "node_modules", "pkg")
	if err := os.MkdirAll(depRoot, 0o755); err != nil {
		t.Fatalf("mkdir dependency root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, packageJSONFile), []byte(`{"name":"pkg"}`), 0o600); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	openDependencyRootNoFollow = func(path string) (safeio.Root, error) {
		root, err := safeio.OpenRootNoFollow(path)
		if err != nil {
			return nil, err
		}
		return &jsCloseErrorRoot{Root: root, closeErr: errors.New("close failed")}, nil
	}

	pkg, warnings := loadDependencyPackageJSONWithinBoundary(depRoot, repoRoot)
	if pkg.Name != "" {
		t.Fatalf("expected close failure to clear package metadata, got %#v", pkg)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unable to read dependency metadata") || !strings.Contains(warnings[0], "close failed") {
		t.Fatalf("expected close failure warning, got %#v", warnings)
	}
}
