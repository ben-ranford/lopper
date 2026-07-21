package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestFormatSPDXJSONIncludesIdentityAndLicense(t *testing.T) {
	reportData := Report{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   time.Date(2026, time.July, 13, 0, 0, 0, 0, time.UTC),
		RepoPath:      "/repo",
		Dependencies: []DependencyReport{{
			Name:     "left-pad",
			Language: "js-ts",
			Identity: &DependencyIdentity{
				Ecosystem:  "npm",
				Name:       "left-pad",
				Version:    "1.3.0",
				PURL:       "pkg:npm/left-pad@1.3.0",
				Confidence: "high",
			},
			License: &DependencyLicense{SPDX: "MIT"},
		}},
	}

	output, err := formatSPDXJSON(reportData)
	if err != nil {
		t.Fatalf("format SPDX JSON: %v", err)
	}

	var doc struct {
		SPDXVersion string `json:"spdxVersion"`
		Packages    []struct {
			Name            string            `json:"name"`
			VersionInfo     string            `json:"versionInfo"`
			LicenseDeclared string            `json:"licenseDeclared"`
			ExternalRefs    []spdxExternalRef `json:"externalRefs"`
		} `json:"packages"`
	}
	if err := json.Unmarshal([]byte(output), &doc); err != nil {
		t.Fatalf("parse SPDX JSON: %v", err)
	}
	if doc.SPDXVersion != "SPDX-2.3" || len(doc.Packages) != 1 {
		t.Fatalf("unexpected SPDX document: %#v", doc)
	}
	pkg := doc.Packages[0]
	if pkg.Name != "left-pad" || pkg.VersionInfo != "1.3.0" || pkg.LicenseDeclared != "MIT" {
		t.Fatalf("unexpected SPDX package: %#v", pkg)
	}
	if len(pkg.ExternalRefs) != 1 || pkg.ExternalRefs[0].ReferenceLocator != "pkg:npm/left-pad@1.3.0" {
		t.Fatalf("expected SPDX purl external ref, got %#v", pkg.ExternalRefs)
	}
}

func TestParseAndFormatPreviewReportFormats(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  Format
	}{
		{value: "spdx-json", want: FormatSPDX},
		{value: "cyclonedx-vex-json", want: FormatVEX},
	} {
		got, err := ParseFormat(tc.value)
		if err != nil || got != tc.want {
			t.Fatalf("parse format %q got %q err=%v", tc.value, got, err)
		}
		formatted, err := NewFormatter().Format(Report{}, got)
		if err != nil {
			t.Fatalf("format %q: %v", got, err)
		}
		if !strings.Contains(formatted, "lopper") {
			t.Fatalf("expected formatted %q output to contain lopper metadata, got %q", got, formatted)
		}
	}
	if _, err := ParseFormat("bogus"); !errors.Is(err, ErrUnknownFormat) {
		t.Fatalf("expected unknown format error, got %v", err)
	}
}

func TestFormatCycloneDXVEXJSONUsesCanonicalExportFormatMetadata(t *testing.T) {
	for _, tc := range []struct {
		name     string
		selector string
	}{
		{name: "canonical selector", selector: "cyclonedx-vex-json"},
		{name: "uppercase alias", selector: "CycloneDX-VEX-JSON"},
		{name: "whitespace-normalized alias", selector: "  CycloneDX-VEX-JSON\t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			format, err := ParseFormat(tc.selector)
			if err != nil {
				t.Fatalf("parse VEX selector %q: %v", tc.selector, err)
			}
			if format != FormatVEX {
				t.Fatalf("expected VEX format for %q, got %q", tc.selector, format)
			}

			output, err := NewFormatter().Format(Report{}, format)
			if err != nil {
				t.Fatalf("format VEX output for %q: %v", tc.selector, err)
			}

			bom := decodeCycloneDXBOM(t, output)
			requireCycloneDXProperty(t, bom.Properties, "lopper:export:format", string(FormatVEX))
		})
	}

	output, err := NewFormatter().Format(Report{}, FormatVEX)
	if err != nil {
		t.Fatalf("format canonical VEX output directly: %v", err)
	}
	bom := decodeCycloneDXBOM(t, output)
	requireCycloneDXProperty(t, bom.Properties, "lopper:export:format", string(FormatVEX))
}

func TestFormatSPDXJSONUsesFallbacksAndAnnotations(t *testing.T) {
	reportData := Report{
		SchemaVersion: SchemaVersion,
		Warnings:      []string{"z warning", "a warning"},
		Dependencies: []DependencyReport{
			{Name: "", Language: "go", License: &DependencyLicense{SPDX: "MIT", Unknown: true}},
			{Name: "", Language: "go"},
		},
	}

	output, err := formatSPDXJSON(reportData)
	if err != nil {
		t.Fatalf("format SPDX fallback JSON: %v", err)
	}

	var doc struct {
		Name          string             `json:"name"`
		CreationInfo  spdxCreationInfo   `json:"creationInfo"`
		Packages      []spdxPackage      `json:"packages"`
		Relationships []spdxRelationship `json:"relationships"`
		Annotations   []spdxAnnotation   `json:"annotations"`
	}
	if err := json.Unmarshal([]byte(output), &doc); err != nil {
		t.Fatalf("parse SPDX fallback JSON: %v", err)
	}
	if doc.Name != "lopper-analysis" || doc.CreationInfo.Created != "1970-01-01T00:00:00Z" {
		t.Fatalf("unexpected SPDX document fallback metadata: %#v", doc)
	}
	if len(doc.Packages) != 2 || doc.Packages[0].Name != "unknown" || doc.Packages[0].LicenseDeclared != "NOASSERTION" {
		t.Fatalf("unexpected SPDX fallback packages: %#v", doc.Packages)
	}
	if doc.Packages[0].SPDXID == doc.Packages[1].SPDXID {
		t.Fatalf("expected duplicate SPDX package IDs to be disambiguated: %#v", doc.Packages)
	}
	assertSPDXPackagesDescribedOnce(t, doc.Packages, doc.Relationships)
	if len(doc.Annotations) != 1 || doc.Annotations[0].Comment != "a warning | z warning" {
		t.Fatalf("unexpected SPDX annotations: %#v", doc.Annotations)
	}
}

func assertSPDXPackagesDescribedOnce(t *testing.T, packages []spdxPackage, relationships []spdxRelationship) {
	t.Helper()

	relationshipCounts := map[string]int{}
	for _, relationship := range relationships {
		if relationship.SPDXElementID != spdxDocumentRef || relationship.RelationshipType != "DESCRIBES" {
			t.Fatalf("unexpected SPDX relationship: %#v", relationship)
		}
		relationshipCounts[relationship.RelatedSPDXElement]++
	}
	for _, pkg := range packages {
		if relationshipCounts[pkg.SPDXID] != 1 {
			t.Fatalf("expected SPDX package %s to be described exactly once, relationships=%#v", pkg.SPDXID, relationships)
		}
	}
}

func TestPreviewReportMetadataEdgeHelpers(t *testing.T) {
	attributions := spdxAttributionTexts(DependencyReport{
		Identity:               &DependencyIdentity{VersionStatus: "declared", Source: "go.mod"},
		ReachabilityConfidence: &ReachabilityConfidence{Summary: "strong static evidence"},
	})
	if strings.Join(attributions, "|") != "lopper:versionStatus=declared|lopper:identitySource=go.mod|lopper:reachability=strong static evidence" {
		t.Fatalf("unexpected SPDX attribution texts: %#v", attributions)
	}
	if got := firstSPDXValue(" ", "\t"); got != "" {
		t.Fatalf("expected blank SPDX fallback, got %q", got)
	}
	source := cycloneDXVulnerabilitySourceForFinding(VulnerabilityFinding{Source: "osv"})
	if source == nil || source.Name != "osv" {
		t.Fatalf("unexpected CycloneDX vulnerability source: %#v", source)
	}
	if row := formatDependencyIdentityCSVRow(nil); len(row) != len(analyseCSVIdentityHeader) {
		t.Fatalf("expected nil identity CSV row to match identity header, got %#v", row)
	}
	if !isCycloneDXEmptyJSONProperty(nil) || !isCycloneDXEmptyJSONProperty([]string{}) {
		t.Fatalf("expected nil and empty slices to be empty CycloneDX JSON properties")
	}
	var props []cycloneDXProperty
	appendCycloneDXJSONProperty(&props, "lopper:test", func() {})
	if len(props) != 0 {
		t.Fatalf("expected unmarshalable CycloneDX property to be skipped, got %#v", props)
	}
	if got := CycloneDXVEXJustification(" CODE_NOT_REACHABLE "); got != "code_not_reachable" {
		t.Fatalf("expected exported VEX justification helper to normalize known value, got %q", got)
	}
	if got := CycloneDXVEXJustification("unsupported"); got != "" {
		t.Fatalf("expected unsupported VEX justification to be omitted, got %q", got)
	}
}

func TestFormatCycloneDXVEXJSONIncludesDecisions(t *testing.T) {
	reportData := Report{
		GeneratedAt: time.Date(2026, time.July, 13, 0, 0, 0, 0, time.UTC),
		Dependencies: []DependencyReport{{
			Name:     "lib",
			Language: "go",
			Identity: &DependencyIdentity{
				Ecosystem: "golang",
				Name:      "example.com/lib",
				Version:   "1.0.0",
				PURL:      "pkg:golang/example.com/lib@1.0.0",
			},
			Vulnerabilities: []VulnerabilityFinding{{
				AdvisoryID: "GHSA-test",
				Package:    "example.com/lib",
				Severity:   "high",
				Priority:   "critical",
				Reachable:  true,
				Decision: &VulnerabilityExceptionDecision{
					Status:        "not-affected",
					Justification: "code_not_reachable",
					Owner:         "security",
					Reason:        "unused vulnerable path",
					Expires:       "2026-08-01",
				},
			}},
		}},
	}

	output, err := formatCycloneDXVEXJSON(reportData)
	if err != nil {
		t.Fatalf("format VEX JSON: %v", err)
	}

	var bom struct {
		Vulnerabilities []struct {
			ID       string `json:"id"`
			Analysis struct {
				State         string `json:"state"`
				Justification string `json:"justification"`
				Detail        string `json:"detail"`
			} `json:"analysis"`
			Affects []struct {
				Ref string `json:"ref"`
			} `json:"affects"`
		} `json:"vulnerabilities"`
	}
	if err := json.Unmarshal([]byte(output), &bom); err != nil {
		t.Fatalf("parse VEX JSON: %v", err)
	}
	if len(bom.Vulnerabilities) != 1 {
		t.Fatalf("expected one VEX vulnerability, got %#v", bom.Vulnerabilities)
	}
	got := bom.Vulnerabilities[0]
	if got.ID != "GHSA-test" || got.Analysis.State != "not_affected" || got.Analysis.Justification != "code_not_reachable" {
		t.Fatalf("unexpected VEX vulnerability: %#v", got)
	}
	if len(got.Affects) != 1 || got.Affects[0].Ref == "" {
		t.Fatalf("expected VEX affected component ref, got %#v", got.Affects)
	}
}

func TestFormatCycloneDXVEXJSONMapsSupportedJustificationsAndOmitsUnsupportedValues(t *testing.T) {
	type analysisView struct {
		State         string
		Justification string
		Detail        string
	}

	supported := []string{
		"code_not_present",
		"code_not_reachable",
		"requires_configuration",
		"requires_dependency",
		"requires_environment",
		"protected_by_compiler",
		"protected_at_runtime",
		"protected_at_perimeter",
		"protected_by_mitigating_control",
	}

	dependencies := make([]DependencyReport, 0, len(supported)+1)
	for _, justification := range supported {
		dependencies = append(dependencies, DependencyReport{
			Name:     justification,
			Language: "go",
			Vulnerabilities: []VulnerabilityFinding{{
				AdvisoryID: justification,
				Package:    justification,
				Decision: &VulnerabilityExceptionDecision{
					Status:        "not-affected",
					Justification: " " + strings.ToUpper(justification) + " ",
					Reason:        "reason for " + justification,
				},
			}},
		})
	}
	dependencies = append(dependencies, DependencyReport{
		Name:     "unsupported",
		Language: "go",
		Vulnerabilities: []VulnerabilityFinding{{
			AdvisoryID: "unsupported",
			Package:    "unsupported",
			Decision: &VulnerabilityExceptionDecision{
				Status:        "not-affected",
				Justification: "vulnerable code path is not present",
				Reason:        "reason for unsupported",
			},
		}},
	})

	output, err := formatCycloneDXVEXJSON(Report{Dependencies: dependencies})
	if err != nil {
		t.Fatalf("format VEX justifications JSON: %v", err)
	}

	var bom struct {
		Vulnerabilities []struct {
			ID       string `json:"id"`
			Analysis struct {
				State         string `json:"state"`
				Justification string `json:"justification,omitempty"`
				Detail        string `json:"detail"`
			} `json:"analysis"`
		} `json:"vulnerabilities"`
	}
	if err := json.Unmarshal([]byte(output), &bom); err != nil {
		t.Fatalf("parse VEX justifications JSON: %v", err)
	}

	byID := make(map[string]analysisView, len(bom.Vulnerabilities))
	for _, vulnerability := range bom.Vulnerabilities {
		byID[vulnerability.ID] = analysisView{
			State:         vulnerability.Analysis.State,
			Justification: vulnerability.Analysis.Justification,
			Detail:        vulnerability.Analysis.Detail,
		}
	}
	for _, justification := range supported {
		got := byID[justification]
		if got.State != "not_affected" || got.Justification != justification || got.Detail != "reason for "+justification {
			t.Fatalf("unexpected supported VEX justification mapping for %q: %#v", justification, got)
		}
	}
	if got := byID["unsupported"]; got.Justification != "" || got.Detail != "reason for unsupported" {
		t.Fatalf("expected unsupported VEX justification to be omitted and detail preserved, got %#v", got)
	}
}

func TestFormatCycloneDXVEXJSONMapsDecisionStatesAndResponses(t *testing.T) {
	reportData := Report{Dependencies: []DependencyReport{
		vexTestDependency("accepted", "accepted-risk", false),
		vexTestDependency("resolved", "resolved", false),
		vexTestDependency("investigate", "under-investigation", false),
		vexTestDependency("expired", "accepted-risk", true),
	}}

	output, err := formatCycloneDXVEXJSON(reportData)
	if err != nil {
		t.Fatalf("format VEX decision JSON: %v", err)
	}

	var bom struct {
		Vulnerabilities []struct {
			ID       string `json:"id"`
			Analysis struct {
				State    string   `json:"state"`
				Response []string `json:"response"`
				Detail   string   `json:"detail"`
			} `json:"analysis"`
		} `json:"vulnerabilities"`
	}
	if err := json.Unmarshal([]byte(output), &bom); err != nil {
		t.Fatalf("parse VEX decision JSON: %v", err)
	}
	byID := map[string]struct {
		State    string
		Response []string
		Detail   string
	}{}
	for _, item := range bom.Vulnerabilities {
		byID[item.ID] = struct {
			State    string
			Response []string
			Detail   string
		}{State: item.Analysis.State, Response: item.Analysis.Response, Detail: item.Analysis.Detail}
	}
	if byID["accepted"].State != "exploitable" || len(byID["accepted"].Response) != 0 {
		t.Fatalf("unexpected accepted-risk VEX mapping: %#v", byID["accepted"])
	}
	if byID["resolved"].State != "resolved" || strings.Join(byID["resolved"].Response, ",") != "update" {
		t.Fatalf("unexpected resolved VEX mapping: %#v", byID["resolved"])
	}
	if byID["investigate"].State != "in_triage" || strings.Join(byID["investigate"].Response, ",") != "update" {
		t.Fatalf("unexpected under-investigation VEX mapping: %#v", byID["investigate"])
	}
	if byID["expired"].State != "exploitable" || byID["expired"].Detail != "reachable=false" {
		t.Fatalf("unexpected expired VEX mapping: %#v", byID["expired"])
	}
}

func TestFormatCycloneDXVEXJSONValidatesAgainstPinnedSchema(t *testing.T) {
	blockCycloneDXSchemaNetwork(t)
	schema := loadCycloneDXSchema(t)

	output, err := formatCycloneDXVEXJSON(Report{
		Dependencies: []DependencyReport{
			vexTestDependency("supported", "not-affected", false),
			{
				Name:     "unsupported",
				Language: "go",
				Vulnerabilities: []VulnerabilityFinding{{
					AdvisoryID: "unsupported",
					Package:    "unsupported",
					Decision: &VulnerabilityExceptionDecision{
						Status:        "not-affected",
						Justification: "free form explanation",
						Reason:        "detail text survives",
					},
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("format schema-valid VEX JSON: %v", err)
	}
	if err := validateCycloneDXSchema(schema, output); err != nil {
		t.Fatalf("VEX output failed official 1.6 schema validation: %v", err)
	}
}

func TestFormatCycloneDXVEXJSONDuplicateDependenciesKeepUniqueRefsAndAffects(t *testing.T) {
	blockCycloneDXSchemaNetwork(t)
	schema := loadCycloneDXSchema(t)

	reportData := Report{Dependencies: []DependencyReport{
		{
			Name:     "duplicate",
			Language: "js-ts",
			Identity: &DependencyIdentity{Version: "2.0.0", PURL: "pkg:npm/duplicate@2.0.0"},
			Vulnerabilities: []VulnerabilityFinding{
				{AdvisoryID: "GHSA-shared", Package: "duplicate", Source: "osv", Severity: "high", Priority: "high", PriorityScore: 9},
				{AdvisoryID: "GHSA-second", Package: "duplicate", Source: "osv", Severity: "medium", Priority: "medium", PriorityScore: 5},
			},
		},
		{
			Name:     "duplicate",
			Language: "js-ts",
			Identity: &DependencyIdentity{Version: "1.0.0", PURL: "pkg:npm/duplicate@1.0.0"},
			Vulnerabilities: []VulnerabilityFinding{
				{AdvisoryID: "GHSA-shared", Package: "duplicate", Source: "osv", Severity: "critical", Priority: "critical", PriorityScore: 10},
				{AdvisoryID: "GHSA-first", Package: "duplicate", Source: "osv", Severity: "low", Priority: "low", PriorityScore: 1},
			},
		},
	}}

	output, err := formatCycloneDXVEXJSON(reportData)
	if err != nil {
		t.Fatalf("format duplicate VEX JSON: %v", err)
	}
	if err := validateCycloneDXSchema(schema, output); err != nil {
		t.Fatalf("duplicate VEX output failed official 1.6 schema validation: %v", err)
	}

	var bom cycloneDXBOM
	if err := json.Unmarshal([]byte(output), &bom); err != nil {
		t.Fatalf("parse duplicate VEX JSON: %v", err)
	}
	if len(bom.Components) != 2 || len(bom.Vulnerabilities) != 4 {
		t.Fatalf("unexpected duplicate VEX BOM: %#v", bom)
	}

	componentRefs := duplicateComponentRefs(t, bom.Components)
	assertDuplicateVEXVulnerabilityRefs(t, bom.Vulnerabilities, componentRefs)
}

func TestFormatCycloneDXVEXJSONKeepsLiteralSuffixLikeComponentRefsAndAffects(t *testing.T) {
	blockCycloneDXSchemaNetwork(t)
	schema := loadCycloneDXSchema(t)

	reportData := Report{Dependencies: []DependencyReport{
		{
			Name:     "x",
			Language: "js-ts",
			Identity: &DependencyIdentity{Version: "1.0.0", PURL: "pkg:npm/x@1.0.0"},
			Vulnerabilities: []VulnerabilityFinding{
				{AdvisoryID: "GHSA-x-first", Package: "x", Source: "osv", Severity: "high", Priority: "high", PriorityScore: 9},
			},
		},
		{
			Name:     "x",
			Language: "js-ts",
			Identity: &DependencyIdentity{Version: "1.1.0", PURL: "pkg:npm/x@1.1.0"},
			Vulnerabilities: []VulnerabilityFinding{
				{AdvisoryID: "GHSA-x-second", Package: "x", Source: "osv", Severity: "medium", Priority: "medium", PriorityScore: 5},
			},
		},
		{
			Name:     "x:2",
			Language: "js-ts",
			Identity: &DependencyIdentity{Version: "2.0.0", PURL: "pkg:npm/x-colon-two@2.0.0"},
			Vulnerabilities: []VulnerabilityFinding{
				{AdvisoryID: "GHSA-x-literal", Package: "x:2", Source: "osv", Severity: "low", Priority: "low", PriorityScore: 1},
			},
		},
	}}

	output, err := formatCycloneDXVEXJSON(reportData)
	if err != nil {
		t.Fatalf("format adversarial duplicate VEX JSON: %v", err)
	}
	if err := validateCycloneDXSchema(schema, output); err != nil {
		t.Fatalf("adversarial duplicate VEX output failed official 1.6 schema validation: %v", err)
	}

	var bom cycloneDXBOM
	if err := json.Unmarshal([]byte(output), &bom); err != nil {
		t.Fatalf("parse adversarial duplicate VEX JSON: %v", err)
	}
	if len(bom.Components) != 3 || len(bom.Vulnerabilities) != 3 {
		t.Fatalf("unexpected adversarial duplicate VEX BOM: %#v", bom)
	}

	seenRefs := adversarialUniqueBOMRefs(t, bom.Components, bom.Vulnerabilities)
	componentRefs := adversarialComponentRefs(t, bom.Components, seenRefs)
	if componentRefs["1.0.0"] != "lopper:dependency:js-ts:x" ||
		componentRefs["1.1.0"] != "lopper:dependency:js-ts:x:3" ||
		componentRefs["2.0.0"] != "lopper:dependency:js-ts:x:2" {
		t.Fatalf("unexpected adversarial duplicate component refs: %#v", componentRefs)
	}

	assertAdversarialVEXAffects(t, bom.Vulnerabilities, map[string]string{
		"GHSA-x-first":   componentRefs["1.0.0"],
		"GHSA-x-second":  componentRefs["1.1.0"],
		"GHSA-x-literal": componentRefs["2.0.0"],
	})
}

func duplicateComponentRefs(t *testing.T, components []cycloneDXComponent) map[string]string {
	t.Helper()
	componentRefs := map[string]string{}
	for _, component := range components {
		componentRefs[component.Version] = component.BOMRef
	}
	if componentRefs["1.0.0"] != "lopper:dependency:js-ts:duplicate" || componentRefs["2.0.0"] != "lopper:dependency:js-ts:duplicate:2" {
		t.Fatalf("unexpected duplicate component refs: %#v", componentRefs)
	}
	return componentRefs
}

func adversarialUniqueBOMRefs(t *testing.T, components []cycloneDXComponent, vulnerabilities []cycloneDXVulnerability) map[string]struct{} {
	t.Helper()
	seenRefs := map[string]struct{}{}
	for _, component := range components {
		if _, exists := seenRefs[component.BOMRef]; exists {
			t.Fatalf("expected globally unique bom-refs across components and vulnerabilities, duplicate component=%q components=%#v vulnerabilities=%#v", component.BOMRef, components, vulnerabilities)
		}
		seenRefs[component.BOMRef] = struct{}{}
	}
	for _, vulnerability := range vulnerabilities {
		if _, exists := seenRefs[vulnerability.BOMRef]; exists {
			t.Fatalf("expected globally unique bom-refs across components and vulnerabilities, duplicate vulnerability=%q components=%#v vulnerabilities=%#v", vulnerability.BOMRef, components, vulnerabilities)
		}
		seenRefs[vulnerability.BOMRef] = struct{}{}
	}
	return seenRefs
}

func adversarialComponentRefs(t *testing.T, components []cycloneDXComponent, seenRefs map[string]struct{}) map[string]string {
	t.Helper()
	componentRefs := map[string]string{}
	for _, component := range components {
		if _, exists := seenRefs[component.BOMRef]; !exists {
			t.Fatalf("expected prevalidated component bom-ref %q in seen set %#v", component.BOMRef, seenRefs)
		}
		componentRefs[component.Version] = component.BOMRef
	}
	return componentRefs
}

func assertAdversarialVEXAffects(t *testing.T, vulnerabilities []cycloneDXVulnerability, wantAffects map[string]string) {
	t.Helper()
	for _, vulnerability := range vulnerabilities {
		if len(vulnerability.Affects) != 1 {
			t.Fatalf("expected one affected ref per vulnerability, got %#v", vulnerability)
		}
		wantRef, ok := wantAffects[vulnerability.ID]
		if !ok {
			t.Fatalf("unexpected vulnerability %#v from %#v", vulnerability, vulnerabilities)
		}
		if vulnerability.Affects[0].Ref != wantRef {
			t.Fatalf("expected vulnerability %q to affect %q, got %#v", vulnerability.ID, wantRef, vulnerability)
		}
		delete(wantAffects, vulnerability.ID)
	}
	if len(wantAffects) != 0 {
		t.Fatalf("expected all adversarial vulnerabilities to be asserted, missing %#v from %#v", wantAffects, vulnerabilities)
	}
}

func assertDuplicateVEXVulnerabilityRefs(t *testing.T, vulnerabilities []cycloneDXVulnerability, componentRefs map[string]string) {
	t.Helper()
	var sharedRefs []string
	seenVulnerabilityRefs := map[string]struct{}{}
	for _, vulnerability := range vulnerabilities {
		assertUniqueVEXVulnerabilityRef(t, seenVulnerabilityRefs, vulnerability)
		sharedRefs = append(sharedRefs, duplicateSharedVulnerabilityRef(t, vulnerability, componentRefs)...)
	}
	if len(sharedRefs) != 2 {
		t.Fatalf("expected shared advisory on both duplicate components, got %#v", sharedRefs)
	}
	if (sharedRefs[0] != componentRefs["1.0.0"] || sharedRefs[1] != componentRefs["2.0.0"]) &&
		(sharedRefs[1] != componentRefs["1.0.0"] || sharedRefs[0] != componentRefs["2.0.0"]) {
		t.Fatalf("expected shared advisory affects refs to map to both duplicate components, got %#v", sharedRefs)
	}
}

func assertUniqueVEXVulnerabilityRef(t *testing.T, seen map[string]struct{}, vulnerability cycloneDXVulnerability) {
	t.Helper()
	if _, ok := seen[vulnerability.BOMRef]; ok {
		t.Fatalf("expected unique vulnerability refs, got duplicate %q", vulnerability.BOMRef)
	}
	seen[vulnerability.BOMRef] = struct{}{}
	if len(vulnerability.Affects) != 1 {
		t.Fatalf("expected one affected ref per vulnerability, got %#v", vulnerability)
	}
}

func duplicateSharedVulnerabilityRef(t *testing.T, vulnerability cycloneDXVulnerability, componentRefs map[string]string) []string {
	t.Helper()
	switch vulnerability.ID {
	case "GHSA-shared":
		return []string{vulnerability.Affects[0].Ref}
	case "GHSA-first":
		if vulnerability.Affects[0].Ref != componentRefs["1.0.0"] {
			t.Fatalf("expected first advisory to affect v1 component, got %#v", vulnerability)
		}
	case "GHSA-second":
		if vulnerability.Affects[0].Ref != componentRefs["2.0.0"] {
			t.Fatalf("expected second advisory to affect v2 component, got %#v", vulnerability)
		}
	}
	return nil
}

func TestSPDXAnnotationRejectsUnknownJSONFields(t *testing.T) {
	decoder := json.NewDecoder(bytes.NewBufferString(`{
		"annotationDate":"2026-07-13T00:00:00Z",
		"annotationType":"OTHER",
		"annotator":"Tool: lopper",
		"comment":"fixture",
		"unexpected":"value"
	}`))
	decoder.DisallowUnknownFields()

	var annotation spdxAnnotation
	if err := decoder.Decode(&annotation); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown SPDX annotation field to fail strict decode, got %v", err)
	}
}

func vexTestDependency(name, status string, expired bool) DependencyReport {
	return DependencyReport{
		Name:     name,
		Language: "go",
		Vulnerabilities: []VulnerabilityFinding{{
			AdvisoryID: name,
			Package:    name,
			Decision: &VulnerabilityExceptionDecision{
				Status:        status,
				Justification: "code_not_reachable",
				Reason:        "fixture",
				Expired:       expired,
			},
		}},
	}
}
