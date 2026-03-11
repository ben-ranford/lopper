package swift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/thresholds"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type Adapter struct {
	Clock func() time.Time
}

const (
	swiftAdapterID          = "swift"
	packageManifestName     = "Package.swift"
	packageResolvedName     = "Package.resolved"
	maxDetectFiles          = 2048
	maxScanFiles            = 4096
	maxScannableSwiftFile   = 2 * 1024 * 1024
	maxManifestDeclarations = 512
	maxWarningSamples       = 5
	ambiguousDependencyKey  = "\x00"
)

var (
	swiftImportPattern          = regexp.MustCompile(`^\s*(?:@[A-Za-z_][A-Za-z0-9_]*(?:\([^)]*\))?\s+)*import\s+(?:(?:typealias|struct|class|enum|protocol|let|var|func|operator)\s+)?([A-Za-z_][A-Za-z0-9_]*)(?:\.[A-Za-z_][A-Za-z0-9_]*)*`)
	swiftUpperIdentifierPattern = regexp.MustCompile(`\b[A-Z][A-Za-z0-9_]*\b`)
	swiftTypeDeclarationPattern = regexp.MustCompile(`\b(?:actor|class|enum|protocol|struct|typealias)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	stringFieldPattern          = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*:\s*"((?:\\.|[^"])*)"`)

	swiftSkippedDirs = map[string]bool{
		".build":      true,
		".swiftpm":    true,
		"carthage":    true,
		"deriveddata": true,
		"pods":        true,
	}

	standardSwiftSymbols = toLookupSet([]string{
		"Swift",
		"Foundation",
		"FoundationNetworking",
		"Dispatch",
		"Darwin",
		"Glibc",
		"XCTest",
		"SwiftUI",
		"Combine",
		"UIKit",
		"AppKit",
		"CoreGraphics",
		"CoreFoundation",
		"CoreData",
		"AVFoundation",
		"Security",
		"MapKit",
		"WebKit",
		"StoreKit",
		"CloudKit",
		"UserNotifications",
		"CryptoKit",
		"Observation",
		"SwiftData",
		"OSLog",
		"os",
		"String",
		"Substring",
		"Character",
		"Int",
		"Int8",
		"Int16",
		"Int32",
		"Int64",
		"UInt",
		"UInt8",
		"UInt16",
		"UInt32",
		"UInt64",
		"Double",
		"Float",
		"Bool",
		"Array",
		"Dictionary",
		"Set",
		"Optional",
		"Result",
		"Any",
		"AnyObject",
		"Data",
		"Date",
		"URL",
		"UUID",
		"Decimal",
		"Error",
		"Never",
	})
)

type importBinding = shared.ImportRecord

type fileScan struct {
	Path    string
	Imports []importBinding
	Usage   map[string]int
}

type dependencyMeta struct {
	Declared bool
	Resolved bool
	Version  string
	Revision string
	Source   string
}

type dependencyCatalog struct {
	Dependencies       map[string]dependencyMeta
	AliasToDependency  map[string]string
	ModuleToDependency map[string]string
	LocalModules       map[string]struct{}
}

type scanResult struct {
	Files                []fileScan
	Warnings             []string
	KnownDependencies    map[string]struct{}
	ImportedDependencies map[string]struct{}
}

type resolvedPin struct {
	Identity      string `json:"identity"`
	Package       string `json:"package"`
	Location      string `json:"location"`
	RepositoryURL string `json:"repositoryURL"`
	State         struct {
		Version  string `json:"version"`
		Revision string `json:"revision"`
		Branch   string `json:"branch"`
	} `json:"state"`
}

type resolvedDocument struct {
	Pins   []resolvedPin `json:"pins"`
	Object struct {
		Pins []resolvedPin `json:"pins"`
	} `json:"object"`
}

func NewAdapter() *Adapter {
	return &Adapter{Clock: time.Now}
}

func (a *Adapter) ID() string {
	return swiftAdapterID
}

func (a *Adapter) Aliases() []string {
	return []string{"swiftpm"}
}

func (a *Adapter) Detect(ctx context.Context, repoPath string) (bool, error) {
	return shared.DetectMatched(ctx, repoPath, a.DetectWithConfidence)
}

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	repoPath = shared.DefaultRepoPath(repoPath)
	detection := language.Detection{}
	roots := make(map[string]struct{})
	rootSignals := []shared.RootSignal{
		{Name: packageManifestName, Confidence: 60},
		{Name: packageResolvedName, Confidence: 25},
	}
	if err := shared.ApplyRootSignals(repoPath, rootSignals, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	visited := 0
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		visited++
		if visited > maxDetectFiles {
			return fs.SkipAll
		}

		switch strings.ToLower(entry.Name()) {
		case strings.ToLower(packageManifestName), strings.ToLower(packageResolvedName):
			detection.Matched = true
			detection.Confidence += 10
			roots[filepath.Dir(path)] = struct{}{}
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".swift") {
			detection.Matched = true
			detection.Confidence += 2
		}
		return nil
	})
	if err != nil && err != fs.SkipAll {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func (a *Adapter) Analyse(ctx context.Context, req language.Request) (report.Report, error) {
	repoPath, err := workspace.NormalizeRepoPath(req.RepoPath)
	if err != nil {
		return report.Report{}, err
	}

	result := report.Report{
		GeneratedAt: a.Clock(),
		RepoPath:    repoPath,
	}

	catalog, catalogWarnings, err := buildDependencyCatalog(repoPath)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, catalogWarnings...)

	scan, err := scanRepo(ctx, repoPath, catalog)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, scan.Warnings...)

	dependencies, warnings := buildRequestedSwiftDependencies(req, scan, catalog)
	result.Dependencies = dependencies
	result.Warnings = append(result.Warnings, warnings...)
	result.Summary = report.ComputeSummary(result.Dependencies)
	return result, nil
}

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
		depID := normalizeDependencyID(pin.Identity)
		if depID == "" {
			depID = normalizeDependencyID(pin.Package)
		}
		if depID == "" {
			depID = normalizeDependencyID(derivePackageIdentity(pin.Location))
		}
		if depID == "" {
			depID = normalizeDependencyID(derivePackageIdentity(pin.RepositoryURL))
		}
		if depID == "" {
			continue
		}

		source := strings.TrimSpace(pin.Location)
		if source == "" {
			source = strings.TrimSpace(pin.RepositoryURL)
		}
		ensureDependency(catalog, depID, false, true, pin.State.Version, pin.State.Revision, source)
		mapAlias(catalog, depID, depID)
		mapModule(catalog, depID, depID)
		if pin.Identity != "" {
			mapAlias(catalog, pin.Identity, depID)
		}
		if pin.Package != "" {
			mapAlias(catalog, pin.Package, depID)
			mapModule(catalog, pin.Package, depID)
		}
		if source != "" {
			identityFromSource := derivePackageIdentity(source)
			mapAlias(catalog, identityFromSource, depID)
			mapModule(catalog, identityFromSource, depID)
		}
	}
	return true, warnings, nil
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
		if inString != 0 {
			if escaped {
				escaped = false
				continue
			}
			switch ch {
			case '\\':
				escaped = true
			case inString:
				inString = 0
			}
			continue
		}

		switch ch {
		case '\'', '"':
			inString = ch
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return content[start:idx], idx + 1, true
			}
			if depth < 0 {
				return "", idx + 1, false
			}
		}
	}
	return "", len(content), false
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

func setLookup(target map[string]string, key string, depID string) {
	if key == "" || depID == "" {
		return
	}
	if existing, ok := target[key]; ok {
		if existing != depID {
			target[key] = ambiguousDependencyKey
		}
		return
	}
	target[key] = depID
}

func resolveLookup(target map[string]string, key string) (string, bool) {
	value, ok := target[key]
	if !ok || value == "" || value == ambiguousDependencyKey {
		return "", false
	}
	return value, true
}

func lookupKey(value string) string {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	if trimmed == "" {
		return ""
	}
	builder := strings.Builder{}
	builder.Grow(len(trimmed))
	for _, r := range trimmed {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func scanRepo(ctx context.Context, repoPath string, catalog dependencyCatalog) (scanResult, error) {
	scan := scanResult{
		KnownDependencies:    make(map[string]struct{}),
		ImportedDependencies: make(map[string]struct{}),
	}
	for dependency := range catalog.Dependencies {
		scan.KnownDependencies[dependency] = struct{}{}
	}
	unresolvedImports := make(map[string]int)
	foundSwift := false
	skippedLargeFiles := 0
	visited := 0

	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".swift") {
			return nil
		}
		foundSwift = true
		visited++
		if visited > maxScanFiles {
			return fs.SkipAll
		}

		info, infoErr := entry.Info()
		if infoErr == nil && info.Size() > maxScannableSwiftFile {
			skippedLargeFiles++
			return nil
		}

		content, err := safeio.ReadFileUnder(repoPath, path)
		if err != nil {
			return err
		}
		relPath, relErr := filepath.Rel(repoPath, path)
		if relErr != nil {
			relPath = entry.Name()
		}
		imports := parseSwiftImports(content, relPath)
		mappedImports := make([]importBinding, 0, len(imports))
		for _, imported := range imports {
			dependency := resolveImportDependency(catalog, imported.Module)
			if dependency == "" {
				if shouldTrackUnresolvedImport(imported.Module, catalog) {
					unresolvedImports[imported.Module]++
				}
				continue
			}
			imported.Dependency = dependency
			if imported.Name == "" {
				imported.Name = imported.Module
			}
			if imported.Local == "" {
				imported.Local = imported.Name
			}
			scan.ImportedDependencies[dependency] = struct{}{}
			mappedImports = append(mappedImports, imported)
		}

		scan.Files = append(scan.Files, fileScan{
			Path:    relPath,
			Imports: mappedImports,
			Usage:   applyUnqualifiedUsageHeuristic(content, mappedImports, shared.CountUsage(content, mappedImports)),
		})
		return nil
	})
	if err != nil && err != fs.SkipAll {
		return scan, err
	}

	if !foundSwift {
		scan.Warnings = append(scan.Warnings, "no Swift files found for analysis")
	}
	if visited >= maxScanFiles {
		scan.Warnings = append(scan.Warnings, fmt.Sprintf("Swift scan capped at %d files", maxScanFiles))
	}
	if skippedLargeFiles > 0 {
		scan.Warnings = append(scan.Warnings, fmt.Sprintf("skipped %d Swift file(s) larger than %d bytes", skippedLargeFiles, maxScannableSwiftFile))
	}
	if len(unresolvedImports) > 0 {
		scan.Warnings = append(scan.Warnings, unresolvedImportWarning(unresolvedImports))
	}
	return scan, nil
}

func unresolvedImportWarning(unresolved map[string]int) string {
	type unresolvedEntry struct {
		Module string
		Count  int
	}
	entries := make([]unresolvedEntry, 0, len(unresolved))
	for module, count := range unresolved {
		entries = append(entries, unresolvedEntry{Module: module, Count: count})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Count == entries[j].Count {
			return entries[i].Module < entries[j].Module
		}
		return entries[i].Count > entries[j].Count
	})
	samples := make([]string, 0, maxWarningSamples)
	for index, item := range entries {
		if index >= maxWarningSamples {
			break
		}
		samples = append(samples, fmt.Sprintf("%s (%d)", item.Module, item.Count))
	}
	if len(entries) > maxWarningSamples {
		samples = append(samples, fmt.Sprintf("+%d more", len(entries)-maxWarningSamples))
	}
	return "could not map some Swift imports to Package.swift/Package.resolved dependencies: " + strings.Join(samples, ", ")
}

func shouldTrackUnresolvedImport(module string, catalog dependencyCatalog) bool {
	if len(catalog.Dependencies) == 0 {
		return false
	}
	key := lookupKey(module)
	if key == "" {
		return false
	}
	if _, ok := catalog.LocalModules[key]; ok {
		return false
	}
	if _, ok := standardSwiftSymbols[key]; ok {
		return false
	}
	return true
}

func parseSwiftImports(content []byte, filePath string) []importBinding {
	return shared.ParseImportLines(content, filePath, func(line string, index int) []shared.ImportRecord {
		line = shared.StripLineComment(line, "//")
		matches := swiftImportPattern.FindStringSubmatch(line)
		if len(matches) != 2 {
			return nil
		}
		moduleName := strings.TrimSpace(matches[1])
		if moduleName == "" {
			return nil
		}
		return []shared.ImportRecord{{
			Module:   moduleName,
			Name:     moduleName,
			Local:    moduleName,
			Wildcard: false,
			Location: shared.LocationFromLine(filePath, index, line),
		}}
	})
}

func applyUnqualifiedUsageHeuristic(content []byte, imports []importBinding, usage map[string]int) map[string]int {
	if len(imports) == 0 {
		return usage
	}
	byDependency := make(map[string][]importBinding)
	for _, imported := range imports {
		dependency := normalizeDependencyID(imported.Dependency)
		if dependency == "" {
			continue
		}
		byDependency[dependency] = append(byDependency[dependency], imported)
	}
	// Unqualified symbol usage cannot be reliably attributed when a file imports
	// multiple third-party dependencies.
	if len(byDependency) != 1 {
		return usage
	}
	for _, importsForDependency := range byDependency {
		hasQualifiedUsage := false
		for _, imported := range importsForDependency {
			if imported.Local != "" && usage[imported.Local] > 0 {
				hasQualifiedUsage = true
				break
			}
		}
		if hasQualifiedUsage {
			return usage
		}
		if !hasPotentialUnqualifiedSymbolUsage(content, importsForDependency) {
			return usage
		}
		for _, imported := range importsForDependency {
			if imported.Local != "" && usage[imported.Local] == 0 {
				usage[imported.Local] = 1
			}
		}
	}
	return usage
}

func hasPotentialUnqualifiedSymbolUsage(content []byte, imports []importBinding) bool {
	importModules := make(map[string]struct{}, len(imports))
	for _, imported := range imports {
		key := lookupKey(imported.Module)
		if key != "" {
			importModules[key] = struct{}{}
		}
	}
	localDeclaredSymbols := collectLocalDeclaredSymbols(content)
	lines := strings.Split(string(content), "\n")
	for _, rawLine := range lines {
		line := strings.TrimSpace(shared.StripLineComment(rawLine, "//"))
		if line == "" || swiftImportPattern.MatchString(line) {
			continue
		}
		symbols := swiftUpperIdentifierPattern.FindAllString(line, -1)
		for _, symbol := range symbols {
			key := lookupKey(symbol)
			if key == "" {
				continue
			}
			if _, ok := importModules[key]; ok {
				continue
			}
			if _, ok := localDeclaredSymbols[key]; ok {
				continue
			}
			if _, ok := standardSwiftSymbols[key]; ok {
				continue
			}
			return true
		}
	}
	return false
}

func collectLocalDeclaredSymbols(content []byte) map[string]struct{} {
	localDeclaredSymbols := make(map[string]struct{})
	lines := strings.Split(string(content), "\n")
	for _, rawLine := range lines {
		line := strings.TrimSpace(shared.StripLineComment(rawLine, "//"))
		if line == "" || swiftImportPattern.MatchString(line) {
			continue
		}
		matches := swiftTypeDeclarationPattern.FindAllStringSubmatch(line, -1)
		for _, match := range matches {
			if len(match) != 2 {
				continue
			}
			key := lookupKey(match[1])
			if key == "" {
				continue
			}
			localDeclaredSymbols[key] = struct{}{}
		}
	}
	return localDeclaredSymbols
}

func buildRequestedSwiftDependencies(req language.Request, scan scanResult, catalog dependencyCatalog) ([]report.DependencyReport, []string) {
	minUsagePercent := resolveMinUsageRecommendationThreshold(req.MinUsagePercentForRecommendations)
	buildDependency := func(dependency string, scan scanResult) (report.DependencyReport, []string) {
		return buildDependencyReport(dependency, scan, catalog, minUsagePercent)
	}
	return shared.BuildRequestedDependenciesWithWeights(req, scan, normalizeDependencyID, buildDependency, resolveRemovalCandidateWeights, buildTopSwiftDependencies(scan, catalog, minUsagePercent))
}

func buildTopSwiftDependencies(scan scanResult, catalog dependencyCatalog, minUsagePercent int) func(int, scanResult, report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
	return func(topN int, _ scanResult, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
		dependencies := allSwiftDependencies(scan)
		reports := make([]report.DependencyReport, 0, len(dependencies))
		warnings := make([]string, 0)
		for _, dependency := range dependencies {
			depReport, depWarnings := buildDependencyReport(dependency, scan, catalog, minUsagePercent)
			reports = append(reports, depReport)
			warnings = append(warnings, depWarnings...)
		}
		shared.SortReportsByWaste(reports, weights)
		if topN > 0 && topN < len(reports) {
			reports = reports[:topN]
		}
		if len(reports) == 0 {
			warnings = append(warnings, "no dependency data available for top-N ranking")
		}
		return reports, warnings
	}
}

func allSwiftDependencies(scan scanResult) []string {
	set := make(map[string]struct{})
	for dependency := range scan.KnownDependencies {
		set[dependency] = struct{}{}
	}
	for dependency := range scan.ImportedDependencies {
		set[dependency] = struct{}{}
	}
	return shared.SortedKeys(set)
}

func buildDependencyReport(dependency string, scan scanResult, catalog dependencyCatalog, minUsagePercent int) (report.DependencyReport, []string) {
	importsOf := func(file fileScan) []shared.ImportRecord { return file.Imports }
	usageOf := func(file fileScan) map[string]int { return file.Usage }
	fileUsages := shared.MapFileUsages(scan.Files, importsOf, usageOf)
	stats := shared.BuildDependencyStats(dependency, fileUsages, normalizeDependencyID)
	depReport := shared.BuildDependencyReportFromStats(dependency, swiftAdapterID, stats)

	meta := catalog.Dependencies[dependency]
	depReport.RiskCues = buildDependencyRiskCues(meta)
	depReport.Recommendations = buildRecommendations(depReport, meta, minUsagePercent)
	if meta.Source != "" {
		depReport.Provenance = &report.DependencyProvenance{
			Source:     "manifest/lockfile",
			Confidence: "high",
			Signals:    []string{meta.Source},
		}
	}

	if stats.HasImports {
		return depReport, nil
	}
	return depReport, []string{fmt.Sprintf("no imports found for dependency %q", dependency)}
}

func buildDependencyRiskCues(meta dependencyMeta) []report.RiskCue {
	cues := make([]report.RiskCue, 0, 1)
	if meta.Declared && !meta.Resolved {
		cues = append(cues, report.RiskCue{
			Code:     "missing-lock-resolution",
			Severity: "medium",
			Message:  "dependency is declared in Package.swift but missing from Package.resolved",
		})
	}
	return cues
}

func buildRecommendations(dep report.DependencyReport, meta dependencyMeta, minUsagePercent int) []report.Recommendation {
	recommendations := make([]report.Recommendation, 0, 3)
	if len(dep.UsedImports) == 0 && len(dep.UnusedImports) > 0 {
		recommendations = append(recommendations, report.Recommendation{
			Code:      "remove-unused-dependency",
			Priority:  "high",
			Message:   fmt.Sprintf("No used imports were detected for %q; consider removing it.", dep.Name),
			Rationale: "Unused dependencies increase maintenance and security surface area.",
		})
	}
	if dep.TotalExportsCount > 0 && dep.UsedPercent < float64(minUsagePercent) {
		recommendations = append(recommendations, report.Recommendation{
			Code:      "low-usage-dependency",
			Priority:  "medium",
			Message:   fmt.Sprintf("Dependency %q has low observed usage (%.1f%%).", dep.Name, dep.UsedPercent),
			Rationale: "Low-usage dependencies are good candidates for cleanup or replacement.",
		})
	}
	if meta.Declared && !meta.Resolved {
		recommendations = append(recommendations, report.Recommendation{
			Code:      "refresh-package-resolved",
			Priority:  "medium",
			Message:   fmt.Sprintf("Dependency %q is declared but not pinned in Package.resolved; refresh lockfile.", dep.Name),
			Rationale: "Keeping lockfile pins aligned improves reproducibility and supply-chain traceability.",
		})
	}
	return recommendations
}

func resolveMinUsageRecommendationThreshold(value *int) int {
	if value != nil {
		return *value
	}
	return thresholds.Defaults().MinUsagePercentForRecommendations
}

func resolveRemovalCandidateWeights(value *report.RemovalCandidateWeights) report.RemovalCandidateWeights {
	if value == nil {
		return report.DefaultRemovalCandidateWeights()
	}
	return report.NormalizeRemovalCandidateWeights(*value)
}

func normalizeDependencyID(value string) string {
	value = shared.NormalizeDependencyID(value)
	value = strings.ReplaceAll(value, "_", "-")
	return strings.Trim(value, "-")
}

func shouldSkipDir(name string) bool {
	if shared.ShouldSkipCommonDir(name) {
		return true
	}
	return swiftSkippedDirs[strings.ToLower(name)]
}

func dedupeWarnings(warnings []string) []string {
	if len(warnings) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(warnings))
	result := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		warning = strings.TrimSpace(warning)
		if warning == "" {
			continue
		}
		if _, ok := seen[warning]; ok {
			continue
		}
		seen[warning] = struct{}{}
		result = append(result, warning)
	}
	return result
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

func toLookupSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		key := lookupKey(value)
		if key == "" {
			continue
		}
		result[key] = struct{}{}
	}
	return result
}
