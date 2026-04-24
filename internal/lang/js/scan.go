package js

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/ben-ranford/lopper/internal/report"
)

type ImportKind string

const (
	ImportNamed      ImportKind = "named"
	ImportNamespace  ImportKind = "namespace"
	ImportDefault    ImportKind = "default"
	ImportSideEffect ImportKind = "side-effect"
)

const sideEffectImportName = "<side-effect>"

type ImportBinding struct {
	Module     string
	ExportName string
	LocalName  string
	Kind       ImportKind
	Location   report.Location
}

type ReExportBinding struct {
	SourceModule     string
	SourceExportName string
	ExportName       string
	Location         report.Location
}

type FileScan struct {
	Path             string
	Imports          []ImportBinding
	UncertainImports []report.ImportUse
	ReExports        []ReExportBinding
	IdentifierUsage  map[string]int
	NamespaceUsage   map[string]map[string]int
}

type ScanResult struct {
	Files    []FileScan
	Warnings []string
}

var supportedExtensions = map[string]bool{
	".js":  true,
	".cjs": true,
	".mjs": true,
	".jsx": true,
	".ts":  true,
	".mts": true,
	".cts": true,
	".tsx": true,
}

var skipDirectories = map[string]bool{
	"out":      true,
	"coverage": true,
	".next":    true,
	".turbo":   true,
}

func ScanRepo(ctx context.Context, repoPath string) (ScanResult, error) {
	result := ScanResult{}
	if repoPath == "" {
		return result, errors.New("repo path is empty")
	}

	parser := newSourceParser()
	state := scanRepoState{
		parser:   parser,
		repoPath: repoPath,
		result:   &result,
	}

	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return scanRepoEntry(ctx, &state, path, entry)
	})

	if err != nil {
		return result, err
	}

	if len(result.Files) == 0 {
		result.Warnings = append(result.Warnings, "no JS/TS files found for analysis")
	}

	if state.parseErrorCount > 0 {
		warning := fmt.Sprintf("parse errors in %d file(s)", state.parseErrorCount)
		if len(state.parseErrorFiles) > 0 {
			warning = fmt.Sprintf("%s: %s", warning, strings.Join(state.parseErrorFiles, ", "))
		}
		result.Warnings = append(result.Warnings, warning)
	}

	return result, nil
}

func analyzeFile(tree *sitter.Tree, content []byte, relPath string) FileScan {
	imports, uncertainImports := collectImportBindings(tree, content, relPath)
	reExports := collectReExportBindings(tree, content, relPath, imports)
	identifierUsage := collectIdentifierUsage(tree, content)
	namespaceUsage := collectNamespaceUsage(tree, content)

	return FileScan{
		Path:             relPath,
		Imports:          imports,
		UncertainImports: uncertainImports,
		ReExports:        reExports,
		IdentifierUsage:  identifierUsage,
		NamespaceUsage:   namespaceUsage,
	}
}
