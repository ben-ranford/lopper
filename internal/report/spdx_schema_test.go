package report

import (
	"crypto/sha1"
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

const spdxSchemaFixtureDirectory = "../../testdata/report/spdx-2.3"

type spdxSchemaMetadata struct {
	Upstream    string                   `json:"upstream"`
	Tag         string                   `json:"tag"`
	TagObject   string                   `json:"tagObject"`
	Commit      string                   `json:"commit"`
	License     string                   `json:"license"`
	LicenseURI  string                   `json:"licenseURI"`
	Attribution string                   `json:"attribution"`
	Files       []spdxSchemaMetadataFile `json:"files"`
}

type spdxSchemaMetadataFile struct {
	Path         string `json:"path"`
	UpstreamPath string `json:"upstreamPath"`
	RawURL       string `json:"rawURL"`
	Bytes        int    `json:"bytes"`
	GitBlobSHA1  string `json:"gitBlobSHA1"`
	SHA256       string `json:"sha256"`
}

type spdxSemanticDocument struct {
	SPDXVersion       string             `json:"spdxVersion"`
	DataLicense       string             `json:"dataLicense"`
	SPDXID            string             `json:"SPDXID"`
	DocumentNamespace string             `json:"documentNamespace"`
	CreationInfo      spdxCreationInfo   `json:"creationInfo"`
	Packages          []spdxPackage      `json:"packages"`
	Relationships     []spdxRelationship `json:"relationships"`
	Annotations       []spdxAnnotation   `json:"annotations"`
}

type spdxRejectNetworkRoundTripper struct{}

func (*spdxRejectNetworkRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("spdx schema validation attempted network access: %s", request.URL)
}

func TestSPDXSchemaMetadataChecksums(t *testing.T) {
	metadataPath := spdxSchemaFilePath(t, "schema-metadata.json")
	payload, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read SPDX schema metadata: %v", err)
	}

	var metadata spdxSchemaMetadata
	if err := json.Unmarshal(payload, &metadata); err != nil {
		t.Fatalf("decode SPDX schema metadata: %v", err)
	}
	assertSPDXSchemaMetadataProvenance(t, metadata)
	for _, file := range metadata.Files {
		assertSPDXSchemaMetadataFile(t, file)
	}
}

func TestSPDXSchemaFixturesValidateOffline(t *testing.T) {
	blockSPDXSchemaNetwork(t)
	schema := loadSPDXSchema(t)

	for _, fixture := range spdxSchemaFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			first, err := NewFormatter().Format(fixture.reportData, FormatSPDX)
			if err != nil {
				t.Fatalf("format SPDX fixture: %v", err)
			}
			second, err := NewFormatter().Format(fixture.reportData, FormatSPDX)
			if err != nil {
				t.Fatalf("repeat SPDX fixture formatting: %v", err)
			}
			if first != second {
				t.Fatalf("SPDX fixture changed across repeated runs\nfirst:\n%s\nsecond:\n%s", first, second)
			}
			if err := validateSPDXSchema(schema, first); err != nil {
				t.Fatalf("SPDX fixture failed official 2.3 schema validation: %v", err)
			}
		})
	}
}

func TestSPDXSchemaFormattedOutputPreservesSemanticFields(t *testing.T) {
	blockSPDXSchemaNetwork(t)
	schema := loadSPDXSchema(t)
	reportData := Report{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   time.Date(2026, time.July, 14, 1, 2, 3, 0, time.UTC),
		RepoPath:      "/repo",
		Warnings:      []string{"z warning", "a warning"},
		Dependencies: []DependencyReport{
			{
				Name:     "left-pad",
				Language: "js-ts",
				Identity: &DependencyIdentity{
					Ecosystem:     "npm",
					Name:          "left-pad",
					Version:       "1.3.0",
					PURL:          "pkg:npm/left-pad@1.3.0",
					VersionStatus: "declared",
					Source:        "package-lock.json",
				},
				License: &DependencyLicense{SPDX: "MIT"},
			},
		},
	}

	output, err := NewFormatter().Format(reportData, FormatSPDX)
	if err != nil {
		t.Fatalf("format SPDX semantic fixture: %v", err)
	}
	assertSPDXSchemaValidation(t, schema, output, "SPDX semantic fixture")

	var doc spdxSemanticDocument
	if err := json.Unmarshal([]byte(output), &doc); err != nil {
		t.Fatalf("parse SPDX semantic fixture: %v", err)
	}
	assertSPDXSemanticDocument(t, doc)
}

func assertSPDXSemanticDocument(t *testing.T, doc spdxSemanticDocument) {
	t.Helper()
	if doc.SPDXVersion != "SPDX-2.3" || doc.DataLicense != "CC0-1.0" || doc.SPDXID != spdxDocumentRef || doc.DocumentNamespace == "" {
		t.Fatalf("unexpected SPDX document metadata: %#v", doc)
	}
	if doc.CreationInfo.Created != "2026-07-14T01:02:03Z" || strings.Join(doc.CreationInfo.Creators, ",") != "Tool: lopper" {
		t.Fatalf("unexpected SPDX creation info: %#v", doc.CreationInfo)
	}
	if len(doc.Packages) != 1 {
		t.Fatalf("expected one SPDX package, got %#v", doc.Packages)
	}
	pkg := doc.Packages[0]
	if pkg.SPDXID == "" || pkg.Name != "left-pad" || pkg.VersionInfo != "1.3.0" || pkg.LicenseDeclared != "MIT" {
		t.Fatalf("unexpected SPDX package fields: %#v", pkg)
	}
	if len(pkg.ExternalRefs) != 1 || pkg.ExternalRefs[0].ReferenceLocator != "pkg:npm/left-pad@1.3.0" {
		t.Fatalf("unexpected SPDX external refs: %#v", pkg.ExternalRefs)
	}
	if strings.Join(pkg.AttributionTexts, "|") != "lopper:versionStatus=declared|lopper:identitySource=package-lock.json" {
		t.Fatalf("unexpected SPDX attribution texts: %#v", pkg.AttributionTexts)
	}
	if len(doc.Relationships) != 1 || doc.Relationships[0].SPDXElementID != spdxDocumentRef || doc.Relationships[0].RelationshipType != "DESCRIBES" || doc.Relationships[0].RelatedSPDXElement != pkg.SPDXID {
		t.Fatalf("unexpected SPDX relationships: %#v", doc.Relationships)
	}
	if len(doc.Annotations) != 1 || doc.Annotations[0].Annotator != "Tool: lopper" || doc.Annotations[0].Comment != "a warning | z warning" {
		t.Fatalf("unexpected SPDX annotations: %#v", doc.Annotations)
	}
}

func TestSPDXSchemaRejectsUnknownFields(t *testing.T) {
	blockSPDXSchemaNetwork(t)
	schema := loadSPDXSchema(t)
	base, err := NewFormatter().Format(Report{Warnings: []string{"fixture"}}, FormatSPDX)
	if err != nil {
		t.Fatalf("format SPDX unknown-field fixture: %v", err)
	}

	rootInjected := strings.Replace(base, "{\n", "{\n  \"unexpectedRoot\": true,\n", 1)
	err = validateSPDXSchema(schema, rootInjected)
	assertSPDXSchemaValidationError(t, err, "$", "Additional property", "unknown root field")

	annotationInjected := strings.Replace(base, "\"comment\": \"fixture\"\n    }\n  ]\n}", "\"comment\": \"fixture\",\n      \"unexpected\": true\n    }\n  ]\n}", 1)
	err = validateSPDXSchema(schema, annotationInjected)
	assertSPDXSchemaValidationError(t, err, "$.annotations.0", "Additional property", "unknown annotation field")
}

func assertSPDXSchemaMetadataProvenance(t *testing.T, metadata spdxSchemaMetadata) {
	t.Helper()
	if metadata.Upstream != "https://github.com/spdx/spdx-spec" ||
		metadata.Tag != "v2.3" ||
		metadata.TagObject != "f7f7bce5511a23fe3c9d8a1edca0d870a7d0bea5" ||
		metadata.Commit != "aadf3b0b8dbbabdb4d880b0fc714255fea436ff7" ||
		metadata.License != "CC-BY-3.0" ||
		metadata.LicenseURI != "https://spdx.dev/spdx-specification-2-3-web-version/#h.jxpfx0ykyb60" {
		t.Fatalf("unexpected SPDX schema provenance: %#v", metadata)
	}
	if !strings.Contains(metadata.Attribution, "SPDX Specification 2.3 JSON schema") {
		t.Fatalf("expected SPDX attribution text, got %q", metadata.Attribution)
	}
	if len(metadata.Files) != 1 {
		t.Fatalf("expected one pinned SPDX schema, got %d", len(metadata.Files))
	}
}

func assertSPDXSchemaMetadataFile(t *testing.T, file spdxSchemaMetadataFile) {
	t.Helper()
	if file.UpstreamPath == "" || file.RawURL == "" || file.GitBlobSHA1 == "" || file.SHA256 == "" || file.Bytes == 0 {
		t.Fatalf("incomplete SPDX schema metadata for %q: %#v", file.Path, file)
	}
	contents, err := os.ReadFile(spdxSchemaFilePath(t, file.Path))
	if err != nil {
		t.Fatalf("read pinned SPDX schema %q: %v", file.Path, err)
	}
	if len(contents) != file.Bytes {
		t.Fatalf("SPDX schema byte count mismatch for %q: got %d, want %d", file.Path, len(contents), file.Bytes)
	}
	digest := sha256.Sum256(contents)
	if got := hex.EncodeToString(digest[:]); got != file.SHA256 {
		t.Fatalf("SPDX schema checksum mismatch for %q: got %s, want %s", file.Path, got, file.SHA256)
	}
	if got := spdxGitBlobSHA1(contents); got != file.GitBlobSHA1 {
		t.Fatalf("SPDX schema git blob mismatch for %q: got %s, want %s", file.Path, got, file.GitBlobSHA1)
	}
}

func assertSPDXSchemaValidation(t *testing.T, schema *gojsonschema.Schema, document, label string) {
	t.Helper()
	if err := validateSPDXSchema(schema, document); err != nil {
		t.Fatalf("%s failed schema validation: %v", label, err)
	}
}

func assertSPDXSchemaValidationError(t *testing.T, err error, path, contains, label string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), contains) {
		t.Fatalf("expected %s to fail schema validation, got %v", label, err)
	}
}

func spdxSchemaFilePath(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join(spdxSchemaFixtureDirectory, name))
	if err != nil {
		t.Fatalf("resolve SPDX schema path %q: %v", name, err)
	}
	return path
}

func blockSPDXSchemaNetwork(t *testing.T) {
	t.Helper()
	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: &spdxRejectNetworkRoundTripper{}}
	t.Cleanup(func() {
		http.DefaultClient = originalClient
	})
}

func loadSPDXSchema(t *testing.T) *gojsonschema.Schema {
	t.Helper()
	schema, err := gojsonschema.NewSchema(gojsonschema.NewReferenceLoader(fileURLFromPath(spdxSchemaFilePath(t, "spdx-schema.json"))))
	if err != nil {
		t.Fatalf("compile pinned SPDX schema: %v", err)
	}
	return schema
}

func validateSPDXSchema(schema *gojsonschema.Schema, document string) error {
	result, err := schema.Validate(gojsonschema.NewStringLoader(document))
	if err != nil {
		return fmt.Errorf("validate SPDX document: %w", err)
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

func spdxGitBlobSHA1(contents []byte) string {
	header := fmt.Sprintf("blob %d\x00", len(contents))
	payload := append([]byte(header), contents...)
	digest := sha1.Sum(payload)
	return hex.EncodeToString(digest[:])
}

func spdxSchemaFixtures() []struct {
	name       string
	reportData Report
} {
	duplicateDependencies := []DependencyReport{
		{
			Language: "js-ts",
			Name:     "duplicate",
			Identity: &DependencyIdentity{Name: "duplicate", Ecosystem: "npm", Version: "1.0.0", PURL: "pkg:npm/duplicate@1.0.0"},
			License:  &DependencyLicense{SPDX: "MIT"},
		},
		{
			Language: "js-ts",
			Name:     "duplicate",
			Identity: &DependencyIdentity{Name: "duplicate", Ecosystem: "npm", Version: "2.0.0", PURL: "pkg:npm/duplicate@2.0.0"},
			License:  &DependencyLicense{SPDX: "Apache-2.0"},
		},
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
				Dependencies: []DependencyReport{{
					Language: "go",
					Name:     "example.org/module",
					Identity: &DependencyIdentity{
						Ecosystem: "golang",
						Name:      "example.org/module",
						Version:   "1.2.3",
						PURL:      "pkg:golang/example.org/module@1.2.3",
					},
					License: &DependencyLicense{SPDX: "MIT"},
				}},
			},
		},
		{
			name: "policy",
			reportData: Report{
				Warnings: []string{"license policy applied"},
				EffectivePolicy: &EffectivePolicy{
					Sources: []string{"defaults", "repository"},
					License: LicensePolicy{Deny: []string{"GPL-3.0-only"}, FailOnDenied: true},
				},
			},
		},
		{
			name: "duplicates",
			reportData: Report{
				Dependencies: duplicateDependencies,
			},
		},
		{
			name: "timestamp",
			reportData: Report{
				GeneratedAt: time.Date(2026, time.July, 12, 3, 4, 5, 678000000, time.FixedZone("fixture", 10*60*60)),
			},
		},
	}
}
