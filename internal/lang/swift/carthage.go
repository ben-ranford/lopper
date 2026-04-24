package swift

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func loadCarthageManifestData(repoPath string, catalog *dependencyCatalog) (bool, []string, error) {
	manifestPath := filepath.Join(repoPath, carthageManifestName)
	content, err := safeio.ReadFileUnder(repoPath, manifestPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("read %s: %w", carthageManifestName, err)
	}

	entries := parseCarthageManifestDependencies(content)
	warnings := make([]string, 0, 2)
	ambiguousModules := make(map[string]struct{})
	for _, entry := range entries {
		depID := normalizeDependencyID(entry.Dependency)
		if depID == "" {
			continue
		}
		ensureDeclaredDependencyForManager(catalog, depID, carthageManager)
		addCarthageMappings(catalog, depID, entry, ambiguousModules)
	}
	if len(entries) == 0 {
		warnings = append(warnings, "no Carthage declarations found in Cartfile")
	}
	if warning := carthageAmbiguityWarning(ambiguousModules); warning != "" {
		warnings = append(warnings, warning)
	}
	return true, warnings, nil
}

func loadCarthageResolvedData(repoPath string, catalog *dependencyCatalog) (bool, []string, error) {
	resolvedPath := filepath.Join(repoPath, carthageResolvedName)
	content, err := safeio.ReadFileUnder(repoPath, resolvedPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("read %s: %w", carthageResolvedName, err)
	}

	entries := parseCarthageResolvedDependencies(content)
	warnings := make([]string, 0, 2)
	ambiguousModules := make(map[string]struct{})
	if len(entries) == 0 {
		warnings = append(warnings, "no Carthage entries found in Cartfile.resolved")
	}
	for _, entry := range entries {
		depID := normalizeDependencyID(entry.Dependency)
		if depID == "" {
			continue
		}
		source := strings.TrimSpace(entry.Source)
		if source == "" {
			source = carthageResolvedName
		}
		version, revision := classifyCarthageReference(entry.Reference)
		ensureResolvedDependencyForManager(catalog, depID, version, revision, source, carthageManager)
		addCarthageMappings(catalog, depID, entry, ambiguousModules)
	}
	if warning := carthageAmbiguityWarning(ambiguousModules); warning != "" {
		warnings = append(warnings, warning)
	}
	return true, warnings, nil
}

func parseCarthageManifestDependencies(content []byte) []carthageDependency {
	return parseCarthageDependencies(content, false)
}

func parseCarthageResolvedDependencies(content []byte) []carthageDependency {
	return parseCarthageDependencies(content, true)
}

func parseCarthageDependencies(content []byte, requireReference bool) []carthageDependency {
	lines := strings.Split(string(content), "\n")
	entries := make([]carthageDependency, 0, len(lines))
	for _, line := range lines {
		entry, ok := parseCarthageLine(line, requireReference)
		if !ok {
			continue
		}
		entries = append(entries, entry)
		if len(entries) >= maxCarthageDeclarations {
			break
		}
	}
	return dedupeCarthageDependencies(entries)
}

func parseCarthageLine(line string, requireReference bool) (carthageDependency, bool) {
	line = strings.TrimSpace(stripHashCommentOutsideQuotes(line))
	if line == "" {
		return carthageDependency{}, false
	}
	kind, rest, ok := splitCarthageLinePrefix(line)
	if !ok {
		return carthageDependency{}, false
	}
	if kind != "github" && kind != "git" && kind != "binary" {
		return carthageDependency{}, false
	}

	source, rest, ok := readQuotedValue(rest)
	if !ok {
		return carthageDependency{}, false
	}
	reference, _, hasReference := readQuotedValue(rest)
	if requireReference && !hasReference {
		return carthageDependency{}, false
	}
	depID := deriveCarthageDependencyID(kind, source)
	if depID == "" {
		return carthageDependency{}, false
	}
	return carthageDependency{
		Kind:       kind,
		Source:     strings.TrimSpace(source),
		Reference:  strings.TrimSpace(reference),
		Dependency: depID,
	}, true
}

func stripHashCommentOutsideQuotes(line string) string {
	state := carthageCommentScanState{}
	for i := 0; i < len(line); i++ {
		if endsCarthageLineAtHash(line[i], &state) {
			return line[:i]
		}
	}
	return line
}

type carthageCommentScanState struct {
	inString bool
	escaped  bool
}

func endsCarthageLineAtHash(ch byte, state *carthageCommentScanState) bool {
	if state.inString {
		if state.escaped {
			state.escaped = false
			return false
		}
		switch ch {
		case '\\':
			state.escaped = true
		case '"':
			state.inString = false
		}
		return false
	}
	if ch == '"' {
		state.inString = true
		return false
	}
	return ch == '#'
}

func splitCarthageLinePrefix(line string) (string, string, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", "", false
	}
	prefix := strings.ToLower(strings.TrimSpace(fields[0]))
	if prefix == "" {
		return "", "", false
	}
	rest := strings.TrimSpace(line[len(fields[0]):])
	if rest == "" {
		return "", "", false
	}
	return prefix, rest, true
}

func readQuotedValue(input string) (string, string, bool) {
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, `"`) {
		return "", input, false
	}
	escaped := false
	for i := 1; i < len(input); i++ {
		ch := input[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch != '"' {
			continue
		}

		raw := input[1:i]
		if unquoted, err := strconv.Unquote(`"` + raw + `"`); err == nil {
			raw = unquoted
		}
		return strings.TrimSpace(raw), strings.TrimSpace(input[i+1:]), true
	}
	return "", input, false
}

func deriveCarthageDependencyID(kind, source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	if kind == "github" {
		cleaned := strings.Trim(source, "/")
		parts := strings.Split(cleaned, "/")
		if len(parts) >= 2 {
			return normalizeDependencyID(strings.TrimSuffix(parts[len(parts)-1], ".git"))
		}
	}
	identity := derivePackageIdentity(source)
	if kind == "binary" {
		identity = strings.TrimSuffix(identity, path.Ext(identity))
	}
	if depID := normalizeDependencyID(identity); depID != "" {
		return depID
	}
	return normalizeDependencyID(source)
}

func addCarthageMappings(catalog *dependencyCatalog, depID string, entry carthageDependency, ambiguousModules map[string]struct{}) {
	mapAlias(catalog, depID, depID)
	for _, alias := range carthageAliasCandidates(entry, depID) {
		mapAlias(catalog, alias, depID)
	}
	for _, module := range carthageModuleCandidates(entry, depID) {
		if setLookupWithStatus(catalog.ModuleToDependency, lookupKey(module), depID) {
			ambiguousModules[module] = struct{}{}
		}
	}
}

func carthageAliasCandidates(entry carthageDependency, depID string) []string {
	candidates := []string{depID, entry.Dependency, entry.Source, derivePackageIdentity(entry.Source)}
	if entry.Kind == "github" {
		parts := strings.Split(strings.Trim(entry.Source, "/"), "/")
		if len(parts) >= 2 {
			candidates = append(candidates, parts[len(parts)-2], parts[len(parts)-1])
		}
	}
	if entry.Kind == "binary" {
		base := derivePackageIdentity(entry.Source)
		candidates = append(candidates, strings.TrimSuffix(base, path.Ext(base)))
	}
	return dedupeStrings(candidates)
}

func carthageModuleCandidates(entry carthageDependency, depID string) []string {
	candidates := append([]string{depID, entry.Dependency}, carthageAliasCandidates(entry, depID)...)
	for _, candidate := range append([]string{}, candidates...) {
		tokens := carthageModuleTokens(candidate)
		if len(tokens) > 1 {
			candidates = append(candidates, strings.Join(tokens, ""))
		}
	}
	return dedupeStrings(candidates)
}

func carthageModuleTokens(value string) []string {
	return strings.FieldsFunc(strings.TrimSpace(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func dedupeCarthageDependencies(entries []carthageDependency) []carthageDependency {
	if len(entries) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(entries))
	result := make([]carthageDependency, 0, len(entries))
	for _, entry := range entries {
		depID := normalizeDependencyID(entry.Dependency)
		if depID == "" {
			continue
		}
		if _, ok := seen[depID]; ok {
			continue
		}
		seen[depID] = struct{}{}
		entry.Dependency = depID
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Dependency < result[j].Dependency
	})
	return result
}

func carthageAmbiguityWarning(ambiguousModules map[string]struct{}) string {
	return formatAmbiguousModuleWarning("Carthage", ambiguousModules)
}

func classifyCarthageReference(value string) (string, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ""
	}
	if isLikelyCarthageVersion(value) {
		return value, ""
	}
	return "", value
}

func isLikelyCarthageVersion(value string) bool {
	trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "v"))
	if trimmed == "" {
		return false
	}
	if !unicode.IsDigit(rune(trimmed[0])) {
		return false
	}
	hasDot := false
	for _, r := range trimmed {
		switch {
		case unicode.IsDigit(r):
		case r == '.':
			hasDot = true
		case r == '-' || r == '+':
		default:
			return false
		}
	}
	return hasDot
}
