package reusecheck

import (
	"go/ast"
	"go/types"
)

// Method names have no parser object, so retain their source signatures beside
// the lexical objects that go/types associates with calls and method values.
func indexFunctionDeclarations(file *ast.File, info *types.Info) {
	for _, declaration := range file.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok {
			if object := info.Defs[function.Name]; object != nil {
				info.Implicits[function] = object
			}
		}
	}
}

func inferredStatsCallType(call *ast.CallExpr, packages map[string]string, info *types.Info, declarations map[types.Object]ast.Node) ast.Expr {
	if result := statsFactoryResultType(call, packages); result != nil {
		return result
	}
	return sourceCallResultType(call, info, declarations)
}

func sourceCallResultType(call *ast.CallExpr, info *types.Info, declarations map[types.Object]ast.Node) ast.Expr {
	return sourceCallResultAt(call, 1, 0, info, declarations, make(map[types.Object]bool))
}

func sourceCallResultAt(call *ast.CallExpr, count, index int, info *types.Info, declarations map[types.Object]ast.Node, seen map[types.Object]bool) ast.Expr {
	signature := sourceFunctionType(call.Fun, info, declarations, seen)
	if signature == nil || !sourceCallArity(call, signature) {
		return nil
	}
	results := fieldListTypes(signature.Results)
	if len(results) != count {
		return nil
	}
	return results[index]
}

func sourceFunctionType(expression ast.Expr, info *types.Info, declarations map[types.Object]ast.Node, seen map[types.Object]bool) *ast.FuncType {
	switch value := unparen(expression).(type) {
	case *ast.FuncLit:
		return value.Type
	case *ast.TypeAssertExpr:
		return declaredFunctionType(value.Type)
	case *ast.IndexExpr:
		return instantiatedSourceFunctionType(value.X, 1, info, declarations, seen)
	case *ast.IndexListExpr:
		return instantiatedSourceFunctionType(value.X, len(value.Indices), info, declarations, seen)
	case *ast.Ident:
		object := info.ObjectOf(value)
		if object == nil || seen[object] {
			return nil
		}
		seen[object] = true
		declaration := sourceBindingDeclaration(sourceCallableDeclaration(object, info, declarations), info, declarations, seen)
		if initializer := aliasInitializer(declaration); initializer != nil {
			return sourceFunctionType(initializer, info, declarations, seen)
		}
		return declaredCallableType(declaration)
	case *ast.SelectorExpr:
		declaration := sourceCallableDeclaration(info.ObjectOf(value.Sel), info, declarations)
		if function, ok := declaration.(*ast.FuncDecl); ok {
			return selectedFunctionType(function, value.X, info)
		}
		return declaredFunctionType(declaredSelectorType(value, info))
	}
	return nil
}

// Type arguments do not change an explicit result type. Keep type-parameter
// results unresolved rather than treating the arguments as substitution proof.
func instantiatedSourceFunctionType(expression ast.Expr, count int, info *types.Info, declarations map[types.Object]ast.Node, seen map[types.Object]bool) *ast.FuncType {
	signature := sourceFunctionType(expression, info, declarations, seen)
	if signature == nil || count == 0 || count > signature.TypeParams.NumFields() {
		return nil
	}
	return signature
}

func sourceCallableDeclaration(object types.Object, info *types.Info, declarations map[types.Object]ast.Node) ast.Node {
	if object == nil {
		return nil
	}
	if declaration := declarations[object]; declaration != nil {
		return declaration
	}
	for node, defined := range info.Implicits {
		if defined == object {
			if function, ok := node.(*ast.FuncDecl); ok {
				return function
			}
		}
	}
	for name, defined := range info.Defs {
		if defined == object && name.Obj != nil {
			return sourceNamedDeclaration(name)
		}
	}
	return nil
}

func sourceNamedDeclaration(name *ast.Ident) ast.Node {
	declaration, ok := name.Obj.Decl.(ast.Node)
	if !ok {
		return nil
	}
	for index, binding := range declarationNames(declaration) {
		if binding == name {
			return bindingDeclaration(declaration, index)
		}
	}
	return declaration
}

func declaredCallableType(declaration ast.Node) *ast.FuncType {
	if function, ok := declaration.(*ast.FuncDecl); ok {
		return function.Type
	}
	return declaredFunctionType(declaredCollectionType(declaration))
}

// A defined function type retains its call signature; its result type is left
// untouched so a distinct named stats type never becomes the owned stats type.
func declaredFunctionType(expression ast.Expr) *ast.FuncType {
	function, _ := underlyingCollectionType(expression).(*ast.FuncType)
	return function
}

func selectedFunctionType(function *ast.FuncDecl, operand ast.Expr, info *types.Info) *ast.FuncType {
	if pointer, ok := unparen(operand).(*ast.StarExpr); ok {
		operand = pointer.X
	}
	name, ok := unparen(operand).(*ast.Ident)
	if !ok {
		return function.Type
	}
	if _, typeName := info.ObjectOf(name).(*types.TypeName); !typeName || function.Recv == nil {
		return function.Type
	}
	// Method expressions require an explicit receiver argument, unlike values.
	signature := *function.Type
	parameters := append([]*ast.Field(nil), function.Recv.List...)
	parameters = append(parameters, function.Type.Params.List...)
	signature.Params = &ast.FieldList{List: parameters}
	return &signature
}

func sourceCallArity(call *ast.CallExpr, signature *ast.FuncType) bool {
	parameters := fieldListTypes(signature.Params)
	if len(parameters) != 0 {
		if _, variadic := parameters[len(parameters)-1].(*ast.Ellipsis); variadic {
			if call.Ellipsis.IsValid() {
				return len(call.Args) == len(parameters)
			}
			return len(call.Args) >= len(parameters)-1
		}
	}
	return !call.Ellipsis.IsValid() && len(call.Args) == len(parameters)
}
