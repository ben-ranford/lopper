package report

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xeipuuv/gojsonschema"
)

const cycloneDXSchemaFixtureDirectory = "../../testdata/report/cyclonedx-1.6"

type cycloneDXSchemaMetadata struct {
	Upstream string                        `json:"upstream"`
	Tag      string                        `json:"tag"`
	Commit   string                        `json:"commit"`
	License  string                        `json:"license"`
	Files    []cycloneDXSchemaMetadataFile `json:"files"`
}

type cycloneDXSchemaMetadataFile struct {
	Path         string `json:"path"`
	UpstreamPath string `json:"upstreamPath"`
	GitBlobSHA1  string `json:"gitBlobSHA1"`
	SHA256       string `json:"sha256"`
}

type cycloneDXRejectNetworkRoundTripper struct{}

func (*cycloneDXRejectNetworkRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("cyclonedx schema validation attempted network access: %s", request.URL)
}

func TestCycloneDXSchemaMetadataChecksums(t *testing.T) {
	metadataPath := cycloneDXSchemaFilePath(t, "schema-metadata.json")
	payload, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read CycloneDX schema metadata: %v", err)
	}

	var metadata cycloneDXSchemaMetadata
	if err := json.Unmarshal(payload, &metadata); err != nil {
		t.Fatalf("decode CycloneDX schema metadata: %v", err)
	}
	if metadata.Upstream != "https://github.com/CycloneDX/specification" || metadata.Tag != "1.6" || metadata.Commit != "55343ba19dee1785acf1ce9191540d5fd7b590db" || metadata.License != "Apache-2.0" {
		t.Fatalf("unexpected CycloneDX schema provenance: %#v", metadata)
	}
	if len(metadata.Files) != 3 {
		t.Fatalf("expected root and two referenced CycloneDX schemas, got %d", len(metadata.Files))
	}

	for _, file := range metadata.Files {
		if file.UpstreamPath == "" || file.GitBlobSHA1 == "" || file.SHA256 == "" {
			t.Fatalf("incomplete CycloneDX schema metadata for %q: %#v", file.Path, file)
		}
		contents, err := os.ReadFile(cycloneDXSchemaFilePath(t, file.Path))
		if err != nil {
			t.Fatalf("read pinned CycloneDX schema %q: %v", file.Path, err)
		}
		digest := sha256.Sum256(contents)
		if got := hex.EncodeToString(digest[:]); got != file.SHA256 {
			t.Fatalf("CycloneDX schema checksum mismatch for %q: got %s, want %s", file.Path, got, file.SHA256)
		}
	}
}

func TestCycloneDXSchemaFixturesValidateOffline(t *testing.T) {
	blockCycloneDXSchemaNetwork(t)
	schema := loadCycloneDXSchema(t)

	for _, fixture := range cycloneDXSchemaFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			first, err := NewFormatter().Format(fixture.reportData, FormatCycloneDX)
			if err != nil {
				t.Fatalf("format CycloneDX fixture: %v", err)
			}
			second, err := NewFormatter().Format(fixture.reportData, FormatCycloneDX)
			if err != nil {
				t.Fatalf("repeat CycloneDX fixture formatting: %v", err)
			}
			if first != second {
				t.Fatalf("CycloneDX fixture changed across repeated runs\nfirst:\n%s\nsecond:\n%s", first, second)
			}
			if err := validateCycloneDXSchema(schema, first); err != nil {
				t.Fatalf("CycloneDX fixture failed official 1.6 schema validation: %v", err)
			}
		})
	}
}

func TestCycloneDXSchemaValidationErrorsIncludeJSONPaths(t *testing.T) {
	blockCycloneDXSchemaNetwork(t)
	schema := loadCycloneDXSchema(t)
	invalid := `{"bomFormat":"CycloneDX","specVersion":"1.6","components":[{"type":"library"}]}`

	err := validateCycloneDXSchema(schema, invalid)
	if err == nil {
		t.Fatal("expected invalid CycloneDX component to fail schema validation")
	}
	if !strings.Contains(err.Error(), "$.components.0") || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("expected actionable JSON path and validation detail, got %q", err)
	}
}

func cycloneDXSchemaFilePath(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join(cycloneDXSchemaFixtureDirectory, name))
	if err != nil {
		t.Fatalf("resolve CycloneDX schema path %q: %v", name, err)
	}
	return path
}

func blockCycloneDXSchemaNetwork(t *testing.T) {
	t.Helper()
	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: &cycloneDXRejectNetworkRoundTripper{}}
	t.Cleanup(func() {
		http.DefaultClient = originalClient
	})
}

func loadCycloneDXSchema(t *testing.T) *gojsonschema.Schema {
	t.Helper()
	loader := gojsonschema.NewSchemaLoader()
	referencedSchemas := []gojsonschema.JSONLoader{
		gojsonschema.NewReferenceLoader(fileURLFromPath(cycloneDXSchemaFilePath(t, "spdx.schema.json"))),
		gojsonschema.NewReferenceLoader(fileURLFromPath(cycloneDXSchemaFilePath(t, "jsf-0.82.schema.json"))),
	}
	if err := loader.AddSchemas(referencedSchemas...); err != nil {
		t.Fatalf("load referenced CycloneDX schemas: %v", err)
	}
	schema, err := loader.Compile(gojsonschema.NewReferenceLoader(fileURLFromPath(cycloneDXSchemaFilePath(t, "bom-1.6.schema.json"))))
	if err != nil {
		t.Fatalf("compile pinned CycloneDX schema: %v", err)
	}
	return schema
}

func validateCycloneDXSchema(schema *gojsonschema.Schema, document string) error {
	result, err := schema.Validate(gojsonschema.NewStringLoader(document))
	if err != nil {
		return fmt.Errorf("validate CycloneDX document: %w", err)
	}
	if result.Valid() {
		return nil
	}

	messages := make([]string, 0, len(result.Errors()))
	for _, item := range result.Errors() {
		path := "$"
		if item.Field() != "(root)" && item.Field() != "" {
			path += "." + strings.TrimPrefix(item.Field(), "(root).")
		}
		messages = append(messages, path+": "+item.Description())
	}
	return errors.New(strings.Join(messages, "; "))
}

func cycloneDXSchemaFixtures() []struct {
	name       string
	reportData Report
} {
	duplicateDependencies := []DependencyReport{
		{Language: "js-ts", Name: "duplicate", UsedExportsCount: 1, TotalExportsCount: 2},
		{Language: "js-ts", Name: "duplicate", UsedExportsCount: 2, TotalExportsCount: 3},
	}
	return []struct {
		name       string
		reportData Report
	}{
		{name: "empty", reportData: Report{}},
		{
			name: "single",
			reportData: Report{
				SchemaVersion: SchemaVersion,
				RepoPath:      "fixture-repository",
				Dependencies:  []DependencyReport{{Language: "go", Name: "example.org/module", UsedExportsCount: 1, TotalExportsCount: 2}},
			},
		},
		{
			name: "policy",
			reportData: Report{
				EffectivePolicy: &EffectivePolicy{
					Sources: []string{"defaults", "repository"},
					License: LicensePolicy{Deny: []string{"GPL-3.0-only"}, FailOnDenied: true},
				},
			},
		},
		{
			name: "baseline",
			reportData: Report{
				Dependencies: []DependencyReport{{Language: "python", Name: "requests", UsedExportsCount: 2, TotalExportsCount: 4}},
				BaselineComparison: &BaselineComparison{
					BaselineKey: "commit:base",
					CurrentKey:  "commit:head",
					Dependencies: []DependencyDelta{{
						Kind: DependencyDeltaChanged, Language: "python", Name: "requests", UsedExportsCountDelta: 1,
					}},
				},
			},
		},
		{
			name: "vulnerability",
			reportData: Report{Dependencies: []DependencyReport{{
				Language: "go",
				Name:     "example.org/vulnerable",
				Vulnerabilities: []VulnerabilityFinding{{
					AdvisoryID: "GO-2099-0001", Package: "example.org/vulnerable", Severity: "high", Source: "fixture", Priority: "high", PriorityScore: 9, Reachable: true,
				}},
			}}},
		},
		{
			name: "license",
			reportData: Report{Dependencies: []DependencyReport{{
				Language: "rust", Name: "licensed", License: &DependencyLicense{SPDX: "MIT", Source: "manifest", Confidence: "high", Evidence: []string{"Cargo.toml", "LICENSE"}},
			}}},
		},
		{
			name:       "timestamp",
			reportData: Report{GeneratedAt: time.Date(2026, time.July, 12, 3, 4, 5, 678000000, time.FixedZone("fixture", 10*60*60))},
		},
		{
			name: "duplicates",
			reportData: Report{
				Dependencies: duplicateDependencies,
				BaselineComparison: &BaselineComparison{Dependencies: []DependencyDelta{
					{Kind: DependencyDeltaChanged, Language: "js-ts", Name: "duplicate", UsedExportsCountDelta: 1},
					{Kind: DependencyDeltaChanged, Language: "js-ts", Name: "duplicate", UsedExportsCountDelta: 2},
				}},
			},
		},
	}
}
