package reusecheck

import "go/ast"

// Expand aliases in signature types without changing their source declarations.
// Restore field types before subsequent provenance analysis reuses the AST.
func expandSignatureAliases(signature *ast.FuncType) func() {
	original := make(map[*ast.Field]ast.Expr)
	ast.Inspect(signature, func(node ast.Node) bool {
		if field, ok := node.(*ast.Field); ok {
			original[field] = field.Type
			field.Type = signatureType(field.Type, make(map[ast.Expr]bool))
			return false
		}
		return true
	})
	return func() {
		for field, expression := range original {
			field.Type = expression
		}
	}
}

func signatureType(expression ast.Expr, active map[ast.Expr]bool) ast.Expr {
	if active[expression] {
		return expression
	}
	active[expression] = true
	defer delete(active, expression)
	resolved := unaliasedType(expression)
	if resolved == nil {
		return expression
	}
	switch item := resolved.(type) {
	case *ast.ArrayType:
		copied := *item
		copied.Elt = signatureType(item.Elt, active)
		return &copied
	case *ast.MapType:
		copied := *item
		copied.Key = signatureType(item.Key, active)
		copied.Value = signatureType(item.Value, active)
		return &copied
	case *ast.StarExpr:
		copied := *item
		copied.X = signatureType(item.X, active)
		return &copied
	case *ast.ChanType:
		copied := *item
		copied.Value = signatureType(item.Value, active)
		return &copied
	case *ast.Ellipsis:
		copied := *item
		copied.Elt = signatureType(item.Elt, active)
		return &copied
	default:
		return resolved
	}
}
