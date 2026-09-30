package reusecheck

import (
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
)

const sharedPackage = module + "lang/shared"

type Finding struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Function string `json:"function"`
	Rule     string `json:"rule"`
	Helper   string `json:"helper"`
	Advisory bool   `json:"advisory,omitempty"`
}

func withoutDecorativeCalls(path string, statements []ast.Stmt, packages map[string]string) []ast.Stmt {
	result := make([]ast.Stmt, 0, len(statements))
	for _, statement := range statements {
		if !decorativeShell(path, statement, packages) && !decorativeCall(path, statement, packages) {
			result = append(result, statement)
		}
	}
	return result
}

func decorativeCall(path string, statement ast.Stmt, packages map[string]string) bool {
	var expr ast.Expr
	switch item := statement.(type) {
	case *ast.ExprStmt:
		expr = item.X
	case *ast.AssignStmt:
		if len(item.Lhs) != 1 || len(item.Rhs) != 1 {
			return false
		}
		ident, ok := item.Lhs[0].(*ast.Ident)
		if !ok || ident.Name != "_" {
			return false
		}
		expr = item.Rhs[0]
	default:
		return false
	}
	call, ok := unparen(expr).(*ast.CallExpr)
	if !ok {
		return false
	}
	owned := imported(call.Fun, packages, module+"report", "SortedUniqueTrimmedStrings") || imported(call.Fun, packages, sharedPackage, "SortedKeys") || imported(call.Fun, packages, sharedPackage, "SortedDependencyUnion")
	if !owned && !samePackageHelper(path, call.Fun) {
		return false
	}
	for _, arg := range call.Args {
		if _, ok := unparen(arg).(*ast.Ident); !ok {
			return false
		}
	}
	return true
}

func samePackageHelper(path string, expr ast.Expr) bool {
	ident, ok := unparen(expr).(*ast.Ident)
	if !ok || (ident.Obj != nil && ident.Obj.Kind != ast.Fun) {
		return false
	}
	for _, owner := range collectionOwners {
		if ident.Name == owner.function && filepath.Dir(filepath.ToSlash(path)) == filepath.Dir(owner.owner) {
			return true
		}
	}
	return false
}

func reportMapping(literal *ast.CompositeLit, packages map[string]string, info *types.Info, declarations map[types.Object]ast.Node) bool {
	if !imported(literal.Type, packages, module+"report", "DependencyReport") {
		return false
	}
	fields := reportLiteralFields(literal)
	if fields["Name"] == nil || fields["Language"] == nil {
		return false
	}
	var stats string
	for field, source := range reportFields {
		receiver := mappedStatsReceiver(fields[field], source, packages, info, declarations)
		if receiver == "" || (stats != "" && stats != receiver) {
			return false
		}
		stats = receiver
	}
	return !reportLiteralHasEffects(literal)
}

// Calls and receives can change the stats between field reads. Without proving
// their effects, replacing the literal with one stats snapshot is only advice.
func reportLiteralHasEffects(literal *ast.CompositeLit) bool {
	effects := false
	ast.Inspect(literal, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			effects = true
		case *ast.UnaryExpr:
			effects = effects || value.Op == token.ARROW
		}
		return !effects
	})
	return effects
}

func dependencyStats(declaration ast.Node, packages map[string]string, info *types.Info) bool {
	switch decl := declaration.(type) {
	case *ast.Field:
		return dependencyStatsType(decl.Type, packages)
	case *ast.ValueSpec:
		if decl.Type != nil {
			return dependencyStatsType(decl.Type, packages)
		}
		return len(decl.Names) == 1 && dependencyStatsFactory(decl.Values, packages, info)
	case *ast.AssignStmt:
		return len(decl.Lhs) == 1 && dependencyStatsFactory(decl.Rhs, packages, info)
	}
	return false
}

func dependencyStatsType(expression ast.Expr, packages map[string]string) bool {
	expression = unaliasedType(expression)
	if imported(expression, packages, sharedPackage, "DependencyStats") {
		return true
	}
	pointer, ok := expression.(*ast.StarExpr)
	return ok && imported(pointer.X, packages, sharedPackage, "DependencyStats")
}

func dependencyStatsFactory(values []ast.Expr, packages map[string]string, info *types.Info) bool {
	if len(values) != 1 {
		return false
	}
	valueExpression := unparen(values[0])
	switch value := valueExpression.(type) {
	case *ast.TypeAssertExpr:
		return dependencyStatsType(value.Type, packages)
	case *ast.CallExpr:
		if len(value.Args) == 1 && dependencyStatsType(value.Fun, packages) {
			return true
		}
		if imported(value.Fun, packages, sharedPackage, "BuildDependencyStats") {
			return true
		}
		return dependencyStatsPointer(value, packages, info)
	case *ast.CompositeLit:
		return imported(value.Type, packages, sharedPackage, "DependencyStats")
	case *ast.StarExpr:
		return dependencyStatsPointer(value.X, packages, info)
	default:
		return dependencyStatsPointer(valueExpression, packages, info)
	}
}

func dependencyStatsPointer(expression ast.Expr, packages map[string]string, info *types.Info) bool {
	switch value := unparen(expression).(type) {
	case *ast.TypeAssertExpr:
		pointer, ok := unaliasedType(value.Type).(*ast.StarExpr)
		return ok && imported(pointer.X, packages, sharedPackage, "DependencyStats")
	case *ast.CallExpr:
		if pointer, ok := unaliasedType(value.Fun).(*ast.StarExpr); ok {
			return len(value.Args) == 1 && imported(pointer.X, packages, sharedPackage, "DependencyStats")
		}
		builtin, ok := unparen(value.Fun).(*ast.Ident)
		if !ok || info == nil {
			return false
		}
		object, resolvesToBuiltin := info.ObjectOf(builtin).(*types.Builtin)
		return resolvesToBuiltin && object.Name() == "new" && len(value.Args) == 1 && imported(value.Args[0], packages, sharedPackage, "DependencyStats")
	case *ast.UnaryExpr:
		literal, ok := unparen(value.X).(*ast.CompositeLit)
		return value.Op == token.AND && ok && imported(literal.Type, packages, sharedPackage, "DependencyStats")
	default:
		return false
	}
}

func (f *Finding) String() string {
	level := "violation"
	if f.Advisory {
		level = "advisory"
	}
	return fmt.Sprintf("%s:%d: %s %s in %s: use %s", f.Path, f.Line, level, f.Rule, f.Function, f.Helper)
}

// LegacyAdvisory scopes migration debt to exact owners, not directory-wide waivers.
// Remaining entries cover pre-helper adapters; new function/path copies still fail.
func LegacyAdvisory(f Finding, source []byte) bool {
	sites := map[string]map[string]string{
		"dependency-report-mapping": {
			"internal/lang/jvm/reporting.go": "buildDependencyReport",
			"internal/lang/php/reporting.go": "buildDependencyReport",
		},
		"sorted-unique-exact":     {"internal/analysis/identity_enrichment.go": "sortedUnique"},
		"sorted-unique-trimmed":   {"internal/report/vulnerability.go": "sortedUniqueStrings"},
		"sorted-dependency-union": {"internal/lang/powershell/reporting.go": "sortedDependencyUnion"},
	}
	return sites[f.Rule][f.Path] == f.Function && legacyDigests[f.Path] == fmt.Sprintf("%x", sha256.Sum256(source))
}

func localDeclarations(fn *ast.FuncDecl, info *types.Info) map[types.Object]ast.Node {
	result := make(map[types.Object]ast.Node)
	ast.Inspect(fn, func(node ast.Node) bool {
		for index, name := range declarationNames(node) {
			if object := info.Defs[name]; object != nil {
				result[object] = bindingDeclaration(node, index)
			}
		}
		if clause, ok := node.(*ast.CaseClause); ok && len(clause.List) == 1 {
			if object := info.Implicits[clause]; object != nil {
				result[object] = &ast.Field{Type: clause.List[0]}
			}
		}
		return true
	})
	return result
}

func contractFingerprints(source string) (map[string]contract, error) {
	templateSet := token.NewFileSet()
	templates, err := parser.ParseFile(templateSet, "contracts.go", source, 0)
	if err != nil {
		return nil, fmt.Errorf("parse rule contracts: %w", err)
	}
	fingerprints := make(map[string]contract)
	for _, decl := range templates.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			fingerprints[canonicalFunction(fn, imports(templates), bindings(templates, templateSet))] = collectionOwners[fn.Name.Name]
		}
	}
	alternateSet := token.NewFileSet()
	alternate, err := parser.ParseFile(alternateSet, "contracts.go", strings.Replace(source, "[]string(nil)", "[]string{}", 1), 0)
	if err != nil {
		return nil, err
	}
	for _, decl := range alternate.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			fingerprints[canonicalFunction(fn, imports(alternate), bindings(alternate, alternateSet))] = collectionOwners[fn.Name.Name]
		}
	}
	return fingerprints, nil
}

func functionFindings(path string, fn *ast.FuncDecl, packages map[string]string, info *types.Info, fingerprints map[string]contract, fset *token.FileSet) []Finding {
	findings := collectionFindings(path, fn, packages, info, fingerprints, fset)
	if filepath.ToSlash(path) == "internal/lang/shared/dependency_usage_stats.go" && fn.Name.Name == "BuildDependencyReportFromStats" {
		return findings
	}
	return append(findings, reportMappingFindings(path, fn, packages, info, fset)...)
}

func collectionFindings(path string, fn *ast.FuncDecl, packages map[string]string, info *types.Info, fingerprints map[string]contract, fset *token.FileSet) []Finding {
	var findings []Finding
	matched, found := fingerprints[canonicalWithoutDecorativeCalls(path, fn, packages, info)]
	if matched.rule == "sorted-set-keys" && !strings.HasPrefix(filepath.ToSlash(path), "internal/lang/") {
		found = false
	}
	if matched.rule == "sorted-unique-exact" && !strings.HasPrefix(filepath.ToSlash(path), "internal/analysis/") {
		found = false
	}
	if found && (filepath.ToSlash(path) != matched.owner || fn.Name.Name != matched.function) {
		findings = append(findings, Finding{Path: filepath.ToSlash(path), Line: fset.Position(fn.Pos()).Line, Function: fn.Name.Name, Rule: matched.rule, Helper: matched.helper})
	}
	return findings
}

func reportMappingFindings(path string, fn *ast.FuncDecl, packages map[string]string, info *types.Info, fset *token.FileSet) []Finding {
	var findings []Finding
	declarations := localDeclarations(fn, info)
	literalTypes := compositeLiteralTypes(fn.Body)
	owners := []string{fn.Name.Name}
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		if node == nil {
			owners = owners[:len(owners)-1]
			return false
		}
		owner := owners[len(owners)-1]
		if closure, ok := node.(*ast.FuncLit); ok {
			owner = functionLiteralName(fn.Name.Name, closure, fset)
		}
		owners = append(owners, owner)
		literal, ok := node.(*ast.CompositeLit)
		if ok {
			resolved := *literal
			resolved.Type = literalTypes[literal]
			literal = &resolved
		}
		if ok && (reportMapping(literal, packages, info, declarations) || partialReportMapping(literal, packages, info, declarations)) {
			findings = append(findings, Finding{Path: filepath.ToSlash(path), Line: fset.Position(literal.Pos()).Line, Function: owner, Rule: "dependency-report-mapping", Helper: "shared.BuildDependencyReportFromStats", Advisory: !reportMapping(literal, packages, info, declarations)})
		}
		return true
	})
	return findings
}

func reportLiteralFields(literal *ast.CompositeLit) map[string]ast.Expr {
	fields := make(map[string]ast.Expr)
	for _, element := range literal.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok {
			return nil
		}
		key, ok := pair.Key.(*ast.Ident)
		if !ok {
			return nil
		}
		fields[key.Name] = pair.Value
	}
	return fields
}

func mappedStatsReceiver(expression ast.Expr, field string, packages map[string]string, info *types.Info, declarations map[types.Object]ast.Node) string {
	selector, ok := unparen(expression).(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != field {
		return ""
	}
	receiver := selector.X
	canonical := canonicalStatsReceiver(receiver, packages, info, declarations)
	key := stableStatsReceiver(canonical, packages, info)
	if key == "" {
		return ""
	}
	typ := sourceStatsReceiverType(receiver, packages, info, declarations)
	if typ != nil {
		if dependencyStatsType(typ, packages) {
			return key
		}
		if member := sourceMemberSelection(typ, field, packages, info); member.stats {
			return stableStatsReceiver(appendReceiverMembers(canonical, member.path[:len(member.path)-1]), packages, info)
		}
		return ""
	}
	ident, ok := unparen(receiver).(*ast.Ident)
	if ok && dependencyStats(resolvedStatsDeclaration(info.ObjectOf(ident), info, declarations), packages, info) {
		return key
	}
	return ""
}

// Preserve inferred provenance while applying each explicit pointer operation.
func inferredStatsReceiverType(expression ast.Expr, packages map[string]string, info *types.Info, declarations map[types.Object]ast.Node) ast.Expr {
	seen := make(map[types.Object]bool)
	var operations []collectionOperation
	for {
		switch item := unparen(expression).(type) {
		case *ast.Ident:
			object := info.ObjectOf(item)
			declaration, _ := unwrapBinding(declarations[object])
			if ranged, ok := declaration.(*ast.RangeStmt); ok {
				return collectionIndirection(rangeValueType(ranged, object, info, declarations), operations)
			}
			resolved, follow := resolveCollectionIdentifier(item, info, declarations, seen)
			if !follow {
				return nil
			}
			expression = resolved
		case *ast.StarExpr:
			operations = append(operations, collectionDereference)
			expression = item.X
		case *ast.UnaryExpr:
			if item.Op != token.AND {
				return nil
			}
			operations = append(operations, collectionAddress)
			expression = item.X
		case *ast.TypeAssertExpr:
			return collectionIndirection(item.Type, operations)
		case *ast.CallExpr:
			return collectionIndirection(inferredStatsCallType(item, packages, info, declarations), operations)
		default:
			return nil
		}
	}
}

func statsFactoryResultType(call *ast.CallExpr, packages map[string]string) ast.Expr {
	if len(call.Args) != 3 || !imported(call.Fun, packages, sharedPackage, "BuildDependencyStats") {
		return nil
	}
	name := ast.NewIdent("DependencyStats")
	if factory, ok := unparen(call.Fun).(*ast.SelectorExpr); ok {
		return &ast.SelectorExpr{X: factory.X, Sel: name}
	}
	return name
}

// A repeated receiver must resolve to the same lexical objects and contain no
// calls or receives: evaluating it once must preserve the copied field values.
func stableStatsReceiver(expression ast.Expr, packages map[string]string, info *types.Info) string {
	switch value := unparen(expression).(type) {
	case *ast.Ident:
		if object := info.ObjectOf(value); object != nil {
			return fmt.Sprintf("%p", object)
		}
	case *ast.SelectorExpr:
		return wrapReceiverIdentity("", stableStatsReceiver(value.X, packages, info), "."+value.Sel.Name)
	case *ast.BasicLit:
		return value.Kind.String() + ":" + value.Value
	case *ast.StarExpr:
		return wrapReceiverIdentity("*(", stableStatsReceiver(value.X, packages, info), ")")
	case *ast.UnaryExpr:
		switch value.Op {
		case token.AND, token.ADD, token.SUB, token.XOR, token.NOT:
			return wrapReceiverIdentity(value.Op.String()+"(", stableStatsReceiver(value.X, packages, info), ")")
		}
	case *ast.BinaryExpr:
		return pairedReceiverIdentity("binary:"+value.Op.String(), stableStatsReceiver(value.X, packages, info), stableStatsReceiver(value.Y, packages, info))
	case *ast.IndexExpr:
		return pairedReceiverIdentity("index", stableStatsReceiver(value.X, packages, info), stableStatsReceiver(value.Index, packages, info))
	case *ast.TypeAssertExpr:
		return assertedReceiverIdentity(value, packages, info)
	}
	return ""
}

func wrapReceiverIdentity(prefix, operand, suffix string) string {
	if operand == "" {
		return ""
	}
	return prefix + operand + suffix
}

func pairedReceiverIdentity(operation, first, second string) string {
	if first == "" || second == "" {
		return ""
	}
	return operation + "(" + first + "," + second + ")"
}

func assertedReceiverIdentity(assertion *ast.TypeAssertExpr, packages map[string]string, info *types.Info) string {
	operand := stableStatsReceiver(assertion.X, packages, info)
	typ := assertedTypeIdentity(assertion.Type, packages, info, make(map[ast.Expr]bool))
	return pairedReceiverIdentity("assert", operand, typ)
}

// Assertion types are part of receiver identity: the same interface operand
// asserted as two different types cannot be replaced by one helper snapshot.
// Follow aliases while retaining named types and lexical array-length bindings.
func assertedTypeIdentity(expression ast.Expr, packages map[string]string, info *types.Info, active map[ast.Expr]bool) string {
	expression = unaliasedType(expression)
	if expression == nil || active[expression] {
		return ""
	}
	active[expression] = true
	defer delete(active, expression)
	switch value := expression.(type) {
	case *ast.Ident:
		return namedAssertionType(value, info)
	case *ast.SelectorExpr:
		return importedAssertionType(value, packages, info)
	case *ast.StarExpr:
		return wrapReceiverIdentity("*", assertedTypeIdentity(value.X, packages, info, active), "")
	case *ast.ArrayType:
		length := "slice"
		if value.Len != nil {
			length = stableStatsReceiver(value.Len, packages, info)
		}
		return pairedReceiverIdentity("array", length, assertedTypeIdentity(value.Elt, packages, info, active))
	case *ast.MapType:
		return pairedReceiverIdentity("map", assertedTypeIdentity(value.Key, packages, info, active), assertedTypeIdentity(value.Value, packages, info, active))
	case *ast.IndexExpr:
		return assertedInstanceIdentity(value.X, []ast.Expr{value.Index}, packages, info, active)
	case *ast.IndexListExpr:
		return assertedInstanceIdentity(value.X, value.Indices, packages, info, active)
	}
	return ""
}

func namedAssertionType(name *ast.Ident, info *types.Info) string {
	object, ok := info.ObjectOf(name).(*types.TypeName)
	if !ok {
		return ""
	}
	// Alias expansion does not substitute generic arguments. An unresolved
	// parameter cannot establish that two asserted types are identical.
	if _, parameter := object.Type().(*types.TypeParam); parameter {
		return ""
	}
	return fmt.Sprintf("type(%p)", object)
}

func importedAssertionType(selector *ast.SelectorExpr, packages map[string]string, info *types.Info) string {
	qualifier, ok := selector.X.(*ast.Ident)
	if !ok {
		return ""
	}
	path := packageQualifierPath(qualifier, packages, info)
	return wrapReceiverIdentity("import(", path, ")."+selector.Sel.Name)
}

func assertedInstanceIdentity(base ast.Expr, arguments []ast.Expr, packages map[string]string, info *types.Info, active map[ast.Expr]bool) string {
	identity := assertedTypeIdentity(base, packages, info, active)
	for _, argument := range arguments {
		identity = pairedReceiverIdentity("instance", identity, assertedTypeIdentity(argument, packages, info, active))
	}
	return identity
}

// Follow lexical aliases without treating an unresolved or cyclic initializer as
// proof. Keep the mapping receiver object separate from its type provenance.
func resolvedStatsDeclaration(object types.Object, info *types.Info, declarations map[types.Object]ast.Node) ast.Node {
	seen := make(map[types.Object]bool)
	for object != nil && !seen[object] {
		seen[object] = true
		declaration, commaOK := unwrapBinding(declarations[object])
		if ranged, ok := declaration.(*ast.RangeStmt); ok {
			return &ast.Field{Type: rangeValueType(ranged, object, info, declarations)}
		}
		initializer := aliasInitializer(declaration)
		if typ := collectionValueType(initializer, commaOK, info, declarations); typ != nil {
			return &ast.Field{Type: typ}
		}
		alias, ok := statsAliasOperand(initializer).(*ast.Ident)
		if !ok {
			return declaration
		}
		object = info.ObjectOf(alias)
	}
	return nil
}

func statsAliasOperand(expression ast.Expr) ast.Expr {
	for {
		expression = unparen(expression)
		switch item := expression.(type) {
		case *ast.StarExpr:
			expression = item.X
		case *ast.UnaryExpr:
			if item.Op != token.AND {
				return expression
			}
			expression = item.X
		default:
			return expression
		}
	}
}

func aliasInitializer(declaration ast.Node) ast.Expr {
	declaration, _ = unwrapBinding(declaration)
	switch item := declaration.(type) {
	case *ast.ValueSpec:
		if item.Type == nil && len(item.Names) == 1 && len(item.Values) == 1 {
			return item.Values[0]
		}
	case *ast.AssignStmt:
		if len(item.Lhs) == 1 && len(item.Rhs) == 1 {
			return item.Rhs[0]
		}
	}
	return nil
}

// Partial mappings retain domain overrides and ambiguous provenance as advice.
func partialReportMapping(literal *ast.CompositeLit, packages map[string]string, info *types.Info, declarations map[types.Object]ast.Node) bool {
	if !imported(literal.Type, packages, module+"report", "DependencyReport") {
		return false
	}
	fields := reportLiteralFields(literal)
	if fields["Name"] == nil || fields["Language"] == nil {
		return false
	}
	matches := 0
	for field, source := range reportFields {
		if fields[field] == nil {
			return false
		}
		if mappedStatsReceiver(fields[field], source, packages, info, declarations) != "" {
			matches++
		}
	}
	return matches >= 4
}
