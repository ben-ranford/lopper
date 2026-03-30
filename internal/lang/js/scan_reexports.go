package js

import (
	sitter "github.com/smacker/go-tree-sitter"

	"github.com/ben-ranford/lopper/internal/report"
)

func collectReExportBindings(tree *sitter.Tree, content []byte, relPath string, imports []ImportBinding) []ReExportBinding {
	importsByLocal := make(map[string][]ImportBinding)
	for _, imp := range imports {
		if imp.LocalName == "" {
			continue
		}
		importsByLocal[imp.LocalName] = append(importsByLocal[imp.LocalName], imp)
	}

	root := tree.RootNode()
	bindings := make([]ReExportBinding, 0)
	walkNode(root, func(node *sitter.Node) {
		if node.Type() != "export_statement" {
			return
		}
		bindings = append(bindings, parseReExportStatement(node, content, relPath, importsByLocal)...)
	})
	return bindings
}

func parseReExportStatement(node *sitter.Node, content []byte, relPath string, importsByLocal map[string][]ImportBinding) []ReExportBinding {
	sourceNode := node.ChildByFieldName("source")
	sourceModule, hasSource := extractStringLiteral(sourceNode, content)

	namespaceExport := firstNamedChildOfType(node, "namespace_export")
	if namespaceExport != nil && hasSource {
		nameNode := firstNamedChildOfType(namespaceExport, "identifier", "property_identifier")
		exportName := nodeText(nameNode, content)
		if exportName != "" {
			return []ReExportBinding{makeReExportBinding(sourceModule, "*", exportName, relPath, namespaceExport)}
		}
	}

	clause := node.ChildByFieldName("export_clause")
	if clause == nil {
		clause = firstNamedChildOfType(node, "export_clause")
	}
	if clause != nil {
		return parseReExportClause(clause, content, relPath, hasSource, sourceModule, importsByLocal)
	}

	if hasSource {
		return []ReExportBinding{makeReExportBinding(sourceModule, "*", "*", relPath, node)}
	}
	return nil
}

func parseReExportClause(node *sitter.Node, content []byte, relPath string, hasSource bool, sourceModule string, importsByLocal map[string][]ImportBinding) []ReExportBinding {
	bindings := make([]ReExportBinding, 0)
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child.Type() != "export_specifier" {
			continue
		}

		nameNode := child.ChildByFieldName("name")
		if nameNode == nil {
			nameNode = firstNamedChildOfType(child, "identifier", "property_identifier")
		}
		aliasNode := child.ChildByFieldName("alias")
		if aliasNode == nil {
			aliasNode = nameNode
		}

		sourceExportName := nodeText(nameNode, content)
		exportName := nodeText(aliasNode, content)
		if sourceExportName == "" {
			continue
		}
		if exportName == "" {
			exportName = sourceExportName
		}

		if hasSource {
			bindings = append(bindings, makeReExportBinding(sourceModule, sourceExportName, exportName, relPath, child))
			continue
		}

		for _, imp := range importsByLocal[sourceExportName] {
			bindings = append(bindings, makeReExportBinding(imp.Module, imp.ExportName, exportName, relPath, child))
		}
	}
	return bindings
}

func makeReExportBinding(sourceModule string, sourceExportName string, exportName string, relPath string, node *sitter.Node) ReExportBinding {
	location := report.Location{
		File:   relPath,
		Line:   int(node.StartPoint().Row) + 1,
		Column: int(node.StartPoint().Column) + 1,
	}
	return ReExportBinding{
		SourceModule:     sourceModule,
		SourceExportName: sourceExportName,
		ExportName:       exportName,
		Location:         location,
	}
}
