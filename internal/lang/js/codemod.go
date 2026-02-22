package js

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

const codemodModeSuggestOnly = "suggest-only"

const (
	codemodReasonSideEffectImport = "side-effect-import"
	codemodReasonNamespaceImport  = "namespace-import"
	codemodReasonDefaultImport    = "default-import"
	codemodReasonAliasConflict    = "alias-conflict"
	codemodReasonUnusedImport     = "unused-import"
	codemodReasonNoSubpathTarget  = "no-subpath-target"
	codemodReasonUnsupportedLine  = "unsupported-import-syntax"
)

var (
	importStatementPattern  = regexp.MustCompile(`^(\s*)import\s+\{\s*([A-Za-z_$][A-Za-z0-9_$]*)\s*\}\s+from\s+(["'])([^"']+)(["'])(\s*;?\s*)$`)
	requireStatementPattern = regexp.MustCompile(`^(\s*)(const|let|var)\s+\{\s*([A-Za-z_$][A-Za-z0-9_$]*)\s*\}\s*=\s*require\((["'])([^"']+)(["'])\)(\s*;?\s*)$`)
)

func BuildSubpathCodemodReport(repoPath, dependency, dependencyRootPath string, scanResult ScanResult) (*report.CodemodReport, []string) {
	suggestions := make([]report.CodemodSuggestion, 0)
	skips := make([]report.CodemodSkip, 0)
	resolver := newSubpathResolver(dependencyRootPath)
	lineCache := make(map[string][]string)
	lineWarnings := make([]string, 0)

	for _, file := range scanResult.Files {
		fileSuggestions, fileSkips, fileWarnings := buildCodemodForFile(repoPath, dependency, resolver, file, lineCache)
		suggestions = append(suggestions, fileSuggestions...)
		skips = append(skips, fileSkips...)
		lineWarnings = append(lineWarnings, fileWarnings...)
	}

	sort.Slice(suggestions, func(i, j int) bool {
		return codemodSuggestionOrder(suggestions[i]) < codemodSuggestionOrder(suggestions[j])
	})
	sort.Slice(skips, func(i, j int) bool {
		return codemodSkipOrder(skips[i]) < codemodSkipOrder(skips[j])
	})

	return &report.CodemodReport{Mode: codemodModeSuggestOnly, Suggestions: suggestions, Skips: skips}, dedupeStrings(lineWarnings)
}

func buildCodemodForFile(
	repoPath string,
	dependency string,
	resolver subpathResolver,
	file FileScan,
	lineCache map[string][]string,
) ([]report.CodemodSuggestion, []report.CodemodSkip, []string) {
	suggestions := make([]report.CodemodSuggestion, 0)
	skips := make([]report.CodemodSkip, 0)
	warnings := make([]string, 0)
	for _, imp := range file.Imports {
		if imp.Module != dependency {
			continue
		}
		outcome, warning, stop := buildCodemodOutcome(repoPath, dependency, resolver, file, imp, lineCache)
		if warning != "" {
			warnings = append(warnings, warning)
		}
		if outcome.skip != nil {
			skips = append(skips, *outcome.skip)
		}
		if outcome.suggestion != nil {
			suggestions = append(suggestions, *outcome.suggestion)
		}
		if stop {
			break
		}
	}
	return suggestions, skips, warnings
}

type codemodOutcome struct {
	suggestion *report.CodemodSuggestion
	skip       *report.CodemodSkip
}

func buildCodemodOutcome(
	repoPath string,
	dependency string,
	resolver subpathResolver,
	file FileScan,
	imp ImportBinding,
	lineCache map[string][]string,
) (codemodOutcome, string, bool) {
	reasonCode, reasonMessage := codemodSkipReason(imp, file)
	if reasonCode != "" {
		skip := newCodemodSkip(file.Path, imp, reasonCode, reasonMessage)
		return codemodOutcome{skip: &skip}, "", false
	}

	targetModule, ok := resolver.Resolve(dependency, imp.ExportName)
	if !ok {
		skip := newCodemodSkip(file.Path, imp, codemodReasonNoSubpathTarget, "no deterministic subpath target was found for this export")
		return codemodOutcome{skip: &skip}, "", false
	}

	lines, warning, loaded := loadSourceLines(repoPath, file.Path, lineCache)
	if !loaded {
		return codemodOutcome{}, warning, true
	}

	if imp.Location.Line <= 0 || imp.Location.Line > len(lines) {
		skip := newCodemodSkip(file.Path, imp, codemodReasonUnsupportedLine, "unable to map import location to source line")
		return codemodOutcome{skip: &skip}, "", false
	}

	oldLine := lines[imp.Location.Line-1]
	newLine, ok := rewriteImportLine(oldLine, dependency, imp.ExportName, targetModule)
	if !ok {
		skip := newCodemodSkip(file.Path, imp, codemodReasonUnsupportedLine, "import statement is not in a supported single-binding form")
		return codemodOutcome{skip: &skip}, "", false
	}
	if oldLine == newLine {
		return codemodOutcome{}, "", false
	}

	suggestion := report.CodemodSuggestion{
		File:        file.Path,
		Line:        imp.Location.Line,
		ImportName:  imp.ExportName,
		FromModule:  dependency,
		ToModule:    targetModule,
		Original:    oldLine,
		Replacement: newLine,
		Patch:       buildSingleLinePatch(file.Path, imp.Location.Line, oldLine, newLine),
	}
	return codemodOutcome{suggestion: &suggestion}, "", false
}

func loadSourceLines(repoPath, filePath string, lineCache map[string][]string) ([]string, string, bool) {
	if lines, ok := lineCache[filePath]; ok {
		return lines, "", true
	}
	content, err := safeio.ReadFileUnder(repoPath, filepath.Join(repoPath, filePath))
	if err != nil {
		return nil, fmt.Sprintf("codemod preview skipped for %s: %v", filePath, err), false
	}
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	lineCache[filePath] = lines
	return lines, "", true
}

func codemodSuggestionOrder(item report.CodemodSuggestion) string {
	return item.File + "\x00" + fmt.Sprintf("%09d", item.Line) + "\x00" + item.ImportName + "\x00" + item.ToModule
}

func codemodSkipOrder(item report.CodemodSkip) string {
	return item.File + "\x00" + fmt.Sprintf("%09d", item.Line) + "\x00" + item.ReasonCode + "\x00" + item.ImportName
}

func codemodSkipReason(imp ImportBinding, file FileScan) (string, string) {
	switch imp.Kind {
	case ImportNamespace:
		if imp.LocalName == "*" && imp.ExportName == "*" {
			return codemodReasonSideEffectImport, "side-effect imports are not safe to rewrite automatically"
		}
		return codemodReasonNamespaceImport, "namespace imports are not safe to rewrite automatically"
	case ImportDefault:
		return codemodReasonDefaultImport, "default imports are not rewritten in this codemod mode"
	case ImportNamed:
		if imp.LocalName != imp.ExportName {
			return codemodReasonAliasConflict, "aliased imports are skipped to avoid local-name conflicts"
		}
		if !isNamedImportUsed(imp, file) {
			return codemodReasonUnusedImport, "unused imports are skipped"
		}
		return "", ""
	default:
		return codemodReasonUnsupportedLine, "import kind is not supported"
	}
}

func isNamedImportUsed(imp ImportBinding, file FileScan) bool {
	return file.IdentifierUsage[imp.LocalName] > 0
}

func newCodemodSkip(file string, imp ImportBinding, reasonCode, message string) report.CodemodSkip {
	return report.CodemodSkip{
		File:       file,
		Line:       imp.Location.Line,
		ImportName: imp.ExportName,
		Module:     imp.Module,
		ReasonCode: reasonCode,
		Message:    message,
	}
}

func rewriteImportLine(line, dependency, exportName, targetModule string) (string, bool) {
	if matches := importStatementPattern.FindStringSubmatch(line); len(matches) == 7 {
		if matches[2] != exportName || matches[4] != dependency || matches[3] != matches[5] {
			return "", false
		}
		quote := matches[3]
		return fmt.Sprintf("%simport %s from %s%s%s%s", matches[1], exportName, quote, targetModule, quote, matches[6]), true
	}
	if matches := requireStatementPattern.FindStringSubmatch(line); len(matches) == 8 {
		if matches[3] != exportName || matches[5] != dependency || matches[4] != matches[6] {
			return "", false
		}
		quote := matches[4]
		return fmt.Sprintf("%s%s %s = require(%s%s%s)%s", matches[1], matches[2], exportName, quote, targetModule, quote, matches[7]), true
	}
	return "", false
}

func buildSingleLinePatch(file string, line int, oldLine, newLine string) string {
	return strings.Join([]string{
		fmt.Sprintf("--- a/%s", file),
		fmt.Sprintf("+++ b/%s", file),
		fmt.Sprintf("@@ -%d +%d @@", line, line),
		"-" + oldLine,
		"+" + newLine,
	}, "\n")
}

type subpathResolver struct {
	dependencyRoot string
	knownSubpaths  map[string]struct{}
}

func newSubpathResolver(dependencyRoot string) subpathResolver {
	resolver := subpathResolver{
		dependencyRoot: dependencyRoot,
		knownSubpaths:  make(map[string]struct{}),
	}
	if strings.TrimSpace(dependencyRoot) == "" {
		return resolver
	}
	pkg, _, err := loadPackageJSONForSurface(dependencyRoot)
	if err != nil {
		return resolver
	}
	exportsMap, ok := pkg.Exports.(map[string]interface{})
	if !ok {
		return resolver
	}
	for key := range exportsMap {
		if !strings.HasPrefix(key, "./") {
			continue
		}
		subpath := strings.TrimPrefix(key, "./")
		if subpath == "" || strings.Contains(subpath, "*") {
			continue
		}
		resolver.knownSubpaths[subpath] = struct{}{}
	}
	return resolver
}

func (r subpathResolver) Resolve(dependency, exportName string) (string, bool) {
	exportName = strings.TrimSpace(exportName)
	if exportName == "" || exportName == "default" || exportName == "*" || strings.Contains(exportName, " ") {
		return "", false
	}
	if _, ok := r.knownSubpaths[exportName]; ok {
		return dependency + "/" + exportName, true
	}
	if r.dependencyRoot == "" {
		return "", false
	}
	if hasResolvableSubpathFile(r.dependencyRoot, exportName) {
		return dependency + "/" + exportName, true
	}
	return "", false
}

func hasResolvableSubpathFile(dependencyRoot, subpath string) bool {
	candidates := []string{
		filepath.Join(dependencyRoot, subpath),
		filepath.Join(dependencyRoot, subpath+".js"),
		filepath.Join(dependencyRoot, subpath+".mjs"),
		filepath.Join(dependencyRoot, subpath+".cjs"),
		filepath.Join(dependencyRoot, subpath+".ts"),
		filepath.Join(dependencyRoot, subpath+".mts"),
		filepath.Join(dependencyRoot, subpath+".cts"),
		filepath.Join(dependencyRoot, subpath, "index.js"),
		filepath.Join(dependencyRoot, subpath, "index.mjs"),
		filepath.Join(dependencyRoot, subpath, "index.cjs"),
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil {
			continue
		}
		if info.IsDir() {
			continue
		}
		return true
	}
	return false
}
