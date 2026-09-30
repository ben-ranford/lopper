package reusecheck

import (
	"go/ast"
	"go/token"
	"go/types"
	"reflect"
	"sort"
	"strconv"
)

func packageBindings(files []*ast.File, fset *token.FileSet) *types.Info {
	info := &types.Info{Implicits: make(map[ast.Node]types.Object), Defs: make(map[*ast.Ident]types.Object), Uses: make(map[*ast.Ident]types.Object)}
	config := types.Config{Error: func(error) {
		// Retain lexical ownership when external imports cannot be checked.
	}}
	defer func() {
		for _, file := range files {
			indexEmbeddedFields(file, info)
			indexFunctionDeclarations(file, info)
		}
		linkSourceObjects(info)
	}()
	if _, err := config.Check("bindings", fset, files, info); err != nil {
		return info
	}
	return info
}

// The parser resolves one file at a time. Extend its source declaration links
// using package-wide lexical ownership, leaving imports and builtins untouched.
func linkSourceObjects(info *types.Info) {
	declarations := make(map[types.Object]*ast.Ident)
	for name, object := range info.Defs {
		if name.Obj != nil && isLocalObject(object) {
			declarations[object] = name
		}
	}
	for name, object := range info.Uses {
		if declaration := declarations[object]; name.Obj == nil && declaration != nil {
			name.Obj = declaration.Obj
		}
	}
}

// Provenance expressions can originate in another file, where an import alias
// has a different meaning. Give every imported path a private, collision-free
// qualifier before those expressions are shared by the package analysis.
func normalizePackageImports(files []*ast.File, info *types.Info) map[string]string {
	names := packageImportNames(files)
	packages := make(map[string]string)
	for path, name := range names {
		packages[name] = path
	}
	for _, file := range files {
		normalizeFileImports(file, info, names)
	}
	return packages
}

func packageImportNames(files []*ast.File) map[string]string {
	reserved := make(map[string]bool)
	paths := make(map[string]string)
	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			if name, ok := node.(*ast.Ident); ok {
				reserved[name.Name] = true
			}
			return true
		})
		for _, path := range imports(file) {
			paths[path] = ""
		}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	index := 0
	for _, path := range ordered {
		name := "_reusecheck_import_" + strconv.Itoa(index)
		for reserved[name] {
			index++
			name = "_reusecheck_import_" + strconv.Itoa(index)
		}
		paths[path] = name
		reserved[name] = true
	}
	return paths
}

func normalizeFileImports(file *ast.File, info *types.Info, names map[string]string) {
	fileImports := imports(file)
	ast.Inspect(file, func(node ast.Node) bool {
		if node == nil {
			return false
		}
		normalizeSelectorImport(node, fileImports, names, info)
		for _, field := range expressionFields(node) {
			normalizeDotImport(field, fileImports, names, info)
		}
		return true
	})
	for _, specification := range file.Imports {
		if specification.Name != nil && (specification.Name.Name == "." || specification.Name.Name == "_") {
			continue
		}
		path, err := strconv.Unquote(specification.Path.Value)
		if err == nil {
			specification.Name = ast.NewIdent(names[path])
		}
	}
}

func normalizeSelectorImport(node ast.Node, packages, names map[string]string, info *types.Info) {
	selector, ok := node.(*ast.SelectorExpr)
	if !ok {
		return
	}
	qualifier, ok := selector.X.(*ast.Ident)
	if !ok {
		return
	}
	if path := packageQualifierPath(qualifier, packages, info); path != "" {
		qualifier.Name = names[path]
	}
}

func packageQualifierPath(ident *ast.Ident, packages map[string]string, info *types.Info) string {
	object := info.ObjectOf(ident)
	if imported, ok := object.(*types.PkgName); ok {
		return imported.Imported().Path()
	}
	// A prior type error can prevent go/types from visiting a later selector.
	// The parser still identifies local shadows independently of type checking.
	if object == nil && ident.Obj == nil {
		return packages[ident.Name]
	}
	return ""
}

func normalizeDotImport(field reflect.Value, packages, names map[string]string, info *types.Info) {
	ident, ok := field.Interface().(*ast.Ident)
	if !ok {
		return
	}
	if _, packageName := info.ObjectOf(ident).(*types.PkgName); packageName {
		return
	}
	path := dotImportedPath(ident, packages)
	if path != "" {
		field.Set(reflect.ValueOf(&ast.SelectorExpr{X: ast.NewIdent(names[path]), Sel: ident}))
	}
}
