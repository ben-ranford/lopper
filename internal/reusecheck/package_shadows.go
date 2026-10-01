package reusecheck

import (
	"go/ast"
	"go/token"
	"go/types"
)

// Omitted build variants still own their package names. Retain only that
// ownership, without making their values, types or callable results available.
func analysisShadows(included, omitted []*ast.File) *ast.File {
	if len(included) == 0 {
		return nil
	}
	types := analysisTypeDeclarations(omitted)
	for name, declarations := range analysisTypeDeclarations(included) {
		types[name] = declarations
	}
	position := included[0].Name.Pos()
	scope := analysisShadowScope{types: types, supplied: analysisShadowKeys(included), position: position}
	shadow := &ast.File{Name: &ast.Ident{Name: included[0].Name.Name, NamePos: position}}
	for _, file := range omitted {
		shadow.Decls = append(shadow.Decls, scope.fileDeclarations(file, true)...)
	}
	// A selected method can still depend on an alias supplied only by omitted
	// variants. Preserve those possible blockers without changing its signature.
	for _, file := range included {
		shadow.Decls = append(shadow.Decls, scope.fileDeclarations(file, false)...)
	}
	if len(shadow.Decls) == 0 {
		return nil
	}
	return shadow
}

type analysisShadowScope struct {
	types    map[string][]*ast.TypeSpec
	supplied map[string]bool
	position token.Pos
}

func (s *analysisShadowScope) fileDeclarations(file *ast.File, objects bool) []ast.Decl {
	var shadows []ast.Decl
	for _, declaration := range file.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok && function.Recv != nil {
			shadows = append(shadows, analysisMethodShadows(function, s.types, s.supplied, s.position)...)
			continue
		}
		if !objects {
			continue
		}
		for _, key := range analysisDeclarationKeys(&ast.File{Decls: []ast.Decl{declaration}}) {
			if !s.supplied[key] {
				shadows = append(shadows, analysisNameShadow(key[1:], s.position))
				s.supplied[key] = true
			}
		}
	}
	return shadows
}

func analysisShadowKeys(files []*ast.File) map[string]bool {
	keys := make(map[string]bool)
	types := analysisTypeDeclarations(files)
	for _, file := range files {
		for _, key := range analysisDeclarationKeysWithTypes(file, types) {
			keys[key] = true
		}
	}
	return keys
}

func analysisNameShadow(name string, position token.Pos) ast.Decl {
	ident := &ast.Ident{Name: name, NamePos: position}
	value := &ast.ValueSpec{Names: []*ast.Ident{ident}, Type: &ast.BadExpr{}}
	ident.Obj = ast.NewObj(ast.Var, name)
	ident.Obj.Decl = value
	return &ast.GenDecl{Tok: token.VAR, Specs: []ast.Spec{value}}
}

// Type errors can leave later operands unvisited by go/types. Unresolved parser
// identifiers still retain their lexical scope, so attach only missing opaque
// package bindings and preserve every proven local or named import binding.
func bindAnalysisShadows(included []*ast.File, shadow *ast.File, info *types.Info) {
	if shadow == nil {
		return
	}
	names := analysisShadowNames(shadow)
	for _, file := range included {
		packages := imports(file)
		for _, ident := range file.Unresolved {
			bindAnalysisShadow(ident, names[ident.Name], packages, info)
		}
	}
}

func analysisShadowNames(shadow *ast.File) map[string]*ast.Ident {
	names := make(map[string]*ast.Ident)
	for _, declaration := range shadow.Decls {
		group, ok := declaration.(*ast.GenDecl)
		if !ok || group.Tok != token.VAR {
			continue
		}
		for _, specification := range group.Specs {
			for _, name := range declarationNames(specification) {
				names[name.Name] = name
			}
		}
	}
	return names
}

func bindAnalysisShadow(ident, shadow *ast.Ident, packages map[string]string, info *types.Info) {
	if shadow == nil || ident.Obj != nil || info.ObjectOf(ident) != nil || packages[ident.Name] != "" {
		return
	}
	if object := info.Defs[shadow]; object != nil {
		ident.Obj = shadow.Obj
		info.Uses[ident] = object
	}
}

func analysisMethodShadows(function *ast.FuncDecl, declarations map[string][]*ast.TypeSpec, supplied map[string]bool, position token.Pos) []ast.Decl {
	if function.Name.Name == "_" || len(function.Recv.List) != 1 {
		return nil
	}
	var shadows []ast.Decl
	for _, receiver := range analysisReceiverAliases(function.Recv.List[0].Type, declarations, make(map[*ast.TypeSpec]bool)) {
		name := analysisReceiverName(receiver)
		key := name + "." + function.Name.Name
		if name == "" || supplied[key] {
			continue
		}
		supplied[key] = true
		shadows = append(shadows, &ast.FuncDecl{
			Name: &ast.Ident{Name: function.Name.Name, NamePos: position},
			Recv: &ast.FieldList{List: []*ast.Field{{Type: analysisReceiverCopy(receiver, position)}}},
			Type: &ast.FuncType{Func: position, Params: &ast.FieldList{}},
		})
	}
	return shadows
}

func analysisTypeDeclarations(files []*ast.File) map[string][]*ast.TypeSpec {
	declarations := make(map[string][]*ast.TypeSpec)
	for _, file := range files {
		for _, declaration := range file.Decls {
			group, ok := declaration.(*ast.GenDecl)
			if !ok || group.Tok != token.TYPE {
				continue
			}
			for _, specification := range group.Specs {
				declared := specification.(*ast.TypeSpec)
				declarations[declared.Name.Name] = append(declarations[declared.Name.Name], declared)
			}
		}
	}
	return declarations
}

// Receiver aliases share methods with their target; distinct defined types do
// not. Keep every possible target when mutually exclusive files disagree.
func analysisMethodKeys(function *ast.FuncDecl, declarations map[string][]*ast.TypeSpec) []string {
	if function.Recv == nil {
		if key := analysisFunctionKey(function); key != "" {
			return []string{key}
		}
		return nil
	}
	if function.Name.Name == "_" || len(function.Recv.List) != 1 {
		return nil
	}
	var keys []string
	seen := make(map[string]bool)
	for _, receiver := range analysisReceiverAliases(function.Recv.List[0].Type, declarations, make(map[*ast.TypeSpec]bool)) {
		if name := analysisReceiverName(receiver); name != "" && !seen[name] {
			keys = append(keys, name+"."+function.Name.Name)
			seen[name] = true
		}
	}
	return keys
}

func analysisReceiverAliases(expression ast.Expr, declarations map[string][]*ast.TypeSpec, seen map[*ast.TypeSpec]bool) []ast.Expr {
	expression = unparen(expression)
	if pointer, ok := expression.(*ast.StarExpr); ok {
		// A shadow records the receiver declaration, not its method-set shape.
		// Pointer paths must not hide an equivalent direct path after deduplication.
		return analysisReceiverAliases(pointer.X, declarations, seen)
	}
	name, arguments := analysisReceiverIdentifier(expression)
	if name == nil {
		return nil
	}
	options := declarations[name.Name]
	if name.Obj != nil {
		options = []*ast.TypeSpec{name.Obj.Decl.(*ast.TypeSpec)}
	}
	if len(options) == 0 {
		return []ast.Expr{expression}
	}
	var receivers []ast.Expr
	for _, declaration := range options {
		if !declaration.Assign.IsValid() || declaration.TypeParams.NumFields() != arguments {
			receivers = append(receivers, expression)
		} else if !seen[declaration] {
			seen[declaration] = true
			receivers = append(receivers, analysisReceiverAliases(declaration.Type, declarations, seen)...)
		}
	}
	return receivers
}

func analysisReceiverIdentifier(expression ast.Expr) (*ast.Ident, int) {
	arguments := 0
	switch instance := expression.(type) {
	case *ast.IndexExpr:
		expression, arguments = instance.X, 1
	case *ast.IndexListExpr:
		expression, arguments = instance.X, len(instance.Indices)
	}
	name, ok := unparen(expression).(*ast.Ident)
	if !ok {
		return nil, 0
	}
	if name.Obj != nil {
		if _, declared := name.Obj.Decl.(*ast.TypeSpec); !declared {
			return nil, 0
		}
	}
	return name, arguments
}

// Receiver syntax is private to the shadow file. In particular, never reuse an
// original identifier's object link to a declaration in an omitted variant.
func analysisReceiverCopy(expression ast.Expr, position token.Pos) ast.Expr {
	switch value := expression.(type) {
	case *ast.Ident:
		return &ast.Ident{Name: value.Name, NamePos: position}
	case *ast.IndexExpr:
		return &ast.IndexExpr{X: analysisReceiverCopy(value.X, position), Lbrack: position, Index: analysisReceiverCopy(value.Index, position), Rbrack: position}
	case *ast.IndexListExpr:
		copied := &ast.IndexListExpr{X: analysisReceiverCopy(value.X, position), Lbrack: position, Rbrack: position}
		for _, index := range value.Indices {
			copied.Indices = append(copied.Indices, analysisReceiverCopy(index, position))
		}
		return copied
	default:
		return &ast.BadExpr{}
	}
}
