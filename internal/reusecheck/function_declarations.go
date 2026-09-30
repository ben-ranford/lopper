package reusecheck

import (
	"go/ast"
	"go/token"
)

// A function stored in a package variable has the same contract-bearing body as
// a function declaration. Give it the variable's name for diagnostic attribution.
func declaredFunctions(declaration ast.Decl) []*ast.FuncDecl {
	if fn, ok := declaration.(*ast.FuncDecl); ok {
		return []*ast.FuncDecl{fn}
	}
	group, ok := declaration.(*ast.GenDecl)
	if !ok || group.Tok != token.VAR {
		return nil
	}
	var functions []*ast.FuncDecl
	for _, specification := range group.Specs {
		value := specification.(*ast.ValueSpec)
		if len(value.Names) != len(value.Values) {
			continue
		}
		for index, expression := range value.Values {
			if literal, ok := unparen(expression).(*ast.FuncLit); ok {
				functions = append(functions, &ast.FuncDecl{
					Name: ast.NewIdent(value.Names[index].Name), Type: literal.Type, Body: literal.Body,
				})
			}
		}
	}
	return functions
}
