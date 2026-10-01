package reusecheck

import (
	"go/ast"
	"go/types"
)

type collectionOperation uint8

const (
	collectionAddress collectionOperation = iota
	collectionDereference
	collectionSlice
	collectionAppend
	collectionIndex
)

func collectionIndirection(expression ast.Expr, operations []collectionOperation) ast.Expr {
	for index := len(operations) - 1; index >= 0; index-- {
		expression = collectionOperationType(expression, operations[index])
	}
	return expression
}

func collectionOperationType(expression ast.Expr, operation collectionOperation) ast.Expr {
	if expression == nil {
		return nil
	}
	expression = unaliasedType(expression)
	switch operation {
	case collectionAddress:
		return &ast.StarExpr{X: expression}
	case collectionDereference:
		if pointer, ok := underlyingCollectionType(expression).(*ast.StarExpr); ok {
			return pointer.X
		}
	case collectionSlice:
		return slicedCollectionType(underlyingCollectionType(expression))
	case collectionAppend:
		if slice, ok := underlyingCollectionType(expression).(*ast.ArrayType); ok && slice.Len == nil {
			return slice
		}
	case collectionIndex:
		return indexedCollectionType(expression, false)
	}
	return nil
}

func indexedCollectionType(expression ast.Expr, commaOK bool) ast.Expr {
	collection := underlyingCollectionType(expression)
	if mapping, ok := collection.(*ast.MapType); ok {
		return mapping.Value
	}
	if commaOK {
		return nil
	}
	if pointer, ok := collection.(*ast.StarExpr); ok {
		array, valid := underlyingCollectionType(pointer.X).(*ast.ArrayType)
		if !valid || array.Len == nil {
			return nil
		}
		collection = array
	}
	if array, ok := collection.(*ast.ArrayType); ok {
		return array.Elt
	}
	return nil
}

func slicedCollectionType(expression ast.Expr) ast.Expr {
	if pointer, ok := expression.(*ast.StarExpr); ok {
		array, valid := underlyingCollectionType(pointer.X).(*ast.ArrayType)
		if !valid || array.Len == nil {
			return nil
		}
		expression = array
	}
	if array, ok := expression.(*ast.ArrayType); ok {
		return &ast.ArrayType{Elt: array.Elt}
	}
	return nil
}

func builtinAppend(call *ast.CallExpr, info *types.Info) bool {
	ident, ok := unparen(call.Fun).(*ast.Ident)
	if !ok || len(call.Args) == 0 {
		return false
	}
	builtin, ok := info.ObjectOf(ident).(*types.Builtin)
	return ok && builtin.Name() == "append"
}

func resolveCollectionIdentifier(ident *ast.Ident, info *types.Info, declarations map[types.Object]ast.Node, seen map[types.Object]bool) (ast.Expr, bool) {
	object := info.ObjectOf(ident)
	if object == nil || seen[object] {
		return nil, false
	}
	seen[object] = true
	source := sourceCallableDeclaration(object, info, declarations)
	_, commaOK := unwrapBinding(source)
	declaration := sourceBindingDeclaration(source, info, declarations, seen)
	if ranged, ok := declaration.(*ast.RangeStmt); ok {
		return rangeValueTypeSeen(ranged, object, info, declarations, seen), false
	}
	if initializer := aliasInitializer(declaration); initializer != nil {
		if indexed, ok := unparen(initializer).(*ast.IndexExpr); ok && commaOK {
			collection := resolvedCollectionTypeSeen(indexed.X, info, declarations, seen)
			return indexedCollectionType(collection, true), false
		}
		return initializer, true
	}
	return declaredCollectionType(declaration), false
}
