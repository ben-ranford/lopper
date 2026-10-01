package reusecheck

import (
	"go/ast"
	"go/types"
)

// Anonymous fields define their name at the embedded type identifier, whose
// parser object still describes the type. Keep the enclosing field separately.
func indexEmbeddedFields(file *ast.File, info *types.Info) {
	ast.Inspect(file, func(node ast.Node) bool {
		field, ok := node.(*ast.Field)
		if !ok || len(field.Names) != 0 {
			return true
		}
		expression := unparen(field.Type)
		if pointer, ok := expression.(*ast.StarExpr); ok {
			expression = unparen(pointer.X)
		}
		if selector, ok := expression.(*ast.SelectorExpr); ok {
			expression = selector.Sel
		}
		if ident, ok := expression.(*ast.Ident); ok {
			if object, ok := info.Defs[ident].(*types.Var); ok && object.IsField() {
				info.Implicits[field] = object
			}
		}
		return true
	})
}

// Lexical field objects retain provenance even when imported types cannot be
// checked in isolated source. Only source-declared fields supply evidence.
func declaredSelectorType(selector *ast.SelectorExpr, info *types.Info) ast.Expr {
	object := info.ObjectOf(selector.Sel)
	field, ok := object.(*types.Var)
	if !ok || !field.IsField() {
		return nil
	}
	for node, declared := range info.Implicits {
		if source, ok := node.(*ast.Field); ok && declared == object {
			return source.Type
		}
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
