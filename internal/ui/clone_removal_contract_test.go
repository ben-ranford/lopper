package ui

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/stave/layout"
)

func TestCodemodCollectionsPreserveOutputAndWriteErrors(t *testing.T) {
	cases := []struct {
		name  string
		print func(io.Writer) error
		want  string
	}{
		{"empty suggestions", func(w io.Writer) error { return printCodemodSuggestions(w, nil) }, "  - suggestions: 0\n"},
		{"empty skips", func(w io.Writer) error { return printCodemodSkips(w, nil) }, "  - skips: 0\n"},
		{"suggestions", func(w io.Writer) error {
			return printCodemodSuggestions(w, []detailCodemodSuggestionView{
				{File: "b.ts", Line: 2, FromModule: "pkg", ToModule: "pkg/b"},
				{File: "a.ts", Line: 1, FromModule: "other", ToModule: "other/a"},
			})
		}, "  - suggestions: 2\n    - b.ts:2 pkg -> pkg/b\n    - a.ts:1 other -> other/a\n"},
		{"skips", func(w io.Writer) error {
			return printCodemodSkips(w, []detailCodemodSkipView{
				{File: "b.ts", Line: 2, ReasonCode: "unsafe", Message: "cannot rewrite"},
				{File: "a.ts", Line: 1, ReasonCode: "dynamic", Message: "unknown target"},
			})
		}, "  - skips: 2\n    - b.ts:2 [unsafe] cannot rewrite\n    - a.ts:1 [dynamic] unknown target\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := tc.print(&out); err != nil {
				t.Fatal(err)
			}
			if got := out.String(); got != tc.want {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
			writeErr := errors.New("collection write failed")
			for failAt := 0; failAt < bytes.Count(out.Bytes(), []byte{'\n'}); failAt++ {
				writer := &failAfterWriter{failAt: failAt, err: writeErr}
				if err := tc.print(writer); !errors.Is(err, writeErr) {
					t.Fatalf("write %d: error = %v, want %v", failAt, err, writeErr)
				}
				if writer.writes != failAt {
					t.Fatalf("write %d: wrote %d successful lines", failAt, writer.writes)
				}
			}
		})
	}
}

func TestStaveModelSerializationAndHashCompatibility(t *testing.T) {
	// Captured from the implementations before their shared report mapping
	// was consolidated. Both the checkpoint bytes and replay hashes stay stable.
	cases := []struct {
		name      string
		model     staveSummaryModel
		jsonHash  string
		modelHash string
	}{
		{name: "absent", jsonHash: "b0aade50d961e2718f22ea27797ab7c9f1aef07281a535685e9a13da351aa667", modelHash: "781e2f93f9d62b8c17a2c74c5c713f7613d067add5278834a0d47ed99af98edd"},
		{name: "empty", model: staveSummaryModel{view: &summaryReportView{}, opts: &Options{}}, jsonHash: "b86a86b68acd66be01e17c3e6964d8a58a7eb3a88207ae4dbd7ba11e4f1409bb", modelHash: "c84e1588e66ae711bd37ab4c36f6fd37d34a394947b0990702b66b2a7916f29c"},
		{name: "populated", model: staveSerializationContractModel(), jsonHash: "fad6e0f9d1cd56e2e9591ad9080ad1b2cc19c15ab432cc89663398364ffb2170", modelHash: "6cdcbb4a82a9e92ce42beeab677bf4422cdcb1d972cd896bafc709245e7e0cbe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.model)
			if err != nil {
				t.Fatal(err)
			}
			hash, err := hashStaveSummaryModel(tc.model)
			if err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != tc.jsonHash {
				t.Errorf("checkpoint JSON checksum = %s, want %s; JSON: %s", got, tc.jsonHash, data)
			}
			if got := fmt.Sprintf("%x", hash); got != tc.modelHash {
				t.Errorf("model hash = %s, want %s", got, tc.modelHash)
			}
		})
	}
}

func staveSerializationContractModel() staveSummaryModel {
	color := true
	view := mapSummaryReportView(report.Report{
		Dependencies: []report.DependencyReport{{
			Language: "go", Name: "alpha", UsedPercent: 12.5,
			UnusedExports: []report.SymbolRef{{Name: "unused", Module: "alpha/extra"}},
		}},
		Warnings:            []string{"warning"},
		UsageUncertainty:    &report.UsageUncertainty{ConfirmedImportUses: 2, UncertainImportUses: 1},
		Scope:               &report.ScopeMetadata{Mode: "workspace", Packages: []string{"app"}},
		Cache:               &report.CacheMetadata{Enabled: true, Path: "cache", Hits: 2, Misses: 1},
		EffectiveThresholds: &report.EffectiveThresholds{FailOnIncreasePercent: 10},
		EffectivePolicy:     &report.EffectivePolicy{Sources: []string{"policy"}},
		BaselineComparison:  &report.BaselineComparison{BaselineKey: "old", CurrentKey: "new"},
	})
	return staveSummaryModel{
		view: &view,
		opts: &Options{RepoPath: ".", Language: "go", Filter: "f", Sort: "name", BaselinePath: "base", BaselineStorePath: "store", BaselineKey: "key", TopN: 2, PageSize: 3, Width: 80, ASCII: true, UseStavePreview: true, Color: &color},
		interaction: staveSummaryInteraction{
			summary:     summaryState{filter: "f", sortMode: sortByName, page: 2, pageSize: 3, showHelp: true, selectedDependency: "go:alpha"},
			selectedRow: 1, focusPane: "detail", commandMode: true, filterBuffer: "filter f",
			viewport: layout.Size{Width: 80, Height: 24}, help: true, status: "ok", error: "err",
			pendingConfirm: "confirm", pendingCallID: "call", pendingActionID: "action", quit: true,
		},
	}
}
