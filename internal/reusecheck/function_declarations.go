package reusecheck

import (
	"fmt"
	"go/ast"
	"go/token"
)

// A function stored in a package variable has the same contract-bearing body as
// a function declaration. Give it the variable's name for diagnostic attribution.
func declaredFunctions(declaration ast.Decl, fset *token.FileSet) []*ast.FuncDecl {
	if fn, ok := declaration.(*ast.FuncDecl); ok {
		if fn.Recv != nil || fn.Name.Name == "_" || fn.Name.Name == "init" {
			copied := *fn
			copied.Name = ast.NewIdent(positionedFunctionName(fn.Name.Name, fn.Pos(), fset))
			return []*ast.FuncDecl{&copied}
		}
		return []*ast.FuncDecl{fn}
	}
	var functions []*ast.FuncDecl
	for _, initializer := range packageInitializers(declaration) {
		functions = append(functions, initializerFunctions(initializer.expression, initializer.name, initializer.paired, fset)...)
	}
	return functions
}

type packageInitializer struct {
	name       string
	expression ast.Expr
	paired     bool
}

func packageInitializers(declaration ast.Decl) []packageInitializer {
	group, ok := declaration.(*ast.GenDecl)
	if !ok || group.Tok != token.VAR {
		return nil
	}
	var initializers []packageInitializer
	for _, specification := range group.Specs {
		value := specification.(*ast.ValueSpec)
		for index, expression := range value.Values {
			name := value.Names[0].Name
			paired := len(value.Names) == len(value.Values)
			if paired {
				name = value.Names[index].Name
			}
			initializers = append(initializers, packageInitializer{name, expression, paired})
		}
	}
	return initializers
}

func initializerFunctions(expression ast.Expr, name string, paired bool, fset *token.FileSet) []*ast.FuncDecl {
	var direct *ast.FuncLit
	if paired && name != "_" {
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
			attribution = functionLiteralName(name, literal, fset)
		}
		functions = append(functions, &ast.FuncDecl{
			Name: ast.NewIdent(attribution), Type: literal.Type, Body: literal.Body,
		})
		// Existing function analysis owns nested bodies and their report mappings.
		return false
	})
	return functions
}

func functionLiteralName(parent string, literal *ast.FuncLit, fset *token.FileSet) string {
	return positionedFunctionName(parent, literal.Pos(), fset)
}

func positionedFunctionName(parent string, pos token.Pos, fset *token.FileSet) string {
	return positionedScopeName(parent, "func", pos, fset)
}

func positionedScopeName(parent, kind string, pos token.Pos, fset *token.FileSet) string {
	// Physical file coordinates distinguish same-line siblings and remain stable
	// when another file changes or a //line directive aliases source positions.
	position := fset.PositionFor(pos, false)
	return fmt.Sprintf("%s.%s@%d:%d", parent, kind, position.Line, position.Column)
}
