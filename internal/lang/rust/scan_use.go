package rust

import (
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
)

func parseUseStatementIndex(content string, idx []int) (string, int, int, bool) {
	if len(idx) < 4 {
		return "", 0, 0, false
	}
	clauseStart, clauseEnd := idx[2], idx[3]
	if clauseStart < 0 || clauseEnd < 0 || clauseEnd > len(content) {
		return "", 0, 0, false
	}
	clause := strings.TrimSpace(content[clauseStart:clauseEnd])
	line, column := lineColumn(content, clauseStart)
	return clause, line, column, true
}

func appendUseClauseImports(imports []importBinding, clause string, ctx useImportContext) []importBinding {
	entries := parseUseClause(clause)
	for _, entry := range entries {
		binding, ok := makeUseImportBinding(entry, ctx)
		if !ok {
			continue
		}
		imports = append(imports, binding)
	}
	return imports
}

func makeUseImportBinding(entry usePathEntry, ctx useImportContext) (importBinding, bool) {
	if entry.Path == "" {
		return importBinding{}, false
	}
	dependency := resolveDependency(entry.Path, ctx.CrateRoot, ctx.DepLookup, ctx.Scan)
	if dependency == "" {
		return importBinding{}, false
	}
	module := strings.TrimPrefix(entry.Path, "::")
	name, local := normalizeUseSymbolNames(entry, module)
	return importBinding{
		Dependency: dependency,
		Module:     module,
		Name:       name,
		Local:      local,
		Wildcard:   entry.Wildcard,
		Location: report.Location{
			File:   ctx.FilePath,
			Line:   ctx.Line,
			Column: ctx.Column,
		},
	}, true
}

func normalizeUseSymbolNames(entry usePathEntry, module string) (string, string) {
	name := entry.Symbol
	if name == "" {
		name = lastPathSegment(module)
	}
	local := entry.Local
	if local == "" {
		local = name
	}
	if entry.Wildcard {
		name = "*"
		if local == "" {
			local = lastPathSegment(module)
		}
	}
	return name, local
}

func parseUseClause(clause string) []usePathEntry {
	parts := splitTopLevel(clause, ',')
	entries := make([]usePathEntry, 0)
	for _, part := range parts {
		expandUsePart(strings.TrimSpace(part), "", &entries)
	}
	return entries
}

func expandUsePart(part, prefix string, out *[]usePathEntry) {
	part = strings.TrimSpace(part)
	if part == "" {
		return
	}
	part = strings.TrimPrefix(part, "pub ")
	if expandUseBraceGroup(part, prefix, out) {
		return
	}
	if expandUsePrefixedBraceGroup(part, prefix, out) {
		return
	}
	part, local := parseUseLocalAlias(part)
	part, prefix, wildcard := normalizeUseWildcard(part, prefix)
	*out = append(*out, makeUsePathEntry(prefix, part, local, wildcard))
}

func expandUseBraceGroup(part, prefix string, out *[]usePathEntry) bool {
	if !strings.HasPrefix(part, "{") || !strings.HasSuffix(part, "}") {
		return false
	}
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(part, "{"), "}"))
	expandUseSegments(inner, prefix, out)
	return true
}

func expandUsePrefixedBraceGroup(part, prefix string, out *[]usePathEntry) bool {
	idx := strings.Index(part, "::{")
	if idx < 0 || !strings.HasSuffix(part, "}") {
		return false
	}
	base := strings.TrimSpace(part[:idx])
	inner := strings.TrimSpace(part[idx+3 : len(part)-1])
	nextPrefix := joinPath(prefix, base)
	expandUseSegments(inner, nextPrefix, out)
	return true
}

func expandUseSegments(inner, prefix string, out *[]usePathEntry) {
	for _, segment := range splitTopLevel(inner, ',') {
		expandUsePart(segment, prefix, out)
	}
}

func parseUseLocalAlias(part string) (string, string) {
	idx := strings.LastIndex(part, " as ")
	if idx <= 0 {
		return part, ""
	}
	local := strings.TrimSpace(part[idx+4:])
	base := strings.TrimSpace(part[:idx])
	return base, local
}

func normalizeUseWildcard(part, prefix string) (string, string, bool) {
	wildcard := part == "*" || strings.HasSuffix(part, "::*")
	if !wildcard {
		return part, prefix, false
	}
	if part == "*" {
		return strings.TrimSpace(prefix), "", true
	}
	return strings.TrimSpace(strings.TrimSuffix(part, "::*")), prefix, true
}

func makeUsePathEntry(prefix, part, local string, wildcard bool) usePathEntry {
	fullPath := joinPath(prefix, part)
	symbol := lastPathSegment(fullPath)
	if strings.EqualFold(symbol, "self") {
		symbol = lastPathSegment(prefix)
	}
	if wildcard {
		symbol = "*"
	}
	if strings.EqualFold(local, "self") {
		local = lastPathSegment(prefix)
	}
	return usePathEntry{
		Path:     fullPath,
		Symbol:   symbol,
		Local:    local,
		Wildcard: wildcard,
	}
}

func joinPath(prefix, value string) string {
	prefix = strings.TrimSpace(prefix)
	value = strings.TrimSpace(value)
	switch {
	case prefix == "":
		return strings.TrimPrefix(value, "::")
	case value == "":
		return strings.TrimPrefix(prefix, "::")
	default:
		return strings.TrimPrefix(prefix+"::"+value, "::")
	}
}

func splitTopLevel(value string, sep rune) []string {
	parts := make([]string, 0)
	depth := 0
	start := 0
	for i, r := range value {
		switch r {
		case '{':
			depth++
		case '}':
			depth--
		case sep:
			if depth == 0 {
				parts = append(parts, strings.TrimSpace(value[start:i]))
				start = i + 1
			}
		}
	}
	parts = append(parts, strings.TrimSpace(value[start:]))
	return parts
}

func lineColumn(content string, offset int) (int, int) {
	if offset < 0 {
		return 1, 1
	}
	if offset > len(content) {
		offset = len(content)
	}
	line := 1 + strings.Count(content[:offset], "\n")
	lineStart := strings.LastIndex(content[:offset], "\n")
	if lineStart < 0 {
		return line, offset + 1
	}
	return line, offset - lineStart
}
