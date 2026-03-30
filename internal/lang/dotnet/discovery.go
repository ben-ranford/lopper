package dotnet

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

type sourceDocument struct {
	RelativePath string
	Content      []byte
}

type scanInputs struct {
	DeclaredDependencies []string
	SourceFiles          []sourceDocument
	Warnings             []string
	SkippedGenerated     int
	SkippedFileLimit     bool
}

func scanRepo(ctx context.Context, repoPath string) (scanResult, error) {
	result := newScanResult()

	inputs, err := discoverScanInputs(ctx, repoPath)
	if err != nil {
		return result, err
	}

	result.DeclaredDependencies = inputs.DeclaredDependencies
	result.Warnings = append(result.Warnings, inputs.Warnings...)
	result.SkippedGeneratedFiles = inputs.SkippedGenerated
	result.SkippedFileLimit = inputs.SkippedFileLimit

	mapper := newDependencyMapper(inputs.DeclaredDependencies)
	for _, source := range inputs.SourceFiles {
		parsed := parseSourceDocument(source, mapper)
		result.Files = append(result.Files, parsed.File)
		addMappingMeta(&result, parsed.Mapping)
	}
	return result, nil
}

func discoverScanInputs(ctx context.Context, repoPath string) (scanInputs, error) {
	inputs := scanInputs{}
	if repoPath == "" {
		return inputs, fs.ErrInvalid
	}

	declared, err := collectDeclaredDependencies(repoPath)
	if err != nil {
		return inputs, err
	}
	inputs.DeclaredDependencies = declared

	sources, err := discoverSourceFiles(ctx, repoPath)
	if err != nil {
		return inputs, err
	}
	inputs.SourceFiles = sources.Files
	inputs.SkippedGenerated = sources.SkippedGeneratedFiles
	inputs.SkippedFileLimit = sources.SkippedFileLimit
	inputs.Warnings = append(inputs.Warnings, sources.Warnings...)
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

func discoverSourceFiles(ctx context.Context, repoPath string) (sourceDiscovery, error) {
	discovery := sourceDiscovery{}
	discoverer := newSourceDiscoverer(repoPath, &discovery)

	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return discoverer.walk(path, entry, walkErr)
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return discovery, err
	}

	appendSourceDiscoveryWarnings(&discovery)
	return discovery, nil
}

func newSourceDiscoverer(repoPath string, discovery *sourceDiscovery) sourceDiscoverer {
	return sourceDiscoverer{
		repoPath:  repoPath,
		discovery: discovery,
	}
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
	return parseManifestDependencies(repoPath, manifestPath, packageReferencePattern)
}

func parsePackageVersions(repoPath, manifestPath string) ([]string, error) {
	return parseManifestDependencies(repoPath, manifestPath, packageVersionPattern)
}

func parseManifestDependencies(repoPath, manifestPath string, pattern *regexp.Regexp) ([]string, error) {
	content, err := safeio.ReadFileUnder(repoPath, manifestPath)
	if err != nil {
		return nil, err
	}
	matches := pattern.FindAllSubmatch(content, -1)
	return captureMatches(matches), nil
}

func captureMatches(matches [][][]byte) []string {
	if len(matches) == 0 {
		return nil
	}
	set := make(map[string]struct{})
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		value := normalizeDependencyID(string(match[1]))
		if value == "" {
			continue
		}
		set[value] = struct{}{}
	}
	items := make([]string, 0, len(set))
	for value := range set {
		items = append(items, value)
	}
	sort.Strings(items)
	return items
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
	packageReferencePattern = regexp.MustCompile(`(?is)<PackageReference\b[^>]*\bInclude\s*=\s*["']([^"']+)["']`)
	packageVersionPattern   = regexp.MustCompile(`(?is)<PackageVersion\b[^>]*\bInclude\s*=\s*["']([^"']+)["']`)
	solutionProjectPattern  = regexp.MustCompile(`Project\([^\)]*\)\s*=\s*"[^"]+"\s*,\s*"([^"]+\.(?:csproj|fsproj))"`)
)
