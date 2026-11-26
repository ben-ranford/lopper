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
	"slices"
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
			return checkScanDirectory(root, path)
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

func checkScanDirectory(root, path string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	switch filepath.ToSlash(relative) {
	case ".git", ".artifacts", ".omx", ".lopper-cache", "vendor", "bin", "dist",
		"extensions/vscode-lopper/node_modules", "extensions/vscode-lopper/out", "extensions/vscode-lopper/.vscode-test":
		return filepath.SkipDir
	default:
		return nil
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
		src, err := safeio.ReadRegularFileLimit(path, maxScanFileBytes)
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
	record := func(fn, callee string, _ token.Pos) {
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
	node     ast.Node
	pushed   bool
	iota     int64
	previous []ast.Expr
}

func collectReferences(node ast.Node, fn string, names safeioNames, scope *nameScope, record referenceRecorder) {
	var frames []walkFrame
	ast.Inspect(node, func(current ast.Node) bool {
		if current == nil {
			frame := frames[len(frames)-1]
			frames = frames[:len(frames)-1]
			finishNode(frame.node, scope)
			if len(frames) > 0 {
				frames[len(frames)-1].finishChild(frame.node, scope)
			}
			if frame.pushed {
				scope = scope.parent
			}
			return false
		}
		var parent ast.Node
		if len(frames) > 0 {
			parent = frames[len(frames)-1].node
		}
		pushed := false
		if child, ok := nestedScope(current, scope); ok {
			scope = child
			pushed = true
		}
		if literal, ok := current.(*ast.FuncLit); ok {
			declareFields(scope, literal.Type.Params)
			declareFields(scope, literal.Type.Results)
		}
		noteReference(current, parent, fn, names, scope, record)
		frames = append(frames, walkFrame{node: current, pushed: pushed})
		return true
	})
}

func nestedScope(node ast.Node, parent *nameScope) (*nameScope, bool) {
	switch typed := node.(type) {
	case *ast.BlockStmt, *ast.FuncLit, *ast.CaseClause, *ast.CommClause:
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

func (f *walkFrame) finishChild(child ast.Node, scope *nameScope) {
	switch parent := f.node.(type) {
	case *ast.GenDecl:
		if spec, ok := child.(*ast.ValueSpec); ok {
			exprs := spec.Values
			if parent.Tok == token.CONST {
				exprs = valueExprs(spec, f.previous)
				f.previous = exprs
			}
			bindSpec(scope, spec, exprs, f.iota)
			f.iota++
		}
	case *ast.RangeStmt:
		if child == parent.X {
			bindRange(scope, parent)
		}
	}
}

func bindRange(scope *nameScope, stmt *ast.RangeStmt) {
	if stmt.Tok == token.DEFINE {
		declareExpr(scope, stmt.Key)
		declareExpr(scope, stmt.Value)
		return
	}
	scope.forget(stmt.Key)
	scope.forget(stmt.Value)
}

func finishNode(node ast.Node, scope *nameScope) {
	switch typed := node.(type) {
	case *ast.AssignStmt:
		if typed.Tok == token.DEFINE {
			bindShortDeclaration(scope, typed)
			return
		}
		for _, expr := range typed.Lhs {
			scope.forget(expr)
		}
	case *ast.IncDecStmt:
		scope.forget(typed.X)
	case *ast.UnaryExpr:
		if typed.Op == token.AND {
			scope.forget(typed.X)
		}
	}
}

func bindShortDeclaration(scope *nameScope, assign *ast.AssignStmt) {
	if len(assign.Lhs) != len(assign.Rhs) {
		declareExprs(scope, assign.Lhs)
		return
	}
	known := scope.visibleInts()
	for i, expr := range assign.Lhs {
		if ident, ok := expr.(*ast.Ident); ok {
			value, knownValue := evalInt(assign.Rhs[i], known, 0)
			scope.declare(ident.Name, value, knownValue)
		}
	}
}

func noteReference(node, parent ast.Node, fn string, names safeioNames, scope *nameScope, record referenceRecorder) {
	switch typed := node.(type) {
	case *ast.SelectorExpr:
		pkg, ok := typed.X.(*ast.Ident)
		if !ok {
			return
		}
		if _, known := names.selectors[pkg.Name]; !known || scope.isDeclared(pkg.Name) {
			return
		}
		if !considerCallee(typed.Sel.Name, parent, typed, scope) {
			return
		}
		record(fn, typed.Sel.Name, typed.Pos())
	case *ast.Ident:
		if (!names.dot && !names.samePkg) || scope.isDeclared(typed.Name) || isBindingName(typed, parent) {
			return
		}
		if !considerCallee(typed.Name, parent, typed, scope) {
			return
		}
		record(fn, typed.Name, typed.Pos())
	}
}

func isBindingName(name *ast.Ident, parent ast.Node) bool {
	switch typed := parent.(type) {
	case *ast.SelectorExpr:
		return typed.Sel == name
	case *ast.AssignStmt:
		return slices.Contains(typed.Lhs, ast.Expr(name))
	case *ast.ValueSpec:
		return slices.Contains(typed.Names, name)
	case *ast.RangeStmt:
		return typed.Key == name || typed.Value == name
	case *ast.Field, *ast.TypeSpec, *ast.LabeledStmt, *ast.BranchStmt:
		return true
	default:
		return false
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
	legacy := isLegacyLimitedRead(name)
	call, ok := parent.(*ast.CallExpr)
	if !ok || call.Fun != fun {
		return legacy
	}
	if len(call.Args) == 0 {
		return true
	}
	value, known := evalInt(call.Args[len(call.Args)-1], scope.visibleInts(), 0)
	return (known && value <= 0) || (!known && legacy)
}

func isLegacyLimitedRead(name string) bool {
	switch name {
	case "ReadFileLimit", "ReadFileUnderLimit", "ReadFileWithinRootLimit":
		return true
	default:
		return false
	}
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

func (s *nameScope) forget(expr ast.Expr) {
	ident, ok := ast.Unparen(expr).(*ast.Ident)
	if !ok {
		return
	}
	for scope := s; scope != nil; scope = scope.parent {
		if scope.names[ident.Name] {
			delete(scope.ints, ident.Name)
			return
		}
	}
}

func (s *nameScope) isDeclared(name string) bool {
	for scope := s; scope != nil; scope = scope.parent {
		if scope.names[name] {
			return true
		}
	}
	return false
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
	if !ok || gen.Tok != token.CONST {
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
	default:
		// Call expressions need type information, including integer conversions.
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
	if expr.Op == token.ADD {
		return evalInt(expr.X, known, iotaValue)
	}
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
