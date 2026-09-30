package reusecheck

import "go/ast"

// Only symbols used by owned contracts are resolved without package type data.
var dotSymbols = map[string]string{
	"DependencyStats":                sharedPackage,
	"BuildDependencyStats":           sharedPackage,
	"BuildDependencyReportFromStats": sharedPackage,
	"SortedKeys":                     sharedPackage,
	"SortedDependencyUnion":          sharedPackage,
	"DependencyReport":               module + "report",
	"SortedUniqueTrimmedStrings":     module + "report",
	"Strings":                        "sort",
	"TrimSpace":                      "strings",
}

func dotImportedPath(ident *ast.Ident, packages map[string]string) string {
	if ident.Obj != nil {
		return ""
	}
	path := dotSymbols[ident.Name]
	if path != "" && packages["."+path] == path {
		return path
	}
	return ""
}
