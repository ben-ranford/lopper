package js

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanRepoParseErrorWarning(t *testing.T) {
	repo := t.TempDir()
	broken := "import { map from 'lodash'\n"
	if err := os.WriteFile(filepath.Join(repo, "broken.js"), []byte(broken), 0o600); err != nil {
		t.Fatalf("write broken js: %v", err)
	}
	result, err := ScanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected scanned file even with parse errors")
	}
	if len(result.Warnings) == 0 || !strings.Contains(result.Warnings[0], "parse errors") {
		t.Fatalf("expected parse error warning, got %#v", result.Warnings)
	}
}

func TestScanHelpers(t *testing.T) {
	var files []string
	for i := 0; i < 7; i++ {
		appendParseErrorFile(&files, "f"+string(rune('a'+i))+".js")
	}
	if len(files) != 5 {
		t.Fatalf("expected parse error file list to cap at 5, got %d", len(files))
	}

	if !isSupportedFile("x.ts") || isSupportedFile("x.css") {
		t.Fatalf("unexpected supported file detection")
	}

	parser := newSourceParser()
	if _, err := parser.languageForPath("x.ts"); err != nil {
		t.Fatalf("expected ts language, got error: %v", err)
	}
	if _, err := parser.languageForPath("x.unknown"); err == nil {
		t.Fatalf("expected unsupported extension error")
	}
}

func TestScanRepoAndEntryBranches(t *testing.T) {
	if _, err := ScanRepo(context.Background(), ""); err == nil {
		t.Fatalf("expected empty repo path error")
	}

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "node_modules"), 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "node_modules", "skip.js"), []byte("export const x = 1"), 0o600); err != nil {
		t.Fatalf("write skipped file: %v", err)
	}
	result, err := ScanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	if len(result.Files) != 0 {
		t.Fatalf("expected no scanned files due to directory skipping, got %#v", result.Files)
	}
	if len(result.Warnings) == 0 || !strings.Contains(strings.Join(result.Warnings, "\n"), "no JS/TS files found") {
		t.Fatalf("expected no-files warning, got %#v", result.Warnings)
	}
}

func TestReadAndParseFileBranches(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "index.js")
	if err := os.WriteFile(path, []byte("export const v = 1\n"), 0o600); err != nil {
		t.Fatalf("write index.js: %v", err)
	}
	parser := newSourceParser()

	content, tree, rel, err := readAndParseFile(context.Background(), parser, "", path)
	if err != nil {
		t.Fatalf("read and parse file: %v", err)
	}
	if len(content) == 0 || tree == nil || rel == "" {
		t.Fatalf("unexpected readAndParseFile result content=%d tree=%v rel=%q", len(content), tree, rel)
	}

	if _, _, _, err := readAndParseFile(context.Background(), parser, repo, filepath.Join(repo, "missing.js")); err == nil {
		t.Fatalf("expected read error for missing file")
	}

	cssPath := filepath.Join(repo, "index.css")
	if err := os.WriteFile(cssPath, []byte("body{}"), 0o600); err != nil {
		t.Fatalf("write index.css: %v", err)
	}
	if _, _, _, err := readAndParseFile(context.Background(), parser, repo, cssPath); err == nil {
		t.Fatalf("expected parser language error for unsupported extension")
	}
}

func TestScanRepoEntrySkipsDirectoriesAndContextCancel(t *testing.T) {
	repo := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ScanRepo(ctx, repo); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled error, got %v", err)
	}

	state := scanRepoState{
		parser:   newSourceParser(),
		repoPath: repo,
		result:   &ScanResult{},
	}
	if err := os.MkdirAll(filepath.Join(repo, "dist"), 0o755); err != nil {
		t.Fatalf("mkdir dist: %v", err)
	}
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("readdir repo: %v", err)
	}
	var dirEntry fs.DirEntry
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() == "dist" {
			dirEntry = entry
			break
		}
	}
	if dirEntry == nil {
		t.Fatalf("expected dist entry")
	}
	if err := scanRepoEntry(context.Background(), &state, filepath.Join(repo, "dist"), dirEntry); !errors.Is(err, fs.SkipDir) {
		t.Fatalf("expected skip dir result, got %v", err)
	}
}
