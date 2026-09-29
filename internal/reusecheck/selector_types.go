package reusecheck

import (
	"go/ast"
	"go/types"
)

// Lexical field objects retain provenance even when imported types cannot be
// checked in isolated source. Only source-declared fields supply evidence.
func declaredSelectorType(selector *ast.SelectorExpr, info *types.Info) ast.Expr {
	object := info.ObjectOf(selector.Sel)
	field, ok := object.(*types.Var)
	if !ok || !field.IsField() {
		return nil
	}
	for ident, declared := range info.Defs {
		if declared != object || ident.Obj == nil {
			continue
		}
		if source, ok := ident.Obj.Decl.(*ast.Field); ok {
			return source.Type
		}
	}
	return nil
}
