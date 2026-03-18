package swift

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func buildDependencyCatalog(repoPath string) (dependencyCatalog, []string, error) {
	catalog := dependencyCatalog{
		Dependencies:       make(map[string]dependencyMeta),
		AliasToDependency:  make(map[string]string),
		ModuleToDependency: make(map[string]string),
		LocalModules:       make(map[string]struct{}),
	}
	warnings := make([]string, 0)

	manifestFound, manifestWarnings, err := loadManifestData(repoPath, &catalog)
	if err != nil {
		return dependencyCatalog{}, nil, err
	}
	warnings = append(warnings, manifestWarnings...)
	if !manifestFound {
		warnings = append(warnings, packageManifestName+" not found; dependency declaration mapping may be incomplete")
	}

	resolvedFound, resolvedWarnings, err := loadResolvedData(repoPath, &catalog)
	if err != nil {
		return dependencyCatalog{}, nil, err
	}
	warnings = append(warnings, resolvedWarnings...)
	if !resolvedFound {
		warnings = append(warnings, packageResolvedName+" not found; version/resolution mapping may be incomplete")
	}

	if len(catalog.Dependencies) == 0 {
		warnings = append(warnings, "no Swift package dependencies were discovered from Package.swift or Package.resolved")
	}
	return catalog, dedupeWarnings(warnings), nil
}

func loadManifestData(repoPath string, catalog *dependencyCatalog) (bool, []string, error) {
	manifestPath := filepath.Join(repoPath, packageManifestName)
	content, err := safeio.ReadFileUnder(repoPath, manifestPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("read %s: %w", packageManifestName, err)
	}

	warnings := make([]string, 0)
	manifestText := string(content)

	packageArgs := extractDotCallArguments(manifestText, "package", maxManifestDeclarations)
	for _, args := range packageArgs {
		depID, aliases := parsePackageDeclaration(args)
		if depID == "" {
			continue
		}
		ensureDependency(catalog, depID, true, false, "", "", "")
		for _, alias := range aliases {
			mapAlias(catalog, alias, depID)
			mapModule(catalog, alias, depID)
		}
		mapAlias(catalog, depID, depID)
		mapModule(catalog, depID, depID)
	}

	productArgs := extractDotCallArguments(manifestText, "product", maxManifestDeclarations)
	for _, args := range productArgs {
		fields := parseStringFields(args)
		productName := strings.TrimSpace(fields["name"])
		dependencyRef := strings.TrimSpace(fields["package"])
		if productName == "" || dependencyRef == "" {
			continue
		}
		depID := resolveDependencyReference(*catalog, dependencyRef)
		if depID == "" {
			depID = normalizeDependencyID(dependencyRef)
			ensureDependency(catalog, depID, true, false, "", "", "")
		}
		mapModule(catalog, productName, depID)
	}

	collectLocalModules(manifestText, catalog)

	if len(packageArgs) == 0 {
		warnings = append(warnings, "no .package(...) declarations found in Package.swift")
	}
	return true, warnings, nil
}

func loadResolvedData(repoPath string, catalog *dependencyCatalog) (bool, []string, error) {
	resolvedPath := filepath.Join(repoPath, packageResolvedName)
	content, err := safeio.ReadFileUnder(repoPath, resolvedPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("read %s: %w", packageResolvedName, err)
	}

	pins, err := parseResolvedPins(content)
	if err != nil {
		return false, nil, fmt.Errorf("parse %s: %w", packageResolvedName, err)
	}
	warnings := make([]string, 0)
	if len(pins) == 0 {
		warnings = append(warnings, "no pins found in Package.resolved")
	}
	for _, pin := range pins {
		depID := resolvedPinDependencyID(pin)
		if depID == "" {
			continue
		}

		source := resolvedPinSource(pin)
		ensureDependency(catalog, depID, false, true, pin.State.Version, pin.State.Revision, source)
		addResolvedPinMappings(catalog, depID, pin, source)
	}
	return true, warnings, nil
}

func resolvedPinDependencyID(pin resolvedPin) string {
	candidates := []string{
		pin.Identity,
		pin.Package,
		derivePackageIdentity(pin.Location),
		derivePackageIdentity(pin.RepositoryURL),
	}
	for _, candidate := range candidates {
		if depID := normalizeDependencyID(candidate); depID != "" {
			return depID
		}
	}
	return ""
}

func resolvedPinSource(pin resolvedPin) string {
	for _, source := range []string{pin.Location, pin.RepositoryURL} {
		if trimmed := strings.TrimSpace(source); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func addResolvedPinMappings(catalog *dependencyCatalog, depID string, pin resolvedPin, source string) {
	mapAlias(catalog, depID, depID)
	mapModule(catalog, depID, depID)
	mapAlias(catalog, pin.Identity, depID)
	if pin.Package != "" {
		mapAlias(catalog, pin.Package, depID)
		mapModule(catalog, pin.Package, depID)
	}
	if identityFromSource := derivePackageIdentity(source); identityFromSource != "" {
		mapAlias(catalog, identityFromSource, depID)
		mapModule(catalog, identityFromSource, depID)
	}
}

func parseResolvedPins(content []byte) ([]resolvedPin, error) {
	doc := resolvedDocument{}
	if err := json.Unmarshal(content, &doc); err != nil {
		return nil, err
	}
	pins := make([]resolvedPin, 0, len(doc.Pins)+len(doc.Object.Pins))
	pins = append(pins, doc.Pins...)
	pins = append(pins, doc.Object.Pins...)
	return pins, nil
}

func collectLocalModules(manifestText string, catalog *dependencyCatalog) {
	callNames := []string{"target", "testTarget", "executableTarget", "binaryTarget", "macro", "plugin", "library", "executable"}
	for _, callName := range callNames {
		argsList := extractDotCallArguments(manifestText, callName, maxManifestDeclarations)
		for _, args := range argsList {
			fields := parseStringFields(args)
			name := strings.TrimSpace(fields["name"])
			if name == "" {
				continue
			}
			key := lookupKey(name)
			if key != "" {
				catalog.LocalModules[key] = struct{}{}
			}
		}
	}
}

func parsePackageDeclaration(args string) (string, []string) {
	fields := parseStringFields(args)
	depID := normalizeDependencyID(fields["id"])
	if depID == "" {
		depID = normalizeDependencyID(derivePackageIdentity(fields["url"]))
	}
	if depID == "" {
		depID = normalizeDependencyID(derivePackageIdentity(fields["path"]))
	}
	if depID == "" {
		depID = normalizeDependencyID(fields["name"])
	}
	aliases := make([]string, 0, 4)
	for _, alias := range []string{fields["name"], fields["id"], derivePackageIdentity(fields["url"]), derivePackageIdentity(fields["path"])} {
		if strings.TrimSpace(alias) != "" {
			aliases = append(aliases, alias)
		}
	}
	aliases = dedupeStrings(aliases)
	return depID, aliases
}

func parseStringFields(expression string) map[string]string {
	matches := stringFieldPattern.FindAllStringSubmatch(expression, -1)
	fields := make(map[string]string, len(matches))
	for _, match := range matches {
		if len(match) != 3 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(match[1]))
		if key == "" {
			continue
		}
		value := match[2]
		if unquoted, err := strconv.Unquote("\"" + value + "\""); err == nil {
			value = unquoted
		}
		fields[key] = strings.TrimSpace(value)
	}
	return fields
}

func extractDotCallArguments(content, callName string, maxItems int) []string {
	token := "." + callName
	items := make([]string, 0)
	searchFrom := 0
	for searchFrom < len(content) {
		idx := strings.Index(content[searchFrom:], token)
		if idx < 0 {
			break
		}
		callStart := searchFrom + idx
		cursor := callStart + len(token)
		for cursor < len(content) && unicode.IsSpace(rune(content[cursor])) {
			cursor++
		}
		if cursor >= len(content) || content[cursor] != '(' {
			searchFrom = callStart + len(token)
			continue
		}
		arguments, nextPos, ok := captureParenthesized(content, cursor)
		if !ok {
			break
		}
		items = append(items, arguments)
		if maxItems > 0 && len(items) >= maxItems {
			break
		}
		searchFrom = nextPos
	}
	return items
}

func captureParenthesized(content string, openParenIndex int) (string, int, bool) {
	if openParenIndex < 0 || openParenIndex >= len(content) || content[openParenIndex] != '(' {
		return "", openParenIndex, false
	}
	start := openParenIndex + 1
	depth := 0
	inString := byte(0)
	escaped := false
	for idx := openParenIndex; idx < len(content); idx++ {
		ch := content[idx]
		if consumeQuotedByte(ch, &inString, &escaped) {
			continue
		}
		closed, valid := advanceParenthesisDepth(ch, &depth)
		if closed {
			return content[start:idx], idx + 1, true
		}
		if !valid {
			return "", idx + 1, false
		}
	}
	return "", len(content), false
}

func consumeQuotedByte(ch byte, inString *byte, escaped *bool) bool {
	if *inString == 0 {
		if ch == '\'' || ch == '"' {
			*inString = ch
			return true
		}
		return false
	}
	if *escaped {
		*escaped = false
		return true
	}
	switch ch {
	case '\\':
		*escaped = true
	case *inString:
		*inString = 0
	}
	return true
}

func advanceParenthesisDepth(ch byte, depth *int) (bool, bool) {
	switch ch {
	case '(':
		(*depth)++
	case ')':
		(*depth)--
		if *depth == 0 {
			return true, true
		}
		if *depth < 0 {
			return false, false
		}
	}
	return false, true
}

func derivePackageIdentity(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	if parsed, err := url.Parse(source); err == nil && parsed != nil && parsed.Path != "" {
		source = parsed.Path
	} else if strings.HasPrefix(source, "git@") {
		if colon := strings.Index(source, ":"); colon >= 0 && colon+1 < len(source) {
			source = source[colon+1:]
		}
	}
	source = strings.TrimSuffix(source, "/")
	base := path.Base(source)
	if base == "." || base == ".." || base == "/" {
		return ""
	}
	base = strings.TrimSuffix(base, ".git")
	return strings.TrimSpace(base)
}

func resolveDependencyReference(catalog dependencyCatalog, value string) string {
	key := lookupKey(value)
	if key == "" {
		return ""
	}
	if depID, ok := resolveLookup(catalog.ModuleToDependency, key); ok {
		return depID
	}
	if depID, ok := resolveLookup(catalog.AliasToDependency, key); ok {
		return depID
	}
	normalized := normalizeDependencyID(value)
	if _, ok := catalog.Dependencies[normalized]; ok {
		return normalized
	}
	return ""
}

func resolveImportDependency(catalog dependencyCatalog, moduleName string) string {
	return resolveDependencyReference(catalog, moduleName)
}

func ensureDependency(catalog *dependencyCatalog, depID string, declared bool, resolved bool, version string, revision string, source string) {
	depID = normalizeDependencyID(depID)
	if depID == "" {
		return
	}
	meta := catalog.Dependencies[depID]
	meta.Declared = meta.Declared || declared
	meta.Resolved = meta.Resolved || resolved
	if meta.Version == "" {
		meta.Version = strings.TrimSpace(version)
	}
	if meta.Revision == "" {
		meta.Revision = strings.TrimSpace(revision)
	}
	if meta.Source == "" {
		meta.Source = strings.TrimSpace(source)
	}
	catalog.Dependencies[depID] = meta
}

func mapAlias(catalog *dependencyCatalog, alias string, depID string) {
	setLookup(catalog.AliasToDependency, lookupKey(alias), normalizeDependencyID(depID))
}

func mapModule(catalog *dependencyCatalog, module string, depID string) {
	setLookup(catalog.ModuleToDependency, lookupKey(module), normalizeDependencyID(depID))
}
