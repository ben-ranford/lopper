package scripts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/dashboard"
	"github.com/ben-ranford/lopper/internal/notify"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/xeipuuv/gojsonschema"
)

type schemaTransportFunc func(*http.Request) (*http.Response, error)

func (f schemaTransportFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestSchemaIdentifiersRemainCompatibleWithoutNetwork(t *testing.T) {
	requests := 0
	blocked := schemaTransportFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return nil, fmt.Errorf("schema test blocked network request: %s", request.URL)
	})
	originalClient, originalTransport := http.DefaultClient, http.DefaultTransport
	http.DefaultClient, http.DefaultTransport = &http.Client{Transport: blocked}, blocked
	t.Cleanup(func() {
		http.DefaultClient, http.DefaultTransport = originalClient, originalTransport
	})

	generatedAt := time.Date(2026, time.September, 23, 0, 0, 0, 0, time.UTC)
	singleReport := report.Report{RepoPath: "fixture", GeneratedAt: generatedAt}
	single, err := report.NewFormatter().Format(singleReport, report.FormatCycloneDX)
	if err != nil {
		t.Fatal(err)
	}
	portfolio, err := dashboard.FormatReport(dashboard.Report{
		GeneratedAt: generatedAt,
		Repos:       []dashboard.RepoResult{{Name: "fixture", Path: "fixture"}},
	}, dashboard.FormatCycloneDXJSON)
	if err != nil {
		t.Fatal(err)
	}

	cycloneSchema, cycloneID := loadIdentifierSchema(t, "report/cyclonedx-1.6", "bom-1.6.schema.json", "$id", "spdx.schema.json", "jsf-0.82.schema.json")
	cardSchema, cardID := loadIdentifierSchema(t, "notify/adaptive-cards-1.4", "adaptive-card.json", "id")
	for _, document := range []struct {
		name, body, identifier string
		schema                 *gojsonschema.Schema
	}{
		{"report", single, cycloneID, cycloneSchema},
		{"dashboard", portfolio, cycloneID, cycloneSchema},
		{"webhook", captureAdaptiveCard(t, singleReport), cardID, cardSchema},
	} {
		t.Run(document.name, func(t *testing.T) {
			assertIdentifierDocument(t, document.schema, document.body, document.identifier)
		})
	}
	if requests != 0 {
		t.Fatalf("serialization or local schema validation attempted %d network requests", requests)
	}
	// Prove an unregistered URI cannot silently fetch a schema over the network.
	if _, err := gojsonschema.NewSchema(gojsonschema.NewReferenceLoader(cycloneID)); err == nil || requests != 1 {
		t.Fatalf("expected one blocked schema fetch, got requests=%d, error=%v", requests, err)
	}
}

func loadIdentifierSchema(t *testing.T, directory, root, idField string, references ...string) (*gojsonschema.Schema, string) {
	t.Helper()
	loader := gojsonschema.NewSchemaLoader()
	for _, reference := range references {
		if err := loader.AddSchemas(gojsonschema.NewBytesLoader(readIdentifierSchema(t, directory, reference))); err != nil {
			t.Fatal(err)
		}
	}
	contents := readIdentifierSchema(t, directory, root)
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(contents, &metadata); err != nil {
		t.Fatal(err)
	}
	var identifier string
	if err := json.Unmarshal(metadata[idField], &identifier); err != nil || identifier == "" {
		t.Fatalf("missing upstream schema identifier: %v", err)
	}
	schema, err := loader.Compile(gojsonschema.NewBytesLoader(contents))
	if err != nil {
		t.Fatal(err)
	}
	return schema, identifier
}

func readIdentifierSchema(t *testing.T, directory, name string) []byte {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("../testdata", directory, name))
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func assertIdentifierDocument(t *testing.T, schema *gojsonschema.Schema, document, identifier string) {
	t.Helper()
	var metadata struct {
		Schema string `json:"$schema"`
	}
	if err := json.Unmarshal([]byte(document), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Schema != identifier {
		t.Fatalf("serialized schema identifier = %q, want upstream identifier %q", metadata.Schema, identifier)
	}
	result, err := schema.Validate(gojsonschema.NewStringLoader(document))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid() {
		t.Fatalf("serialized document does not conform to upstream schema: %v", result.Errors())
	}
}

func captureAdaptiveCard(t *testing.T, reportData report.Report) string {
	t.Helper()
	const endpoint = "https://webhook.example.test/delivery"
	var body []byte
	requests := 0
	client := &http.Client{Transport: schemaTransportFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.String() != endpoint || request.Method != http.MethodPost {
			t.Errorf("unexpected network destination: %s %s", request.Method, request.URL)
		}
		var err error
		body, err = io.ReadAll(request.Body)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, err
	})}
	if err := notify.NewWebhookNotifier(client).Notify(context.Background(), notify.Delivery{
		Channel: notify.ChannelTeams, WebhookURL: endpoint, Report: reportData,
	}); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("expected only the configured HTTPS delivery, got %d requests", requests)
	}
	var envelope struct {
		Attachments []struct {
			Content json.RawMessage `json:"content"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Attachments) != 1 {
		t.Fatalf("expected one Adaptive Card attachment, got %d", len(envelope.Attachments))
	}
	return string(envelope.Attachments[0].Content)
}

func TestAdaptiveCardSchemaFixtureChecksum(t *testing.T) {
	contents := readIdentifierSchema(t, "notify/adaptive-cards-1.4", "adaptive-card.json")
	digest := sha256.Sum256(contents)
	const expected = "e1750b01c13459b1d937ae0a8792d825314d54ddd22b1c8159cfd809d2caab09"
	if got := hex.EncodeToString(digest[:]); got != expected {
		t.Fatalf("pinned Adaptive Card schema checksum = %s, want %s", got, expected)
	}
}
