package js

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/javascript"
	tsxlang "github.com/smacker/go-tree-sitter/typescript/tsx"
	tslang "github.com/smacker/go-tree-sitter/typescript/typescript"
)

type sourceParser struct {
	js  *sitter.Language
	ts  *sitter.Language
	tsx *sitter.Language
}

func newSourceParser() *sourceParser {
	return &sourceParser{
		js:  javascript.GetLanguage(),
		ts:  tslang.GetLanguage(),
		tsx: tsxlang.GetLanguage(),
	}
}

func (p *sourceParser) Parse(ctx context.Context, path string, content []byte) (*sitter.Tree, error) {
	lang, err := p.languageForPath(path)
	if err != nil {
		return nil, err
	}

	parser := sitter.NewParser()
	parser.SetLanguage(lang)

	return parser.ParseCtx(ctx, nil, content)
}

func (p *sourceParser) languageForPath(path string) (*sitter.Language, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".js", ".cjs", ".mjs", ".jsx":
		return p.js, nil
	case ".ts", ".mts", ".cts":
		return p.ts, nil
	case ".tsx":
		return p.tsx, nil
	default:
		return nil, fmt.Errorf("unsupported extension: %s", ext)
	}
}

func readAndParseFile(ctx context.Context, parser *sourceParser, repoPath string, path string) ([]byte, *sitter.Tree, string, error) {
	var (
		content []byte
		readErr error
	)
	if strings.TrimSpace(repoPath) == "" {
		content, readErr = safeio.ReadFile(path)
	} else {
		content, readErr = safeio.ReadFileUnder(repoPath, path)
	}
	if readErr != nil {
		return nil, nil, "", readErr
	}
	tree, langErr := parser.Parse(ctx, path, content)
	if langErr != nil {
		return nil, nil, "", langErr
	}
	if tree == nil {
		return nil, nil, "", fmt.Errorf("tree-sitter returned nil tree for %s", path)
	}
	relPath, relErr := filepath.Rel(repoPath, path)
	if relErr != nil {
		relPath = path
	}
	return content, tree, relPath, nil
}

func isSupportedFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return supportedExtensions[ext]
}
