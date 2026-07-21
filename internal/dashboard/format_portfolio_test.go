package dashboard

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type testCycloneDXProperty struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type testCycloneDXComponent struct {
	Type       string                  `json:"type"`
	Name       string                  `json:"name"`
	BOMRef     string                  `json:"bom-ref"`
	Version    string                  `json:"version,omitempty"`
	PURL       string                  `json:"purl,omitempty"`
	Properties []testCycloneDXProperty `json:"properties,omitempty"`
}

type testCycloneDXBOM struct {
	BOMFormat  string                   `json:"bomFormat"`
	Components []testCycloneDXComponent `json:"components"`
	Properties []testCycloneDXProperty  `json:"properties"`
}

func formatPortfolioCycloneDXForTest(t *testing.T, reportData Report) (string, testCycloneDXBOM) {
	t.Helper()

	output, err := FormatReport(reportData, FormatCycloneDXJSON)
	if err != nil {
		t.Fatalf("format portfolio CycloneDX: %v", err)
	}
	var bom testCycloneDXBOM
	if err := json.Unmarshal([]byte(output), &bom); err != nil {
		t.Fatalf("decode portfolio CycloneDX: %v\n%s", err, output)
	}
	return output, bom
}

func TestFormatPortfolioCycloneDXIncludesReposDependenciesAndProperties(t *testing.T) {
	reportData := Report{
		GeneratedAt: time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC),
		Summary:     Summary{TotalRepos: 2},
		Repos: []RepoResult{
			{Name: "api service", Path: "services/api", ResolvedCommit: "abc123"},
			{Name: "web", Error: "clone failed"},
		},
		PortfolioComponents: []PortfolioComponent{{
			Repo:      "api service",
			Language:  "go",
			Ecosystem: "golang",
			Name:      "github.com/acme/lib",
			Version:   "v1.2.3",
			PURL:      "pkg:golang/github.com/acme/lib@v1.2.3",
		}},
	}

	_, bom := formatPortfolioCycloneDXForTest(t, reportData)
	if bom.BOMFormat != "CycloneDX" {
		t.Fatalf("expected CycloneDX BOM, got %#v", bom)
	}
	if !hasCycloneDXComponent(bom.Components, "application", "api service", "lopper:repo:api%20service:services%2Fapi", "abc123", "") {
		t.Fatalf("expected api repo component, got %#v", bom.Components)
	}
	if !hasCycloneDXComponent(bom.Components, "library", "github.com/acme/lib", "lopper:dependency:api%20service:go:github.com%2Facme%2Flib:v1.2.3", "v1.2.3", "pkg:golang/github.com/acme/lib@v1.2.3") {
		t.Fatalf("expected dependency component, got %#v", bom.Components)
	}
	assertCycloneDXProperty(t, bom.Properties, "lopper:dashboard:total-repos", "2")
	assertCycloneDXProperty(t, bom.Properties, "lopper:dashboard:partial-failures", "1")
}

func TestFormatPortfolioCycloneDXDisambiguatesDuplicateComponentRefsDeterministically(t *testing.T) {
	reportData := Report{
		PortfolioComponents: []PortfolioComponent{
			{
				Repo:      "api",
				RepoPath:  "services/api",
				Language:  "go",
				Ecosystem: "golang",
				Name:      "github.com/acme/lib",
				Version:   "v1.2.3",
				PURL:      "pkg:golang/github.com/acme/lib@v1.2.3?variant=b",
			},
			{
				Repo:      "api",
				RepoPath:  "services/api",
				Language:  "go",
				Ecosystem: "golang",
				Name:      "github.com/acme/lib",
				Version:   "v1.2.3",
				PURL:      "pkg:golang/github.com/acme/lib@v1.2.3?variant=a",
			},
		},
	}

	firstOutput, bom := formatPortfolioCycloneDXForTest(t, reportData)
	reportData.PortfolioComponents[0], reportData.PortfolioComponents[1] = reportData.PortfolioComponents[1], reportData.PortfolioComponents[0]
	secondOutput, _ := formatPortfolioCycloneDXForTest(t, reportData)
	if firstOutput != secondOutput {
		t.Fatalf("expected duplicate portfolio refs to stay stable across input order\nfirst:\n%s\nsecond:\n%s", firstOutput, secondOutput)
	}
	if !hasCycloneDXComponent(bom.Components, "library", "github.com/acme/lib", "lopper:dependency:api:services%2Fapi:go:github.com%2Facme%2Flib:v1.2.3", "v1.2.3", "pkg:golang/github.com/acme/lib@v1.2.3?variant=a") {
		t.Fatalf("expected base ref for deterministically first duplicate, got %#v", bom.Components)
	}
	if !hasCycloneDXComponent(bom.Components, "library", "github.com/acme/lib", "lopper:dependency:api:services%2Fapi:go:github.com%2Facme%2Flib:v1.2.3:2", "v1.2.3", "pkg:golang/github.com/acme/lib@v1.2.3?variant=b") {
		t.Fatalf("expected suffixed ref for second duplicate, got %#v", bom.Components)
	}
}

func TestFormatPortfolioCycloneDXKeepsBomRefsGloballyUniqueAndDeterministic(t *testing.T) {
	reportData := Report{
		Repos: []RepoResult{
			{Name: "api", Path: "services/api", ResolvedCommit: "aaa"},
			{Name: "api", Path: "apps/api", ResolvedCommit: "bbb"},
		},
		PortfolioComponents: []PortfolioComponent{
			{Repo: "api", RepoPath: "services/api", Language: "go", Ecosystem: "golang", Name: "github.com/acme/lib", Version: "v1.2.3"},
			{Repo: "api", RepoPath: "services/api", Language: "go", Ecosystem: "golang", Name: "github.com/acme/lib", Version: "v1.2.3"},
			{Repo: "api", RepoPath: "apps/api", Language: "go", Ecosystem: "golang", Name: "github.com/acme/lib", Version: "v1.2.3"},
		},
	}

	firstOutput, bom := formatPortfolioCycloneDXForTest(t, reportData)
	reversed := reportData
	reversed.Repos = []RepoResult{reportData.Repos[1], reportData.Repos[0]}
	reversed.PortfolioComponents = []PortfolioComponent{
		reportData.PortfolioComponents[2],
		reportData.PortfolioComponents[1],
		reportData.PortfolioComponents[0],
	}
	secondOutput, _ := formatPortfolioCycloneDXForTest(t, reversed)
	if firstOutput != secondOutput {
		t.Fatalf("expected globally unique bom-refs to stay deterministic across input order\nfirst:\n%s\nsecond:\n%s", firstOutput, secondOutput)
	}
	seen := map[string]struct{}{}
	for _, component := range bom.Components {
		if component.BOMRef == "" {
			t.Fatalf("expected component bom-ref, got %#v", component)
		}
		if _, exists := seen[component.BOMRef]; exists {
			t.Fatalf("expected JSON-wide unique bom-refs, duplicate=%q components=%#v", component.BOMRef, bom.Components)
		}
		seen[component.BOMRef] = struct{}{}
	}
}

func TestFormatPortfolioCycloneDXEscapesLiteralSuffixLikeNamesAndNormalizationCollisions(t *testing.T) {
	reportData := Report{
		Repos: []RepoResult{
			{Name: "x"},
			{Name: "x"},
			{Name: "x:2"},
			{Name: "api service"},
			{Name: "api-service"},
		},
	}

	_, bom := formatPortfolioCycloneDXForTest(t, reportData)
	wantRefs := map[string]struct{}{
		"lopper:repo:x":             {},
		"lopper:repo:x:2":           {},
		"lopper:repo:x%3A2":         {},
		"lopper:repo:api%20service": {},
		"lopper:repo:api-service":   {},
	}
	for _, component := range bom.Components {
		if _, ok := wantRefs[component.BOMRef]; !ok {
			t.Fatalf("unexpected bom-ref %q from %#v", component.BOMRef, bom.Components)
		}
		delete(wantRefs, component.BOMRef)
	}
	if len(wantRefs) != 0 {
		t.Fatalf("expected all adversarial refs to be present, missing %#v from %#v", wantRefs, bom.Components)
	}
}

func TestPortfolioRefAllocatorSkipsReservedAndUsedSuffixCollisions(t *testing.T) {
	allocator := newPortfolioRefAllocator([]string{
		"lopper:repo:api",
		"lopper:repo:api:2",
		"lopper:repo:api:3",
	})

	if got := allocator.allocate("lopper:repo:api"); got != "lopper:repo:api" {
		t.Fatalf("first allocate() = %q, want base ref", got)
	}

	if got := allocator.allocate("lopper:repo:api"); got != "lopper:repo:api:4" {
		t.Fatalf("second allocate() = %q, want first free suffix after reserved collisions", got)
	}

	if got := allocator.allocate("lopper:repo:api"); got != "lopper:repo:api:5" {
		t.Fatalf("third allocate() = %q, want suffix after reserved and used collisions", got)
	}
}

func TestJoinPortfolioRefPartsTrimsBlanksAndEscapesLiteralColons(t *testing.T) {
	if got := joinPortfolioRefParts(" api service ", "", "release:2", "services/api", "   "); got != "api%20service:release%3A2:services%2Fapi" {
		t.Fatalf("joinPortfolioRefParts() = %q", got)
	}
}

func TestFormatReportPreviewFormatsAndUnknown(t *testing.T) {
	reportData := Report{
		Summary: Summary{TotalRepos: 1},
		RemediationItems: []RemediationItem{{
			Repo:            "api",
			Category:        remediationCategoryVulnerability,
			Team:            "security",
			SuggestedAction: "Upgrade lib.",
		}},
	}
	for _, format := range []Format{FormatSlackSummary, FormatTeamsSummary, FormatCycloneDXJSON, FormatCSV} {
		output, err := FormatReport(reportData, format)
		if err != nil {
			t.Fatalf("format dashboard report %s: %v", format, err)
		}
		if output == "" {
			t.Fatalf("expected non-empty dashboard output for %s", format)
		}
	}
	if _, err := FormatReport(reportData, Format("xml")); err == nil {
		t.Fatalf("expected unknown dashboard format error")
	}
}

func TestSortPortfolioComponentsUsesStableTieBreakers(t *testing.T) {
	got := sortPortfolioComponents([]PortfolioComponent{
		{Repo: "web", Language: "js-ts", Name: "zeta", Version: "2.0.0"},
		{Repo: "api", Language: "go", Name: "lib", Version: "2.0.0"},
		{Repo: "api", Language: "go", Name: "lib", Version: "1.0.0"},
		{Repo: "api", Language: "python", Name: "alpha", Version: "1.0.0"},
	})
	want := []string{"api/go/lib/1.0.0", "api/go/lib/2.0.0", "api/python/alpha/1.0.0", "web/js-ts/zeta/2.0.0"}
	for index, component := range got {
		key := component.Repo + "/" + component.Language + "/" + component.Name + "/" + component.Version
		if key != want[index] {
			t.Fatalf("unexpected portfolio sort at %d: got %q want %q from %#v", index, key, want[index], got)
		}
	}
}

func TestFormatReportCSVIncludesBaselineAndCrossRepoSections(t *testing.T) {
	reportData := Report{
		GeneratedAt: time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC),
		Summary:     Summary{TotalRepos: 1, TotalDeps: 2},
		Repos:       []RepoResult{{Name: "api", Revision: &RepoRevision{Branch: "main"}, RuntimeTraceData: true}},
		RemediationItems: []RemediationItem{{
			ID:              "rem-1",
			Repo:            "api",
			Dependency:      "lib",
			Category:        remediationCategoryVulnerability,
			Evidence:        []string{"call path", "runtime"},
			SuggestedAction: "Upgrade lib.",
		}},
		CrossRepoDeps: []CrossRepoDependency{{Name: "shared-lib", Count: 2, Repositories: []string{"api", "web"}}},
		BaselineComparison: &BaselineComparison{
			BaselineKey: "base-1",
			CurrentKey:  "current-1",
			RepoDeltas: []RepoDelta{{
				Name:                        "api",
				Kind:                        RepoDeltaChanged,
				DependencyCountDelta:        1,
				RuntimeRegressionCountDelta: 1,
			}},
			RemediationItemDeltas: []RemediationItemDelta{{
				ID:         "rem-1",
				Status:     "new",
				Category:   remediationCategoryVulnerability,
				Repo:       "api",
				Dependency: "lib",
			}},
		},
	}

	output, err := FormatReport(reportData, FormatCSV)
	if err != nil {
		t.Fatalf("format CSV report: %v", err)
	}
	for _, want := range []string{
		"generated_at,2026-07-13T01:02:03Z",
		"remediation_id,baseline_status,repo,repo_path,dependency,category,owner,team,due,status,routing_source,severity,priority,evidence,suggested_action",
		"dependency_name,repo_count,repositories",
		"baseline_key,base-1",
		"current_key,current-1",
		"repo_name,repo_path,kind,dependency_count_delta,waste_candidate_count_delta,waste_candidate_percent_delta,critical_cves_delta,vulnerability_findings_delta,reachable_vulnerabilities_delta,denied_license_count_delta,runtime_regression_count_delta,runtime_improvement_count_delta,current_error,baseline_error",
		"shared-lib,2,api|web",
		"call path|runtime",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected CSV output to contain %q, got:\n%s", want, output)
		}
	}
}

func hasCycloneDXComponent(components []testCycloneDXComponent, componentType, name, bomRef, version, purl string) bool {
	for _, component := range components {
		if component.Type == componentType && component.Name == name && component.BOMRef == bomRef && component.Version == version && component.PURL == purl {
			return true
		}
	}
	return false
}

func assertCycloneDXProperty(t *testing.T, properties []testCycloneDXProperty, name, value string) {
	t.Helper()

	for _, property := range properties {
		if property.Name == name && property.Value == value {
			return
		}
	}
	t.Fatalf("expected property %s=%s, got %#v", name, value, properties)
}
