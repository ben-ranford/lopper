package dotnet

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

type sourceDocument struct {
	RelativePath          string
	Content               []byte
	DeclaredDependencies  []string
	HasProjectDeclaration bool
	ProjectRoot           string
	MapperKey             string
}

type scanInputs struct {
	DeclaredDependencies []string
	SourceFiles          []sourceDocument
	CoverageGaps         []report.CoverageGap
	Warnings             []string
	SkippedGenerated     int
	SkippedFileLimit     bool
}

func scanRepo(ctx context.Context, repoPath string, scopeModes ...string) (scanResult, error) {
	scopeMode := ""
	if len(scopeModes) > 0 {
		scopeMode = scopeModes[0]
	}
	result := newScanResult()

	inputs, err := discoverScanInputs(ctx, repoPath, scopeMode)
	if err != nil {
		return result, err
	}

	result.DeclaredDependencies = inputs.DeclaredDependencies
	result.CoverageGaps = inputs.CoverageGaps
	result.Warnings = append(result.Warnings, inputs.Warnings...)
	result.SkippedGeneratedFiles = inputs.SkippedGenerated
	result.SkippedFileLimit = inputs.SkippedFileLimit

	mapper := newDependencyMapper(inputs.DeclaredDependencies)
	projectMappers := make(map[string]dependencyMapper)
	for _, source := range inputs.SourceFiles {
		currentMapper := mapper
		if source.HasProjectDeclaration {
			var ok bool
			currentMapper, ok = projectMappers[source.MapperKey]
			if !ok {
				currentMapper = newProjectDependencyMapper(source.DeclaredDependencies, source.ProjectRoot != "")
				projectMappers[source.MapperKey] = currentMapper
			}
		}
		parsed := parseSourceDocument(source, currentMapper)
		result.Files = append(result.Files, parsed.File)
		addMappingMeta(&result, parsed.Mapping)
	}
	return result, nil
}

func discoverScanInputs(ctx context.Context, repoPath string, scopeModes ...string) (scanInputs, error) {
	scopeMode := ""
	if len(scopeModes) > 0 {
		scopeMode = scopeModes[0]
	}
	inputs := scanInputs{}
	if repoPath == "" {
		return inputs, fs.ErrInvalid
	}

	sourceScan := sourceDiscovery{}
	scanner := newScanInputDiscoverer(repoPath, &sourceScan)

	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return scanner.walk(path, entry, walkErr)
	})
	if err != nil {
		return inputs, err
	}

	appendSourceDiscoveryWarnings(&sourceScan)

	inputs.DeclaredDependencies = scanner.declaredDependencies(scopeMode)
	inputs.SourceFiles = scanner.sourceFiles(scopeMode)
	inputs.CoverageGaps = scanner.coverageGaps
	inputs.SkippedGenerated = sourceScan.SkippedGeneratedFiles
	inputs.SkippedFileLimit = sourceScan.SkippedFileLimit
	inputs.Warnings = append(inputs.Warnings, sourceScan.Warnings...)
	return inputs, nil
}

func newScanResult() scanResult {
	return scanResult{
		AmbiguousByDependency:  make(map[string]int),
		UndeclaredByDependency: make(map[string]int),
	}
}

func addMappingMeta(result *scanResult, meta mappingMetadata) {
	for dep, count := range meta.ambiguousByDependency {
		result.AmbiguousByDependency[dep] += count
	}
	for dep, count := range meta.undeclaredByDependency {
		result.UndeclaredByDependency[dep] += count
	}
}

type sourceDiscovery struct {
	Files                 []sourceDocument
	Warnings              []string
	SkippedGeneratedFiles int
	SkippedFileLimit      bool
}

type sourceDiscoverer struct {
	repoPath           string
	discovery          *sourceDiscovery
	visitedSourceFiles int
}

type scanInputDiscoverer struct {
	dependencySet          map[string]struct{}
	projectDependencies    map[string][]string
	centralDependencies    map[string][]string
	malformedManifestRoots map[string]struct{}
	malformedCentralRoots  map[string]struct{}
	coverageGaps           []report.CoverageGap
	sourceDiscoverer       sourceDiscoverer
	sourceScanLimited      bool
}

func newSourceDiscoverer(repoPath string, discovery *sourceDiscovery) sourceDiscoverer {
	return sourceDiscoverer{
		repoPath:  repoPath,
		discovery: discovery,
	}
}

func newScanInputDiscoverer(repoPath string, source *sourceDiscovery) scanInputDiscoverer {
	return scanInputDiscoverer{
		dependencySet:          make(map[string]struct{}),
		projectDependencies:    make(map[string][]string),
		centralDependencies:    make(map[string][]string),
		malformedManifestRoots: make(map[string]struct{}),
		malformedCentralRoots:  make(map[string]struct{}),
		sourceDiscoverer:       newSourceDiscoverer(repoPath, source),
	}
}

func (d *scanInputDiscoverer) walk(path string, entry fs.DirEntry, walkErr error) error {
	if walkErr != nil {
		return walkErr
	}
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}

	dependencies, err := parseManifestDependenciesForEntry(d.sourceDiscoverer.repoPath, path, entry.Name())
	if err != nil {
		if isDotNetManifestParseError(err) {
			d.recordMalformedManifest(path, entry.Name())
			warning := malformedDotNetManifestWarning(d.sourceDiscoverer.repoPath, path, err)
			d.sourceDiscoverer.discovery.Warnings = append(d.sourceDiscoverer.discovery.Warnings, warning)
			d.coverageGaps = append(d.coverageGaps, report.CoverageGap{
				Code:     report.CoverageGapDotNetMalformedManifest,
				Language: "dotnet",
				Path:     malformedDotNetManifestPath(d.sourceDiscoverer.repoPath, path),
				Evidence: []string{warning},
			})
			return nil
		}
		return err
	}
	addDependencies(d.dependencySet, dependencies)
	d.recordManifestDependencies(path, entry.Name(), dependencies)

	if d.sourceScanLimited {
		return nil
	}
	err = d.sourceDiscoverer.discoverFile(path)
	if errors.Is(err, fs.SkipAll) {
		d.sourceScanLimited = true
		return nil
	}
	return err
}

func (d *scanInputDiscoverer) recordMalformedManifest(path, name string) {
	switch signalForName(name) {
	case fileSignalProject:
		d.malformedManifestRoots[filepath.Dir(path)] = struct{}{}
	case fileSignalCentral:
		d.malformedCentralRoots[filepath.Dir(path)] = struct{}{}
	}
}

func (d *scanInputDiscoverer) recordManifestDependencies(path, name string, dependencies []string) {
	switch signalForName(name) {
	case fileSignalProject:
		d.projectDependencies[filepath.Dir(path)] = mergeDependencies(d.projectDependencies[filepath.Dir(path)], dependencies)
	case fileSignalCentral:
		d.centralDependencies[filepath.Dir(path)] = mergeDependencies(d.centralDependencies[filepath.Dir(path)], dependencies)
	}
}

func mergeDependencies(existing, added []string) []string {
	dependencies := make(map[string]struct{}, len(existing)+len(added))
	addDependencies(dependencies, existing)
	addDependencies(dependencies, added)
	return sortedDependencies(dependencies)
}

func (d *scanInputDiscoverer) hasMalformedRootProject() bool {
	_, malformed := d.malformedManifestRoots[d.sourceDiscoverer.repoPath]
	return malformed
}

func (d *scanInputDiscoverer) hasMalformedRootFallback() bool {
	if d.hasMalformedRootProject() {
		return true
	}
	_, malformedCentral := d.malformedCentralRoots[d.sourceDiscoverer.repoPath]
	_, hasRootProject := d.projectDependencies[d.sourceDiscoverer.repoPath]
	return malformedCentral && !hasRootProject
}

func (d *scanInputDiscoverer) declaredDependencies(scopeMode string) []string {
	rootDependencies, hasRootProject := d.projectDependencies[d.sourceDiscoverer.repoPath]
	if scopeMode == "repo" || !hasRootProject || d.hasMalformedRootProject() {
		return sortedDependencies(d.dependencySet)
	}
	dependencies := make(map[string]struct{})
	addDependencies(dependencies, rootDependencies)
	addDependencies(dependencies, d.centralDependencies[d.sourceDiscoverer.repoPath])
	return sortedDependencies(dependencies)
}

func (d *scanInputDiscoverer) sourceFiles(scopeMode string) []sourceDocument {
	files := append([]sourceDocument(nil), d.sourceDiscoverer.discovery.Files...)
	for index := range files {
		dependencies, hasProject, projectRoot := d.sourceDependencies(files[index].RelativePath)
		files[index].DeclaredDependencies = dependencies
		files[index].HasProjectDeclaration = hasProject
		files[index].ProjectRoot = projectRoot
		fallbackMode := "fallback-disabled"
		if projectRoot != "" {
			fallbackMode = "fallback-enabled"
		}
		files[index].MapperKey = fallbackMode + "\x00" + strings.Join(dependencies, "\x00")
	}
	if (scopeMode == "package" || scopeMode == "changed-packages") && !d.hasMalformedRootFallback() {
		files = excludeNestedProjectSources(files, d.sourceDiscoverer.repoPath, d.malformedManifestRoots)
	}
	return files
}

func (d *scanInputDiscoverer) sourceDependencies(relativePath string) ([]string, bool, string) {
	directory := filepath.Dir(filepath.Join(d.sourceDiscoverer.repoPath, relativePath))
	projectRoot := ""
	for current := directory; ; current = filepath.Dir(current) {
		if _, ok := d.projectDependencies[current]; ok {
			projectRoot = current
			break
		}
		if _, malformed := d.malformedManifestRoots[current]; malformed {
			break
		}
		if sameDotNetPath(current, d.sourceDiscoverer.repoPath) {
			break
		}
	}

	dependencies := make(map[string]struct{})
	if projectRoot != "" {
		addDependencies(dependencies, d.projectDependencies[projectRoot])
		directory = projectRoot
	}
	for current := directory; ; current = filepath.Dir(current) {
		addDependencies(dependencies, d.centralDependencies[current])
		if sameDotNetPath(current, d.sourceDiscoverer.repoPath) {
			break
		}
	}
	return sortedDependencies(dependencies), projectRoot != "" || len(d.projectDependencies) > 0, projectRoot
}

func excludeNestedProjectSources(files []sourceDocument, repoPath string, malformedRoots map[string]struct{}) []sourceDocument {
	filtered := files[:0]
	for _, file := range files {
		if file.ProjectRoot != "" && !sameDotNetPath(file.ProjectRoot, repoPath) {
			continue
		}
		if sourceIsUnderMalformedRoot(repoPath, file.RelativePath, malformedRoots) {
			continue
		}
		filtered = append(filtered, file)
	}
	return filtered
}

func sourceIsUnderMalformedRoot(repoPath, relativePath string, malformedRoots map[string]struct{}) bool {
	directory := filepath.Dir(filepath.Join(repoPath, relativePath))
	for current := directory; ; current = filepath.Dir(current) {
		if _, malformed := malformedRoots[current]; malformed && !sameDotNetPath(current, repoPath) {
			return true
		}
		if sameDotNetPath(current, repoPath) {
			return false
		}
	}
}

func sameDotNetPath(first, second string) bool {
	return filepath.Clean(first) == filepath.Clean(second)
}

func (d *sourceDiscoverer) walk(path string, entry fs.DirEntry, walkErr error) error {
	if walkErr != nil {
		return walkErr
	}
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}
	return d.discoverFile(path)
}

func (d *sourceDiscoverer) discoverFile(path string) error {
	if !isSourceFile(path) {
		return nil
	}
	if isGeneratedSource(path) {
		d.discovery.SkippedGeneratedFiles++
		return nil
	}
	d.visitedSourceFiles++
	if d.visitedSourceFiles > maxScanFiles {
		d.discovery.SkippedFileLimit = true
		return fs.SkipAll
	}
	content, relativePath, err := readSourceFile(d.repoPath, path)
	if err != nil {
		return err
	}
	d.discovery.Files = append(d.discovery.Files, sourceDocument{
		RelativePath: relativePath,
		Content:      content,
	})
	return nil
}

func appendSourceDiscoveryWarnings(discovery *sourceDiscovery) {
	if len(discovery.Files) == 0 {
		discovery.Warnings = append(discovery.Warnings, "no C#/F# source files found for analysis")
	}
	if discovery.SkippedGeneratedFiles > 0 {
		discovery.Warnings = append(discovery.Warnings, fmt.Sprintf("skipped %d generated source file(s)", discovery.SkippedGeneratedFiles))
	}
	if discovery.SkippedFileLimit {
		discovery.Warnings = append(discovery.Warnings, fmt.Sprintf("source scan capped at %d files", maxScanFiles))
	}
}

func collectDeclaredDependencies(repoPath string) ([]string, error) {
	set := make(map[string]struct{})
	err := filepath.WalkDir(repoPath, newDependencyCollector(repoPath, set).walk)
	if err != nil {
		return nil, err
	}
	return sortedDependencies(set), nil
}

type dependencyCollector struct {
	repoPath string
	set      map[string]struct{}
}

func newDependencyCollector(repoPath string, set map[string]struct{}) *dependencyCollector {
	return &dependencyCollector{
		repoPath: repoPath,
		set:      set,
	}
}

func (c *dependencyCollector) walk(path string, entry fs.DirEntry, walkErr error) error {
	if walkErr != nil {
		return walkErr
	}
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}
	dependencies, err := parseManifestDependenciesForEntry(c.repoPath, path, entry.Name())
	if err != nil {
		if isDotNetManifestParseError(err) {
			return nil
		}
		return err
	}
	addDependencies(c.set, dependencies)
	return nil
}

func parseManifestDependenciesForEntry(repoPath, path, name string) ([]string, error) {
	lower := strings.ToLower(name)
	switch {
	case isProjectManifestName(lower):
		return parsePackageReferences(repoPath, path)
	case strings.EqualFold(name, centralPackagesFile):
		return parsePackageVersions(repoPath, path)
	default:
		return nil, nil
	}
}

func parsePackageReferences(repoPath, manifestPath string) ([]string, error) {
	return parseManifestDependencies(repoPath, manifestPath, "PackageReference")
}

func parsePackageVersions(repoPath, manifestPath string) ([]string, error) {
	return parseManifestDependencies(repoPath, manifestPath, "PackageVersion")
}

func parseManifestDependencies(repoPath, manifestPath string, elementName string) ([]string, error) {
	content, err := safeio.ReadFileUnder(repoPath, manifestPath)
	if err != nil {
		return nil, err
	}
	dependencies, err := parseXMLManifestIncludes(content, elementName)
	if err != nil {
		var syntaxErr *xml.SyntaxError
		if errors.As(err, &syntaxErr) {
			return nil, &dotNetManifestParseError{err: err}
		}
		return nil, err
	}
	return dependencies, nil
}

type dotNetManifestParseError struct {
	err error
}

func (e *dotNetManifestParseError) Error() string {
	return e.err.Error()
}

func (e *dotNetManifestParseError) Unwrap() error {
	return e.err
}

func isDotNetManifestParseError(err error) bool {
	var parseErr *dotNetManifestParseError
	return errors.As(err, &parseErr)
}

func malformedDotNetManifestWarning(repoPath, manifestPath string, err error) string {
	path := malformedDotNetManifestPath(repoPath, manifestPath)
	return fmt.Sprintf("skipped malformed .NET manifest %s: %v", path, err)
}

func malformedDotNetManifestPath(repoPath, manifestPath string) string {
	path, err := filepath.Rel(repoPath, manifestPath)
	if err != nil {
		path = filepath.Base(manifestPath)
	}
	return filepath.ToSlash(path)
}

func parseXMLManifestIncludes(content []byte, elementName string) ([]string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(content))
	set := make(map[string]struct{})
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if include := parseManifestInclude(token, elementName); include != "" {
			set[include] = struct{}{}
		}
	}
	return sortedDependencies(set), nil
}

func parseManifestInclude(token xml.Token, elementName string) string {
	start, ok := token.(xml.StartElement)
	if !ok || !strings.EqualFold(start.Name.Local, elementName) {
		return ""
	}
	for _, attr := range start.Attr {
		if strings.EqualFold(attr.Name.Local, "Include") {
			return normalizeDependencyID(attr.Value)
		}
	}
	return ""
}

func readSourceFile(repoPath, sourcePath string) ([]byte, string, error) {
	content, err := safeio.ReadFileUnder(repoPath, sourcePath)
	if err != nil {
		return nil, "", err
	}
	relativePath, err := filepath.Rel(repoPath, sourcePath)
	if err != nil {
		relativePath = sourcePath
	}
	return content, relativePath, nil
}

func addDependencies(set map[string]struct{}, dependencies []string) {
	for _, dep := range dependencies {
		set[normalizeDependencyID(dep)] = struct{}{}
	}
}

func addAncestorCentralPackages(repoPath string, set map[string]struct{}) error {
	for ancestorDir := filepath.Dir(repoPath); ancestorDir != "" && ancestorDir != filepath.Dir(ancestorDir); ancestorDir = filepath.Dir(ancestorDir) {
		path := filepath.Join(ancestorDir, centralPackagesFile)
		_, err := os.Stat(path)
		if err == nil {
			deps, parseErr := parsePackageVersions(ancestorDir, path)
			if parseErr != nil {
				return parseErr
			}
			addDependencies(set, deps)
			return nil
		}
		if os.IsNotExist(err) {
			continue
		}
		return err
	}
	return nil
}

func sortedDependencies(set map[string]struct{}) []string {
	dependencies := make([]string, 0, len(set))
	for dep := range set {
		dependencies = append(dependencies, dep)
	}
	sort.Strings(dependencies)
	return dependencies
}

func shouldSkipDir(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".idea", ".vscode", "node_modules", "vendor", "bin", "obj", "dist", "build", "packages":
		return true
	default:
		return false
	}
}

func isSourceFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case csharpSourceExt, fsharpSourceExt:
		return true
	default:
		return false
	}
}

func isGeneratedSource(path string) bool {
	lower := strings.ToLower(filepath.Base(path))
	switch {
	case strings.HasSuffix(lower, ".g.cs"),
		strings.HasSuffix(lower, ".g.i.cs"),
		strings.HasSuffix(lower, ".designer.cs"),
		strings.HasSuffix(lower, ".assemblyinfo.cs"):
		return true
	default:
		return false
	}
}

func isProjectManifestName(lowerName string) bool {
	return strings.HasSuffix(lowerName, csharpProjectExt) || strings.HasSuffix(lowerName, fsharpProjectExt)
}

func isSolutionFileName(lowerName string) bool {
	return strings.HasSuffix(lowerName, solutionFileExt)
}

func isSourceFileName(lowerName string) bool {
	return strings.HasSuffix(lowerName, csharpSourceExt) || strings.HasSuffix(lowerName, fsharpSourceExt)
}

func addSolutionRoots(repoPath string, solutionPath string, roots map[string]struct{}) error {
	content, err := safeio.ReadFileUnder(repoPath, solutionPath)
	if err != nil {
		return err
	}
	matches := solutionProjectPattern.FindAllSubmatch(content, -1)
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		relPath := strings.TrimSpace(string(match[1]))
		if relPath == "" {
			continue
		}
		relPath = strings.ReplaceAll(relPath, "\\", string(filepath.Separator))
		projectPath := filepath.Clean(filepath.Join(filepath.Dir(solutionPath), relPath))
		if !isRepoBoundedPath(repoPath, projectPath) {
			continue
		}
		roots[filepath.Dir(projectPath)] = struct{}{}
	}
	return nil
}

func isRepoBoundedPath(repoPath, candidatePath string) bool {
	repoAbs, err := filepath.Abs(repoPath)
	if err != nil {
		return false
	}
	candidateAbs, err := filepath.Abs(candidatePath)
	if err != nil {
		return false
	}
	relativeToRepo, err := filepath.Rel(repoAbs, candidateAbs)
	if err != nil {
		return false
	}
	return relativeToRepo != ".." && !strings.HasPrefix(relativeToRepo, ".."+string(filepath.Separator))
}

var (
	solutionProjectPattern = regexp.MustCompile(`Project\([^\)]*\)\s*=\s*"[^"]+"\s*,\s*"([^"]+\.(?:csproj|fsproj))"`)
)
