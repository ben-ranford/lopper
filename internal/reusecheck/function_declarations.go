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
		for index, expression := range value.Values {
			name := value.Names[0].Name
			paired := len(value.Names) == len(value.Values)
			if paired {
				name = value.Names[index].Name
			}
			functions = append(functions, initializerFunctions(expression, name, paired)...)
		}
	}
	return functions
}

func initializerFunctions(expression ast.Expr, name string, paired bool) []*ast.FuncDecl {
	var direct *ast.FuncLit
	if paired {
		direct, _ = unparen(expression).(*ast.FuncLit)
	}
	var functions []*ast.FuncDecl
	ast.Inspect(expression, func(node ast.Node) bool {
		literal, ok := node.(*ast.FuncLit)
		if !ok {
			return true
		}
		attribution := name
		if literal != direct {
			attribution += ".func"
		}
		functions = append(functions, &ast.FuncDecl{
			Name: ast.NewIdent(attribution), Type: literal.Type, Body: literal.Body,
		})
		// Existing function analysis owns nested bodies and their report mappings.
		return false
	})
	return functions
}
