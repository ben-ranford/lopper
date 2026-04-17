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

const (
	defaultGradleCatalogFileName            = "libs.versions.toml"
	gradleCatalogReadWarningFormat          = "unable to read %s: %v"
	unsupportedGradleCatalogLibraryFormat   = "unsupported Gradle version catalog library %q in %s"
	unsupportedGradleCatalogModuleFormat    = "unsupported Gradle version catalog module %q in %s"
	unsupportedGradleCatalogBundleFormat    = "unsupported Gradle version catalog bundle %q in %s"
	unresolvedGradleCatalogAliasFormat      = "unable to resolve Gradle version catalog alias %s.%s in %s"
	unresolvedGradleCatalogBundleFormat     = "unable to resolve Gradle version catalog bundle %s.bundles.%s in %s"
	unsupportedGradleCatalogReferenceFormat = "unsupported Gradle version catalog reference %s in %s"
)

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
	lookup        gradleCatalogLookupIndex
}

type gradleCatalogScope struct {
	root      string
	libraries map[string]GradleCatalogLibrary
	bundles   map[string][]GradleCatalogLibrary
}

type gradleCatalogLookupIndex struct {
	scopesByRoot    map[string]*gradleCatalogScope
	cachedScopes    map[string]*gradleCatalogScope
	cachedUnmatched map[string]struct{}
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

type gradleCatalogRegistry struct {
	repoPath      string
	knownCatalogs map[string]struct{}
	sources       map[string]gradleCatalogSource
	warnings      []string
	scopesByRoot  map[string]*gradleCatalogScope
}

type gradleCatalogReferenceCollector struct {
	resolver      *GradleCatalogResolver
	buildFilePath string
	dependencies  []GradleCatalogLibrary
	warnings      []string
	seen          map[string]struct{}
}

type gradleCatalogFileParser struct {
	catalogName    string
	relativePath   string
	versions       map[string]string
	libraries      map[string]GradleCatalogLibrary
	bundleSpecs    map[string][]string
	warnings       []string
	section        string
	pendingSection string
	pendingKey     string
	pendingValue   strings.Builder
}

type gradleCatalogDelimiterState struct {
	braceDepth   int
	bracketDepth int
	inDouble     bool
	inSingle     bool
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
	if strings.TrimSpace(repoPath) == "" {
		return GradleCatalogResolver{knownCatalogs: make(map[string]struct{})}, nil
	}
	registry := newGradleCatalogRegistry(repoPath)
	registry.collectSources()
	return registry.buildResolver(), dedupeGradleCatalogWarnings(registry.warnings)
}

func newGradleCatalogRegistry(repoPath string) *gradleCatalogRegistry {
	return &gradleCatalogRegistry{
		repoPath:      repoPath,
		knownCatalogs: make(map[string]struct{}),
		sources:       make(map[string]gradleCatalogSource),
		scopesByRoot:  make(map[string]*gradleCatalogScope),
	}
}

func (r *gradleCatalogRegistry) collectSources() {
	walkErr := filepath.WalkDir(r.repoPath, r.visit)
	if walkErr != nil {
		r.warnings = append(r.warnings, fmt.Sprintf("unable to scan Gradle version catalogs: %v", walkErr))
	}
}

func (r *gradleCatalogRegistry) visit(path string, entry fs.DirEntry, err error) error {
	if err != nil {
		return err
	}
	if entry.IsDir() {
		return maybeSkipGradleCatalogDirectory(entry)
	}
	switch strings.ToLower(entry.Name()) {
	case "settings.gradle", "settings.gradle.kts":
		r.loadSettingsFile(path)
	case defaultGradleCatalogFileName:
		r.registerDefaultCatalog(path)
	}
	return nil
}

func maybeSkipGradleCatalogDirectory(entry fs.DirEntry) error {
	name := strings.ToLower(entry.Name())
	if ShouldSkipDir(name, gradleCatalogSkippedDirectories) || ShouldSkipCommonDir(name) {
		return filepath.SkipDir
	}
	return nil
}

func (r *gradleCatalogRegistry) loadSettingsFile(path string) {
	content, readErr := safeio.ReadFileUnder(r.repoPath, path)
	if readErr != nil {
		r.warnings = append(r.warnings, formatGradleCatalogReadWarning(r.repoPath, path, readErr))
		return
	}
	root := filepath.Dir(path)
	refs, parseWarnings := parseGradleSettingsCatalogRefs(string(content), relativeGradleCatalogPath(r.repoPath, path))
	r.warnings = append(r.warnings, parseWarnings...)
	for _, ref := range refs {
		r.trackKnownCatalog(ref.Name)
		if strings.TrimSpace(ref.Path) == "" {
			continue
		}
		r.registerSource(root, ref.Name, resolveGradleCatalogSourcePath(root, ref.Path))
	}
}

func (r *gradleCatalogRegistry) registerDefaultCatalog(path string) {
	if strings.ToLower(filepath.Base(filepath.Dir(path))) != "gradle" {
		return
	}
	r.registerSource(filepath.Dir(filepath.Dir(path)), "libs", path)
}

func resolveGradleCatalogSourcePath(root, sourcePath string) string {
	if filepath.IsAbs(sourcePath) {
		return filepath.Clean(sourcePath)
	}
	return filepath.Join(root, filepath.FromSlash(sourcePath))
}

func (r *gradleCatalogRegistry) registerSource(root, name, path string) {
	normalizedName := normalizeGradleCatalogName(name)
	normalizedPath := filepath.Clean(path)
	if normalizedName == "" || normalizedPath == "" {
		return
	}
	r.trackKnownCatalog(normalizedName)
	root = filepath.Clean(root)
	key := buildGradleCatalogScopeKey(root, normalizedName)
	if existing, ok := r.sources[key]; ok {
		if existing.path != normalizedPath {
			r.warnings = append(r.warnings, fmt.Sprintf("multiple Gradle version catalog sources configured for %s under %s; using %s", name, root, existing.path))
		}
		return
	}
	r.sources[key] = gradleCatalogSource{root: root, name: normalizedName, path: normalizedPath}
}

func (r *gradleCatalogRegistry) trackKnownCatalog(name string) {
	normalized := normalizeGradleCatalogName(name)
	if normalized == "" {
		return
	}
	r.knownCatalogs[normalized] = struct{}{}
}

func (r *gradleCatalogRegistry) buildResolver() GradleCatalogResolver {
	r.parseSources()
	scopes := r.sortedScopes()
	resolver := GradleCatalogResolver{
		knownCatalogs: r.knownCatalogs,
		scopes:        scopes,
	}
	resolver.lookup = newGradleCatalogLookupIndex(resolver.scopes)
	return resolver
}

func (r *gradleCatalogRegistry) parseSources() {
	for _, source := range r.sources {
		r.loadSource(source)
	}
}

func (r *gradleCatalogRegistry) sortedScopes() []gradleCatalogScope {
	scopes := make([]gradleCatalogScope, 0, len(r.scopesByRoot))
	for _, scope := range r.scopesByRoot {
		scopes = append(scopes, *scope)
	}
	sort.Slice(scopes, func(i, j int) bool {
		if len(scopes[i].root) == len(scopes[j].root) {
			return scopes[i].root < scopes[j].root
		}
		return len(scopes[i].root) > len(scopes[j].root)
	})
	return scopes
}

func (r *gradleCatalogRegistry) loadSource(source gradleCatalogSource) {
	content, readErr := safeio.ReadFileUnder(r.repoPath, source.path)
	if readErr != nil {
		r.warnings = append(r.warnings, formatGradleCatalogReadWarning(r.repoPath, source.path, readErr))
		return
	}
	parsed, parseWarnings := parseGradleCatalogFile(string(content), source.name, relativeGradleCatalogPath(r.repoPath, source.path))
	r.warnings = append(r.warnings, parseWarnings...)
	scope := r.ensureScope(source.root)
	r.mergeLibraries(scope, source, parsed.libraries)
	r.mergeBundles(scope, source, parsed.bundles)
}

func (r *gradleCatalogRegistry) ensureScope(root string) *gradleCatalogScope {
	scope := r.scopesByRoot[root]
	if scope != nil {
		return scope
	}
	scope = &gradleCatalogScope{
		root:      root,
		libraries: make(map[string]GradleCatalogLibrary),
		bundles:   make(map[string][]GradleCatalogLibrary),
	}
	r.scopesByRoot[root] = scope
	return scope
}

func (r *gradleCatalogRegistry) mergeLibraries(scope *gradleCatalogScope, source gradleCatalogSource, libraries map[string]GradleCatalogLibrary) {
	for accessor, library := range libraries {
		key := buildGradleCatalogScopeKey(source.name, accessor)
		if existing, ok := scope.libraries[key]; ok {
			if existing.Group != library.Group || existing.Artifact != library.Artifact || existing.Version != library.Version {
				r.warnings = append(r.warnings, fmt.Sprintf("Gradle version catalog alias %s.%s resolves to multiple coordinates under %s; keeping %s:%s", source.name, library.Alias, source.root, existing.Group, existing.Artifact))
			}
			continue
		}
		scope.libraries[key] = library
	}
}

func (r *gradleCatalogRegistry) mergeBundles(scope *gradleCatalogScope, source gradleCatalogSource, bundles map[string][]GradleCatalogLibrary) {
	for accessor, bundle := range bundles {
		key := buildGradleCatalogScopeKey(source.name, accessor)
		if existing, ok := scope.bundles[key]; ok {
			if !slices.Equal(existing, bundle) {
				r.warnings = append(r.warnings, fmt.Sprintf("Gradle version catalog bundle %s.%s resolves to multiple dependency sets under %s; keeping the first definition", source.name, accessor, source.root))
			}
			continue
		}
		scope.bundles[key] = append([]GradleCatalogLibrary(nil), bundle...)
	}
}

func IsGradleVersionCatalogFile(name string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(name)), ".versions.toml")
}

func (r *GradleCatalogResolver) ParseDependencyReferences(buildFilePath, content string) ([]GradleCatalogLibrary, []string) {
	if r == nil {
		return nil, nil
	}
	collector := newGradleCatalogReferenceCollector(r, buildFilePath)
	collector.collectFinderMatches(content)
	collector.collectBracketMatches(content)
	collector.collectPropertyMatches(content)
	return collector.dependencies, dedupeGradleCatalogWarnings(collector.warnings)
}

func newGradleCatalogReferenceCollector(resolver *GradleCatalogResolver, buildFilePath string) *gradleCatalogReferenceCollector {
	return &gradleCatalogReferenceCollector{
		resolver:      resolver,
		buildFilePath: buildFilePath,
		seen:          make(map[string]struct{}),
	}
}

func (c *gradleCatalogReferenceCollector) collectFinderMatches(content string) {
	for _, match := range gradleCatalogFinderPattern.FindAllStringSubmatch(content, -1) {
		if len(match) != 4 {
			continue
		}
		catalogName := normalizeGradleCatalogName(match[1])
		if !c.resolver.shouldProcessCatalogReference(catalogName) {
			continue
		}
		alias := normalizeGradleCatalogAccessor(match[3])
		if strings.EqualFold(strings.TrimSpace(match[2]), "findBundle") {
			c.addBundleReference(catalogName, alias)
			continue
		}
		c.addLibraryReference(catalogName, alias)
	}
}

func (c *gradleCatalogReferenceCollector) collectBracketMatches(content string) {
	for _, match := range gradleCatalogBracketPattern.FindAllStringSubmatch(content, -1) {
		if len(match) != 3 {
			continue
		}
		catalogName := normalizeGradleCatalogName(match[1])
		if !c.resolver.shouldProcessCatalogReference(catalogName) {
			continue
		}
		c.addLibraryReference(catalogName, normalizeGradleCatalogAccessor(match[2]))
	}
}

func (c *gradleCatalogReferenceCollector) collectPropertyMatches(content string) {
	for _, match := range gradleCatalogPropertyPattern.FindAllStringSubmatch(content, -1) {
		if len(match) != 2 {
			continue
		}
		c.handlePropertyExpression(normalizeGradleCatalogExpression(match[1]))
	}
}

func (c *gradleCatalogReferenceCollector) handlePropertyExpression(expression string) {
	if expression == "" || strings.Contains(expression, ".findlibrary") || strings.Contains(expression, ".findbundle") {
		return
	}
	segments := strings.Split(expression, ".")
	if len(segments) < 2 {
		return
	}
	catalogName := normalizeGradleCatalogName(segments[0])
	if !c.resolver.shouldProcessCatalogReference(catalogName) {
		return
	}
	if len(segments) >= 3 && segments[1] == "bundles" {
		c.addBundleReference(catalogName, normalizeGradleCatalogAccessor(strings.Join(segments[2:], ".")))
		return
	}
	if len(segments) >= 3 && (segments[1] == "versions" || segments[1] == "plugins") {
		c.warnings = append(c.warnings, fmt.Sprintf(unsupportedGradleCatalogReferenceFormat, expression, relativeGradleCatalogPathFromFile(c.buildFilePath)))
		return
	}
	c.addLibraryReference(catalogName, normalizeGradleCatalogAccessor(strings.Join(segments[1:], ".")))
}

func (c *gradleCatalogReferenceCollector) addLibraryReference(catalogName, alias string) {
	library, warning := c.resolver.resolveLibraryReference(c.buildFilePath, catalogName, alias)
	c.appendLibrary(library)
	c.appendWarning(warning)
}

func (c *gradleCatalogReferenceCollector) addBundleReference(catalogName, alias string) {
	libraries, warning := c.resolver.resolveBundleReference(c.buildFilePath, catalogName, alias)
	c.appendLibraries(libraries)
	c.appendWarning(warning)
}

func (c *gradleCatalogReferenceCollector) appendLibraries(libraries []GradleCatalogLibrary) {
	for _, library := range libraries {
		c.appendLibrary(library)
	}
}

func (c *gradleCatalogReferenceCollector) appendLibrary(library GradleCatalogLibrary) {
	if library.Group == "" || library.Artifact == "" {
		return
	}
	key := library.Group + ":" + library.Artifact
	if _, ok := c.seen[key]; ok {
		return
	}
	c.seen[key] = struct{}{}
	c.dependencies = append(c.dependencies, library)
}

func (c *gradleCatalogReferenceCollector) appendWarning(warning string) {
	if strings.TrimSpace(warning) == "" {
		return
	}
	c.warnings = append(c.warnings, warning)
}

func (r *GradleCatalogResolver) shouldProcessCatalogReference(name string) bool {
	if name == "libs" {
		return true
	}
	_, ok := r.knownCatalogs[name]
	return ok
}

func (r *GradleCatalogResolver) resolveLibraryReference(buildFilePath, catalogName, alias string) (GradleCatalogLibrary, string) {
	if alias == "" {
		return GradleCatalogLibrary{}, ""
	}
	scope := r.scopeForBuildFile(buildFilePath)
	if scope == nil {
		return GradleCatalogLibrary{}, fmt.Sprintf(unresolvedGradleCatalogAliasFormat, catalogName, alias, relativeGradleCatalogPathFromFile(buildFilePath))
	}
	key := buildGradleCatalogScopeKey(catalogName, alias)
	library, ok := scope.libraries[key]
	if !ok {
		return GradleCatalogLibrary{}, fmt.Sprintf(unresolvedGradleCatalogAliasFormat, catalogName, alias, relativeGradleCatalogPathFromFile(buildFilePath))
	}
	return library, ""
}

func (r *GradleCatalogResolver) resolveBundleReference(buildFilePath, catalogName, alias string) ([]GradleCatalogLibrary, string) {
	if alias == "" {
		return nil, ""
	}
	scope := r.scopeForBuildFile(buildFilePath)
	if scope == nil {
		return nil, fmt.Sprintf(unresolvedGradleCatalogBundleFormat, catalogName, alias, relativeGradleCatalogPathFromFile(buildFilePath))
	}
	key := buildGradleCatalogScopeKey(catalogName, alias)
	bundle, ok := scope.bundles[key]
	if !ok {
		return nil, fmt.Sprintf(unresolvedGradleCatalogBundleFormat, catalogName, alias, relativeGradleCatalogPathFromFile(buildFilePath))
	}
	return append([]GradleCatalogLibrary(nil), bundle...), ""
}

func (r *GradleCatalogResolver) scopeForBuildFile(buildFilePath string) *gradleCatalogScope {
	r.ensureLookupIndex()
	return r.lookup.resolve(buildFilePath)
}

func (r *GradleCatalogResolver) ensureLookupIndex() {
	if r.lookup.scopesByRoot != nil {
		return
	}
	r.lookup = newGradleCatalogLookupIndex(r.scopes)
}

func newGradleCatalogLookupIndex(scopes []gradleCatalogScope) gradleCatalogLookupIndex {
	index := gradleCatalogLookupIndex{
		scopesByRoot:    make(map[string]*gradleCatalogScope, len(scopes)),
		cachedScopes:    make(map[string]*gradleCatalogScope),
		cachedUnmatched: make(map[string]struct{}),
	}
	for i := range scopes {
		scope := &scopes[i]
		index.scopesByRoot[filepath.Clean(scope.root)] = scope
	}
	return index
}

func (i *gradleCatalogLookupIndex) resolve(buildFilePath string) *gradleCatalogScope {
	if i == nil || len(i.scopesByRoot) == 0 {
		return nil
	}
	cleanPath := filepath.Clean(buildFilePath)
	if scope, ok := i.cachedScopes[cleanPath]; ok {
		return scope
	}
	if _, ok := i.cachedUnmatched[cleanPath]; ok {
		return nil
	}

	candidate := cleanPath
	for {
		if scope, ok := i.scopesByRoot[candidate]; ok {
			i.cachedScopes[cleanPath] = scope
			return scope
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			break
		}
		candidate = parent
	}
	i.cachedUnmatched[cleanPath] = struct{}{}
	return nil
}

func parseGradleSettingsCatalogRefs(content, relativePath string) ([]gradleCatalogSettingRef, []string) {
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

func parseGradleCatalogFile(content, catalogName, relativePath string) (gradleCatalogFile, []string) {
	parser := newGradleCatalogFileParser(catalogName, relativePath)
	parser.parse(content)
	return parser.finalize()
}

func newGradleCatalogFileParser(catalogName, relativePath string) *gradleCatalogFileParser {
	return &gradleCatalogFileParser{
		catalogName:  catalogName,
		relativePath: relativePath,
		versions:     make(map[string]string),
		libraries:    make(map[string]GradleCatalogLibrary),
		bundleSpecs:  make(map[string][]string),
	}
}

func (p *gradleCatalogFileParser) parse(content string) {
	for _, line := range strings.Split(content, "\n") {
		p.consumeLine(line)
	}
	if p.pendingKey != "" {
		p.warnings = append(p.warnings, fmt.Sprintf("unterminated Gradle version catalog entry %q in %s", p.pendingKey, p.relativePath))
	}
}

func (p *gradleCatalogFileParser) consumeLine(line string) {
	clean := strings.TrimSpace(stripGradleCatalogComment(line))
	if clean == "" {
		return
	}
	if p.appendPending(clean) {
		return
	}
	if nextSection, ok := parseGradleCatalogSection(clean); ok {
		p.section = nextSection
		return
	}
	key, value, ok := parseGradleCatalogAssignment(clean)
	if !ok {
		return
	}
	if gradleCatalogValueBalanced(value) {
		p.consumeAssignment(p.section, key, value)
		return
	}
	p.startPendingAssignment(key, value)
}

func (p *gradleCatalogFileParser) appendPending(clean string) bool {
	if p.pendingKey == "" {
		return false
	}
	p.pendingValue.WriteByte('\n')
	p.pendingValue.WriteString(clean)
	if !gradleCatalogValueBalanced(p.pendingValue.String()) {
		return true
	}
	p.consumeAssignment(p.pendingSection, p.pendingKey, p.pendingValue.String())
	p.clearPendingAssignment()
	return true
}

func (p *gradleCatalogFileParser) startPendingAssignment(key, value string) {
	p.pendingKey = key
	p.pendingSection = p.section
	p.pendingValue.Reset()
	p.pendingValue.WriteString(value)
}

func (p *gradleCatalogFileParser) clearPendingAssignment() {
	p.pendingKey = ""
	p.pendingSection = ""
	p.pendingValue.Reset()
}

func (p *gradleCatalogFileParser) consumeAssignment(section, key, value string) {
	switch section {
	case "versions":
		p.consumeVersionEntry(key, value)
	case "libraries":
		p.consumeLibraryEntry(key, value)
	case "bundles":
		p.consumeBundleEntry(key, value)
	}
}

func (p *gradleCatalogFileParser) consumeVersionEntry(key, value string) {
	version, ok := parseGradleCatalogStringValue(value)
	if !ok || version == "" {
		return
	}
	trimmedKey := strings.TrimSpace(key)
	p.versions[trimmedKey] = version
	p.versions[strings.ToLower(trimmedKey)] = version
}

func (p *gradleCatalogFileParser) consumeLibraryEntry(key, value string) {
	library, libraryWarnings := parseGradleCatalogLibraryEntry(p.catalogName, key, value, p.versions, p.relativePath)
	p.warnings = append(p.warnings, libraryWarnings...)
	if library.Group == "" || library.Artifact == "" {
		return
	}
	p.libraries[normalizeGradleCatalogAccessor(key)] = library
}

func (p *gradleCatalogFileParser) consumeBundleEntry(key, value string) {
	members := parseGradleCatalogBundleMembers(value)
	if len(members) == 0 {
		p.warnings = append(p.warnings, fmt.Sprintf(unsupportedGradleCatalogBundleFormat, key, p.relativePath))
		return
	}
	normalizedMembers := make([]string, 0, len(members))
	for _, member := range members {
		normalizedMembers = append(normalizedMembers, normalizeGradleCatalogAccessor(member))
	}
	p.bundleSpecs[normalizeGradleCatalogAccessor(key)] = normalizedMembers
}

func (p *gradleCatalogFileParser) finalize() (gradleCatalogFile, []string) {
	return gradleCatalogFile{
		libraries: p.libraries,
		bundles:   p.resolveBundles(),
	}, dedupeGradleCatalogWarnings(p.warnings)
}

func (p *gradleCatalogFileParser) resolveBundles() map[string][]GradleCatalogLibrary {
	bundles := make(map[string][]GradleCatalogLibrary)
	for alias, members := range p.bundleSpecs {
		resolved := make([]GradleCatalogLibrary, 0, len(members))
		seen := make(map[string]struct{})
		for _, member := range members {
			library, ok := p.libraries[member]
			if !ok {
				p.warnings = append(p.warnings, fmt.Sprintf("unable to resolve Gradle version catalog bundle member %q in %s", member, p.relativePath))
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
	return bundles
}

func parseGradleCatalogLibraryEntry(catalogName, alias, value string, versions map[string]string, relativePath string) (GradleCatalogLibrary, []string) {
	library := GradleCatalogLibrary{
		Alias:   normalizeGradleCatalogAccessor(alias),
		Catalog: normalizeGradleCatalogName(catalogName),
	}

	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return library, nil
	}

	if coords, ok := parseGradleCatalogStringValue(trimmed); ok {
		return parseGradleCatalogStringLibrary(library, alias, coords, relativePath)
	}

	if !strings.HasPrefix(trimmed, "{") {
		return GradleCatalogLibrary{}, unsupportedGradleCatalogLibraryWarning(alias, relativePath)
	}
	return parseGradleCatalogInlineLibrary(library, alias, trimmed, versions, relativePath)
}

func parseGradleCatalogStringLibrary(library GradleCatalogLibrary, alias, coords, relativePath string) (GradleCatalogLibrary, []string) {
	group, artifact, version, parsed := parseGradleCatalogCoordinates(coords)
	if !parsed {
		return GradleCatalogLibrary{}, unsupportedGradleCatalogLibraryWarning(alias, relativePath)
	}
	library.Group = group
	library.Artifact = artifact
	library.Version = version
	return library, nil
}

func parseGradleCatalogInlineLibrary(library GradleCatalogLibrary, alias, value string, versions map[string]string, relativePath string) (GradleCatalogLibrary, []string) {
	fields := parseGradleCatalogInlineFields(value)
	if module := fields["module"]; module != "" {
		group, artifact, _, ok := parseGradleCatalogCoordinates(module)
		if !ok {
			return GradleCatalogLibrary{}, unsupportedGradleCatalogModuleWarning(alias, relativePath)
		}
		library.Group = group
		library.Artifact = artifact
	} else {
		library.Group = fields["group"]
		library.Artifact = fields["name"]
	}
	library.Version = resolveGradleCatalogVersion(fields, value, versions)
	if library.Group == "" || library.Artifact == "" {
		return GradleCatalogLibrary{}, unsupportedGradleCatalogLibraryWarning(alias, relativePath)
	}
	return library, nil
}

func resolveGradleCatalogVersion(fields map[string]string, value string, versions map[string]string) string {
	version := fields["version"]
	if version != "" {
		return version
	}
	versionRef := fields["version.ref"]
	if versionRef == "" {
		versionRef = parseGradleCatalogNestedVersionRef(value)
	}
	if versionRef == "" {
		return ""
	}
	if version = versions[versionRef]; version != "" {
		return version
	}
	return versions[strings.ToLower(versionRef)]
}

func unsupportedGradleCatalogLibraryWarning(alias, relativePath string) []string {
	return []string{fmt.Sprintf(unsupportedGradleCatalogLibraryFormat, alias, relativePath)}
}

func unsupportedGradleCatalogModuleWarning(alias, relativePath string) []string {
	return []string{fmt.Sprintf(unsupportedGradleCatalogModuleFormat, alias, relativePath)}
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
	state := gradleCatalogDelimiterState{}
	for _, r := range value {
		state.consume(r)
	}
	return state.balanced()
}

func (s *gradleCatalogDelimiterState) consume(r rune) {
	if s.toggleQuote(r) || s.inQuoted() {
		return
	}
	if delta, ok := gradleCatalogBraceDelta(r); ok {
		s.braceDepth += delta
	}
	if delta, ok := gradleCatalogBracketDelta(r); ok {
		s.bracketDepth += delta
	}
}

func (s *gradleCatalogDelimiterState) toggleQuote(r rune) bool {
	switch r {
	case '"':
		if s.inSingle {
			return false
		}
		s.inDouble = !s.inDouble
		return true
	case '\'':
		if s.inDouble {
			return false
		}
		s.inSingle = !s.inSingle
		return true
	default:
		return false
	}
}

func (s *gradleCatalogDelimiterState) inQuoted() bool {
	return s.inDouble || s.inSingle
}

func (s *gradleCatalogDelimiterState) balanced() bool {
	return s.braceDepth <= 0 && s.bracketDepth <= 0
}

func gradleCatalogBraceDelta(r rune) (int, bool) {
	switch r {
	case '{':
		return 1, true
	case '}':
		return -1, true
	default:
		return 0, false
	}
}

func gradleCatalogBracketDelta(r rune) (int, bool) {
	switch r {
	case '[':
		return 1, true
	case ']':
		return -1, true
	default:
		return 0, false
	}
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

func buildGradleCatalogScopeKey(left, right string) string {
	return strings.TrimSpace(left) + gradleCatalogScopeKeySeparator + strings.TrimSpace(right)
}

func isGradleCatalogSubPath(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if root == path {
		return true
	}
	rootWithSeparator := root + string(filepath.Separator)
	return strings.HasPrefix(path, rootWithSeparator)
}

func relativeGradleCatalogPath(repoPath, path string) string {
	if rel, err := filepath.Rel(repoPath, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}

func relativeGradleCatalogPathFromFile(path string) string {
	return filepath.ToSlash(filepath.Clean(path))
}

func formatGradleCatalogReadWarning(repoPath, path string, err error) string {
	return fmt.Sprintf(gradleCatalogReadWarningFormat, relativeGradleCatalogPath(repoPath, path), err)
}

func trimGradleCatalogQuotes(value string) string {
	return strings.Trim(strings.TrimSpace(value), `"'`)
}

func DedupeWarnings(warnings []string) []string {
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

func dedupeGradleCatalogWarnings(warnings []string) []string {
	return DedupeWarnings(warnings)
}
