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

func imported(expr ast.Expr, packages map[string]string, path, name string) bool {
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
		replacement := ""
		object := info.ObjectOf(ident)
		variable, local := object.(*types.Var)
		switch {
		case local && !variable.IsField():
			replacement = names[object]
			if replacement == "" {
				replacement = "local" + strconv.Itoa(len(names))
				names[object] = replacement
			}
		case isLocalObject(object) && ident != fn.Name:
			replacement = "bound_" + ident.Name
		case packages[ident.Name] != "":
			replacement = "package_" + strings.NewReplacer("/", "_", ".", "_").Replace(packages[ident.Name])
		}
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

// bindings asks go/types for lexical def/use ownership. The isolated function may
// reference unavailable package declarations; no type error is interpreted as
// proof of a contract. Import ownership and concrete field provenance are checked
// separately, while unresolved syntax cannot match a template.
func bindings(file *ast.File, fset *token.FileSet) *types.Info {
	info := &types.Info{Defs: make(map[*ast.Ident]types.Object), Uses: make(map[*ast.Ident]types.Object)}
	config := types.Config{Error: func(error) {}}
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
