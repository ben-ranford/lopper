package dart

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestDartCoverageErrorAndSourceBranches(t *testing.T) {
	t.Run("analyse path normalization failure", func(t *testing.T) {
		originalWD, err := os.Getwd()
		if err != nil {
			t.Fatalf("getwd: %v", err)
		}
		tempWD := t.TempDir()
		if err := os.Chdir(tempWD); err != nil {
			t.Fatalf("chdir temp: %v", err)
		}
		t.Cleanup(func() {
			if chdirErr := os.Chdir(originalWD); chdirErr != nil {
				t.Fatalf("restore working directory: %v", chdirErr)
			}
		})
		if err := os.RemoveAll(tempWD); err != nil {
			t.Fatalf("remove temp wd: %v", err)
		}

		_, analyseErr := NewAdapter().Analyse(context.Background(), language.Request{})
		if analyseErr == nil {
			t.Fatalf("expected analyse to fail when working directory can no longer be resolved")
		}
	})

	t.Run("scan cancellation returns adapter scan error", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, pubspecYAMLName), "name: app\ndependencies:\n  http: ^1.0.0\n")
		writeFile(t, filepath.Join(repo, "lib", "main.dart"), "import 'package:http/http.dart';\n")

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		var out report.Report
		_, err := NewAdapter().scanRepo(ctx, repo, &out)
		if err == nil {
			t.Fatalf("expected canceled context to fail scan")
		}
	})

	t.Run("manifest merge + source helpers", func(t *testing.T) {
		dest := map[string]dependencyInfo{
			"git_dep": {
				Source:       dependencySourceGit,
				SourceDetail: "",
			},
			"federated_app": {
				FederatedFamily: "",
				FederatedRole:   "",
			},
		}
		mergeDependencyInfo(dest, "git_dep", dependencyInfo{
			Source:       dependencySourceGit,
			SourceDetail: "https://example.com/repo.git",
		})
		mergeDependencyInfo(dest, "federated_app", dependencyInfo{
			FederatedMembers: []string{"federated_android", "federated_ios"},
		})
		if dest["git_dep"].SourceDetail == "" {
			t.Fatalf("expected mergeDependencyInfo to fill missing source detail")
		}
		if len(dest["federated_app"].FederatedMembers) != 2 {
			t.Fatalf("expected mergeDependencyInfo to merge federated members, got %#v", dest["federated_app"].FederatedMembers)
		}

		if got := dependencySourceDetail(map[string]any{"ignored": "value"}, "url"); got != "" {
			t.Fatalf("expected empty source detail when preferred keys are absent, got %q", got)
		}
		if got := dependencySourceDetail(" https://example.com/pkg ", "url"); got != "https://example.com/pkg" {
			t.Fatalf("expected scalar source detail trim, got %q", got)
		}

		if got := dependencySourcePriority("unknown"); got != 0 {
			t.Fatalf("expected unknown source priority 0, got %d", got)
		}

		assignDependencySource(nil, dependencySourceHosted, "x")
		info := dependencyInfo{}
		assignDependencySource(&info, "", "x")
		if info.Source != "" {
			t.Fatalf("expected empty source assignment to be ignored, got %#v", info)
		}
		assignDependencySource(&info, dependencySourceHosted, "")
		assignDependencySource(&info, dependencySourcePath, "/tmp/local")
		assignDependencySource(&info, dependencySourcePath, "filled")
		if info.Source != dependencySourcePath || info.SourceDetail == "" {
			t.Fatalf("expected source priority/detail assignment, got %#v", info)
		}

		specInfo := dependencyInfoFromSpec("pkg", map[string]any{"hosted": map[string]any{"url": "https://pub.dev"}, "version": "^1.2.3"}, nil)
		if specInfo.Source != dependencySourceHosted {
			t.Fatalf("expected hosted source from spec, got %#v", specInfo)
		}

		if _, _, ok := federatedFamilyRole("  "); ok {
			t.Fatalf("expected blank dependency not to produce federated family role")
		}

		sourceMeta := dependencyInfo{LocalPath: true}
		if got := dependencySource(sourceMeta); got != dependencySourcePath {
			t.Fatalf("expected local-path fallback source, got %q", got)
		}
		sourceMeta = dependencyInfo{FlutterSDK: true}
		if got := dependencySource(sourceMeta); got != dependencySourceSDK {
			t.Fatalf("expected flutter-sdk fallback source, got %q", got)
		}
		if label := dependencySourceLabel(dependencyInfo{Source: dependencySourceGit}); label != "git" {
			t.Fatalf("expected git source label, got %q", label)
		}
		if label := dependencySourceLabel(dependencyInfo{Source: dependencySourceHosted}); label != "hosted" {
			t.Fatalf("expected hosted source label, got %q", label)
		}
		if label := dependencySourceLabel(dependencyInfo{Source: dependencySourceSDK}); label != "SDK" {
			t.Fatalf("expected non-flutter sdk label, got %q", label)
		}
		if message := dependencyOverrideMessage(dependencyInfo{}, true); message != "dependency is marked in dependency_overrides" {
			t.Fatalf("unexpected override message for unknown source: %q", message)
		}
		if message := dependencyOverrideRecommendation(dependencyInfo{}, true); message != "Review dependency_overrides usage and limit overrides to active blockers." {
			t.Fatalf("unexpected override recommendation for unknown source: %q", message)
		}
		if message := federatedPluginMessage(dependencyInfo{}); message != "dependency participates in a Flutter federated plugin family" {
			t.Fatalf("unexpected federated message without family: %q", message)
		}
		if message := federatedPluginMessage(dependencyInfo{FederatedFamily: "foo"}); message != `dependency participates in the "foo" Flutter federated plugin family` {
			t.Fatalf("unexpected federated message without members: %q", message)
		}
		if prov := buildDartDependencyProvenance(dependencyInfo{}); prov != nil {
			t.Fatalf("expected no provenance without source, got %#v", prov)
		}
		prov := buildDartDependencyProvenance(dependencyInfo{
			Source:             dependencySourceHosted,
			DeclaredInManifest: true,
		})
		if prov == nil || prov.Confidence != "medium" {
			t.Fatalf("expected medium-confidence manifest provenance, got %#v", prov)
		}
	})

	t.Run("directive parser edge branches", func(t *testing.T) {
		if kind, mod, clause, ok := parseImportDirective(`part "x.dart";`); ok || kind != "" || mod != "" || clause != "" {
			t.Fatalf("expected non-directive line to fail parse")
		}
		if _, _, _, ok := parseImportDirective(`using "x.dart";`); ok {
			t.Fatalf("expected unsupported directive kind to fail parse")
		}
		if symbols := parseShowSymbols("show hide Foo"); len(symbols) != 0 {
			t.Fatalf("expected empty show list after hide trim, got %#v", symbols)
		}
	})
}
