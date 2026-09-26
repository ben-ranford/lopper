package reusecheck

import (
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
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

// Analyze parses source fail-closed. Matches preserve exact operations, constants,
// import ownership and local def/use bindings; they are not similarity scores.
func Analyze(path string, source []byte) ([]Finding, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, source, 0)
	if err != nil {
		return nil, err
	}
	fingerprints, err := contractFingerprints(collectionContracts)
	if err != nil {
		return nil, err
	}
	packages := imports(file)
	info := bindings(file, fset)
	var findings []Finding
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		findings = append(findings, functionFindings(path, fn, packages, info, fingerprints, fset)...)

	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].Line < findings[j].Line })
	return findings, nil
}

func withoutDecorativeCalls(path string, statements []ast.Stmt, packages map[string]string) []ast.Stmt {
	result := make([]ast.Stmt, 0, len(statements))
	for _, statement := range statements {
		if !decorativeCall(path, statement, packages) {
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
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	owned := imported(call.Fun, packages, module+"report", "SortedUniqueTrimmedStrings") || imported(call.Fun, packages, sharedPackage, "SortedKeys") || imported(call.Fun, packages, sharedPackage, "SortedDependencyUnion")
	if !owned && !samePackageHelper(path, call.Fun) {
		return false
	}
	for _, arg := range call.Args {
		if _, ok := arg.(*ast.Ident); !ok {
			return false
		}
	}
	return true
}

func samePackageHelper(path string, expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
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
	var stats types.Object
	for field, source := range reportFields {
		object := mappedStatsObject(fields[field], source, packages, info, declarations)
		if object == nil || (stats != nil && stats != object) {
			return false
		}
		stats = object
	}
	return true
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
	for {
		parenthesized, ok := expression.(*ast.ParenExpr)
		if !ok {
			break
		}
		expression = parenthesized.X
	}
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
	valueExpression := values[0]
	for {
		parenthesized, ok := valueExpression.(*ast.ParenExpr)
		if !ok {
			break
		}
		valueExpression = parenthesized.X
	}
	switch value := valueExpression.(type) {
	case *ast.CallExpr:
		if imported(value.Fun, packages, sharedPackage, "BuildDependencyStats") {
			return true
		}
		builtin, ok := value.Fun.(*ast.Ident)
		if !ok || info == nil {
			return false
		}
		object, resolvesToBuiltin := info.ObjectOf(builtin).(*types.Builtin)
		return resolvesToBuiltin && object.Name() == "new" && len(value.Args) == 1 && imported(value.Args[0], packages, sharedPackage, "DependencyStats")
	case *ast.CompositeLit:
		return imported(value.Type, packages, sharedPackage, "DependencyStats")
	case *ast.UnaryExpr:
		operand := value.X
		for {
			parenthesized, ok := operand.(*ast.ParenExpr)
			if !ok {
				break
			}
			operand = parenthesized.X
		}
		literal, ok := operand.(*ast.CompositeLit)
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
		var names []*ast.Ident
		switch item := node.(type) {
		case *ast.Field:
			names = item.Names
		case *ast.ValueSpec:
			names = item.Names
		case *ast.AssignStmt:
			for _, expr := range item.Lhs {
				if ident, ok := expr.(*ast.Ident); ok {
					names = append(names, ident)
				}
			}
		}
		for _, name := range names {
			if object := info.Defs[name]; object != nil {
				result[object] = node
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
	var findings []Finding
	statements := fn.Body.List
	fn.Body.List = withoutDecorativeCalls(path, statements, packages)
	matched, found := fingerprints[canonicalFunction(fn, packages, info)]
	fn.Body.List = statements
	if matched.rule == "sorted-set-keys" && !strings.HasPrefix(filepath.ToSlash(path), "internal/lang/") {
		found = false
	}
	if matched.rule == "sorted-unique-exact" && !strings.HasPrefix(filepath.ToSlash(path), "internal/analysis/") {
		found = false
	}
	if found && (filepath.ToSlash(path) != matched.owner || fn.Name.Name != matched.function) {
		findings = append(findings, Finding{Path: filepath.ToSlash(path), Line: fset.Position(fn.Pos()).Line, Function: fn.Name.Name, Rule: matched.rule, Helper: matched.helper})
	}
	if filepath.ToSlash(path) == "internal/lang/shared/dependency_usage_stats.go" && fn.Name.Name == "BuildDependencyReportFromStats" {
		return findings
	}
	declarations := localDeclarations(fn, info)
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if ok && (reportMapping(literal, packages, info, declarations) || partialReportMapping(literal, packages, info, declarations)) {
			findings = append(findings, Finding{Path: filepath.ToSlash(path), Line: fset.Position(literal.Pos()).Line, Function: fn.Name.Name, Rule: "dependency-report-mapping", Helper: "shared.BuildDependencyReportFromStats", Advisory: !reportMapping(literal, packages, info, declarations)})
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

func mappedStatsObject(expression ast.Expr, field string, packages map[string]string, info *types.Info, declarations map[types.Object]ast.Node) types.Object {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != field {
		return nil
	}
	receiver, ok := selector.X.(*ast.Ident)
	if !ok {
		return nil
	}
	object := info.ObjectOf(receiver)
	if object == nil || !dependencyStats(declarations[object], packages, info) {
		return nil
	}
	return object
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
		if mappedStatsObject(fields[field], source, packages, info, declarations) != nil {
			matches++
		}
	}
	return matches >= 4
}
