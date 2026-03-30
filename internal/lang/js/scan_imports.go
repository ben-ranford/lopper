package js

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report"
)

func collectImportBindings(tree *sitter.Tree, content []byte, relPath string) ([]ImportBinding, []report.ImportUse) {
	root := tree.RootNode()
	bindings := make([]ImportBinding, 0)
	uncertainImports := make([]report.ImportUse, 0)
	walkNode(root, func(node *sitter.Node) {
		switch node.Type() {
		case "import_statement":
			bindings = append(bindings, parseImportStatement(node, content, relPath)...)
		case "call_expression":
			bindings = append(bindings, parseRequireCall(node, content, relPath)...)
			item, ok := parseUncertainDynamicImport(node, content, relPath)
			if ok {
				uncertainImports = append(uncertainImports, item)
			}
		}
	})

	return bindings, uncertainImports
}

func parseImportStatement(node *sitter.Node, content []byte, relPath string) []ImportBinding {
	sourceNode := node.ChildByFieldName("source")
	module, ok := extractStringLiteral(sourceNode, content)
	if !ok {
		return nil
	}

	clause := node.ChildByFieldName("import_clause")
	if clause == nil {
		clause = firstNamedChildOfType(node, "import_clause")
	}
	if clause == nil {
		return []ImportBinding{makeImportBinding(module, "*", "*", ImportNamespace, relPath, node)}
	}

	bindings := parseImportClause(clause, content, module, relPath)
	if len(bindings) == 0 {
		bindings = []ImportBinding{makeImportBinding(module, "*", "*", ImportNamespace, relPath, node)}
	}

	return bindings
}

func parseImportClause(node *sitter.Node, content []byte, module string, relPath string) []ImportBinding {
	bindings := make([]ImportBinding, 0)
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		switch child.Type() {
		case "identifier":
			local := nodeText(child, content)
			bindings = append(bindings, makeImportBinding(module, "default", local, ImportDefault, relPath, child))
		case "namespace_import":
			nameNode := child.ChildByFieldName("name")
			if nameNode == nil {
				nameNode = firstNamedChildOfType(child, "identifier")
			}
			local := nodeText(nameNode, content)
			bindings = append(bindings, makeImportBinding(module, "*", local, ImportNamespace, relPath, child))
		case "named_imports":
			bindings = append(bindings, parseNamedImports(child, content, module, relPath)...)
		}
	}

	return bindings
}

func parseNamedImports(node *sitter.Node, content []byte, module string, relPath string) []ImportBinding {
	bindings := make([]ImportBinding, 0)
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child.Type() != "import_specifier" {
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

		exportName := nodeText(nameNode, content)
		localName := nodeText(aliasNode, content)
		if exportName == "" {
			continue
		}
		if localName == "" {
			localName = exportName
		}

		bindings = append(bindings, makeImportBinding(module, exportName, localName, ImportNamed, relPath, child))
	}

	return bindings
}

func parseRequireCall(node *sitter.Node, content []byte, relPath string) []ImportBinding {
	functionNode := node.ChildByFieldName("function")
	if functionNode == nil || functionNode.Type() != "identifier" {
		return nil
	}
	if nodeText(functionNode, content) != "require" {
		return nil
	}

	argumentsNode := node.ChildByFieldName("arguments")
	if argumentsNode == nil || argumentsNode.NamedChildCount() == 0 {
		return nil
	}

	module, ok := extractStaticModuleLiteral(argumentsNode.NamedChild(0), content)
	if !ok {
		return nil
	}

	bindings := parseRequireBinding(node, content, module, relPath)
	if len(bindings) == 0 {
		return []ImportBinding{makeImportBinding(module, "*", "*", ImportNamespace, relPath, node)}
	}
	return bindings
}

func parseUncertainDynamicImport(node *sitter.Node, content []byte, relPath string) (report.ImportUse, bool) {
	functionNode := node.ChildByFieldName("function")
	if functionNode == nil {
		return report.ImportUse{}, false
	}
	functionName := nodeText(functionNode, content)
	if functionName != "require" && functionName != "import" {
		return report.ImportUse{}, false
	}
	argumentsNode := node.ChildByFieldName("arguments")
	if argumentsNode == nil || argumentsNode.NamedChildCount() == 0 {
		return report.ImportUse{}, false
	}
	if _, ok := extractStaticModuleLiteral(argumentsNode.NamedChild(0), content); ok {
		return report.ImportUse{}, false
	}
	location := shared.Location(relPath, int(node.StartPoint().Row)+1, int(node.StartPoint().Column)+1)
	return report.ImportUse{
		Name:       "*",
		Module:     "<dynamic>",
		Locations:  []report.Location{location},
		Provenance: []string{"unresolved-" + functionName},
	}, true
}

func parseRequireBinding(call *sitter.Node, content []byte, module string, relPath string) []ImportBinding {
	declarator := call.Parent()
	for declarator != nil && declarator.Type() != "variable_declarator" {
		declarator = declarator.Parent()
	}
	if declarator == nil {
		return nil
	}

	nameNode := declarator.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}

	switch nameNode.Type() {
	case "identifier":
		local := nodeText(nameNode, content)
		return []ImportBinding{makeImportBinding(module, "*", local, ImportNamespace, relPath, nameNode)}
	case "object_pattern":
		bindings := parseObjectPattern(nameNode, content, module, relPath)
		return bindings
	default:
		return nil
	}
}

func parseObjectPattern(node *sitter.Node, content []byte, module string, relPath string) []ImportBinding {
	bindings := make([]ImportBinding, 0)
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		switch child.Type() {
		case "shorthand_property_identifier_pattern", "property_identifier":
			name := nodeText(child, content)
			if name != "" {
				bindings = append(bindings, makeImportBinding(module, name, name, ImportNamed, relPath, child))
			}
		case "pair_pattern":
			keyNode := child.ChildByFieldName("key")
			valueNode := child.ChildByFieldName("value")
			exportName := nodeText(keyNode, content)
			localName := nodeText(valueNode, content)
			if exportName == "" {
				continue
			}
			if localName == "" {
				localName = exportName
			}
			bindings = append(bindings, makeImportBinding(module, exportName, localName, ImportNamed, relPath, child))
		}
	}

	return bindings
}

func makeImportBinding(module string, exportName string, localName string, kind ImportKind, relPath string, node *sitter.Node) ImportBinding {
	location := shared.Location(relPath, int(node.StartPoint().Row)+1, int(node.StartPoint().Column)+1)
	return ImportBinding{
		Module:     module,
		ExportName: exportName,
		LocalName:  localName,
		Kind:       kind,
		Location:   location,
	}
}

func extractStringLiteral(node *sitter.Node, content []byte) (string, bool) {
	if node == nil {
		return "", false
	}

	text := nodeText(node, content)
	if text == "" {
		return "", false
	}

	if len(text) >= 2 {
		quote := text[0]
		if (quote == '"' || quote == '\'') && text[len(text)-1] == quote {
			return text[1 : len(text)-1], true
		}
	}

	text = strings.Trim(text, "\"'`")
	if text == "" {
		return "", false
	}
	return text, true
}

func extractStaticModuleLiteral(node *sitter.Node, content []byte) (string, bool) {
	if node == nil {
		return "", false
	}
	text := nodeText(node, content)
	if len(text) < 2 {
		return "", false
	}
	quote := text[0]
	if (quote != '"' && quote != '\'' && quote != '`') || text[len(text)-1] != quote {
		return "", false
	}
	if quote == '`' {
		for i := 0; i < int(node.NamedChildCount()); i++ {
			child := node.NamedChild(i)
			if child != nil && child.Type() == "template_substitution" {
				return "", false
			}
		}
	}
	return text[1 : len(text)-1], true
}
