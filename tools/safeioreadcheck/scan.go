package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
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
		src, err := safeio.ReadFileLimit(path, maxScanFileBytes)
		if err != nil {
			if errors.Is(err, safeio.ErrFileTooLarge) {
				return nil, fmt.Errorf("%s exceeds the %d byte scan limit", path, maxScanFileBytes)
			}
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
		collectDeclReferences(decl, names, consts, record)
	}
	return refs
}

func collectDeclReferences(decl ast.Decl, names safeioNames, consts map[string]int64, record referenceRecorder) {
	switch node := decl.(type) {
	case *ast.FuncDecl:
		collectFuncReferences(node, names, consts, record)
	case *ast.GenDecl:
		collectValueReferences(node, names, consts, record)
	}
}

func collectFuncReferences(fn *ast.FuncDecl, names safeioNames, consts map[string]int64, record referenceRecorder) {
	if fn.Body == nil {
		return
	}
	scope := newNameScope(packageScope(consts))
	declareFields(scope, fn.Recv)
	if fn.Type != nil {
		declareFields(scope, fn.Type.Params)
		declareFields(scope, fn.Type.Results)
	}
	collectReferences(fn.Body, functionName(fn), names, scope, record)
}

func collectValueReferences(decl *ast.GenDecl, names safeioNames, consts map[string]int64, record referenceRecorder) {
	scope := packageScope(consts)
	for _, spec := range decl.Specs {
		value, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		fnName := packageValueName(decl.Tok, value)
		for _, expr := range value.Values {
			collectReferences(expr, fnName, names, scope, record)
		}
	}
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

type walkFrame struct {
	pushed bool
}

func collectReferences(node ast.Node, fn string, names safeioNames, scope *nameScope, record referenceRecorder) {
	var stack []ast.Node
	var frames []walkFrame
	ast.Inspect(node, func(current ast.Node) bool {
		if current == nil {
			frame := frames[len(frames)-1]
			frames = frames[:len(frames)-1]
			stack = stack[:len(stack)-1]
			if frame.pushed {
				scope = scope.parent
			}
			return false
		}
		var parent ast.Node
		if len(stack) > 0 {
			parent = stack[len(stack)-1]
		}
		pushed := false
		if child, ok := nestedScope(current, scope); ok {
			scope = child
			pushed = true
		}
		bindNode(current, scope)
		noteReference(current, parent, fn, names, scope, record)
		stack = append(stack, current)
		frames = append(frames, walkFrame{pushed: pushed})
		return true
	})
}

func nestedScope(node ast.Node, parent *nameScope) (*nameScope, bool) {
	switch typed := node.(type) {
	case *ast.BlockStmt, *ast.FuncLit:
		return newNameScope(parent), true
	case *ast.IfStmt:
		return scopeForDefine(typed.Init, parent)
	case *ast.ForStmt:
		return scopeForDefine(typed.Init, parent)
	case *ast.SwitchStmt:
		return scopeForDefine(typed.Init, parent)
	case *ast.TypeSwitchStmt:
		return scopeForDefine(typed.Assign, parent)
	case *ast.RangeStmt:
		if typed.Tok == token.DEFINE {
			return newNameScope(parent), true
		}
	}
	return parent, false
}

func scopeForDefine(stmt ast.Stmt, parent *nameScope) (*nameScope, bool) {
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok || assign.Tok != token.DEFINE {
		return parent, false
	}
	return newNameScope(parent), true
}

func bindNode(node ast.Node, scope *nameScope) {
	switch typed := node.(type) {
	case *ast.GenDecl:
		if typed.Tok == token.CONST || typed.Tok == token.VAR {
			bindGenDecl(scope, typed)
		}
	case *ast.FuncLit:
		declareFields(scope, typed.Type.Params)
		declareFields(scope, typed.Type.Results)
	case *ast.AssignStmt:
		if typed.Tok == token.DEFINE {
			declareExprs(scope, typed.Lhs)
		}
	case *ast.RangeStmt:
		if typed.Tok == token.DEFINE {
			declareExpr(scope, typed.Key)
			declareExpr(scope, typed.Value)
		}
	case *ast.IfStmt, *ast.ForStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt:
		declareDefineInit(scope, defineInit(typed))
	}
}

func defineInit(node ast.Node) ast.Stmt {
	switch typed := node.(type) {
	case *ast.IfStmt:
		return typed.Init
	case *ast.ForStmt:
		return typed.Init
	case *ast.SwitchStmt:
		return typed.Init
	case *ast.TypeSwitchStmt:
		return typed.Assign
	default:
		return nil
	}
}

func declareDefineInit(scope *nameScope, stmt ast.Stmt) {
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok || assign.Tok != token.DEFINE {
		return
	}
	declareExprs(scope, assign.Lhs)
}

func noteReference(node, parent ast.Node, fn string, names safeioNames, scope *nameScope, record referenceRecorder) {
	switch typed := node.(type) {
	case *ast.SelectorExpr:
		pkg, ok := typed.X.(*ast.Ident)
		if !ok {
			return
		}
		if _, known := names.selectors[pkg.Name]; !known {
			return
		}
		if !considerCallee(typed.Sel.Name, parent, typed, scope) {
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
		if !considerCallee(typed.Name, parent, typed, scope) {
			return
		}
		record(fn, typed.Name, typed.Pos())
	}
}

func considerCallee(name string, parent, fun ast.Node, scope *nameScope) bool {
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
	value, known := evalInt(call.Args[len(call.Args)-1], scope.visibleInts(), 0)
	return known && value <= 0
}

func isLimitedRead(name string) bool {
	return strings.HasPrefix(name, "Read") && strings.HasSuffix(name, "Limit")
}

type nameScope struct {
	parent *nameScope
	names  map[string]bool
	ints   map[string]int64
}

func newNameScope(parent *nameScope) *nameScope {
	return &nameScope{parent: parent, names: map[string]bool{}, ints: map[string]int64{}}
}

func packageScope(consts map[string]int64) *nameScope {
	scope := newNameScope(nil)
	for name, value := range consts {
		scope.declare(name, value, true)
	}
	return scope
}

func (s *nameScope) declare(name string, value int64, ok bool) {
	if s == nil || name == "" || name == "_" {
		return
	}
	s.names[name] = true
	if ok {
		s.ints[name] = value
		return
	}
	delete(s.ints, name)
}

func (s *nameScope) visibleInts() map[string]int64 {
	out := map[string]int64{}
	seen := map[string]bool{}
	for scope := s; scope != nil; scope = scope.parent {
		for name := range scope.names {
			if seen[name] {
				continue
			}
			seen[name] = true
			if value, ok := scope.ints[name]; ok {
				out[name] = value
			}
		}
	}
	return out
}

func declareFields(scope *nameScope, fields *ast.FieldList) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		declareExprs(scope, identsToExprs(field.Names))
	}
}

func identsToExprs(names []*ast.Ident) []ast.Expr {
	exprs := make([]ast.Expr, 0, len(names))
	for _, name := range names {
		exprs = append(exprs, name)
	}
	return exprs
}

func declareExprs(scope *nameScope, exprs []ast.Expr) {
	for _, expr := range exprs {
		declareExpr(scope, expr)
	}
}

func declareExpr(scope *nameScope, expr ast.Expr) {
	ident, ok := expr.(*ast.Ident)
	if !ok {
		return
	}
	scope.declare(ident.Name, 0, false)
}

func bindGenDecl(scope *nameScope, gen *ast.GenDecl) {
	iotaValue := int64(0)
	var previous []ast.Expr
	for _, spec := range gen.Specs {
		value, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		exprs := valueExprs(value, previous)
		if len(value.Values) > 0 {
			previous = exprs
		}
		bindSpec(scope, value, exprs, iotaValue)
		iotaValue++
	}
}

func bindSpec(scope *nameScope, spec *ast.ValueSpec, exprs []ast.Expr, iotaValue int64) {
	known := scope.visibleInts()
	for index, name := range spec.Names {
		if name == nil {
			continue
		}
		var value int64
		ok := false
		if index < len(exprs) {
			value, ok = evalInt(exprs[index], known, iotaValue)
		}
		scope.declare(name.Name, value, ok)
	}
}

type constBinding struct {
	name string
	expr ast.Expr
	iota int64
}

func collectConstValues(files []*ast.File) map[string]int64 {
	return resolveConstBindings(constBindings(files))
}

func constBindings(files []*ast.File) []constBinding {
	var bindings []constBinding
	for _, file := range files {
		bindings = append(bindings, bindingsInFile(file)...)
	}
	return bindings
}

func bindingsInFile(file *ast.File) []constBinding {
	if file == nil {
		return nil
	}
	var bindings []constBinding
	for _, decl := range file.Decls {
		bindings = append(bindings, bindingsInDecl(decl)...)
	}
	return bindings
}

func bindingsInDecl(decl ast.Decl) []constBinding {
	gen, ok := decl.(*ast.GenDecl)
	if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
		return nil
	}
	return bindingsInGenDecl(gen)
}

func bindingsInGenDecl(gen *ast.GenDecl) []constBinding {
	var bindings []constBinding
	iotaValue := int64(0)
	var previous []ast.Expr
	for _, spec := range gen.Specs {
		value, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		exprs := valueExprs(value, previous)
		if len(value.Values) > 0 {
			previous = exprs
		}
		bindings = append(bindings, namedBindings(value.Names, exprs, iotaValue)...)
		iotaValue++
	}
	return bindings
}

func valueExprs(value *ast.ValueSpec, previous []ast.Expr) []ast.Expr {
	if len(value.Values) == 0 {
		return previous
	}
	return value.Values
}

func namedBindings(names []*ast.Ident, exprs []ast.Expr, iotaValue int64) []constBinding {
	var bindings []constBinding
	for i, name := range names {
		if name == nil || i >= len(exprs) {
			continue
		}
		bindings = append(bindings, constBinding{name: name.Name, expr: exprs[i], iota: iotaValue})
	}
	return bindings
}

func resolveConstBindings(bindings []constBinding) map[string]int64 {
	values := map[string]int64{}
	for resolvePending(bindings, values) {
	}
	return values
}

func resolvePending(bindings []constBinding, values map[string]int64) bool {
	progress := false
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
	return progress
}

func evalInt(expr ast.Expr, known map[string]int64, iotaValue int64) (int64, bool) {
	switch typed := expr.(type) {
	case *ast.BasicLit:
		return evalBasicInt(typed)
	case *ast.ParenExpr:
		return evalInt(typed.X, known, iotaValue)
	case *ast.UnaryExpr:
		return evalUnaryInt(typed, known, iotaValue)
	case *ast.Ident:
		return evalIdentInt(typed, known, iotaValue)
	case *ast.BinaryExpr:
		return evalBinaryInt(typed, known, iotaValue)
	case *ast.CallExpr:
		return evalCallInt(typed, known, iotaValue)
	default:
		return 0, false
	}
}

func evalBasicInt(lit *ast.BasicLit) (int64, bool) {
	if lit.Kind != token.INT {
		return 0, false
	}
	value, err := strconv.ParseInt(lit.Value, 0, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func evalUnaryInt(expr *ast.UnaryExpr, known map[string]int64, iotaValue int64) (int64, bool) {
	if expr.Op != token.SUB {
		return 0, false
	}
	value, ok := evalInt(expr.X, known, iotaValue)
	if !ok {
		return 0, false
	}
	return -value, true
}

func evalIdentInt(ident *ast.Ident, known map[string]int64, iotaValue int64) (int64, bool) {
	if ident.Name == "iota" {
		return iotaValue, true
	}
	value, ok := known[ident.Name]
	return value, ok
}

func evalBinaryInt(expr *ast.BinaryExpr, known map[string]int64, iotaValue int64) (int64, bool) {
	left, lok := evalInt(expr.X, known, iotaValue)
	right, rok := evalInt(expr.Y, known, iotaValue)
	if !lok || !rok {
		return 0, false
	}
	return applyBinaryInt(expr.Op, left, right)
}

func applyBinaryInt(op token.Token, left, right int64) (int64, bool) {
	switch op {
	case token.ADD:
		return left + right, true
	case token.SUB:
		return left - right, true
	case token.MUL:
		return left * right, true
	case token.QUO:
		return applyQuotient(left, right)
	case token.SHL, token.SHR:
		return applyShift(op, left, right)
	default:
		return 0, false
	}
}

func applyQuotient(left, right int64) (int64, bool) {
	if right == 0 {
		return 0, false
	}
	return left / right, true
}

func applyShift(op token.Token, left, right int64) (int64, bool) {
	if right < 0 || right >= 63 {
		return 0, false
	}
	if op == token.SHL {
		return left << right, true
	}
	return left >> right, true
}

func evalCallInt(expr *ast.CallExpr, known map[string]int64, iotaValue int64) (int64, bool) {
	if len(expr.Args) != 1 || !isIntConversion(expr.Fun) {
		return 0, false
	}
	return evalInt(expr.Args[0], known, iotaValue)
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
