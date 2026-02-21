package js

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/javascript"
	tsxlang "github.com/smacker/go-tree-sitter/typescript/tsx"
	tslang "github.com/smacker/go-tree-sitter/typescript/typescript"

	"github.com/ben-ranford/lopper/internal/report"
)

type ImportKind string

const (
	ImportNamed     ImportKind = "named"
	ImportNamespace ImportKind = "namespace"
	ImportDefault   ImportKind = "default"
)

type ImportBinding struct {
	Module     string
	ExportName string
	LocalName  string
	Kind       ImportKind
	Location   report.Location
}

type FileScan struct {
	Path            string
	Imports         []ImportBinding
	IdentifierUsage map[string]int
	NamespaceUsage  map[string]map[string]int
}

type ScanResult struct {
	Files    []FileScan
	Warnings []string
}

type scanRepoState struct {
	parser          *sourceParser
	repoPath        string
	result          *ScanResult
	parseErrorCount int
	parseErrorFiles []string
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
	".git":         true,
	"node_modules": true,
	"dist":         true,
	"build":        true,
	"out":          true,
	"coverage":     true,
	"vendor":       true,
	".next":        true,
	".turbo":       true,
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

func scanRepoEntry(ctx context.Context, state *scanRepoState, path string, entry fs.DirEntry) error {
	if entry.IsDir() {
		if skipDirectories[entry.Name()] {
			return fs.SkipDir
		}
		return nil
	}
	if !isSupportedFile(path) {
		return nil
	}

	content, tree, relPath, err := readAndParseFile(ctx, state.parser, state.repoPath, path)
	if err != nil {
		return err
	}
	if tree.RootNode().HasError() {
		state.parseErrorCount++
		appendParseErrorFile(&state.parseErrorFiles, relPath)
	}
	state.result.Files = append(state.result.Files, analyzeFile(tree, content, relPath))
	return nil
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

func appendParseErrorFile(parseErrorFiles *[]string, relPath string) {
	if len(*parseErrorFiles) < 5 {
		*parseErrorFiles = append(*parseErrorFiles, relPath)
	}
}

func analyzeFile(tree *sitter.Tree, content []byte, relPath string) FileScan {
	imports := collectImportBindings(tree, content, relPath)
	identifierUsage := collectIdentifierUsage(tree, content)
	namespaceUsage := collectNamespaceUsage(tree, content)

	return FileScan{
		Path:            relPath,
		Imports:         imports,
		IdentifierUsage: identifierUsage,
		NamespaceUsage:  namespaceUsage,
	}
}

func isSupportedFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return supportedExtensions[ext]
}

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

func collectImportBindings(tree *sitter.Tree, content []byte, relPath string) []ImportBinding {
	root := tree.RootNode()
	bindings := make([]ImportBinding, 0)
	walkNode(root, func(node *sitter.Node) {
		switch node.Type() {
		case "import_statement":
			bindings = append(bindings, parseImportStatement(node, content, relPath)...)
		case "call_expression":
			bindings = append(bindings, parseRequireCall(node, content, relPath)...)
		}
	})

	return bindings
}

func collectIdentifierUsage(tree *sitter.Tree, content []byte) map[string]int {
	counts := make(map[string]int)
	root := tree.RootNode()
	walkNode(root, func(node *sitter.Node) {
		if node.Type() != "identifier" {
			return
		}
		if !isIdentifierUsage(node) {
			return
		}
		name := nodeText(node, content)
		if name == "" {
			return
		}
		counts[name]++
	})

	return counts
}

func collectNamespaceUsage(tree *sitter.Tree, content []byte) map[string]map[string]int {
	counts := make(map[string]map[string]int)
	for _, ref := range collectNamespaceReferences(tree, content) {
		entry, ok := counts[ref.Local]
		if !ok {
			entry = make(map[string]int)
			counts[ref.Local] = entry
		}
		entry[ref.Property] += ref.Count
	}
	return counts
}

func isIdentifierUsage(node *sitter.Node) bool {
	parent := node.Parent()
	if parent == nil {
		return false
	}

	switch parent.Type() {
	case "import_specifier", "import_clause", "namespace_import", "named_imports", "import_statement":
		return false
	case "variable_declarator", "function_declaration", "class_declaration":
		nameNode := parent.ChildByFieldName("name")
		return nameNode == nil || nameNode.ID() != node.ID()
	case "formal_parameters", "required_parameter", "optional_parameter", "rest_parameter":
		return false
	case "shorthand_property_identifier_pattern", "property_identifier":
		return false
	case "pair_pattern":
		key := parent.ChildByFieldName("key")
		return key == nil || key.ID() != node.ID()
	case "object_pattern", "array_pattern":
		return false
	case "member_expression", "subscript_expression":
		// The object side (e.g. `util` in `util.map`) is tracked via namespace
		// property access, so only non-object identifiers count as direct usage.
		objectNode := parent.ChildByFieldName("object")
		if objectNode != nil && objectNode.ID() == node.ID() {
			return false
		}
		return true
	default:
		return true
	}
}

func walkNode(node *sitter.Node, visit func(*sitter.Node)) {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		visit(child)
		walkNode(child, visit)
	}
}

func parseImportStatement(node *sitter.Node, content []byte, relPath string) []ImportBinding {
	sourceNode := node.ChildByFieldName("source")
	module, ok := extractStringLiteral(sourceNode, content)
	if !ok {
		return nil
	}

	clause := node.ChildByFieldName("import_clause")
	if clause == nil {
		clause = firstNamedChildOfType(node, "import_clause")
	}
	if clause == nil {
		return []ImportBinding{makeImportBinding(module, "*", "*", ImportNamespace, relPath, node)}
	}

	bindings := parseImportClause(clause, content, module, relPath)
	if len(bindings) == 0 {
		bindings = []ImportBinding{makeImportBinding(module, "*", "*", ImportNamespace, relPath, node)}
	}

	return bindings
}

func parseImportClause(node *sitter.Node, content []byte, module string, relPath string) []ImportBinding {
	bindings := make([]ImportBinding, 0)
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		switch child.Type() {
		case "identifier":
			local := nodeText(child, content)
			bindings = append(bindings, makeImportBinding(module, "default", local, ImportDefault, relPath, child))
		case "namespace_import":
			nameNode := child.ChildByFieldName("name")
			if nameNode == nil {
				nameNode = firstNamedChildOfType(child, "identifier")
			}
			local := nodeText(nameNode, content)
			bindings = append(bindings, makeImportBinding(module, "*", local, ImportNamespace, relPath, child))
		case "named_imports":
			bindings = append(bindings, parseNamedImports(child, content, module, relPath)...)
		}
	}

	return bindings
}

func parseNamedImports(node *sitter.Node, content []byte, module string, relPath string) []ImportBinding {
	bindings := make([]ImportBinding, 0)
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child.Type() != "import_specifier" {
			continue
		}

		nameNode := child.ChildByFieldName("name")
		if nameNode == nil {
			nameNode = firstNamedChildOfType(child, "identifier", "property_identifier")
		}
		aliasNode := child.ChildByFieldName("alias")
		if aliasNode == nil {
			aliasNode = nameNode
		}

		exportName := nodeText(nameNode, content)
		localName := nodeText(aliasNode, content)
		if exportName == "" {
			continue
		}
		if localName == "" {
			localName = exportName
		}

		bindings = append(bindings, makeImportBinding(module, exportName, localName, ImportNamed, relPath, child))
	}

	return bindings
}

func parseRequireCall(node *sitter.Node, content []byte, relPath string) []ImportBinding {
	functionNode := node.ChildByFieldName("function")
	if functionNode == nil || functionNode.Type() != "identifier" {
		return nil
	}
	if nodeText(functionNode, content) != "require" {
		return nil
	}

	argumentsNode := node.ChildByFieldName("arguments")
	if argumentsNode == nil || argumentsNode.NamedChildCount() == 0 {
		return nil
	}

	module, ok := extractStringLiteral(argumentsNode.NamedChild(0), content)
	if !ok {
		return nil
	}

	bindings := parseRequireBinding(node, content, module, relPath)
	if len(bindings) == 0 {
		return []ImportBinding{makeImportBinding(module, "*", "*", ImportNamespace, relPath, node)}
	}
	return bindings
}

func parseRequireBinding(call *sitter.Node, content []byte, module string, relPath string) []ImportBinding {
	declarator := call.Parent()
	for declarator != nil && declarator.Type() != "variable_declarator" {
		declarator = declarator.Parent()
	}
	if declarator == nil {
		return nil
	}

	nameNode := declarator.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}

	switch nameNode.Type() {
	case "identifier":
		local := nodeText(nameNode, content)
		return []ImportBinding{makeImportBinding(module, "*", local, ImportNamespace, relPath, nameNode)}
	case "object_pattern":
		bindings := parseObjectPattern(nameNode, content, module, relPath)
		return bindings
	default:
		return nil
	}
}

func parseObjectPattern(node *sitter.Node, content []byte, module string, relPath string) []ImportBinding {
	bindings := make([]ImportBinding, 0)
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		switch child.Type() {
		case "shorthand_property_identifier_pattern", "property_identifier":
			name := nodeText(child, content)
			if name != "" {
				bindings = append(bindings, makeImportBinding(module, name, name, ImportNamed, relPath, child))
			}
		case "pair_pattern":
			keyNode := child.ChildByFieldName("key")
			valueNode := child.ChildByFieldName("value")
			exportName := nodeText(keyNode, content)
			localName := nodeText(valueNode, content)
			if exportName == "" {
				continue
			}
			if localName == "" {
				localName = exportName
			}
			bindings = append(bindings, makeImportBinding(module, exportName, localName, ImportNamed, relPath, child))
		}
	}

	return bindings
}

func makeImportBinding(module string, exportName string, localName string, kind ImportKind, relPath string, node *sitter.Node) ImportBinding {
	location := report.Location{
		File:   relPath,
		Line:   int(node.StartPoint().Row) + 1,
		Column: int(node.StartPoint().Column) + 1,
	}
	return ImportBinding{
		Module:     module,
		ExportName: exportName,
		LocalName:  localName,
		Kind:       kind,
		Location:   location,
	}
}

func extractStringLiteral(node *sitter.Node, content []byte) (string, bool) {
	if node == nil {
		return "", false
	}

	text := nodeText(node, content)
	if text == "" {
		return "", false
	}

	if len(text) >= 2 {
		quote := text[0]
		if (quote == '"' || quote == '\'') && text[len(text)-1] == quote {
			return text[1 : len(text)-1], true
		}
	}

	text = strings.Trim(text, "\"'`")
	if text == "" {
		return "", false
	}
	return text, true
}

func nodeText(node *sitter.Node, content []byte) string {
	if node == nil {
		return ""
	}
	return string(content[node.StartByte():node.EndByte()])
}

func firstNamedChildOfType(node *sitter.Node, types ...string) *sitter.Node {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		for _, typ := range types {
			if child.Type() == typ {
				return child
			}
		}
	}
	return nil
}
