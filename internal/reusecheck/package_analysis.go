package reusecheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
)

// Analyze checks one source file. AnalyzeSources also resolves declarations
// from sibling files in the same directory and package.
func Analyze(path string, source []byte) ([]Finding, error) {
	return AnalyzeSources(map[string][]byte{path: source})
}

// AnalyzeSources parses all input before checking contracts. Each directory and
// package has its own lexical scope; unavailable imports are never executed.
func AnalyzeSources(sources map[string][]byte) ([]Finding, error) {
	fset := token.NewFileSet()
	groups, err := parseSourcePackages(sources, fset)
	if err != nil {
		return nil, err
	}
	fingerprints, err := contractFingerprints(collectionContracts)
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, files := range groups {
		for _, group := range analysisGroups(files) {
			fresh, positions, parseErr := parseAnalysisGroup(group, sources, fset)
			if parseErr != nil {
				return nil, parseErr
			}
			findings = append(findings, packageFindings(fresh, fingerprints, positions)...)
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Path != findings[j].Path {
			return findings[i].Path < findings[j].Path
		}
		return findings[i].Line < findings[j].Line
	})
	return findings, nil
}

func parseSourcePackages(sources map[string][]byte, fset *token.FileSet) (map[string][]*ast.File, error) {
	paths := make([]string, 0, len(sources))
	for path := range sources {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	groups := make(map[string][]*ast.File)
	for _, path := range paths {
		file, err := parser.ParseFile(fset, path, sources[path], 0)
		if err != nil {
			return nil, err
		}
		key := filepath.Dir(path) + "\x00" + file.Name.Name
		groups[key] = append(groups[key], file)
	}
	return groups, nil
}

// Binding and import normalization mutate AST links and qualifiers. Reparse
// every scope from source bytes so shared files never retain a variant's state.
func parseAnalysisGroup(group analysisGroup, sources map[string][]byte, original *token.FileSet) (analysisGroup, *token.FileSet, error) {
	fset := token.NewFileSet()
	fresh := analysisGroup{omitted: group.omitted}
	for _, file := range group.files {
		path := original.PositionFor(file.Pos(), false).Filename
		parsed, err := parser.ParseFile(fset, path, sources[path], 0)
		if err != nil {
			return analysisGroup{}, nil, err
		}
		fresh.files = append(fresh.files, parsed)
		if file == group.target {
			fresh.target = parsed
		}
	}
	return fresh, fset, nil
}

func fileFindings(file *ast.File, packages map[string]string, info *types.Info, fingerprints map[string]contract, fset *token.FileSet) []Finding {
	var findings []Finding
	path := fset.PositionFor(file.Pos(), false).Filename
	for _, decl := range file.Decls {
		for _, initializer := range packageInitializers(decl) {
			scope := reportMappingScope{root: initializer.expression, name: initializer.name}
			findings = append(findings, scopedReportMappingFindings(path, scope, packages, info, fset)...)
		}
		for _, fn := range declaredFunctions(decl, fset) {
			if fn.Body != nil {
				findings = append(findings, functionFindings(path, fn, packages, info, fingerprints, fset)...)
				findings = append(findings, localCollectionFindings(path, fn, packages, info, fingerprints, fset)...)
			}
		}
	}
	return findings
}

// Report mappings already walk nested bodies. Check each local collection
// contract separately without repeating those report findings.
func localCollectionFindings(path string, fn *ast.FuncDecl, packages map[string]string, info *types.Info, fingerprints map[string]contract, fset *token.FileSet) []Finding {
	var findings []Finding
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		if literal, ok := node.(*ast.FuncLit); ok {
			local := &ast.FuncDecl{Name: ast.NewIdent(functionLiteralName(fn.Name.Name, literal, fset)), Type: literal.Type, Body: literal.Body}
			findings = append(findings, collectionFindings(path, local, packages, info, fingerprints, fset)...)
		}
		return true
	})
	return findings
}

func packageFindings(group analysisGroup, fingerprints map[string]contract, fset *token.FileSet) []Finding {
	files := group.files
	shadows := analysisShadows(files, group.omitted)
	if shadows != nil {
		files = append([]*ast.File{shadows}, files...)
	}
	info := packageBindings(files, fset)
	bindAnalysisShadows(group.files, shadows, info)
	packages := normalizePackageImports(files, info)
	var findings []Finding
	for _, file := range group.files {
		if group.target == nil || file == group.target {
			findings = append(findings, fileFindings(file, packages, info, fingerprints, fset)...)
		}
	}
	return findings
}
