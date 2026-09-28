package reusecheck

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/scanner"
	"go/token"
	"go/types"
	"reflect"
	"strconv"
	"strings"
)

func imports(file *ast.File) map[string]string {
	result := make(map[string]string)
	for _, item := range file.Imports {
		path, err := strconv.Unquote(item.Path.Value)
		if err != nil {
			continue
		}
		name := path[strings.LastIndex(path, "/")+1:]
		if item.Name != nil {
			name = item.Name.Name
		}
		result[name] = path
	}
	return result
}

func unparen(expression ast.Expr) ast.Expr {
	for {
		parenthesized, ok := expression.(*ast.ParenExpr)
		if !ok {
			return expression
		}
		expression = parenthesized.X
	}
}

func imported(expr ast.Expr, packages map[string]string, path, name string) bool {
	expr = unparen(expr)
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != name {
		return false
	}
	ident, ok := selector.X.(*ast.Ident)
	return ok && ident.Obj == nil && packages[ident.Name] == path
}

func canonicalFunction(fn *ast.FuncDecl, packages map[string]string, info *types.Info) string {
	restore := stripExpressionParentheses(fn)
	defer restore()
	names := make(map[types.Object]string)
	original := make(map[*ast.Ident]string)
	ast.Inspect(fn, func(node ast.Node) bool {
		ident, ok := node.(*ast.Ident)
		if !ok {
			return true
		}
		replacement := canonicalName(ident, fn.Name, packages, info, names)
		if replacement != "" {
			original[ident] = ident.Name
			ident.Name = replacement
		}
		return true
	})
	defer func() {
		for ident, name := range original {
			ident.Name = name
		}
	}()
	var out bytes.Buffer
	// Both nodes came from the parser and formatting ASTs cannot perform I/O.
	if err := format.Node(&out, token.NewFileSet(), fn.Type); err != nil {
		return ""
	}
	if err := format.Node(&out, token.NewFileSet(), fn.Body); err != nil {
		return ""
	}
	var scan scanner.Scanner
	fset := token.NewFileSet()
	scan.Init(fset.AddFile("", -1, out.Len()), out.Bytes(), nil, 0)
	var result strings.Builder
	for {
		_, kind, literal := scan.Scan()
		if kind == token.EOF {
			break
		}
		result.WriteString(kind.String())
		result.WriteString(strconv.Quote(literal))
	}
	return result.String()
}

func canonicalName(ident, functionName *ast.Ident, packages map[string]string, info *types.Info, names map[types.Object]string) string {
	object := info.ObjectOf(ident)
	if variable, local := object.(*types.Var); local && !variable.IsField() {
		if names[object] == "" {
			names[object] = "local" + strconv.Itoa(len(names))
		}
		return names[object]
	}
	if isLocalObject(object) && ident != functionName {
		return "bound_" + ident.Name
	}
	if path := packages[ident.Name]; path != "" {
		return "package_" + strings.NewReplacer("/", "_", ".", "_").Replace(path)
	}
	return ""
}

// bindings asks go/types for lexical def/use ownership. The isolated function may
// reference unavailable package declarations; no type error is interpreted as
// proof of a contract. Import ownership and concrete field provenance are checked
// separately, while unresolved syntax cannot match a template.
func bindings(file *ast.File, fset *token.FileSet) *types.Info {
	info := &types.Info{Defs: make(map[*ast.Ident]types.Object), Uses: make(map[*ast.Ident]types.Object)}
	config := types.Config{Error: func(error) {
		// Continue collecting lexical bindings when isolated source cannot type-check.
	}}
	if _, err := config.Check("bindings", fset, []*ast.File{file}, info); err != nil {
		// Keep lexical bindings even when imports are unavailable.
		return info
	}
	return info
}

func isLocalObject(object types.Object) bool {
	if object == nil || object.Parent() == types.Universe {
		return false
	}
	_, imported := object.(*types.PkgName)
	return !imported
}

// Normalize only discarded helper calls, preserving all meaningful block scopes.
// Restore the parsed tree before report-mapping analysis inspects declarations.
func canonicalWithoutDecorativeCalls(path string, fn *ast.FuncDecl, packages map[string]string, info *types.Info) string {
	original := make(map[*ast.BlockStmt][]ast.Stmt)
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		if block, ok := node.(*ast.BlockStmt); ok {
			original[block] = block.List
			block.List = withoutDecorativeCalls(path, block.List, packages)
		}
		return true
	})
	defer func() {
		for block, statements := range original {
			block.List = statements
		}
	}()
	return canonicalFunction(fn, packages, info)
}

func rangeValueType(statement *ast.RangeStmt, object types.Object, info *types.Info, declarations map[types.Object]ast.Node) ast.Expr {
	expression := unparen(statement.X)
	if ident, ok := expression.(*ast.Ident); ok {
		expression = declaredCollectionType(declarations[info.ObjectOf(ident)], info)
	} else {
		expression = allocatedCollectionType(expression, info)
	}
	var element ast.Expr
	binding := statement.Value
	switch collection := unparen(expression).(type) {
	case *ast.ArrayType:
		element = collection.Elt
	case *ast.MapType:
		element = collection.Value
	case *ast.ChanType:
		element = collection.Value
		binding = statement.Key
	}
	ident, ok := binding.(*ast.Ident)
	if !ok || info.ObjectOf(ident) != object {
		return nil
	}
	return element
}

func declaredCollectionType(declaration ast.Node, info *types.Info) ast.Expr {
	var values []ast.Expr
	switch item := declaration.(type) {
	case *ast.Field:
		return item.Type
	case *ast.ValueSpec:
		if item.Type != nil {
			return item.Type
		}
		if len(item.Names) == 1 {
			values = item.Values
		}
	case *ast.AssignStmt:
		if len(item.Lhs) == 1 {
			values = item.Rhs
		}
	}
	if len(values) == 1 {
		return allocatedCollectionType(values[0], info)
	}
	return nil
}

func allocatedCollectionType(expression ast.Expr, info *types.Info) ast.Expr {
	switch item := unparen(expression).(type) {
	case *ast.CompositeLit:
		return item.Type
	case *ast.CallExpr:
		ident, ok := unparen(item.Fun).(*ast.Ident)
		if !ok || len(item.Args) == 0 {
			return nil
		}
		builtin, ok := info.ObjectOf(ident).(*types.Builtin)
		if ok && builtin.Name() == "make" {
			return item.Args[0]
		}
	}
	return nil
}

func declarationNames(node ast.Node) []*ast.Ident {
	switch item := node.(type) {
	case *ast.Field:
		return item.Names
	case *ast.ValueSpec:
		return item.Names
	case *ast.RangeStmt:
		return expressionNames(item.Key, item.Value)
	case *ast.AssignStmt:
		return expressionNames(item.Lhs...)
	default:
		return nil
	}
}

func expressionNames(expressions ...ast.Expr) []*ast.Ident {
	var names []*ast.Ident
	for _, expression := range expressions {
		if ident, ok := expression.(*ast.Ident); ok {
			names = append(names, ident)
		}
	}
	return names
}

// Only discard control flow whose condition cannot call, dereference, index or
// mutate anything, and whose branches contain no meaningful operations.
func decorativeShell(path string, statement ast.Stmt, packages map[string]string) bool {
	switch item := statement.(type) {
	case *ast.BlockStmt:
		return len(item.List) > 0 && len(withoutDecorativeCalls(path, item.List, packages)) == 0
	case *ast.IfStmt:
		if item.Init != nil || !decorativeCondition(item.Cond) || len(withoutDecorativeCalls(path, item.Body.List, packages)) != 0 {
			return false
		}
		if block, ok := item.Else.(*ast.BlockStmt); ok {
			return len(withoutDecorativeCalls(path, block.List, packages)) == 0
		}
		return item.Else == nil || decorativeShell(path, item.Else, packages)
	default:
		return false
	}
}

func decorativeCondition(expression ast.Expr) bool {
	switch item := unparen(expression).(type) {
	case *ast.Ident:
		return true
	case *ast.UnaryExpr:
		return item.Op == token.NOT && decorativeCondition(item.X)
	case *ast.BinaryExpr:
		return (item.Op == token.LAND || item.Op == token.LOR) && decorativeCondition(item.X) && decorativeCondition(item.Y)
	default:
		return false
	}
}

// Rewrite expression slots only; retain all identifier nodes for lexical binding.
// The formatter adds back grouping required by operator precedence. Restore every
// slot afterwards because report provenance uses the original parsed tree.
func stripExpressionParentheses(node ast.Node) func() {
	var restore []func()
	ast.Inspect(node, func(node ast.Node) bool {
		if node == nil {
			return false
		}
		for _, field := range expressionFields(node) {
			restore = appendParenthesisRestore(restore, field)
		}
		return true
	})
	return func() {
		for index := len(restore) - 1; index >= 0; index-- {
			restore[index]()
		}
	}
}

func appendParenthesisRestore(restore []func(), field reflect.Value) []func() {
	expression, ok := field.Interface().(*ast.ParenExpr)
	if !ok {
		return restore
	}
	field.Set(reflect.ValueOf(unparen(expression)))
	return append(restore, func() { field.Set(reflect.ValueOf(expression)) })
}

func expressionFields(node ast.Node) []reflect.Value {
	var expressions []reflect.Value
	fields := reflect.ValueOf(node).Elem()
	for index := 0; index < fields.NumField(); index++ {
		field := fields.Field(index)
		if field.Type() == reflect.TypeFor[ast.Expr]() {
			expressions = append(expressions, field)
		} else if field.Type() == reflect.TypeFor[[]ast.Expr]() {
			for index := 0; index < field.Len(); index++ {
				expressions = append(expressions, field.Index(index))
			}
		}
	}
	return expressions
}
