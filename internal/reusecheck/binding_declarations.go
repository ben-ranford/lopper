package reusecheck

import "go/ast"

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
	// A comma-ok assertion proves only the first binding's asserted type.
	if count == 2 && len(values) == 1 && index == 0 {
		if _, ok := unparen(values[0]).(*ast.TypeAssertExpr); ok {
			return values
		}
	}
	return nil
}
