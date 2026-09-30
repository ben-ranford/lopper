package reusecheck

import (
	"go/ast"
	"go/types"
)

type sourceMember struct {
	path  []string
	typ   ast.Expr
	stats bool
}

type sourceMemberNode struct {
	typ      ast.Expr
	path     []string
	multiple bool
}

// Search one embedding depth at a time. An unresolved peer may shadow a known
// field, and two paths remain ambiguous even when they reach the same type.
func sourceMemberSelection(typ ast.Expr, name string, packages map[string]string, info *types.Info) sourceMember {
	nodes := []sourceMemberNode{{typ: typ}}
	visited := make(map[*ast.StructType]bool)
	for len(nodes) != 0 {
		var next []sourceMemberNode
		matches, structures, blocked := sourceMemberLevel(nodes, name, packages, info, visited)
		for structure, node := range structures {
			visited[structure] = true
			members, children, unknown := inspectSourceFields(structure, node, name)
			matches = append(matches, members...)
			next = append(next, children...)
			blocked = blocked || unknown
		}
		if blocked || len(matches) > 1 {
			return sourceMember{}
		}
		if len(matches) == 1 {
			return matches[0]
		}
		nodes = next
	}
	return sourceMember{}
}

// Merge only after checking each defined type's own methods. Defined types can
// share struct fields without inheriting each other's methods. Repeated paths
// need only a second occurrence to retain ambiguity; deeper repeats add no new
// selections because this structure's fields were searched at a smaller depth.
func sourceMemberLevel(nodes []sourceMemberNode, name string, packages map[string]string, info *types.Info, visited map[*ast.StructType]bool) ([]sourceMember, map[*ast.StructType]sourceMemberNode, bool) {
	var matches []sourceMember
	structures := make(map[*ast.StructType]sourceMemberNode)
	for _, node := range nodes {
		member, structure, unknown := inspectSourceMember(node, name, packages, info)
		if unknown {
			return nil, nil, true
		}
		if len(member.path) != 0 {
			matches = appendSourceMatches(matches, member, node.multiple)
		}
		if structure == nil || visited[structure] {
			continue
		}
		if previous, exists := structures[structure]; exists {
			previous.multiple = true
			structures[structure] = previous
		} else {
			structures[structure] = node
		}
	}
	return matches, structures, false
}

func inspectSourceMember(node sourceMemberNode, name string, packages map[string]string, info *types.Info) (sourceMember, *ast.StructType, bool) {
	if dependencyStatsType(node.typ, packages) {
		for _, field := range reportFields {
			if name == field {
				return sourceMember{path: appendMemberPath(node.path, name), stats: true}, nil, false
			}
		}
		return sourceMember{}, nil, false
	}
	if sourceMethodShadows(node.typ, name, info) {
		return sourceMember{}, nil, true
	}
	structure, known := sourceStructType(node.typ, info)
	return sourceMember{}, structure, !known
}

func inspectSourceFields(structure *ast.StructType, node sourceMemberNode, name string) ([]sourceMember, []sourceMemberNode, bool) {
	var matches []sourceMember
	var children []sourceMemberNode
	for _, field := range structure.Fields.List {
		names := field.Names
		if len(names) == 0 {
			embedded := embeddedFieldName(field.Type)
			if embedded == "" {
				return nil, nil, true
			}
			names = []*ast.Ident{ast.NewIdent(embedded)}
			children = append(children, sourceMemberNode{typ: field.Type, path: appendMemberPath(node.path, embedded), multiple: node.multiple})
		}
		for _, declared := range names {
			if declared.Name == name {
				member := sourceMember{path: appendMemberPath(node.path, name), typ: field.Type}
				matches = appendSourceMatches(matches, member, node.multiple)
			}
		}
	}
	return matches, children, false
}

func appendSourceMatches(matches []sourceMember, member sourceMember, multiple bool) []sourceMember {
	if multiple {
		matches = append(matches, member)
	}
	return append(matches, member)
}

func appendMemberPath(path []string, name string) []string {
	return append(append([]string(nil), path...), name)
}

func embeddedFieldName(expression ast.Expr) string {
	for {
		switch field := unparen(expression).(type) {
		case *ast.Ident:
			return field.Name
		case *ast.SelectorExpr:
			return field.Sel.Name
		case *ast.StarExpr:
			expression = field.X
		case *ast.IndexExpr:
			expression = field.X
		case *ast.IndexListExpr:
			expression = field.X
		default:
			return ""
		}
	}
}

func sourceStructType(expression ast.Expr, info *types.Info) (*ast.StructType, bool) {
	expression = resolveTypeDeclarations(expression, false)
	if pointer, ok := expression.(*ast.StarExpr); ok {
		expression = resolveTypeDeclarations(pointer.X, false)
	}
	switch shape := expression.(type) {
	case *ast.StructType:
		return shape, true
	case *ast.ArrayType, *ast.MapType, *ast.ChanType, *ast.FuncType:
		return nil, true
	case *ast.Ident:
		object := info.ObjectOf(shape)
		if object == nil {
			return nil, false
		}
		basic, ok := object.Type().(*types.Basic)
		return nil, ok && basic.Kind() != types.Invalid
	default:
		return nil, false
	}
}

func sourceMethodShadows(expression ast.Expr, name string, info *types.Info) bool {
	declaration := sourceReceiverDeclaration(expression)
	if declaration == nil {
		return false
	}
	for node := range info.Implicits {
		function, ok := node.(*ast.FuncDecl)
		if !ok || function.Name.Name != name || function.Recv == nil {
			continue
		}
		if sourceReceiverDeclaration(function.Recv.List[0].Type) == declaration {
			return true
		}
	}
	return false
}

func sourceReceiverDeclaration(expression ast.Expr) *ast.TypeSpec {
	expression = unaliasedType(expression)
	if pointer, ok := expression.(*ast.StarExpr); ok {
		expression = unaliasedType(pointer.X)
	}
	return instantiatedTypeDeclaration(expression)
}

func sourceStatsReceiverType(expression ast.Expr, packages map[string]string, info *types.Info, declarations map[types.Object]ast.Node) ast.Expr {
	if selector, ok := unparen(expression).(*ast.SelectorExpr); ok {
		receiver := sourceStatsReceiverType(selector.X, packages, info, declarations)
		if receiver != nil {
			return sourceMemberSelection(receiver, selector.Sel.Name, packages, info).typ
		}
	}
	typ := collectionValueType(expression, false, info, declarations)
	if typ == nil {
		typ = resolvedCollectionType(expression, info, declarations)
	}
	if typ == nil {
		typ = inferredStatsReceiverType(expression, packages, info, declarations)
	}
	return typ
}

func appendReceiverMembers(expression ast.Expr, names []string) ast.Expr {
	for _, name := range names {
		expression = &ast.SelectorExpr{X: expression, Sel: ast.NewIdent(name)}
	}
	return expression
}

// Copy expression nodes before expanding promoted selectors. Lexical objects
// and assertion types stay attached to the original parsed source.
func canonicalStatsReceiver(expression ast.Expr, packages map[string]string, info *types.Info, declarations map[types.Object]ast.Node) ast.Expr {
	canonical := func(expression ast.Expr) ast.Expr {
		return canonicalStatsReceiver(expression, packages, info, declarations)
	}
	switch value := unparen(expression).(type) {
	case *ast.SelectorExpr:
		typ := sourceStatsReceiverType(value.X, packages, info, declarations)
		member := sourceMemberSelection(typ, value.Sel.Name, packages, info)
		if len(member.path) != 0 {
			return appendReceiverMembers(canonical(value.X), member.path)
		}
		copied := *value
		copied.X = canonical(value.X)
		return &copied
	case *ast.IndexExpr:
		copied := *value
		copied.X, copied.Index = canonical(value.X), canonical(value.Index)
		return &copied
	case *ast.SliceExpr:
		copied := *value
		copied.X = canonical(value.X)
		copied.Low, copied.High, copied.Max = canonical(value.Low), canonical(value.High), canonical(value.Max)
		return &copied
	case *ast.StarExpr:
		copied := *value
		copied.X = canonical(value.X)
		return &copied
	case *ast.UnaryExpr:
		copied := *value
		copied.X = canonical(value.X)
		return &copied
	case *ast.BinaryExpr:
		copied := *value
		copied.X, copied.Y = canonical(value.X), canonical(value.Y)
		return &copied
	case *ast.TypeAssertExpr:
		copied := *value
		copied.X = canonical(value.X)
		return &copied
	case *ast.CallExpr:
		if provenTypeConversion(value, packages, info) {
			copied := *value
			copied.Args = []ast.Expr{canonical(value.Args[0])}
			return &copied
		}
		return expression
	default:
		return expression
	}
}
