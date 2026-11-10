package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	safeioImportPath = "github.com/ben-ranford/lopper/internal/safeio"
	maxScanFileBytes = 8 << 20
)

// Reference is one zero-limit use of a safeio read API.
type Reference struct {
	Path     string
	Function string
	Callee   string
	Index    int
}

func formatReference(ref Reference) string {
	return fmt.Sprintf("%s\t%s\t%s\t%d", ref.Path, ref.Function, ref.Callee, ref.Index)
}

type rawReference struct {
	Reference
}

type safeioNames struct {
	selectors map[string]struct{}
	dot       bool
	samePkg   bool
}

func listZeroLimitReads(root string) ([]Reference, error) {
	root = filepath.Clean(root)
	files, err := productionGoFiles(root)
	if err != nil {
		return nil, err
	}
	byDir := map[string][]string{}
	var dirs []string
	for _, file := range files {
		dir := filepath.Dir(file)
		if _, ok := byDir[dir]; !ok {
			dirs = append(dirs, dir)
		}
		byDir[dir] = append(byDir[dir], file)
	}

	var refs []rawReference
	for _, dir := range dirs {
		parsed, err := parsePackageFiles(root, byDir[dir])
		if err != nil {
			return nil, err
		}
		refs = append(refs, parsed...)
	}
	return assignIndexes(refs), nil
}

func productionGoFiles(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("root %s is not a directory", root)
	}

	var files []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := entry.Name()
		if entry.IsDir() {
			if path != root && skipScanDir(name) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", path)
		}
		if info.Size() > maxScanFileBytes {
			return fmt.Errorf("%s exceeds the %d byte scan limit", path, maxScanFileBytes)
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func skipScanDir(name string) bool {
	switch name {
	case ".git", ".artifacts", ".omx", ".lopper-cache", "vendor", "node_modules", "bin", "dist":
		return true
	default:
		return false
	}
}

type parsedFile struct {
	rel     string
	astFile *ast.File
}

func parsePackageFiles(root string, paths []string) ([]rawReference, error) {
	fset := token.NewFileSet()
	grouped := map[string][]parsedFile{}
	var order []string
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		astFile, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		if astFile.Name == nil || astFile.Name.Name == "" {
			return nil, fmt.Errorf("parse %s: missing package name", path)
		}
		pkg := astFile.Name.Name
		if _, ok := grouped[pkg]; !ok {
			order = append(order, pkg)
		}
		grouped[pkg] = append(grouped[pkg], parsedFile{
			rel:     filepath.ToSlash(rel),
			astFile: astFile,
		})
	}

	var refs []rawReference
	for _, pkg := range order {
		files := grouped[pkg]
		astFiles := make([]*ast.File, 0, len(files))
		for _, file := range files {
			astFiles = append(astFiles, file.astFile)
		}
		consts := collectConstValues(astFiles)
		for _, file := range files {
			refs = append(refs, scanFile(file, safeioNamesFor(file.astFile), consts)...)
		}
	}
	return refs, nil
}

func safeioNamesFor(file *ast.File) safeioNames {
	names := safeioNames{selectors: map[string]struct{}{}}
	if file.Name != nil && file.Name.Name == "safeio" {
		names.samePkg = true
	}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != safeioImportPath {
			continue
		}
		switch {
		case spec.Name == nil:
			names.selectors["safeio"] = struct{}{}
		case spec.Name.Name == ".":
			names.dot = true
		case spec.Name.Name == "_":
			continue
		default:
			names.selectors[spec.Name.Name] = struct{}{}
		}
	}
	return names
}

func scanFile(file parsedFile, names safeioNames, consts map[string]int64) []rawReference {
	var refs []rawReference
	record := func(fn string, callee string, _ token.Pos) {
		refs = append(refs, rawReference{Reference: Reference{
			Path:     file.rel,
			Function: fn,
			Callee:   "safeio." + callee,
		}})
	}

	for _, decl := range file.astFile.Decls {
		switch node := decl.(type) {
		case *ast.FuncDecl:
			fnName := functionName(node)
			if node.Body == nil {
				continue
			}
			collectReferences(node.Body, fnName, names, consts, record)
		case *ast.GenDecl:
			for _, spec := range node.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				fnName := packageValueName(node.Tok, value)
				for _, expr := range value.Values {
					collectReferences(expr, fnName, names, consts, record)
				}
			}
		}
	}
	return refs
}

func functionName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	return receiverTypeName(fn.Recv.List[0].Type) + "." + fn.Name.Name
}

func receiverTypeName(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.StarExpr:
		return receiverTypeName(typed.X)
	case *ast.IndexExpr:
		return receiverTypeName(typed.X)
	case *ast.IndexListExpr:
		return receiverTypeName(typed.X)
	default:
		return "receiver"
	}
}

func packageValueName(tok token.Token, spec *ast.ValueSpec) string {
	kind := "value"
	switch tok {
	case token.CONST:
		kind = "const"
	case token.VAR:
		kind = "var"
	}
	names := make([]string, 0, len(spec.Names))
	for _, name := range spec.Names {
		if name != nil {
			names = append(names, name.Name)
		}
	}
	if len(names) == 0 {
		return kind
	}
	return kind + ":" + strings.Join(names, ",")
}

type referenceRecorder func(function, callee string, pos token.Pos)

func collectReferences(node ast.Node, fn string, names safeioNames, consts map[string]int64, record referenceRecorder) {
	var stack []ast.Node
	ast.Inspect(node, func(current ast.Node) bool {
		if current == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		var parent ast.Node
		if len(stack) > 0 {
			parent = stack[len(stack)-1]
		}
		noteReference(current, parent, fn, names, consts, record)
		stack = append(stack, current)
		return true
	})
}

func noteReference(node, parent ast.Node, fn string, names safeioNames, consts map[string]int64, record referenceRecorder) {
	switch typed := node.(type) {
	case *ast.SelectorExpr:
		pkg, ok := typed.X.(*ast.Ident)
		if !ok {
			return
		}
		if _, known := names.selectors[pkg.Name]; !known {
			return
		}
		if !considerCallee(typed.Sel.Name, parent, typed, consts) {
			return
		}
		record(fn, typed.Sel.Name, typed.Pos())
	case *ast.Ident:
		if selector, ok := parent.(*ast.SelectorExpr); ok && selector.Sel == typed {
			return
		}
		if !names.dot && !names.samePkg {
			return
		}
		if !considerCallee(typed.Name, parent, typed, consts) {
			return
		}
		record(fn, typed.Name, typed.Pos())
	}
}

func considerCallee(name string, parent, fun ast.Node, consts map[string]int64) bool {
	switch name {
	case "ReadFile", "ReadFileUnder", "ReadFileWithinRoot":
		return true
	}
	if !isLimitedRead(name) {
		return false
	}
	call, ok := parent.(*ast.CallExpr)
	if !ok || call.Fun != fun {
		return false
	}
	if len(call.Args) == 0 {
		return true
	}
	value, known := evalInt(call.Args[len(call.Args)-1], consts, 0)
	return known && value <= 0
}

func isLimitedRead(name string) bool {
	return strings.HasPrefix(name, "Read") && strings.HasSuffix(name, "Limit")
}

func collectConstValues(files []*ast.File) map[string]int64 {
	type binding struct {
		name string
		expr ast.Expr
		iota int64
	}
	var bindings []binding
	for _, file := range files {
		if file == nil {
			continue
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
				continue
			}
			iotaValue := int64(0)
			var previous []ast.Expr
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				exprs := value.Values
				if len(exprs) == 0 {
					exprs = previous
				} else {
					previous = exprs
				}
				for i, name := range value.Names {
					if name == nil || i >= len(exprs) {
						continue
					}
					bindings = append(bindings, binding{name: name.Name, expr: exprs[i], iota: iotaValue})
				}
				iotaValue++
			}
		}
	}

	values := map[string]int64{}
	progress := true
	for progress {
		progress = false
		for _, binding := range bindings {
			if _, done := values[binding.name]; done {
				continue
			}
			value, ok := evalInt(binding.expr, values, binding.iota)
			if !ok {
				continue
			}
			values[binding.name] = value
			progress = true
		}
	}
	return values
}

func evalInt(expr ast.Expr, known map[string]int64, iotaValue int64) (int64, bool) {
	switch typed := expr.(type) {
	case *ast.BasicLit:
		if typed.Kind != token.INT {
			return 0, false
		}
		value, err := strconv.ParseInt(typed.Value, 0, 64)
		if err != nil {
			return 0, false
		}
		return value, true
	case *ast.ParenExpr:
		return evalInt(typed.X, known, iotaValue)
	case *ast.UnaryExpr:
		if typed.Op != token.SUB {
			return 0, false
		}
		value, ok := evalInt(typed.X, known, iotaValue)
		if !ok {
			return 0, false
		}
		return -value, true
	case *ast.Ident:
		if typed.Name == "iota" {
			return iotaValue, true
		}
		value, ok := known[typed.Name]
		return value, ok
	case *ast.BinaryExpr:
		left, lok := evalInt(typed.X, known, iotaValue)
		right, rok := evalInt(typed.Y, known, iotaValue)
		if !lok || !rok {
			return 0, false
		}
		switch typed.Op {
		case token.ADD:
			return left + right, true
		case token.SUB:
			return left - right, true
		case token.MUL:
			return left * right, true
		case token.QUO:
			if right == 0 {
				return 0, false
			}
			return left / right, true
		case token.SHL:
			if right < 0 || right >= 63 {
				return 0, false
			}
			return left << right, true
		case token.SHR:
			if right < 0 || right >= 63 {
				return 0, false
			}
			return left >> right, true
		default:
			return 0, false
		}
	case *ast.CallExpr:
		if len(typed.Args) != 1 || !isIntConversion(typed.Fun) {
			return 0, false
		}
		return evalInt(typed.Args[0], known, iotaValue)
	default:
		return 0, false
	}
}

func isIntConversion(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	if !ok {
		return false
	}
	switch ident.Name {
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64":
		return true
	default:
		return false
	}
}

func assignIndexes(refs []rawReference) []Reference {
	counts := map[string]int{}
	indexed := make([]Reference, 0, len(refs))
	for _, ref := range refs {
		key := ref.Path + "\t" + ref.Function + "\t" + ref.Callee
		counts[key]++
		ref.Index = counts[key]
		indexed = append(indexed, ref.Reference)
	}
	return indexed
}
