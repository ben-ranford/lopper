package reusecheck

import (
	"go/ast"
	"go/types"
	"maps"
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
	results := sourceCallResults(call, info, declarations, seen)
	if len(results) != count {
		return nil
	}
	return results[index]
}

func sourceCallResults(call *ast.CallExpr, info *types.Info, declarations map[types.Object]ast.Node, seen map[types.Object]bool) []ast.Expr {
	signature := sourceFunctionType(call.Fun, info, declarations, seen)
	if signature == nil || !sourceCallArity(call, signature, info, declarations, seen) {
		return nil
	}
	return fieldListTypes(signature.Results)
}

func sourceFunctionType(expression ast.Expr, info *types.Info, declarations map[types.Object]ast.Node, seen map[types.Object]bool) *ast.FuncType {
	switch value := unparen(expression).(type) {
	case *ast.FuncLit:
		return value.Type
	case *ast.TypeAssertExpr:
		return declaredFunctionType(value.Type)
	case *ast.CallExpr:
		return declaredFunctionType(sourceCallResultAt(value, 1, 0, info, declarations, seen))
	case *ast.IndexExpr:
		return indexedSourceFunctionType(value, info, declarations, seen)
	case *ast.IndexListExpr:
		return instantiatedSourceFunctionType(value.X, len(value.Indices), info, declarations, seen)
	case *ast.StarExpr:
		return indirectSourceFunctionType(value, info, declarations, seen)
	case *ast.Ident:
		object := info.ObjectOf(value)
		if object == nil || seen[object] {
			return nil
		}
		seen[object] = true
		defer delete(seen, object)
		declaration := sourceBindingDeclaration(sourceCallableDeclaration(object, info, declarations), info, declarations, seen)
		if initializer := aliasInitializer(declaration); initializer != nil {
			return sourceFunctionType(initializer, info, declarations, seen)
		}
		return declaredCallableType(declaration)
	case *ast.SelectorExpr:
		return sourceSelectorFunctionType(value, info, declarations)
	}
	return nil
}

func indexedSourceFunctionType(expression *ast.IndexExpr, info *types.Info, declarations map[types.Object]ast.Node, seen map[types.Object]bool) *ast.FuncType {
	if _, namedFunction := argumentCallObject(expression.X, info).(*types.Func); namedFunction {
		if signature := instantiatedSourceFunctionType(expression.X, 1, info, declarations, seen); signature != nil {
			return signature
		}
	}
	return indirectSourceFunctionType(expression, info, declarations, seen)
}

func indirectSourceFunctionType(expression ast.Expr, info *types.Info, declarations map[types.Object]ast.Node, seen map[types.Object]bool) *ast.FuncType {
	// Collection traversal keeps every ancestor guard, but its local bindings
	// must not prevent a sibling argument from using the same callable again.
	return declaredFunctionType(resolvedCollectionTypeSeen(expression, info, declarations, maps.Clone(seen)))
}

func sourceSelectorFunctionType(selector *ast.SelectorExpr, info *types.Info, declarations map[types.Object]ast.Node) *ast.FuncType {
	object := info.ObjectOf(selector.Sel)
	if method, ok := object.(*types.Func); ok {
		// Instantiated methods still derive ownership from their source signature.
		object = method.Origin()
	}
	declaration := sourceCallableDeclaration(object, info, declarations)
	if function, ok := declaration.(*ast.FuncDecl); ok {
		return selectedFunctionType(function, selector.X, info)
	}
	if _, method := object.(*types.Func); method {
		signature := declaredCallableType(declaration)
		if receiver := methodExpressionType(selector.X, info); signature != nil && receiver != nil {
			return functionWithReceiver(signature, []*ast.Field{{Type: receiver}})
		}
		return signature
	}
	return declaredFunctionType(declaredSelectorType(selector, info))
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
	if function.Recv == nil || methodExpressionType(operand, info) == nil {
		return function.Type
	}
	return functionWithReceiver(function.Type, function.Recv.List)
}

func methodExpressionType(expression ast.Expr, info *types.Info) ast.Expr {
	original := expression
	for {
		switch operand := unparen(expression).(type) {
		case *ast.Ident:
			if _, typeName := info.ObjectOf(operand).(*types.TypeName); !typeName {
				return nil
			}
			return original
		case *ast.InterfaceType, *ast.StructType:
			return original
		case *ast.StarExpr:
			expression = operand.X
		case *ast.IndexExpr:
			expression = operand.X
		case *ast.IndexListExpr:
			expression = operand.X
		default:
			return nil
		}
	}
}

func functionWithReceiver(original *ast.FuncType, receiver []*ast.Field) *ast.FuncType {
	// Method expressions require an explicit receiver argument, unlike values.
	signature := *original
	parameters := append([]*ast.Field(nil), receiver...)
	parameters = append(parameters, original.Params.List...)
	signature.Params = &ast.FieldList{List: parameters}
	return &signature
}

func sourceCallArity(call *ast.CallExpr, signature *ast.FuncType, info *types.Info, declarations map[types.Object]ast.Node, seen map[types.Object]bool) bool {
	arguments, known := sourceCallArgumentCount(call, info, declarations, seen)
	if !known {
		return false
	}
	parameters := fieldListTypes(signature.Params)
	if len(parameters) != 0 {
		if _, variadic := parameters[len(parameters)-1].(*ast.Ellipsis); variadic {
			if call.Ellipsis.IsValid() {
				return arguments == len(parameters)
			}
			return arguments >= len(parameters)-1
		}
	}
	return !call.Ellipsis.IsValid() && arguments == len(parameters)
}

// Only a sole call without ellipsis may expand its result tuple into arguments.
// Source signatures must prove each nested call's arity and result count first.
func sourceCallArgumentCount(call *ast.CallExpr, info *types.Info, declarations map[types.Object]ast.Node, seen map[types.Object]bool) (int, bool) {
	for _, expression := range call.Args {
		argument, ok := unparen(expression).(*ast.CallExpr)
		if !ok {
			continue
		}
		results := sourceArgumentResultCount(argument, info, declarations, seen)
		if results == 0 {
			return 0, false
		}
		if len(call.Args) == 1 && !call.Ellipsis.IsValid() {
			return results, true
		}
		if results != 1 {
			return 0, false
		}
	}
	return len(call.Args), true
}

func sourceArgumentResultCount(call *ast.CallExpr, info *types.Info, declarations map[types.Object]ast.Node, seen map[types.Object]bool) int {
	name := ""
	if builtin, ok := argumentCallObject(call.Fun, info).(*types.Builtin); ok {
		name = builtin.Name()
	} else if !argumentConversionType(call.Fun, info) {
		return len(sourceCallResults(call, info, declarations, seen))
	}
	arguments, known := sourceCallArgumentCount(call, info, declarations, seen)
	if known && scalarArgumentCallArity(name, arguments, call.Ellipsis.IsValid()) {
		return 1
	}
	return 0
}

func argumentCallObject(expression ast.Expr, info *types.Info) types.Object {
	switch value := unparen(expression).(type) {
	case *ast.Ident:
		return info.ObjectOf(value)
	case *ast.SelectorExpr:
		return info.ObjectOf(value.Sel)
	}
	return nil
}

func argumentConversionType(expression ast.Expr, info *types.Info) bool {
	switch value := unparen(expression).(type) {
	case *ast.Ident, *ast.SelectorExpr:
		_, typeName := argumentCallObject(value, info).(*types.TypeName)
		return typeName
	case *ast.ArrayType, *ast.MapType, *ast.ChanType, *ast.StructType, *ast.InterfaceType, *ast.FuncType:
		return true
	case *ast.StarExpr:
		return argumentConversionType(value.X, info)
	case *ast.IndexExpr:
		return argumentConversionType(value.X, info)
	case *ast.IndexListExpr:
		return argumentConversionType(value.X, info)
	}
	return false
}

// Builtins and conversions have known scalar results, unlike unproven imported
// calls. Check their argument shape without duplicating Go's type checker.
func scalarArgumentCallArity(name string, arguments int, ellipsis bool) bool {
	if ellipsis {
		return name == "append" && arguments == 2
	}
	switch name {
	case "", "len", "cap", "real", "imag", "new":
		return arguments == 1
	case "copy", "complex":
		return arguments == 2
	case "recover":
		return arguments == 0
	case "append", "min", "max":
		return arguments >= 1
	case "make":
		return arguments >= 1 && arguments <= 3
	}
	return false
}
