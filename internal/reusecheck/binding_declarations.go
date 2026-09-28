package reusecheck

import (
	"go/ast"
	"go/types"
)

// Keep provenance local to each binding without mutating shared source nodes.
func bindingDeclaration(node ast.Node, index int) ast.Node {
	switch item := node.(type) {
	case *ast.ValueSpec:
		copied := *item
		copied.Names = item.Names[index : index+1]
		copied.Values = bindingValues(item.Values, len(item.Names), index)
		return &copied
	case *ast.AssignStmt:
		copied := *item
		copied.Lhs = item.Lhs[index : index+1]
		copied.Rhs = bindingValues(item.Rhs, len(item.Lhs), index)
		return &copied
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
		switch unparen(values[0]).(type) {
		case *ast.TypeAssertExpr, *ast.IndexExpr:
			return values
		}
	}
	return nil
}

func mapValueDeclaration(indexed *ast.IndexExpr, info *types.Info, declarations map[types.Object]ast.Node) ast.Node {
	collection := resolvedCollectionType(indexed.X, info, declarations)
	mapping, ok := unaliasedType(collection).(*ast.MapType)
	if !ok {
		return nil
	}
	return &ast.Field{Type: mapping.Value}
}
