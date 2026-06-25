package python

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report"
)

const (
	pythonCodemodReasonAllImportsUnused      = "all-imports-unused"
	pythonCodemodReasonSingleLineImport      = "single-line-import"
	pythonCodemodReasonSourceLineMatch       = "source-line-match"
	pythonCodemodReasonMixedDependencyLine   = "mixed-dependency-import-line"
	pythonCodemodReasonMixedUsedLine         = "mixed-used-import-line"
	pythonCodemodReasonUnsupportedSyntax     = "unsupported-import-syntax"
	pythonCodemodReasonInlineComment         = "inline-comment"
	pythonCodemodReasonPublicAPIFile         = "public-api-file"
	pythonCodemodReasonSourceLineUnavailable = "source-line-unavailable"
)

func BuildUnusedImportCodemodReport(repoPath, dependency string, scan scanResult) (*report.CodemodReport, []string) {
	suggestions := make([]report.CodemodSuggestion, 0)
	skips := make([]report.CodemodSkip, 0)
	warnings := make([]string, 0)
	lineCache := make(map[string][]string)

	for _, file := range scan.Files {
		fileSuggestions, fileSkips, fileWarnings := buildPythonCodemodForFile(repoPath, dependency, file, lineCache)
		suggestions = append(suggestions, fileSuggestions...)
		skips = append(skips, fileSkips...)
		warnings = append(warnings, fileWarnings...)
	}

	sort.Slice(suggestions, func(i, j int) bool {
		return pythonCodemodSuggestionOrder(suggestions[i]) < pythonCodemodSuggestionOrder(suggestions[j])
	})
	sort.Slice(skips, func(i, j int) bool {
		return pythonCodemodSkipOrder(skips[i]) < pythonCodemodSkipOrder(skips[j])
	})

	return &report.CodemodReport{Mode: shared.CodemodModeSuggestOnly, Suggestions: suggestions, Skips: skips}, shared.DedupeWarnings(warnings)
}

func buildPythonCodemodForFile(repoPath string, dependency string, file fileScan, lineCache map[string][]string) ([]report.CodemodSuggestion, []report.CodemodSkip, []string) {
	suggestions := make([]report.CodemodSuggestion, 0)
	skips := make([]report.CodemodSkip, 0)
	warnings := make([]string, 0)

	importsByLine := pythonImportsByLine(file.Imports)
	lines, warning, loaded := shared.LoadCodemodSourceLines(repoPath, file.Path, lineCache)
	if !loaded {
		if warning != "" {
			warnings = append(warnings, warning)
		}
		return suggestions, skips, warnings
	}

	for _, line := range pythonSortedImportLines(importsByLine) {
		lineImports := importsByLine[line]
		targetImports := pythonDependencyImports(dependency, lineImports)
		unusedTargetImports := pythonUnusedImports(file, targetImports)
		if len(unusedTargetImports) == 0 {
			continue
		}

		sourceLine, ok := pythonSourceLine(lines, line)
		if !ok {
			skips = append(skips, newPythonCodemodSkips(dependency, file.Path, unusedTargetImports, pythonCodemodReasonSourceLineUnavailable, "unable to map import location to source line")...)
			continue
		}
		if reasonCode, message := pythonUnsafeUnusedImportLineReason(repoPath, dependency, file.Path, sourceLine, lineImports, targetImports, unusedTargetImports); reasonCode != "" {
			skips = append(skips, newPythonCodemodSkips(dependency, file.Path, unusedTargetImports, reasonCode, message)...)
			continue
		}

		suggestions = append(suggestions, shared.NewCodemodSuggestion(shared.CodemodSuggestionSpec{
			Language:    "python",
			Dependency:  dependency,
			File:        file.Path,
			Line:        line,
			ImportName:  pythonImportNames(unusedTargetImports),
			FromModule:  pythonLineModule(unusedTargetImports),
			Original:    sourceLine,
			Replacement: "",
			Patch:       shared.BuildDeleteLinePatch(file.Path, line, sourceLine),
			SafetyReasonCodes: []string{
				pythonCodemodReasonAllImportsUnused,
				pythonCodemodReasonSingleLineImport,
				pythonCodemodReasonSourceLineMatch,
			},
			DeleteLine: true,
		}))
	}

	return suggestions, skips, warnings
}

func pythonImportsByLine(imports []importBinding) map[int][]importBinding {
	grouped := make(map[int][]importBinding)
	for _, imported := range imports {
		if imported.Location.Line <= 0 {
			continue
		}
		grouped[imported.Location.Line] = append(grouped[imported.Location.Line], imported)
	}
	return grouped
}

func pythonSortedImportLines(importsByLine map[int][]importBinding) []int {
	lines := make([]int, 0, len(importsByLine))
	for line := range importsByLine {
		lines = append(lines, line)
	}
	sort.Ints(lines)
	return lines
}

func pythonDependencyImports(dependency string, imports []importBinding) []importBinding {
	targets := make([]importBinding, 0, len(imports))
	for _, imported := range imports {
		if normalizeDependencyID(imported.Dependency) == dependency {
			targets = append(targets, imported)
		}
	}
	return targets
}

func pythonUnusedImports(file fileScan, imports []importBinding) []importBinding {
	unused := make([]importBinding, 0, len(imports))
	for _, imported := range imports {
		if pythonImportUsed(file, imported) {
			continue
		}
		unused = append(unused, imported)
	}
	return unused
}

func pythonImportUsed(file fileScan, imported importBinding) bool {
	return imported.Wildcard || file.Usage[imported.Local] > 0
}

func pythonSourceLine(lines []string, line int) (string, bool) {
	if line <= 0 || line > len(lines) {
		return "", false
	}
	return lines[line-1], true
}

func pythonUnsafeUnusedImportLineReason(repoPath, dependency, filePath, sourceLine string, lineImports, targetImports, unusedTargetImports []importBinding) (string, string) {
	if filepath.Base(filePath) == "__init__.py" {
		return pythonCodemodReasonPublicAPIFile, "__init__.py imports may define package public API"
	}
	trimmed := strings.TrimSpace(sourceLine)
	if trimmed == "" {
		return pythonCodemodReasonUnsupportedSyntax, "empty import line is not safe to remove automatically"
	}
	if strings.Contains(sourceLine, "#") {
		return pythonCodemodReasonInlineComment, "import lines with comments are skipped to avoid deleting human context"
	}
	if strings.Contains(sourceLine, ";") || strings.Contains(sourceLine, "\\") || strings.ContainsAny(sourceLine, "()") {
		return pythonCodemodReasonUnsupportedSyntax, "compound, continued, or parenthesized imports are not removed automatically"
	}
	if len(targetImports) != len(unusedTargetImports) {
		return pythonCodemodReasonMixedUsedLine, "line mixes used and unused imports for this dependency"
	}
	if !pythonLineTextMatchesParsedImports(repoPath, dependency, sourceLine, lineImports) {
		return pythonCodemodReasonMixedDependencyLine, "line includes imports that are not all unused imports for this dependency"
	}
	return "", ""
}

func pythonLineTextMatchesParsedImports(repoPath, dependency, sourceLine string, lineImports []importBinding) bool {
	if matches := importLinePattern.FindStringSubmatch(sourceLine); len(matches) == 2 {
		parts := splitCSV(matches[1])
		return len(parts) == len(lineImports) && pythonImportPartsTargetDependency(repoPath, dependency, parts)
	}
	if matches := fromLinePattern.FindStringSubmatch(sourceLine); len(matches) == 3 {
		parts := splitCSV(normalizeFromImportSymbols(matches[2]))
		moduleName, resolvedDependency, ok := resolveFromImportDependency(matches[1], repoPath)
		return ok && moduleName != "" && resolvedDependency == dependency && len(parts) == len(lineImports)
	}
	return false
}

func pythonImportPartsTargetDependency(repoPath, dependency string, parts []string) bool {
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		moduleName, _ := parseImportPart(part)
		if normalizeDependencyID(dependencyFromModule(repoPath, moduleName)) != dependency {
			return false
		}
	}
	return true
}

func newPythonCodemodSkips(dependency, file string, imports []importBinding, reasonCode, message string) []report.CodemodSkip {
	skips := make([]report.CodemodSkip, 0, len(imports))
	for _, imported := range imports {
		skips = append(skips, shared.NewCodemodSkip(shared.CodemodSkipSpec{
			Language:   "python",
			Dependency: dependency,
			File:       file,
			Line:       imported.Location.Line,
			ImportName: pythonSingleImportName(imported),
			Module:     imported.Module,
			ReasonCode: reasonCode,
			Message:    message,
		}))
	}
	return skips
}

func pythonImportNames(imports []importBinding) string {
	names := make([]string, 0, len(imports))
	for _, imported := range imports {
		names = append(names, pythonSingleImportName(imported))
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func pythonSingleImportName(imported importBinding) string {
	if strings.TrimSpace(imported.Local) != "" && imported.Local != imported.Name {
		return imported.Name + " as " + imported.Local
	}
	return imported.Name
}

func pythonLineModule(imports []importBinding) string {
	modules := make([]string, 0, len(imports))
	seen := make(map[string]struct{}, len(imports))
	for _, imported := range imports {
		if strings.TrimSpace(imported.Module) == "" {
			continue
		}
		if _, ok := seen[imported.Module]; ok {
			continue
		}
		seen[imported.Module] = struct{}{}
		modules = append(modules, imported.Module)
	}
	sort.Strings(modules)
	return strings.Join(modules, ",")
}

func pythonCodemodSuggestionOrder(item report.CodemodSuggestion) string {
	return item.File + "\x00" + fmt.Sprintf("%09d", item.Line) + "\x00" + item.ImportName
}

func pythonCodemodSkipOrder(item report.CodemodSkip) string {
	return item.File + "\x00" + fmt.Sprintf("%09d", item.Line) + "\x00" + item.ReasonCode + "\x00" + item.ImportName
}
