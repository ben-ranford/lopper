package reusecheck

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/scanner"
	"go/token"
	"go/types"
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

func rangeValueType(expression ast.Expr, info *types.Info, declarations map[types.Object]ast.Node) ast.Expr {
	expression = unparen(expression)
	if ident, ok := expression.(*ast.Ident); ok {
		expression = declaredCollectionType(declarations[info.ObjectOf(ident)])
	} else if literal, ok := expression.(*ast.CompositeLit); ok {
		expression = literal.Type
	}
	switch collection := unparen(expression).(type) {
	case *ast.ArrayType:
		return collection.Elt
	case *ast.MapType:
		return collection.Value
	default:
		return nil
	}
}

func declaredCollectionType(declaration ast.Node) ast.Expr {
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
		if literal, ok := unparen(values[0]).(*ast.CompositeLit); ok {
			return literal.Type
		}
	}
	return nil
}
