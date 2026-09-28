package reusecheck

import "go/ast"

// Composite elements can inherit their type from a containing array, slice or
// map. Keep those inferred types separate so analysis never changes the source AST.
func compositeLiteralTypes(root ast.Node) map[*ast.CompositeLit]ast.Expr {
	types := make(map[*ast.CompositeLit]ast.Expr)
	var parents []ast.Node
	ast.Inspect(root, func(node ast.Node) bool {
		if node == nil {
			parents = parents[:len(parents)-1]
			return false
		}
		if literal, ok := node.(*ast.CompositeLit); ok {
			types[literal] = literal.Type
			if literal.Type == nil {
				types[literal] = inheritedLiteralType(literal, parents, types)
			}
		}
		parents = append(parents, node)
		return true
	})
	return types
}

func inheritedLiteralType(literal *ast.CompositeLit, parents []ast.Node, types map[*ast.CompositeLit]ast.Expr) ast.Expr {
	if len(parents) == 0 {
		return nil
	}
	key := false
	if pair, ok := parents[len(parents)-1].(*ast.KeyValueExpr); ok {
		key = pair.Key == literal
		parents = parents[:len(parents)-1]
	}
	if len(parents) == 0 {
		return nil
	}
	parent, ok := parents[len(parents)-1].(*ast.CompositeLit)
	if !ok {
		return nil
	}
	element := collectionLiteralElement(types[parent], key)
	if pointer, ok := unparen(element).(*ast.StarExpr); ok {
		return pointer.X
	}
	return element
}

func collectionLiteralElement(expression ast.Expr, key bool) ast.Expr {
	switch collection := unparen(expression).(type) {
	case *ast.ArrayType:
		return collection.Elt
	case *ast.MapType:
		if key {
			return collection.Key
		}
		return collection.Value
	default:
		return nil
	}
}
