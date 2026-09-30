package reusecheck

import (
	"go/ast"
	"go/token"
	"go/types"
)

// Keep provenance local to each binding without mutating shared source nodes.
func bindingDeclaration(node ast.Node, index int) ast.Node {
	switch item := node.(type) {
	case *ast.ValueSpec:
		copied := *item
		copied.Names = item.Names[index : index+1]
		copied.Values = bindingValues(item.Values, len(item.Names), index)
		return preserveBinding(&copied, item.Values, len(item.Names), index)
	case *ast.AssignStmt:
		copied := *item
		copied.Lhs = item.Lhs[index : index+1]
		copied.Rhs = bindingValues(item.Rhs, len(item.Lhs), index)
		return preserveBinding(&copied, item.Rhs, len(item.Lhs), index)
	default:
		return node
	}
}

func bindingValues(values []ast.Expr, count, index int) []ast.Expr {
	if len(values) == count {
		return values[index : index+1]
	}
	// A comma-ok expression provides its value only to the first binding.
	if count == 2 && len(values) == 1 && index == 0 {
		switch expression := unparen(values[0]).(type) {
		case *ast.TypeAssertExpr, *ast.IndexExpr:
			return values
		case *ast.UnaryExpr:
			if expression.Op == token.ARROW {
				return values
			}
		}
	}
	return nil
}

// Retain tuple context after selecting one binding; slices have no comma-ok form.
type commaOKBinding struct{ ast.Node }

type tupleCallBinding struct {
	ast.Node
	call         *ast.CallExpr
	count, index int
}

func preserveBinding(node ast.Node, values []ast.Expr, count, index int) ast.Node {
	if count > 1 && len(values) == 1 {
		if call, ok := unparen(values[0]).(*ast.CallExpr); ok {
			return &tupleCallBinding{Node: node, call: call, count: count, index: index}
		}
	}
	if count == 2 && len(values) == 1 {
		return &commaOKBinding{node}
	}
	return node
}

func sourceBindingDeclaration(node ast.Node, info *types.Info, declarations map[types.Object]ast.Node, seen map[types.Object]bool) ast.Node {
	if binding, ok := node.(*tupleCallBinding); ok {
		result := sourceCallResultAt(binding.call, binding.count, binding.index, info, declarations, seen)
		return &ast.Field{Type: result}
	}
	declaration, _ := unwrapBinding(node)
	return declaration
}

func unwrapBinding(node ast.Node) (ast.Node, bool) {
	if binding, ok := node.(*commaOKBinding); ok {
		return binding.Node, true
	}
	return node, false
}

func indexedValueType(indexed *ast.IndexExpr, commaOK bool, info *types.Info, declarations map[types.Object]ast.Node) ast.Expr {
	collection := underlyingCollectionType(resolvedCollectionType(indexed.X, info, declarations))
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

func receivedValueType(receive *ast.UnaryExpr, info *types.Info, declarations map[types.Object]ast.Node) ast.Expr {
	channel, ok := underlyingCollectionType(resolvedCollectionType(receive.X, info, declarations)).(*ast.ChanType)
	if !ok || channel.Dir == ast.SEND {
		return nil
	}
	return channel.Value
}

func collectionValueType(expression ast.Expr, commaOK bool, info *types.Info, declarations map[types.Object]ast.Node) ast.Expr {
	switch value := unparen(expression).(type) {
	case *ast.IndexExpr:
		return indexedValueType(value, commaOK, info, declarations)
	case *ast.UnaryExpr:
		if value.Op == token.ARROW {
			return receivedValueType(value, info, declarations)
		}
		if value.Op == token.AND {
			return collectionIndirection(collectionValueType(value.X, commaOK, info, declarations), []collectionOperation{collectionAddress})
		}
	case *ast.StarExpr:
		return collectionIndirection(collectionValueType(value.X, commaOK, info, declarations), []collectionOperation{collectionDereference})
	}
	return nil
}
