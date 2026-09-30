package reusecheck

import "go/ast"

type analysisGroup struct {
	files   []*ast.File
	target  *ast.File
	omitted []*ast.File
}

// Common files retain their shared declarations. Each conflicting file gets
// that same context, but contributes findings only for its own source.
func analysisGroups(files []*ast.File) []analysisGroup {
	if len(files) == 0 {
		return nil
	}
	conflicts := conflictingAnalysisFiles(files)
	if len(conflicts) == 0 {
		return []analysisGroup{{files: files}}
	}
	var common []*ast.File
	for _, file := range files {
		if !conflicts[file] {
			common = append(common, file)
		}
	}
	var groups []analysisGroup
	if len(common) != 0 {
		groups = append(groups, scopedAnalysisGroup(common, nil, files))
	}
	for _, file := range files {
		if conflicts[file] {
			members := append([]*ast.File{file}, common...)
			groups = append(groups, scopedAnalysisGroup(members, file, files))
		}
	}
	return groups
}

func scopedAnalysisGroup(included []*ast.File, target *ast.File, all []*ast.File) analysisGroup {
	group := analysisGroup{files: included, target: target}
	selected := make(map[*ast.File]bool, len(included))
	for _, file := range included {
		selected[file] = true
	}
	for _, file := range all {
		if !selected[file] {
			group.omitted = append(group.omitted, file)
		}
	}
	return group
}

func conflictingAnalysisFiles(files []*ast.File) map[*ast.File]bool {
	owners := make(map[string]*ast.File)
	conflicts := make(map[*ast.File]bool)
	types := analysisTypeDeclarations(files)
	for _, file := range files {
		for _, key := range analysisDeclarationKeysWithTypes(file, types) {
			if owner := owners[key]; owner != nil && owner != file {
				conflicts[owner], conflicts[file] = true, true
			}
			owners[key] = file
		}
	}
	return conflicts
}

func analysisDeclarationKeys(file *ast.File) []string {
	return analysisDeclarationKeysWithTypes(file, analysisTypeDeclarations([]*ast.File{file}))
}

func analysisDeclarationKeysWithTypes(file *ast.File, types map[string][]*ast.TypeSpec) []string {
	var keys []string
	for _, declaration := range file.Decls {
		switch item := declaration.(type) {
		case *ast.FuncDecl:
			if item.Recv != nil {
				keys = append(keys, analysisMethodKeys(item, types)...)
				continue
			}
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
