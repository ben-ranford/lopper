package reusecheck

import (
	"go/ast"
	"go/types"
)

func iteratorRangeValue(iterator *ast.FuncType, statement *ast.RangeStmt, object types.Object, info *types.Info) ast.Expr {
	yielded := iteratorYieldTypes(iterator)
	for index, expression := range []ast.Expr{statement.Key, statement.Value} {
		if ident, ok := expression.(*ast.Ident); ok && info.ObjectOf(ident) == object && index < len(yielded) {
			return yielded[index]
		}
	}
	return nil
}

func iteratorYieldTypes(iterator *ast.FuncType) []ast.Expr {
	parameters := fieldListTypes(iterator.Params)
	if len(parameters) != 1 || len(fieldListTypes(iterator.Results)) != 0 {
		return nil
	}
	yield, ok := unaliasedType(parameters[0]).(*ast.FuncType)
	if !ok {
		return nil
	}
	results := fieldListTypes(yield.Results)
	if len(results) != 1 {
		return nil
	}
	boolean, ok := unaliasedType(results[0]).(*ast.Ident)
	if !ok || boolean.Name != "bool" || boolean.Obj != nil {
		return nil
	}
	values := fieldListTypes(yield.Params)
	if len(values) > 2 {
		return nil
	}
	return values
}

func fieldListTypes(fields *ast.FieldList) []ast.Expr {
	var result []ast.Expr
	if fields == nil {
		return result
	}
	for _, field := range fields.List {
		count := max(1, len(field.Names))
		for range count {
			result = append(result, field.Type)
		}
	}
	return result
}
