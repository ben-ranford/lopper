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
			findings = append(findings, packageFindings(group, fingerprints, fset)...)
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

func fileFindings(file *ast.File, packages map[string]string, info *types.Info, fingerprints map[string]contract, fset *token.FileSet) []Finding {
	var findings []Finding
	path := fset.Position(file.Pos()).Filename
	for _, decl := range file.Decls {
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

func packageFindings(files []*ast.File, fingerprints map[string]contract, fset *token.FileSet) []Finding {
	info := packageBindings(files, fset)
	packages := normalizePackageImports(files, info)
	var findings []Finding
	for _, file := range files {
		findings = append(findings, fileFindings(file, packages, info, fingerprints, fset)...)
	}
	return findings
}
