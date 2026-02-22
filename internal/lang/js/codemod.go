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
		for _, imp := range file.Imports {
			if imp.Module != dependency {
				continue
			}
			reasonCode, reasonMessage := codemodSkipReason(imp, file)
			if reasonCode != "" {
				skips = append(skips, newCodemodSkip(file.Path, imp, reasonCode, reasonMessage))
				continue
			}

			targetModule, ok := resolver.Resolve(dependency, imp.ExportName)
			if !ok {
				skips = append(skips, newCodemodSkip(
					file.Path,
					imp,
					codemodReasonNoSubpathTarget,
					"no deterministic subpath target was found for this export",
				))
				continue
			}

			lines, ok := lineCache[file.Path]
			if !ok {
				content, err := safeio.ReadFileUnder(repoPath, filepath.Join(repoPath, file.Path))
				if err != nil {
					lineWarnings = append(lineWarnings, fmt.Sprintf("codemod preview skipped for %s: %v", file.Path, err))
					break
				}
				lines = strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
				lineCache[file.Path] = lines
			}

			if imp.Location.Line <= 0 || imp.Location.Line > len(lines) {
				skips = append(skips, newCodemodSkip(
					file.Path,
					imp,
					codemodReasonUnsupportedLine,
					"unable to map import location to source line",
				))
				continue
			}

			oldLine := lines[imp.Location.Line-1]
			newLine, ok := rewriteImportLine(oldLine, dependency, imp.ExportName, targetModule)
			if !ok {
				skips = append(skips, newCodemodSkip(
					file.Path,
					imp,
					codemodReasonUnsupportedLine,
					"import statement is not in a supported single-binding form",
				))
				continue
			}
			if oldLine == newLine {
				continue
			}
			suggestions = append(suggestions, report.CodemodSuggestion{
				File:        file.Path,
				Line:        imp.Location.Line,
				ImportName:  imp.ExportName,
				FromModule:  dependency,
				ToModule:    targetModule,
				Original:    oldLine,
				Replacement: newLine,
				Patch:       buildSingleLinePatch(file.Path, imp.Location.Line, oldLine, newLine),
			})
		}
	}

	sort.Slice(suggestions, func(i, j int) bool {
		if suggestions[i].File == suggestions[j].File {
			if suggestions[i].Line == suggestions[j].Line {
				return suggestions[i].ImportName < suggestions[j].ImportName
			}
			return suggestions[i].Line < suggestions[j].Line
		}
		return suggestions[i].File < suggestions[j].File
	})
	sort.Slice(skips, func(i, j int) bool {
		if skips[i].File == skips[j].File {
			if skips[i].Line == skips[j].Line {
				if skips[i].ReasonCode == skips[j].ReasonCode {
					return skips[i].ImportName < skips[j].ImportName
				}
				return skips[i].ReasonCode < skips[j].ReasonCode
			}
			return skips[i].Line < skips[j].Line
		}
		return skips[i].File < skips[j].File
	})

	return &report.CodemodReport{Mode: codemodModeSuggestOnly, Suggestions: suggestions, Skips: skips}, dedupeStrings(lineWarnings)
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
