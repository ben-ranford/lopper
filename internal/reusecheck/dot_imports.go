package reusecheck

import (
	"go/ast"
	"reflect"
	"strings"
)

// Only symbols used by owned contracts are resolved without package type data.
var dotSymbols = map[string]string{
	"DependencyStats":                sharedPackage,
	"BuildDependencyStats":           sharedPackage,
	"BuildDependencyReportFromStats": sharedPackage,
	"SortedKeys":                     sharedPackage,
	"SortedDependencyUnion":          sharedPackage,
	"DependencyReport":               module + "report",
	"SortedUniqueTrimmedStrings":     module + "report",
	"Strings":                        "sort",
	"TrimSpace":                      "strings",
}

func dotImportedPath(ident *ast.Ident, packages map[string]string) string {
	if ident.Obj != nil {
		return ""
	}
	path := dotSymbols[ident.Name]
	if path != "" && packages["."+path] == path {
		return path
	}
	return ""
}

// Temporarily use the same selector representation as qualified imports.
func expandDotImports(root ast.Node, packages map[string]string) func() {
	var restore []func()
	ast.Inspect(root, func(node ast.Node) bool {
		if node == nil {
			return false
		}
		for _, field := range expressionFields(node) {
			restore = appendDotImportRestore(restore, field, packages)
		}
		return true
	})
	return func() {
		for index := len(restore) - 1; index >= 0; index-- {
			restore[index]()
		}
	}
}

func appendDotImportRestore(restore []func(), field reflect.Value, packages map[string]string) []func() {
	ident, ok := field.Interface().(*ast.Ident)
	if !ok {
		return restore
	}
	path := dotImportedPath(ident, packages)
	if path == "" {
		return restore
	}
	packageName := "package_" + strings.NewReplacer("/", "_", ".", "_").Replace(path)
	field.Set(reflect.ValueOf(&ast.SelectorExpr{X: ast.NewIdent(packageName), Sel: ident}))
	return append(restore, func() { field.Set(reflect.ValueOf(ident)) })
}
