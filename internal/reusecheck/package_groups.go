package reusecheck

import "go/ast"

// Mutually exclusive build variants can declare the same package objects. Do
// not choose a sibling's provenance when the available source is ambiguous.
func analysisGroups(files []*ast.File) [][]*ast.File {
	if len(files) == 0 {
		return nil
	}
	owners := make(map[string]*ast.File)
	for _, file := range files {
		for _, key := range analysisDeclarationKeys(file) {
			if owner := owners[key]; owner != nil && owner != file {
				groups := make([][]*ast.File, len(files))
				for index, source := range files {
					groups[index] = []*ast.File{source}
				}
				return groups
			}
			owners[key] = file
		}
	}
	return [][]*ast.File{files}
}

func analysisDeclarationKeys(file *ast.File) []string {
	var keys []string
	for _, declaration := range file.Decls {
		switch item := declaration.(type) {
		case *ast.FuncDecl:
			if key := analysisFunctionKey(item); key != "" {
				keys = append(keys, key)
			}
		case *ast.GenDecl:
			keys = append(keys, analysisObjectKeys(item)...)
		}
	}
	return keys
}

func analysisObjectKeys(declaration *ast.GenDecl) []string {
	var keys []string
	for _, specification := range declaration.Specs {
		names := declarationNames(specification)
		if declared, ok := specification.(*ast.TypeSpec); ok {
			names = []*ast.Ident{declared.Name}
		}
		for _, name := range names {
			if name.Name != "_" && name.Name != "init" {
				keys = append(keys, "."+name.Name)
			}
		}
	}
	return keys
}

func analysisFunctionKey(function *ast.FuncDecl) string {
	if function.Name.Name == "_" {
		return ""
	}
	if function.Recv == nil {
		if function.Name.Name == "init" {
			return ""
		}
		return "." + function.Name.Name
	}
	if len(function.Recv.List) == 1 {
		if receiver := analysisReceiverName(function.Recv.List[0].Type); receiver != "" {
			return receiver + "." + function.Name.Name
		}
	}
	return ""
}

func analysisReceiverName(expression ast.Expr) string {
	for {
		switch receiver := unparen(expression).(type) {
		case *ast.Ident:
			return receiver.Name
		case *ast.StarExpr:
			expression = receiver.X
		case *ast.IndexExpr:
			expression = receiver.X
		case *ast.IndexListExpr:
			expression = receiver.X
		default:
			return ""
		}
	}
}
