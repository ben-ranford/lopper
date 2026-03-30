package shared

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

const gradleCatalogScopeKeySeparator = "\x00"

type GradleCatalogLibrary struct {
	Alias    string
	Catalog  string
	Group    string
	Artifact string
	Version  string
}

type GradleCatalogResolver struct {
	knownCatalogs map[string]struct{}
	scopes        []gradleCatalogScope
}

type gradleCatalogScope struct {
	root      string
	libraries map[string]GradleCatalogLibrary
	bundles   map[string][]GradleCatalogLibrary
}

type gradleCatalogSource struct {
	root string
	name string
	path string
}

type gradleCatalogSettingRef struct {
	Name string
	Path string
}

type gradleCatalogFile struct {
	libraries map[string]GradleCatalogLibrary
	bundles   map[string][]GradleCatalogLibrary
}

var (
	gradleCatalogCreateBlockPattern    = regexp.MustCompile(`(?ms)\bcreate\s*\(\s*["']([^"']+)["']\s*\)\s*\{(.*?)\}`)
	gradleCatalogQuotedFilePathPattern = regexp.MustCompile(`["']([^"']+\.toml)["']`)
	gradleCatalogSectionPattern        = regexp.MustCompile(`^\s*\[([^\]]+)\]\s*$`)
	gradleCatalogInlineFieldPattern    = regexp.MustCompile(`\b([A-Za-z0-9_.-]+)\s*=\s*("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')`)
	gradleCatalogNestedVersionRefRegex = regexp.MustCompile(`\bversion\s*=\s*\{\s*ref\s*=\s*("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')`)
	gradleCatalogPropertyPattern       = regexp.MustCompile(`(?ms)\b(?:implementation|api|compileOnly|runtimeOnly|kapt|ksp|testImplementation|androidTestImplementation|testRuntimeOnly)\s*\(?\s*(?:platform\s*\(\s*)?([A-Za-z_][A-Za-z0-9_]*(?:\s*\.\s*[A-Za-z_][A-Za-z0-9_]*)+)`)
	gradleCatalogBracketPattern        = regexp.MustCompile(`(?ms)\b(?:implementation|api|compileOnly|runtimeOnly|kapt|ksp|testImplementation|androidTestImplementation|testRuntimeOnly)\s*\(?\s*(?:platform\s*\(\s*)?([A-Za-z_][A-Za-z0-9_]*)\s*\[\s*["']([^"']+)["']\s*\]`)
	gradleCatalogFinderPattern         = regexp.MustCompile(`(?ms)\b(?:implementation|api|compileOnly|runtimeOnly|kapt|ksp|testImplementation|androidTestImplementation|testRuntimeOnly)\s*\(?\s*(?:platform\s*\(\s*)?([A-Za-z_][A-Za-z0-9_]*)\s*\.\s*(findLibrary|findBundle)\s*\(\s*["']([^"']+)["']\s*\)\s*\.get\s*\(\s*\)`)
	gradleCatalogAliasSeparatorPattern = regexp.MustCompile(`[-_.]+`)
	gradleCatalogCollapsedSpacePattern = regexp.MustCompile(`\s+`)
)

var gradleCatalogSkippedDirectories = map[string]bool{
	".gradle": true,
	"build":   true,
}

func LoadGradleCatalogResolver(repoPath string) (GradleCatalogResolver, []string) {
	resolver := GradleCatalogResolver{
		knownCatalogs: make(map[string]struct{}),
	}
	if strings.TrimSpace(repoPath) == "" {
		return resolver, nil
	}

	sources := make(map[string]gradleCatalogSource)
	warnings := make([]string, 0)
	register := func(root string, name string, path string) {
		normalizedName := normalizeGradleCatalogName(name)
		normalizedPath := filepath.Clean(path)
		if normalizedName == "" || normalizedPath == "" {
			return
		}
		resolver.knownCatalogs[normalizedName] = struct{}{}

		key := buildGradleCatalogScopeKey(filepath.Clean(root), normalizedName)
		if existing, ok := sources[key]; ok {
			if existing.path != normalizedPath {
				warnings = append(warnings, fmt.Sprintf("multiple Gradle version catalog sources configured for %s under %s; using %s", name, filepath.Clean(root), existing.path))
			}
			return
		}
		sources[key] = gradleCatalogSource{
			root: filepath.Clean(root),
			name: normalizedName,
			path: normalizedPath,
		}
	}

	walkErr := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if ShouldSkipDir(strings.ToLower(entry.Name()), gradleCatalogSkippedDirectories) || ShouldSkipCommonDir(strings.ToLower(entry.Name())) {
				return filepath.SkipDir
			}
			return nil
		}

		lowerName := strings.ToLower(entry.Name())
		switch lowerName {
		case "settings.gradle", "settings.gradle.kts":
			content, readErr := safeio.ReadFileUnder(repoPath, path)
			if readErr != nil {
				warnings = append(warnings, formatGradleCatalogReadWarning(repoPath, path, readErr))
				return nil
			}
			root := filepath.Dir(path)
			refs, parseWarnings := parseGradleSettingsCatalogRefs(string(content), relativeGradleCatalogPath(repoPath, path))
			warnings = append(warnings, parseWarnings...)
			for _, ref := range refs {
				resolver.knownCatalogs[normalizeGradleCatalogName(ref.Name)] = struct{}{}
				if strings.TrimSpace(ref.Path) == "" {
					continue
				}
				sourcePath := ref.Path
				if !filepath.IsAbs(sourcePath) {
					sourcePath = filepath.Join(root, filepath.FromSlash(ref.Path))
				}
				register(root, ref.Name, sourcePath)
			}
		case "libs.versions.toml":
			parent := strings.ToLower(filepath.Base(filepath.Dir(path)))
			if parent == "gradle" {
				root := filepath.Dir(filepath.Dir(path))
				register(root, "libs", path)
			}
		}
		return nil
	})
	if walkErr != nil {
		warnings = append(warnings, fmt.Sprintf("unable to scan Gradle version catalogs: %v", walkErr))
	}

	scopesByRoot := make(map[string]*gradleCatalogScope)
	for _, source := range sources {
		content, readErr := safeio.ReadFileUnder(repoPath, source.path)
		if readErr != nil {
			warnings = append(warnings, formatGradleCatalogReadWarning(repoPath, source.path, readErr))
			continue
		}
		parsed, parseWarnings := parseGradleCatalogFile(string(content), source.name, relativeGradleCatalogPath(repoPath, source.path))
		warnings = append(warnings, parseWarnings...)

		scope := scopesByRoot[source.root]
		if scope == nil {
			scope = &gradleCatalogScope{
				root:      source.root,
				libraries: make(map[string]GradleCatalogLibrary),
				bundles:   make(map[string][]GradleCatalogLibrary),
			}
			scopesByRoot[source.root] = scope
		}
		for accessor, library := range parsed.libraries {
			key := buildGradleCatalogScopeKey(source.name, accessor)
			if existing, ok := scope.libraries[key]; ok {
				if existing.Group != library.Group || existing.Artifact != library.Artifact || existing.Version != library.Version {
					warnings = append(warnings, fmt.Sprintf("Gradle version catalog alias %s.%s resolves to multiple coordinates under %s; keeping %s:%s", source.name, library.Alias, source.root, existing.Group, existing.Artifact))
				}
				continue
			}
			scope.libraries[key] = library
		}
		for accessor, bundle := range parsed.bundles {
			key := buildGradleCatalogScopeKey(source.name, accessor)
			if existing, ok := scope.bundles[key]; ok {
				if !slices.Equal(existing, bundle) {
					warnings = append(warnings, fmt.Sprintf("Gradle version catalog bundle %s.%s resolves to multiple dependency sets under %s; keeping the first definition", source.name, accessor, source.root))
				}
				continue
			}
			scope.bundles[key] = append([]GradleCatalogLibrary(nil), bundle...)
		}
	}

	resolver.scopes = make([]gradleCatalogScope, 0, len(scopesByRoot))
	for _, scope := range scopesByRoot {
		resolver.scopes = append(resolver.scopes, *scope)
	}
	sort.Slice(resolver.scopes, func(i, j int) bool {
		if len(resolver.scopes[i].root) == len(resolver.scopes[j].root) {
			return resolver.scopes[i].root < resolver.scopes[j].root
		}
		return len(resolver.scopes[i].root) > len(resolver.scopes[j].root)
	})

	return resolver, dedupeGradleCatalogWarnings(warnings)
}

func IsGradleVersionCatalogFile(name string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(name)), ".versions.toml")
}

func (r GradleCatalogResolver) ParseDependencyReferences(buildFilePath string, content string) ([]GradleCatalogLibrary, []string) {
	dependencies := make([]GradleCatalogLibrary, 0)
	warnings := make([]string, 0)
	seen := make(map[string]struct{})

	appendLibraries := func(libraries []GradleCatalogLibrary) {
		for _, library := range libraries {
			key := library.Group + ":" + library.Artifact
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			dependencies = append(dependencies, library)
		}
	}

	for _, match := range gradleCatalogFinderPattern.FindAllStringSubmatch(content, -1) {
		if len(match) != 4 {
			continue
		}
		catalogName := normalizeGradleCatalogName(match[1])
		method := strings.ToLower(strings.TrimSpace(match[2]))
		alias := normalizeGradleCatalogAccessor(match[3])
		if !r.shouldProcessCatalogReference(catalogName) {
			continue
		}
		switch method {
		case "findbundle":
			libraries, warning := r.resolveBundleReference(buildFilePath, catalogName, alias)
			appendLibraries(libraries)
			if warning != "" {
				warnings = append(warnings, warning)
			}
		default:
			library, warning := r.resolveLibraryReference(buildFilePath, catalogName, alias)
			if library.Group != "" && library.Artifact != "" {
				appendLibraries([]GradleCatalogLibrary{library})
			}
			if warning != "" {
				warnings = append(warnings, warning)
			}
		}
	}

	for _, match := range gradleCatalogBracketPattern.FindAllStringSubmatch(content, -1) {
		if len(match) != 3 {
			continue
		}
		catalogName := normalizeGradleCatalogName(match[1])
		alias := normalizeGradleCatalogAccessor(match[2])
		if !r.shouldProcessCatalogReference(catalogName) {
			continue
		}
		library, warning := r.resolveLibraryReference(buildFilePath, catalogName, alias)
		if library.Group != "" && library.Artifact != "" {
			appendLibraries([]GradleCatalogLibrary{library})
		}
		if warning != "" {
			warnings = append(warnings, warning)
		}
	}

	for _, match := range gradleCatalogPropertyPattern.FindAllStringSubmatch(content, -1) {
		if len(match) != 2 {
			continue
		}
		expression := normalizeGradleCatalogExpression(match[1])
		if expression == "" || strings.Contains(expression, ".findlibrary") || strings.Contains(expression, ".findbundle") {
			continue
		}
		segments := strings.Split(expression, ".")
		if len(segments) < 2 {
			continue
		}
		catalogName := normalizeGradleCatalogName(segments[0])
		if !r.shouldProcessCatalogReference(catalogName) {
			continue
		}
		if len(segments) >= 3 && segments[1] == "bundles" {
			alias := normalizeGradleCatalogAccessor(strings.Join(segments[2:], "."))
			libraries, warning := r.resolveBundleReference(buildFilePath, catalogName, alias)
			appendLibraries(libraries)
			if warning != "" {
				warnings = append(warnings, warning)
			}
			continue
		}
		if len(segments) >= 3 && (segments[1] == "versions" || segments[1] == "plugins") {
			warnings = append(warnings, fmt.Sprintf("unsupported Gradle version catalog reference %s in %s", expression, relativeGradleCatalogPathFromFile(buildFilePath)))
			continue
		}
		alias := normalizeGradleCatalogAccessor(strings.Join(segments[1:], "."))
		library, warning := r.resolveLibraryReference(buildFilePath, catalogName, alias)
		if library.Group != "" && library.Artifact != "" {
			appendLibraries([]GradleCatalogLibrary{library})
		}
		if warning != "" {
			warnings = append(warnings, warning)
		}
	}

	return dependencies, dedupeGradleCatalogWarnings(warnings)
}

func (r GradleCatalogResolver) shouldProcessCatalogReference(name string) bool {
	if name == "libs" {
		return true
	}
	_, ok := r.knownCatalogs[name]
	return ok
}

func (r GradleCatalogResolver) resolveLibraryReference(buildFilePath string, catalogName string, alias string) (GradleCatalogLibrary, string) {
	if alias == "" {
		return GradleCatalogLibrary{}, ""
	}
	scope := r.scopeForBuildFile(buildFilePath)
	if scope == nil {
		return GradleCatalogLibrary{}, fmt.Sprintf("unable to resolve Gradle version catalog alias %s.%s in %s", catalogName, alias, relativeGradleCatalogPathFromFile(buildFilePath))
	}
	key := buildGradleCatalogScopeKey(catalogName, alias)
	library, ok := scope.libraries[key]
	if !ok {
		return GradleCatalogLibrary{}, fmt.Sprintf("unable to resolve Gradle version catalog alias %s.%s in %s", catalogName, alias, relativeGradleCatalogPathFromFile(buildFilePath))
	}
	return library, ""
}

func (r GradleCatalogResolver) resolveBundleReference(buildFilePath string, catalogName string, alias string) ([]GradleCatalogLibrary, string) {
	if alias == "" {
		return nil, ""
	}
	scope := r.scopeForBuildFile(buildFilePath)
	if scope == nil {
		return nil, fmt.Sprintf("unable to resolve Gradle version catalog bundle %s.bundles.%s in %s", catalogName, alias, relativeGradleCatalogPathFromFile(buildFilePath))
	}
	key := buildGradleCatalogScopeKey(catalogName, alias)
	bundle, ok := scope.bundles[key]
	if !ok {
		return nil, fmt.Sprintf("unable to resolve Gradle version catalog bundle %s.bundles.%s in %s", catalogName, alias, relativeGradleCatalogPathFromFile(buildFilePath))
	}
	return append([]GradleCatalogLibrary(nil), bundle...), ""
}

func (r GradleCatalogResolver) scopeForBuildFile(buildFilePath string) *gradleCatalogScope {
	cleanPath := filepath.Clean(buildFilePath)
	for index := range r.scopes {
		scope := &r.scopes[index]
		if isGradleCatalogSubPath(scope.root, cleanPath) {
			return scope
		}
	}
	return nil
}

func parseGradleSettingsCatalogRefs(content string, relativePath string) ([]gradleCatalogSettingRef, []string) {
	refs := make([]gradleCatalogSettingRef, 0)
	warnings := make([]string, 0)
	for _, match := range gradleCatalogCreateBlockPattern.FindAllStringSubmatch(content, -1) {
		if len(match) != 3 {
			continue
		}
		name := strings.TrimSpace(match[1])
		if name == "" {
			continue
		}
		ref := gradleCatalogSettingRef{Name: name}
		fileMatches := gradleCatalogQuotedFilePathPattern.FindAllStringSubmatch(match[2], -1)
		if len(fileMatches) == 0 {
			warnings = append(warnings, fmt.Sprintf("unsupported Gradle version catalog source for %s in %s", name, relativePath))
			refs = append(refs, ref)
			continue
		}
		if len(fileMatches) > 1 {
			warnings = append(warnings, fmt.Sprintf("multiple Gradle version catalog files declared for %s in %s; using %s", name, relativePath, fileMatches[0][1]))
		}
		ref.Path = fileMatches[0][1]
		refs = append(refs, ref)
	}
	return refs, dedupeGradleCatalogWarnings(warnings)
}

func parseGradleCatalogFile(content string, catalogName string, relativePath string) (gradleCatalogFile, []string) {
	versions := make(map[string]string)
	libraries := make(map[string]GradleCatalogLibrary)
	bundleSpecs := make(map[string][]string)
	warnings := make([]string, 0)

	section := ""
	pendingSection := ""
	pendingKey := ""
	pendingValue := strings.Builder{}
	consumeAssignment := func(targetSection string, key string, value string) {
		switch targetSection {
		case "versions":
			version, ok := parseGradleCatalogStringValue(value)
			if ok && version != "" {
				versions[strings.TrimSpace(key)] = version
				versions[strings.ToLower(strings.TrimSpace(key))] = version
			}
		case "libraries":
			library, libraryWarnings := parseGradleCatalogLibraryEntry(catalogName, key, value, versions, relativePath)
			warnings = append(warnings, libraryWarnings...)
			if library.Group == "" || library.Artifact == "" {
				return
			}
			libraries[normalizeGradleCatalogAccessor(key)] = library
		case "bundles":
			members := parseGradleCatalogBundleMembers(value)
			if len(members) == 0 {
				warnings = append(warnings, fmt.Sprintf("unsupported Gradle version catalog bundle %q in %s", key, relativePath))
				return
			}
			normalizedMembers := make([]string, 0, len(members))
			for _, member := range members {
				normalizedMembers = append(normalizedMembers, normalizeGradleCatalogAccessor(member))
			}
			bundleSpecs[normalizeGradleCatalogAccessor(key)] = normalizedMembers
		}
	}

	lines := strings.Split(content, "\n")
	for _, line := range lines {
		clean := strings.TrimSpace(stripGradleCatalogComment(line))
		if clean == "" {
			continue
		}

		if pendingKey != "" {
			pendingValue.WriteByte('\n')
			pendingValue.WriteString(clean)
			if gradleCatalogValueBalanced(pendingValue.String()) {
				consumeAssignment(pendingSection, pendingKey, pendingValue.String())
				pendingKey = ""
				pendingSection = ""
				pendingValue.Reset()
			}
			continue
		}

		if nextSection, ok := parseGradleCatalogSection(clean); ok {
			section = nextSection
			continue
		}

		key, value, ok := parseGradleCatalogAssignment(clean)
		if !ok {
			continue
		}
		if !gradleCatalogValueBalanced(value) {
			pendingKey = key
			pendingSection = section
			pendingValue.Reset()
			pendingValue.WriteString(value)
			continue
		}
		consumeAssignment(section, key, value)
	}

	if pendingKey != "" {
		warnings = append(warnings, fmt.Sprintf("unterminated Gradle version catalog entry %q in %s", pendingKey, relativePath))
	}

	bundles := make(map[string][]GradleCatalogLibrary)
	for alias, members := range bundleSpecs {
		resolved := make([]GradleCatalogLibrary, 0, len(members))
		seen := make(map[string]struct{})
		for _, member := range members {
			library, ok := libraries[member]
			if !ok {
				warnings = append(warnings, fmt.Sprintf("unable to resolve Gradle version catalog bundle member %q in %s", member, relativePath))
				continue
			}
			key := library.Group + ":" + library.Artifact
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			resolved = append(resolved, library)
		}
		if len(resolved) > 0 {
			bundles[alias] = resolved
		}
	}

	return gradleCatalogFile{
		libraries: libraries,
		bundles:   bundles,
	}, dedupeGradleCatalogWarnings(warnings)
}

func parseGradleCatalogLibraryEntry(catalogName string, alias string, value string, versions map[string]string, relativePath string) (GradleCatalogLibrary, []string) {
	library := GradleCatalogLibrary{
		Alias:   normalizeGradleCatalogAccessor(alias),
		Catalog: normalizeGradleCatalogName(catalogName),
	}

	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return library, nil
	}

	if coords, ok := parseGradleCatalogStringValue(trimmed); ok {
		group, artifact, version, parsed := parseGradleCatalogCoordinates(coords)
		if !parsed {
			return GradleCatalogLibrary{}, []string{fmt.Sprintf("unsupported Gradle version catalog library %q in %s", alias, relativePath)}
		}
		library.Group = group
		library.Artifact = artifact
		library.Version = version
		return library, nil
	}

	if !strings.HasPrefix(trimmed, "{") {
		return GradleCatalogLibrary{}, []string{fmt.Sprintf("unsupported Gradle version catalog library %q in %s", alias, relativePath)}
	}

	fields := parseGradleCatalogInlineFields(trimmed)
	if module := fields["module"]; module != "" {
		group, artifact, _, ok := parseGradleCatalogCoordinates(module)
		if !ok {
			return GradleCatalogLibrary{}, []string{fmt.Sprintf("unsupported Gradle version catalog module %q in %s", alias, relativePath)}
		}
		library.Group = group
		library.Artifact = artifact
	} else {
		library.Group = fields["group"]
		library.Artifact = fields["name"]
	}
	library.Version = fields["version"]
	if library.Version == "" {
		versionRef := fields["version.ref"]
		if versionRef == "" {
			versionRef = parseGradleCatalogNestedVersionRef(trimmed)
		}
		if versionRef != "" {
			library.Version = versions[versionRef]
			if library.Version == "" {
				library.Version = versions[strings.ToLower(versionRef)]
			}
		}
	}
	if library.Group == "" || library.Artifact == "" {
		return GradleCatalogLibrary{}, []string{fmt.Sprintf("unsupported Gradle version catalog library %q in %s", alias, relativePath)}
	}
	return library, nil
}

func parseGradleCatalogCoordinates(value string) (string, string, string, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 && len(parts) != 3 {
		return "", "", "", false
	}
	group := strings.TrimSpace(parts[0])
	artifact := strings.TrimSpace(parts[1])
	version := ""
	if len(parts) == 3 {
		version = strings.TrimSpace(parts[2])
	}
	if group == "" || artifact == "" {
		return "", "", "", false
	}
	return group, artifact, version, true
}

func parseGradleCatalogInlineFields(value string) map[string]string {
	fields := make(map[string]string)
	for _, match := range gradleCatalogInlineFieldPattern.FindAllStringSubmatch(value, -1) {
		if len(match) != 3 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(match[1]))
		fields[key] = trimGradleCatalogQuotes(strings.TrimSpace(match[2]))
	}
	return fields
}

func parseGradleCatalogNestedVersionRef(value string) string {
	match := gradleCatalogNestedVersionRefRegex.FindStringSubmatch(value)
	if len(match) != 2 {
		return ""
	}
	return trimGradleCatalogQuotes(strings.TrimSpace(match[1]))
}

func parseGradleCatalogBundleMembers(value string) []string {
	return extractGradleCatalogQuotedStrings(value)
}

func parseGradleCatalogStringValue(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if len(value) < 2 {
		return "", false
	}
	if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
		return trimGradleCatalogQuotes(value), true
	}
	return "", false
}

func parseGradleCatalogSection(line string) (string, bool) {
	match := gradleCatalogSectionPattern.FindStringSubmatch(line)
	if len(match) != 2 {
		return "", false
	}
	return strings.ToLower(strings.TrimSpace(match[1])), true
}

func parseGradleCatalogAssignment(line string) (string, string, bool) {
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	key := strings.Trim(strings.TrimSpace(parts[0]), `"'`)
	value := strings.TrimSpace(parts[1])
	if key == "" || value == "" {
		return "", "", false
	}
	return key, value, true
}

func stripGradleCatalogComment(line string) string {
	inDouble := false
	inSingle := false
	for index, r := range line {
		switch r {
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '#':
			if !inDouble && !inSingle {
				return line[:index]
			}
		}
	}
	return line
}

func extractGradleCatalogQuotedStrings(value string) []string {
	values := make([]string, 0)
	current := strings.Builder{}
	inString := false
	quote := byte(0)
	for index := 0; index < len(value); index++ {
		ch := value[index]
		if !inString {
			if ch == '"' || ch == '\'' {
				inString = true
				quote = ch
				current.Reset()
			}
			continue
		}
		if ch == quote {
			inString = false
			values = append(values, current.String())
			continue
		}
		current.WriteByte(ch)
	}
	return values
}

func gradleCatalogValueBalanced(value string) bool {
	braceDepth := 0
	bracketDepth := 0
	inDouble := false
	inSingle := false
	for _, r := range value {
		switch r {
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '{':
			if !inDouble && !inSingle {
				braceDepth++
			}
		case '}':
			if !inDouble && !inSingle {
				braceDepth--
			}
		case '[':
			if !inDouble && !inSingle {
				bracketDepth++
			}
		case ']':
			if !inDouble && !inSingle {
				bracketDepth--
			}
		}
	}
	return braceDepth <= 0 && bracketDepth <= 0
}

func normalizeGradleCatalogName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeGradleCatalogAccessor(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.Trim(value, ".")
	value = gradleCatalogCollapsedSpacePattern.ReplaceAllString(value, "")
	value = gradleCatalogAliasSeparatorPattern.ReplaceAllString(value, ".")
	value = strings.Trim(value, ".")
	return strings.ToLower(value)
}

func normalizeGradleCatalogExpression(value string) string {
	value = gradleCatalogCollapsedSpacePattern.ReplaceAllString(strings.TrimSpace(value), "")
	return normalizeGradleCatalogAccessor(value)
}

func buildGradleCatalogScopeKey(left string, right string) string {
	return strings.TrimSpace(left) + gradleCatalogScopeKeySeparator + strings.TrimSpace(right)
}

func isGradleCatalogSubPath(root string, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if root == path {
		return true
	}
	rootWithSeparator := root + string(filepath.Separator)
	return strings.HasPrefix(path, rootWithSeparator)
}

func relativeGradleCatalogPath(repoPath string, path string) string {
	if rel, err := filepath.Rel(repoPath, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}

func relativeGradleCatalogPathFromFile(path string) string {
	return filepath.ToSlash(filepath.Clean(path))
}

func formatGradleCatalogReadWarning(repoPath string, path string, err error) string {
	return fmt.Sprintf("unable to read %s: %v", relativeGradleCatalogPath(repoPath, path), err)
}

func trimGradleCatalogQuotes(value string) string {
	return strings.Trim(strings.TrimSpace(value), `"'`)
}

func dedupeGradleCatalogWarnings(warnings []string) []string {
	if len(warnings) == 0 {
		return nil
	}
	unique := make(map[string]struct{})
	items := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		warning = strings.TrimSpace(warning)
		if warning == "" {
			continue
		}
		if _, ok := unique[warning]; ok {
			continue
		}
		unique[warning] = struct{}{}
		items = append(items, warning)
	}
	sort.Strings(items)
	return items
}
