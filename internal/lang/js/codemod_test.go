package js

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestAdapterAnalyseSuggestOnlyCodemodPreview(t *testing.T) {
	repo := t.TempDir()
	source := "import { map } from \"lodash\";\nmap([1], (x) => x)\n"
	sourcePath := filepath.Join(repo, "index.js")
	if err := os.WriteFile(sourcePath, []byte(source), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	depRoot := filepath.Join(repo, "node_modules", "lodash")
	if err := os.MkdirAll(depRoot, 0o755); err != nil {
		t.Fatalf("mkdir dependency root: %v", err)
	}
	packageJSON := "{\n  \"main\": \"index.js\",\n  \"exports\": {\n    \".\": \"./index.js\",\n    \"./map\": \"./map.js\"\n  }\n}\n"
	if err := os.WriteFile(filepath.Join(depRoot, "package.json"), []byte(packageJSON), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, "index.js"), []byte("export { map } from './map.js'\n"), 0o644); err != nil {
		t.Fatalf("write entrypoint: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, "map.js"), []byte("export default function map() {}\n"), 0o644); err != nil {
		t.Fatalf("write map.js: %v", err)
	}

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:       repo,
		Dependency:     "lodash",
		SuggestOnly:    true,
		RuntimeProfile: "node-import",
	})
	if err != nil {
		t.Fatalf("analyse suggest-only: %v", err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected 1 dependency report, got %d", len(reportData.Dependencies))
	}
	codemod := reportData.Dependencies[0].Codemod
	if codemod == nil {
		t.Fatalf("expected codemod report")
	}
	if codemod.Mode != codemodModeSuggestOnly {
		t.Fatalf("expected codemod mode %q, got %q", codemodModeSuggestOnly, codemod.Mode)
	}
	if len(codemod.Suggestions) != 1 {
		t.Fatalf("expected one codemod suggestion, got %#v", codemod.Suggestions)
	}
	suggestion := codemod.Suggestions[0]
	if suggestion.ToModule != "lodash/map" {
		t.Fatalf("expected lodash/map replacement, got %#v", suggestion)
	}
	if !strings.Contains(suggestion.Patch, "@@ -1 +1 @@") {
		t.Fatalf("expected deterministic one-line patch hunk, got %q", suggestion.Patch)
	}
	if !strings.Contains(suggestion.Patch, "-import { map } from \"lodash\";") || !strings.Contains(suggestion.Patch, "+import map from \"lodash/map\";") {
		t.Fatalf("unexpected patch preview: %q", suggestion.Patch)
	}

	contentAfter, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read source after analyse: %v", err)
	}
	if string(contentAfter) != source {
		t.Fatalf("expected suggest-only mode to avoid mutating source, got %q", string(contentAfter))
	}
}

func TestAdapterAnalyseSuggestOnlySkipsUnsafeTransforms(t *testing.T) {
	repo := t.TempDir()
	source := strings.Join([]string{
		"import \"lodash\"",
		"import * as lodash from \"lodash\"",
		"import { map as mapAlias } from \"lodash\"",
		"mapAlias([1], (x) => x)",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(repo, "index.js"), []byte(source), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	depRoot := filepath.Join(repo, "node_modules", "lodash")
	if err := os.MkdirAll(depRoot, 0o755); err != nil {
		t.Fatalf("mkdir dependency root: %v", err)
	}
	packageJSON := "{\n  \"main\": \"index.js\",\n  \"exports\": {\n    \".\": \"./index.js\",\n    \"./map\": \"./map.js\"\n  }\n}\n"
	if err := os.WriteFile(filepath.Join(depRoot, "package.json"), []byte(packageJSON), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, "index.js"), []byte("export { map } from './map.js'\n"), 0o644); err != nil {
		t.Fatalf("write entrypoint: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, "map.js"), []byte("export default function map() {}\n"), 0o644); err != nil {
		t.Fatalf("write map.js: %v", err)
	}

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:    repo,
		Dependency:  "lodash",
		SuggestOnly: true,
	})
	if err != nil {
		t.Fatalf("analyse suggest-only skips: %v", err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected single dependency report, got %d", len(reportData.Dependencies))
	}
	codemod := reportData.Dependencies[0].Codemod
	if codemod == nil {
		t.Fatalf("expected codemod report")
	}
	if len(codemod.Suggestions) != 0 {
		t.Fatalf("expected no safe suggestions, got %#v", codemod.Suggestions)
	}

	reasons := make([]string, 0, len(codemod.Skips))
	for _, skip := range codemod.Skips {
		reasons = append(reasons, skip.ReasonCode)
	}
	for _, expected := range []string{codemodReasonSideEffectImport, codemodReasonNamespaceImport, codemodReasonAliasConflict} {
		if !slices.Contains(reasons, expected) {
			t.Fatalf("expected skip reason %q, got %#v", expected, reasons)
		}
	}
}
