package report

import (
	"encoding/csv"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testJSONEncodingModule = "encoding/json"

func TestFormatCSV(t *testing.T) {
	reportData := Report{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   time.Date(2026, time.March, 30, 12, 34, 56, 0, time.UTC),
		RepoPath:      "/repo",
		Scope: &ScopeMetadata{
			Mode:     "package",
			Packages: []string{"packages/z", "packages/a"},
		},
		Dependencies: []DependencyReport{
			{
				Language:             "python",
				Name:                 "zeta",
				UsedExportsCount:     0,
				TotalExportsCount:    0,
				EstimatedUnusedBytes: 64,
			},
			{
				Language:             "go",
				Name:                 "alpha",
				UsedExportsCount:     2,
				TotalExportsCount:    4,
				EstimatedUnusedBytes: 2048,
				TopUsedSymbols: []SymbolUsage{
					{Name: "Println", Module: "fmt", Count: 2},
					{Name: "render", Count: 5},
				},
				UsedImports: []ImportUse{
					{Name: "Println", Module: "fmt"},
					{Name: "NewDecoder", Module: testJSONEncodingModule},
				},
				UnusedImports: []ImportUse{
					{Name: "Marshal", Module: testJSONEncodingModule},
				},
				UnusedExports: []SymbolRef{
					{Name: "Hidden", Module: "internal/alpha"},
				},
				RiskCues: []RiskCue{
					{Code: "deprecated-api", Severity: "medium"},
					{Code: "native-build", Severity: "high"},
				},
				Recommendations: []Recommendation{
					{Code: "trim-dependency", Priority: "medium"},
					{Code: "audit-runtime", Priority: "high"},
				},
				RuntimeUsage: &RuntimeUsage{
					LoadCount:   3,
					Correlation: RuntimeCorrelationOverlap,
					RuntimeOnly: true,
					Modules: []RuntimeModuleUsage{
						{Module: "alpha/runtime", Count: 1},
						{Module: "alpha/core", Count: 3},
					},
					TopSymbols: []RuntimeSymbolUsage{
						{Symbol: "New", Module: "alpha/core", Count: 2},
						{Symbol: "Run", Module: "alpha/runtime", Count: 1},
					},
				},
				ReachabilityConfidence: &ReachabilityConfidence{
					Model:          "v2",
					Score:          82.3,
					Summary:        "runtime overlap",
					RationaleCodes: []string{"runtime-correlation", "export-inventory"},
				},
				RemovalCandidate: &RemovalCandidate{
					Score:      77.1,
					Usage:      25.0,
					Impact:     60.0,
					Confidence: 82.3,
					Rationale:  []string{"low-usage", "runtime-overlap"},
				},
				License: &DependencyLicense{
					SPDX:       "MIT",
					Raw:        "MIT License",
					Source:     "go.mod",
					Confidence: "high",
					Denied:     true,
					Evidence:   []string{"license-file", "module-metadata"},
				},
				Provenance: &DependencyProvenance{
					Source:     "manifest",
					Confidence: "high",
					Signals:    []string{"checksum", "go-sum"},
				},
			},
		},
	}

	output, err := NewFormatter().Format(reportData, FormatCSV)
	if err != nil {
		t.Fatalf("format csv: %v", err)
	}

	rows := readCSVRows(t, output)
	if len(rows) != 3 {
		t.Fatalf("expected header and two dependency rows, got %d rows", len(rows))
	}
	if !reflect.DeepEqual(rows[0], analyseCSVHeader) {
		t.Fatalf("unexpected csv header: %#v", rows[0])
	}

	first := csvRowMap(rows[0], rows[1])
	if first["language"] != "go" || first["dependency_name"] != "alpha" {
		t.Fatalf("expected stable language/name row ordering, got %#v", first)
	}
	assertCSVRowHasValues(t, first, map[string]string{
		"generated_at":                 "2026-03-30T12:34:56Z",
		"scope_packages":               "packages/a|packages/z",
		"used_percent":                 "50.0",
		"waste_percent":                "50.0",
		"top_used_symbols":             "render=5|fmt:Println=2",
		"used_imports":                 testJSONEncodingModule + ":NewDecoder|fmt:Println",
		"risk_cues":                    "deprecated-api:medium|native-build:high",
		"recommendations":              "audit-runtime:high|trim-dependency:medium",
		"runtime_modules":              "alpha/core=3|alpha/runtime=1",
		"runtime_top_symbols":          "alpha/core:New=2|alpha/runtime:Run=1",
		"reachability_rationale_codes": "export-inventory|runtime-correlation",
		"removal_candidate_rationale":  "low-usage|runtime-overlap",
		"license_spdx":                 "MIT",
		"license_source":               "go.mod",
		"license_unknown":              "false",
		"license_denied":               "true",
		"license_evidence":             "license-file|module-metadata",
		"provenance_source":            "manifest",
		"provenance_signals":           "checksum|go-sum",
	})

	second := csvRowMap(rows[0], rows[2])
	if second["language"] != "python" || second["dependency_name"] != "zeta" {
		t.Fatalf("unexpected second row ordering: %#v", second)
	}
	assertCSVRowHasValues(t, second, map[string]string{
		"license_source":     "unknown",
		"license_confidence": "low",
		"license_unknown":    "true",
	})
}

func TestFormatCSVDependencyNamesSanitizeFormulaPrefixes(t *testing.T) {
	reportData := Report{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   time.Date(2026, time.March, 30, 12, 34, 56, 0, time.UTC),
		Dependencies: []DependencyReport{
			{Name: "=2+3", Language: "go"},
			{Name: "+cmd", Language: "go"},
			{Name: "-cmd", Language: "go"},
			{Name: "@cmd", Language: "go"},
			{Name: "\tcmd", Language: "go"},
			{Name: "\rcmd", Language: "go"},
		},
	}

	output, err := NewFormatter().Format(reportData, FormatCSV)
	if err != nil {
		t.Fatalf("format csv: %v", err)
	}

	rows := readCSVRows(t, output)
	if len(rows) != 7 {
		t.Fatalf("expected header and six dependency rows, got %d rows", len(rows))
	}

	rowByName := map[string]map[string]string{}
	for _, row := range rows[1:] {
		decoded := csvRowMap(rows[0], row)
		rowByName[decoded["dependency_name"]] = decoded
	}

	for _, name := range []string{"=2+3", "+cmd", "-cmd", "@cmd"} {
		if got, ok := rowByName["'"+name]; !ok {
			t.Fatalf("expected sanitized dependency name %q in csv rows, got %#v", "'"+name, rowByName)
		} else if got["dependency_name"] != "'"+name {
			t.Fatalf("expected dependency_name %q, got %q", "'"+name, got["dependency_name"])
		}
	}

	for _, name := range []string{"\tcmd", "\rcmd"} {
		if got, ok := rowByName["'"+name]; !ok {
			t.Fatalf("expected sanitized dependency name %q in csv rows, got %#v", "'"+name, rowByName)
		} else if got["dependency_name"] != "'"+name {
			t.Fatalf("expected dependency_name %q, got %q", "'"+name, got["dependency_name"])
		}
	}
}

func TestFormatCSVEmptyReport(t *testing.T) {
	output, err := NewFormatter().Format(Report{}, FormatCSV)
	if err != nil {
		t.Fatalf("format empty csv: %v", err)
	}

	rows := readCSVRows(t, output)
	if len(rows) != 1 {
		t.Fatalf("expected header-only csv for empty report, got %d rows", len(rows))
	}
	if !reflect.DeepEqual(rows[0], analyseCSVHeader) {
		t.Fatalf("unexpected csv header: %#v", rows[0])
	}
}

func TestFormatCSVTime(t *testing.T) {
	if got := formatCSVTime(time.Time{}); got != "" {
		t.Fatalf("expected empty zero time, got %q", got)
	}

	value := time.Date(2026, time.March, 30, 1, 2, 3, 0, time.UTC)
	if got := formatCSVTime(value); got != "2026-03-30T01:02:03Z" {
		t.Fatalf("unexpected formatted time: %q", got)
	}
}

func TestNormalizedDependencyLicenseCSV(t *testing.T) {
	got := normalizedDependencyLicenseCSV(&DependencyLicense{})
	if got.Source != licenseSourceUnknown || got.Confidence != "low" || !got.Unknown {
		t.Fatalf("expected normalized unknown license, got %#v", got)
	}
}

func TestSortedDependenciesForCSV(t *testing.T) {
	input := []DependencyReport{
		{Language: "ruby", Name: "zeta"},
		{Language: "go", Name: "beta"},
		{Language: "go", Name: "alpha"},
	}
	sorted := sortedDependenciesForCSV(input)
	if want := []string{"alpha", "beta", "zeta"}; sorted[0].Name != want[0] || sorted[1].Name != want[1] || sorted[2].Name != want[2] {
		t.Fatalf("unexpected sorted dependency order: %#v", sorted)
	}
	if input[0].Language != "ruby" || input[0].Name != "zeta" {
		t.Fatalf("expected input slice to remain unchanged, got %#v", input)
	}
}

func TestFormatCSVQualifiedValues(t *testing.T) {
	if got := formatCSVQualifiedName("", "plain"); got != "plain" {
		t.Fatalf("expected unqualified name, got %q", got)
	}
	if got := formatCSVSymbolRefs([]SymbolRef{{Name: "Visible"}, {Name: "Hidden", Module: "pkg"}}); got != "Visible|pkg:Hidden" {
		t.Fatalf("unexpected symbol refs formatting: %q", got)
	}
	if got := formatCSVImportUses([]ImportUse{{Name: "LocalOnly"}, {Name: "Decode", Module: testJSONEncodingModule}}); got != "LocalOnly|"+testJSONEncodingModule+":Decode" {
		t.Fatalf("unexpected import formatting: %q", got)
	}
}

func TestFormatCSVSortingHelpers(t *testing.T) {
	if got := formatCSVTopUsedSymbols([]SymbolUsage{
		{Name: "Beta", Module: "pkg", Count: 2},
		{Name: "Alpha", Module: "pkg", Count: 2},
		{Name: "Root", Count: 2},
	}); got != "Root=2|pkg:Alpha=2|pkg:Beta=2" {
		t.Fatalf("unexpected top symbol tie-break order: %q", got)
	}

	if got := formatCSVImportUses([]ImportUse{
		{Name: "Zulu", Module: "pkg"},
		{Name: "Alpha", Module: "pkg"},
	}); got != "pkg:Alpha|pkg:Zulu" {
		t.Fatalf("unexpected import tie-break order: %q", got)
	}

	if got := formatCSVRiskCues([]RiskCue{
		{Code: "same", Severity: "high"},
		{Code: "same", Severity: "low"},
	}); got != "same:high|same:low" {
		t.Fatalf("unexpected risk cue tie-break order: %q", got)
	}

	if got := formatCSVRecommendations([]Recommendation{
		{Code: "same", Priority: "medium"},
		{Code: "same", Priority: "high"},
	}); got != "same:high|same:medium" {
		t.Fatalf("unexpected recommendation tie-break order: %q", got)
	}

	if got := formatCSVRuntimeModules(&RuntimeUsage{Modules: []RuntimeModuleUsage{
		{Module: "zeta", Count: 2},
		{Module: "alpha", Count: 2},
	}}); got != "alpha=2|zeta=2" {
		t.Fatalf("unexpected runtime module tie-break order: %q", got)
	}

	if got := formatCSVRuntimeTopSymbols(&RuntimeUsage{TopSymbols: []RuntimeSymbolUsage{
		{Symbol: "Zulu", Module: "pkg", Count: 2},
		{Symbol: "Alpha", Module: "pkg", Count: 2},
		{Symbol: "Root", Count: 2},
	}}); got != "Root=2|pkg:Alpha=2|pkg:Zulu=2" {
		t.Fatalf("unexpected runtime symbol tie-break order: %q", got)
	}
}

func TestRuntimeCorrelationValue(t *testing.T) {
	assertRuntimeCorrelationValue(t, nil, "")
	assertRuntimeCorrelationValue(t, &RuntimeUsage{Correlation: RuntimeCorrelationRuntimeOnly}, string(RuntimeCorrelationRuntimeOnly))
	assertRuntimeCorrelationValue(t, &RuntimeUsage{RuntimeOnly: true}, string(RuntimeCorrelationRuntimeOnly))
	assertRuntimeCorrelationValue(t, &RuntimeUsage{LoadCount: 2}, string(RuntimeCorrelationOverlap))
	assertRuntimeCorrelationValue(t, &RuntimeUsage{}, string(RuntimeCorrelationStaticOnly))
}

func TestFormatCSVRemovalCandidateMetric(t *testing.T) {
	candidate := &RemovalCandidate{Usage: 1.5, Impact: 2.5, Confidence: 3.5}
	if got := formatCSVRemovalCandidateMetric(candidate, "usage"); got != "1.5" {
		t.Fatalf("unexpected usage metric: %q", got)
	}
	if got := formatCSVRemovalCandidateMetric(candidate, "impact"); got != "2.5" {
		t.Fatalf("unexpected impact metric: %q", got)
	}
	if got := formatCSVRemovalCandidateMetric(candidate, "confidence"); got != "3.5" {
		t.Fatalf("unexpected confidence metric: %q", got)
	}
	if got := formatCSVRemovalCandidateMetric(candidate, "mystery"); got != "" {
		t.Fatalf("expected empty metric for unknown field, got %q", got)
	}
}

func readCSVRows(t *testing.T, output string) [][]string {
	t.Helper()
	rows, err := csv.NewReader(strings.NewReader(output)).ReadAll()
	if err != nil {
		t.Fatalf("read csv output: %v", err)
	}
	return rows
}

func csvRowMap(header, row []string) map[string]string {
	result := make(map[string]string, len(header))
	for i, name := range header {
		result[name] = row[i]
	}
	return result
}

func assertCSVRowHasValues(t *testing.T, got map[string]string, expected map[string]string) {
	t.Helper()
	for key, value := range expected {
		if got[key] != value {
			t.Fatalf("unexpected value for %q: got %q want %q", key, got[key], value)
		}
	}
}

func assertRuntimeCorrelationValue(t *testing.T, usage *RuntimeUsage, want string) {
	t.Helper()
	if got := runtimeCorrelationValue(usage); got != want {
		t.Fatalf("runtimeCorrelationValue(%#v)=%q want %q", usage, got, want)
	}
}
