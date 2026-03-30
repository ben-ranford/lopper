package swift

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestSwiftAdapterDetectWithCocoaPodsRoots(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, podManifestName), buildPodfileContent([]swiftFixturePodDependency{alamofirePodFixtureDependency()}))
	testutil.MustWriteFile(t, filepath.Join(repo, "Sources", "App", swiftMainFileName), "import Alamofire\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "Packages", "Feature", podLockName), buildPodLockContent([]swiftFixturePodDependency{{name: "Kingfisher", version: "7.9.0"}}))

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected swift detection to match")
	}
	if !slices.Contains(detection.Roots, repo) {
		t.Fatalf("expected repo root in detection roots, got %#v", detection.Roots)
	}
	nested := filepath.Join(repo, "Packages", "Feature")
	if !slices.Contains(detection.Roots, nested) {
		t.Fatalf("expected nested CocoaPods root in detection roots, got %#v", detection.Roots)
	}
}

func TestSwiftAdapterAnalyseCocoaPodsDependency(t *testing.T) {
	repo := t.TempDir()
	writeSwiftDemoCocoaPodsProject(t, repo, []swiftFixturePodDependency{alamofirePodFixtureDependency()}, `import Alamofire
func run() {
  _ = Session.default
}`)

	depReport := mustSingleSwiftDependencyReport(t, language.Request{RepoPath: repo, Dependency: "alamofire"})
	if depReport.Language != swiftAdapterID {
		t.Fatalf("expected swift language, got %q", depReport.Language)
	}
	if depReport.TotalExportsCount == 0 {
		t.Fatalf("expected CocoaPods import evidence for alamofire, got %#v", depReport)
	}
}

func TestSwiftAdapterCocoaPodsModuleMappings(t *testing.T) {
	t.Run("base pod name maps to module import", func(t *testing.T) {
		repo := t.TempDir()
		writeSwiftDemoCocoaPodsProject(t, repo, []swiftFixturePodDependency{{name: "GoogleUtilities/Environment", version: "8.0.0"}}, `import GoogleUtilities
let value = Logger.self`)

		depReport := mustSingleSwiftDependencyReport(t, language.Request{RepoPath: repo, Dependency: "GoogleUtilities"})
		if depReport.Name != "googleutilities/environment" {
			t.Fatalf("expected dependency alias to resolve to declared pod, got %#v", depReport)
		}
		if len(depReport.UsedImports) == 0 && len(depReport.UnusedImports) == 0 {
			t.Fatalf("expected mapped import evidence for GoogleUtilities pod, got %#v", depReport)
		}
	})

	t.Run("subspec concatenation maps to module import", func(t *testing.T) {
		repo := t.TempDir()
		writeSwiftDemoCocoaPodsProject(t, repo, []swiftFixturePodDependency{{name: "Firebase/Analytics", version: "10.20.0"}}, `import FirebaseAnalytics
let analytics = Analytics.self`)

		depReport := mustSingleSwiftDependencyReport(t, language.Request{RepoPath: repo, Dependency: "FirebaseAnalytics"})
		if depReport.Name != "firebase/analytics" {
			t.Fatalf("expected concatenated module alias to resolve to declared pod, got %#v", depReport)
		}
		if len(depReport.UsedImports) == 0 && len(depReport.UnusedImports) == 0 {
			t.Fatalf("expected mapped import evidence for FirebaseAnalytics pod, got %#v", depReport)
		}
	})
}

func TestSwiftAdapterMergesSwiftPMAndCocoaPodsCatalogs(t *testing.T) {
	repo := t.TempDir()
	writeSwiftDemoPackage(t, repo, []swiftFixtureDependency{alamofireFixtureDependency()}, `import Alamofire
import Kingfisher
func run() {
  _ = Session.default
  _ = KingfisherManager.shared
}`)
	testutil.MustWriteFile(t, filepath.Join(repo, podManifestName), buildPodfileContent([]swiftFixturePodDependency{{name: "Kingfisher", version: "7.9.0"}}))
	testutil.MustWriteFile(t, filepath.Join(repo, podLockName), buildPodLockContent([]swiftFixturePodDependency{{name: "Kingfisher", version: "7.9.0"}}))

	reportData := mustAnalyseSwiftRequest(t, language.Request{RepoPath: repo, TopN: 10})
	names := make([]string, 0, len(reportData.Dependencies))
	for _, dep := range reportData.Dependencies {
		names = append(names, dep.Name)
	}
	if !slices.Contains(names, "alamofire") {
		t.Fatalf("expected swiftpm dependency in merged report, got %#v", names)
	}
	if !slices.Contains(names, "kingfisher") {
		t.Fatalf("expected CocoaPods dependency in merged report, got %#v", names)
	}
}

func TestSwiftAdapterWarnsOnAmbiguousCocoaPodsModuleMappings(t *testing.T) {
	repo := t.TempDir()
	writeSwiftDemoCocoaPodsProject(t, repo, []swiftFixturePodDependency{
		{name: "GoogleUtilities/Environment", version: "8.0.0"},
		{name: "GoogleUtilities/AppDelegateSwizzler", version: "8.0.0"},
	}, `import GoogleUtilities
let value = AppDelegateSwizzler.self`)

	reportData := mustAnalyseSwiftRequest(t, language.Request{RepoPath: repo, TopN: 5})
	assertWarningContains(t, reportData.Warnings, "ambiguous CocoaPods module mapping")
	assertWarningContains(t, reportData.Warnings, "CocoaPods module mapping may be incomplete")
}

func TestSwiftAdapterCocoaPodsMissingLockfileRiskCue(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, podManifestName), buildPodfileContent([]swiftFixturePodDependency{alamofirePodFixtureDependency()}))
	testutil.MustWriteFile(t, filepath.Join(repo, "Sources", "Demo", swiftMainFileName), `import Alamofire
let value = Session.default`)

	reportData := mustAnalyseSwiftRequest(t, language.Request{RepoPath: repo, Dependency: "alamofire"})
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyReport, len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if !hasRiskCueCode(dep, "missing-pod-lock-resolution") {
		t.Fatalf("expected Podfile.lock risk cue, got %#v", dep.RiskCues)
	}
	if !hasRecommendationCode(dep, "refresh-podfile-lock") {
		t.Fatalf("expected Podfile.lock recommendation, got %#v", dep.Recommendations)
	}
	assertWarningContains(t, reportData.Warnings, podLockName+" not found")
}

func hasRiskCueCode(dep report.DependencyReport, code string) bool {
	for _, cue := range dep.RiskCues {
		if cue.Code == code {
			return true
		}
	}
	return false
}

func hasRecommendationCode(dep report.DependencyReport, code string) bool {
	for _, recommendation := range dep.Recommendations {
		if recommendation.Code == code {
			return true
		}
	}
	return false
}
