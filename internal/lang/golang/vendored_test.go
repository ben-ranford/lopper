package golang

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestParseVendoredModuleMetadata(t *testing.T) {
	lines := []string{
		"# github.com/acme/one v1.2.3",
		"## explicit; go 1.22",
		"github.com/acme/one/pkg",
		"github.com/acme/one/sub",
		"# badheader",
		"orphan/pkg",
		"# github.com/acme/two v0.4.0 => github.com/acme/two-fork v0.4.1",
		"## go 1.21",
		"github.com/acme/mismatch/pkg",
		"# github.com/acme/empty v1.0.0",
	}
	content := []byte(strings.Join(lines, "\n"))

	metadata := parseVendoredModuleMetadata(content)
	if len(metadata.Dependencies) != 3 {
		t.Fatalf("expected three parsed vendored dependencies, got %#v", metadata.Dependencies)
	}

	one := metadata.Dependencies["github.com/acme/one"]
	if one.ModulePath != "github.com/acme/one" || !one.Explicit || one.GoVersionDirective != "1.22" || one.PackageCount != 2 {
		t.Fatalf("unexpected metadata for github.com/acme/one: %#v", one)
	}

	two := metadata.Dependencies["github.com/acme/two"]
	if !two.Replacement || two.ReplacementTarget != "github.com/acme/two-fork" || two.GoVersionDirective != "1.21" {
		t.Fatalf("unexpected replacement metadata for github.com/acme/two: %#v", two)
	}

	if got := metadata.ImportToDependency["github.com/acme/one/pkg"]; got != "github.com/acme/one" {
		t.Fatalf("expected package mapping for acme one, got %q", got)
	}

	assertWarningContains(t, metadata.Warnings, "malformed module header")
	assertWarningContains(t, metadata.Warnings, "without a preceding module header")
	assertWarningContains(t, metadata.Warnings, "do not match their module header")
	assertWarningContains(t, metadata.Warnings, "without package entries")
}

func TestLoadVendoredModuleMetadata(t *testing.T) {
	repo := t.TempDir()
	metadata, err := loadVendoredModuleMetadata(repo)
	if err != nil {
		t.Fatalf("load vendored metadata missing file: %v", err)
	}
	if metadata.ManifestFound {
		t.Fatalf("expected missing vendor/modules.txt to report ManifestFound=false")
	}

	vendorLines := []string{
		"# github.com/acme/dep v1.0.0",
		"github.com/acme/dep/pkg",
	}
	testutil.MustWriteFile(t, filepath.Join(repo, vendorModulesTxtName), strings.Join(vendorLines, "\n"))

	metadata, err = loadVendoredModuleMetadata(repo)
	if err != nil {
		t.Fatalf("load vendored metadata: %v", err)
	}
	if !metadata.ManifestFound {
		t.Fatalf("expected vendored metadata to report manifest found")
	}
	if got := metadata.ImportToDependency["github.com/acme/dep/pkg"]; got != "github.com/acme/dep" {
		t.Fatalf("expected package mapping for vendored dep, got %q", got)
	}
}

func TestLoadGoModuleInfoVendoredOption(t *testing.T) {
	repo := t.TempDir()
	goModLines := []string{
		"module example.com/root",
		"",
		"require github.com/declared/dep v1.2.3",
	}
	testutil.MustWriteFile(t, filepath.Join(repo, goModName), strings.Join(goModLines, "\n"))
	vendorLines := []string{
		"# github.com/vendor/dep v1.0.0",
		"## explicit",
		"github.com/vendor/dep/pkg",
	}
	testutil.MustWriteFile(t, filepath.Join(repo, vendorModulesTxtName), strings.Join(vendorLines, "\n"))

	withoutVendored, err := loadGoModuleInfo(repo)
	if err != nil {
		t.Fatalf("load module info without vendored preview: %v", err)
	}
	if withoutVendored.VendoredProvenanceEnabled || len(withoutVendored.VendoredDependencies) != 0 {
		t.Fatalf("expected vendored metadata to stay disabled by default, got %#v", withoutVendored)
	}

	withVendored, err := loadGoModuleInfo(repo, moduleLoadOptions{EnableVendoredProvenance: true})
	if err != nil {
		t.Fatalf("load module info with vendored preview: %v", err)
	}
	if !withVendored.VendoredProvenanceEnabled {
		t.Fatalf("expected vendored provenance enabled when preview option is on")
	}
	if len(withVendored.VendoredDependencies) == 0 {
		t.Fatalf("expected vendored dependencies to be populated")
	}
	if isDeclaredDependency("github.com/vendor/dep", withVendored.DeclaredDependencies) {
		t.Fatalf("expected vendored-only dependency to stay separate from go.mod declarations")
	}
}

func TestResolveDependencyFromImportVendoredProvenance(t *testing.T) {
	info := moduleInfo{
		LocalModulePaths: []string{"example.com/root"},
		DeclaredDependencies: []string{
			"github.com/declared/dep",
		},
		ReplacementImports: map[string]string{
			"github.com/replaced/dep": "github.com/original/dep",
		},
		VendoredProvenanceEnabled: true,
		VendoredImportDependencies: map[string]string{
			"github.com/declared/dep": "github.com/declared/dep",
			"github.com/vendor/dep":   "github.com/vendor/dep",
		},
	}

	declared := resolveDependencyFromImport("github.com/declared/dep/pkg", info)
	if declared.Dependency != "github.com/declared/dep" || !declared.Provenance.Declared || !declared.Provenance.Vendored {
		t.Fatalf("expected combined declared and vendored dependency provenance, got %#v", declared)
	}

	replaced := resolveDependencyFromImport("github.com/replaced/dep/pkg", info)
	if replaced.Dependency != "github.com/original/dep" || !replaced.Provenance.Replacement {
		t.Fatalf("expected replacement dependency provenance, got %#v", replaced)
	}

	vendored := resolveDependencyFromImport("github.com/vendor/dep/pkg/sub", info)
	if vendored.Dependency != "github.com/vendor/dep" || !vendored.Provenance.Vendored {
		t.Fatalf("expected vendored dependency provenance, got %#v", vendored)
	}

	inferred := resolveDependencyFromImport("github.com/inferred/dep/pkg", info)
	if inferred.Dependency != "github.com/inferred/dep" {
		t.Fatalf("expected inferred dependency, got %#v", inferred)
	}

	local := resolveDependencyFromImport("example.com/root/internal/pkg", info)
	if local.Dependency != "" {
		t.Fatalf("expected local module imports to be ignored, got %#v", local)
	}
}

func TestBuildGoDependencyProvenance(t *testing.T) {
	if got := buildGoDependencyProvenance(goDependencyProvenance{}); got != nil {
		t.Fatalf("expected nil provenance for empty provenance flags, got %#v", got)
	}

	declared := buildGoDependencyProvenance(goDependencyProvenance{Declared: true})
	if declared == nil || declared.Source != "go.mod" || declared.Confidence != "high" {
		t.Fatalf("expected go.mod provenance, got %#v", declared)
	}

	vendored := buildGoDependencyProvenance(goDependencyProvenance{Vendored: true})
	if vendored == nil || vendored.Source != "vendor/modules.txt" || vendored.Confidence != "medium" {
		t.Fatalf("expected vendor provenance, got %#v", vendored)
	}

	combined := buildGoDependencyProvenance(goDependencyProvenance{Declared: true, Vendored: true})
	if combined == nil || combined.Source != "go.mod+vendor" || combined.Confidence != "high" {
		t.Fatalf("expected combined provenance, got %#v", combined)
	}

	replaced := buildGoDependencyProvenance(goDependencyProvenance{Replacement: true})
	if replaced == nil || replaced.Source != "go.mod-replace" || replaced.Confidence != "high" {
		t.Fatalf("expected replacement provenance, got %#v", replaced)
	}
}

func TestVendoredOnlyImportRemainsUndeclared(t *testing.T) {
	info := moduleInfo{
		ModulePath:                 "example.com/root",
		LocalModulePaths:           []string{"example.com/root"},
		VendoredProvenanceEnabled:  true,
		VendoredImportDependencies: map[string]string{"github.com/vendor/dep/pkg": "github.com/vendor/dep"},
	}
	_, metadata := parseImports([]byte(`package main

import "github.com/vendor/dep/pkg"
`), "main.go", info)
	if len(metadata) != 1 {
		t.Fatalf("expected one import metadata entry, got %#v", metadata)
	}
	if metadata[0].Dependency != "github.com/vendor/dep" || !metadata[0].Undeclared || !metadata[0].Provenance.Vendored {
		t.Fatalf("expected vendored-only import to keep undeclared provenance, got %#v", metadata[0])
	}
}

func TestLongestVendoredDependencyAndLoadVendoredMetadataNoop(t *testing.T) {
	if got := longestVendoredDependency("github.com/acme/dep/pkg/sub", map[string]string{
		"github.com/acme":         "github.com/acme",
		"github.com/acme/dep/pkg": "github.com/acme/dep",
	}); got != "github.com/acme/dep" {
		t.Fatalf("expected longest vendored dependency match, got %q", got)
	}
	if got := longestVendoredDependency("github.com/acme/dep/pkg/sub", nil); got != "" {
		t.Fatalf("expected empty vendored match for nil mappings, got %q", got)
	}

	repo := t.TempDir()
	if err := loadVendoredMetadata(repo, moduleLoadOptions{}, &moduleInfo{}); err != nil {
		t.Fatalf("expected disabled vendored metadata load to no-op, got %v", err)
	}
	if err := loadVendoredMetadata(repo, moduleLoadOptions{EnableVendoredProvenance: true}, nil); err != nil {
		t.Fatalf("expected nil module info vendored load to no-op, got %v", err)
	}
}

func TestVendoredHelperBranches(t *testing.T) {
	if looksExternalImport("fmt") {
		t.Fatalf("expected stdlib import path to be treated as non-external")
	}
	if !looksExternalImport("github.com/acme/dep/pkg") {
		t.Fatalf("expected fully qualified module path to be treated as external")
	}

	if _, _, ok := parseVendoredModuleHeader("#"); ok {
		t.Fatalf("expected empty vendored module header to fail")
	}
	if _, _, ok := parseVendoredModuleHeader("# local/module v1.0.0"); ok {
		t.Fatalf("expected non-external vendored module header to fail")
	}
	modulePath, replacement, ok := parseVendoredModuleHeader("# github.com/acme/dep v1.0.0 => github.com/acme/fork v1.1.0")
	if !ok || modulePath != "github.com/acme/dep" || replacement != "github.com/acme/fork" {
		t.Fatalf("unexpected vendored module header parse result: module=%q replacement=%q ok=%v", modulePath, replacement, ok)
	}

	directive := vendoredDependencyMetadata{}
	applyVendoredMetadataDirective("## explicit; go 1.23; unsupported", &directive)
	if !directive.Explicit || directive.GoVersionDirective != "1.23" {
		t.Fatalf("unexpected parsed vendored metadata directive: %#v", directive)
	}
	applyVendoredMetadataDirective("##", nil)

	empty := &vendoredModuleMetadata{}
	appendVendoredMetadataWarnings(empty, vendoredParseState{})
	assertWarningContains(t, empty.Warnings, "no module entries were parsed")

	repo := t.TempDir()
	if _, ok := resolveRepoBoundedPath(repo, ""); ok {
		t.Fatalf("expected empty path to be rejected")
	}
	if resolved, ok := resolveRepoBoundedPath(repo, "./module"); !ok || !strings.HasPrefix(resolved, repo) {
		t.Fatalf("expected in-repo path to resolve, got resolved=%q ok=%v", resolved, ok)
	}
	if _, ok := resolveRepoBoundedPath(repo, "../outside"); ok {
		t.Fatalf("expected path escape outside repo to be rejected")
	}
}

func TestLoadGoModuleInfoWithOptionsErrorAndInferDependencyBranches(t *testing.T) {
	if _, err := loadGoModuleInfoWithOptions(filepath.Join(t.TempDir(), "missing"), moduleLoadOptions{}); err == nil {
		t.Fatalf("expected missing repo path to fail module info load")
	}
	if err := loadVendoredMetadata("\x00", moduleLoadOptions{EnableVendoredProvenance: true}, &moduleInfo{}); err == nil {
		t.Fatalf("expected invalid repo path to fail vendored metadata load")
	}

	if got := inferDependency("github.com/acme/dep/pkg"); got != "github.com/acme/dep" {
		t.Fatalf("expected inferred dependency root, got %q", got)
	}
	if got := inferDependency("gopkg.in/yaml.v3"); got != "gopkg.in/yaml.v3" {
		t.Fatalf("expected inferred two-part dependency, got %q", got)
	}
	if got := inferDependency("stdlib/path"); got != "" {
		t.Fatalf("expected stdlib-like dependency to be ignored, got %q", got)
	}
}

func assertWarningContains(t *testing.T, warnings []string, want string) {
	t.Helper()
	for _, warning := range warnings {
		if strings.Contains(warning, want) {
			return
		}
	}
	t.Fatalf("expected warning containing %q, got %#v", want, warnings)
}
