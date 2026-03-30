package dotnet

import (
	"bytes"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report"
)

type mappingMetadata struct {
	ambiguousByDependency  map[string]int
	undeclaredByDependency map[string]int
}

type parsedSourceFile struct {
	File    fileScan
	Mapping mappingMetadata
}

func parseSourceDocument(source sourceDocument, mapper dependencyMapper) parsedSourceFile {
	imports, mappingMeta := parseImports(source.Content, source.RelativePath, mapper)
	return parsedSourceFile{
		File: fileScan{
			Path:    source.RelativePath,
			Imports: imports,
			Usage:   shared.CountUsage(source.Content, imports),
		},
		Mapping: mappingMeta,
	}
}

func parseImports(content []byte, relativePath string, mapper dependencyMapper) ([]importBinding, mappingMetadata) {
	meta := mappingMetadata{
		ambiguousByDependency:  make(map[string]int),
		undeclaredByDependency: make(map[string]int),
	}
	imports := make([]importBinding, 0)
	forEachSourceLine(content, func(lineNo int, raw, line []byte) {
		if binding := parseImportLine(line, raw, relativePath, lineNo, mapper, &meta); binding != nil {
			imports = append(imports, *binding)
		}
	})

	return imports, meta
}

func forEachSourceLine(content []byte, visit func(lineNo int, raw, line []byte)) {
	for lineNo, start := 1, 0; start <= len(content); lineNo++ {
		end := start
		for end < len(content) && content[end] != '\n' {
			end++
		}
		raw := trimTrailingCarriageReturn(content[start:end])
		visit(lineNo, raw, stripLineCommentBytes(raw))
		if end == len(content) {
			break
		}
		start = end + 1
	}
}

func parseImportLine(line, raw []byte, relativePath string, lineNo int, mapper dependencyMapper, meta *mappingMetadata) *importBinding {
	if len(line) == 0 {
		return nil
	}
	column := firstContentColumnBytes(raw)
	if binding, handled := parseCSharpImportLine(line, relativePath, lineNo, column, mapper, meta); handled {
		return binding
	}
	return parseFSharpImportLine(line, relativePath, lineNo, column, mapper, meta)
}

func parseCSharpImportLine(line []byte, relativePath string, lineNumber, column int, mapper dependencyMapper, meta *mappingMetadata) (*importBinding, bool) {
	module, alias, ok := parseCSharpUsingBytes(line)
	if !ok {
		return nil, false
	}
	dependency, resolved := resolveImportDependency(module, mapper, meta)
	if !resolved {
		return nil, true
	}
	if alias == "" {
		binding := buildImportBinding(importBindingArgs{
			dependency:   dependency,
			module:       module,
			name:         "*",
			wildcard:     true,
			relativePath: relativePath,
			lineNumber:   lineNumber,
			column:       column,
		})
		return &binding, true
	}
	binding := buildImportBinding(importBindingArgs{
		dependency:   dependency,
		module:       module,
		name:         alias,
		local:        alias,
		relativePath: relativePath,
		lineNumber:   lineNumber,
		column:       column,
	})
	return &binding, true
}

func parseFSharpImportLine(line []byte, relativePath string, lineNumber, column int, mapper dependencyMapper, meta *mappingMetadata) *importBinding {
	module, ok := parseFSharpOpenBytes(line)
	if !ok {
		return nil
	}
	dependency, resolved := resolveImportDependency(module, mapper, meta)
	if !resolved {
		return nil
	}
	binding := buildImportBinding(importBindingArgs{
		dependency:   dependency,
		module:       module,
		name:         "*",
		wildcard:     true,
		relativePath: relativePath,
		lineNumber:   lineNumber,
		column:       column,
	})
	return &binding
}

type importBindingArgs struct {
	dependency   string
	module       string
	name         string
	local        string
	wildcard     bool
	relativePath string
	lineNumber   int
	column       int
}

func buildImportBinding(args importBindingArgs) importBinding {
	return importBinding{
		Dependency: args.dependency,
		Module:     args.module,
		Name:       args.name,
		Local:      args.local,
		Wildcard:   args.wildcard,
		Location: report.Location{
			File:   args.relativePath,
			Line:   args.lineNumber,
			Column: args.column,
		},
	}
}

func parseCSharpUsing(line string) (module string, alias string, ok bool) {
	return parseCSharpUsingBytes([]byte(line))
}

func parseFSharpOpen(line string) (module string, ok bool) {
	return parseFSharpOpenBytes([]byte(line))
}

func parseCSharpUsingBytes(line []byte) (module string, alias string, ok bool) {
	line = bytes.TrimSpace(line)
	if next, matched := consumeKeyword(line, "global"); matched {
		line = next
	}
	next, matched := consumeKeyword(line, "using")
	if !matched {
		return "", "", false
	}
	line = next
	if next, matched = consumeKeyword(line, "static"); matched {
		line = next
	}
	end := bytes.IndexByte(line, ';')
	if end < 0 {
		return "", "", false
	}
	expression := bytes.TrimSpace(line[:end])
	if len(expression) == 0 {
		return "", "", false
	}
	if split := bytes.IndexByte(expression, '='); split >= 0 {
		alias = string(bytes.TrimSpace(expression[:split]))
		module = normalizeNamespace(string(bytes.TrimSpace(expression[split+1:])))
		if alias == "" || module == "" {
			return "", "", false
		}
		return module, alias, true
	}
	module = normalizeNamespace(string(expression))
	return module, "", module != ""
}

func parseFSharpOpenBytes(line []byte) (module string, ok bool) {
	line = bytes.TrimSpace(line)
	next, matched := consumeKeyword(line, "open")
	if !matched || len(next) == 0 || !isNamespaceStartByte(next[0]) {
		return "", false
	}
	end := 1
	for end < len(next) && isNamespaceByte(next[end]) {
		end++
	}
	module = normalizeNamespace(string(next[:end]))
	return module, module != ""
}

func consumeKeyword(line []byte, keyword string) ([]byte, bool) {
	if !hasBytesPrefix(line, keyword) || len(line) <= len(keyword) || !isSpaceByte(line[len(keyword)]) {
		return nil, false
	}
	return bytes.TrimSpace(line[len(keyword):]), true
}

func hasBytesPrefix(line []byte, prefix string) bool {
	if len(line) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		if line[i] != prefix[i] {
			return false
		}
	}
	return true
}

func isSpaceByte(ch byte) bool {
	switch ch {
	case ' ', '\t', '\r', '\n':
		return true
	default:
		return false
	}
}

func isNamespaceStartByte(ch byte) bool {
	return ch == '_' || (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z')
}

func isNamespaceByte(ch byte) bool {
	return isNamespaceStartByte(ch) || ch == '.' || (ch >= '0' && ch <= '9')
}

func stripLineComment(line string) string {
	if index := strings.Index(line, "//"); index >= 0 {
		line = line[:index]
	}
	return strings.TrimSpace(line)
}

func stripLineCommentBytes(line []byte) []byte {
	for i := 0; i+1 < len(line); i++ {
		if line[i] == '/' && line[i+1] == '/' {
			line = line[:i]
			break
		}
	}
	return bytes.TrimSpace(line)
}

func trimTrailingCarriageReturn(line []byte) []byte {
	if len(line) > 0 && line[len(line)-1] == '\r' {
		return line[:len(line)-1]
	}
	return line
}

func firstContentColumnBytes(line []byte) int {
	for i := 0; i < len(line); i++ {
		if line[i] != ' ' && line[i] != '\t' {
			return i + 1
		}
	}
	return 1
}
