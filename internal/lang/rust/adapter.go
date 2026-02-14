package rust

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

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
	cargoTomlName        = "Cargo.toml"
	cargoLockName        = "Cargo.lock"
	maxDetectionFiles    = 2048
	maxScannableRustFile = 2 * 1024 * 1024
	maxManifestCount     = 256
	maxWarningSamples    = 5
)

type dependencyInfo struct {
	Canonical string
	LocalPath bool
	Renamed   bool
}

type manifestMeta struct {
	HasPackage       bool
	WorkspaceMembers []string
}

type importBinding = shared.ImportRecord

type fileScan struct {
	Path    string
	Imports []importBinding
	Usage   map[string]int
}

type scanResult struct {
	Files                    []fileScan
	Warnings                 []string
	UnresolvedImports        map[string]int
	RenamedAliasesByDep      map[string][]string
	MacroAmbiguityDetected   bool
	SkippedLargeFiles        int
	SkippedFilesByBoundLimit bool
}

var (
	tablePattern        = regexp.MustCompile(`^\s*\[([^\]]+)\]\s*$`)
	stringFieldPattern  = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_-]*)\s*=\s*"(.*?)"`)
	externCratePattern  = regexp.MustCompile(`^\s*extern\s+crate\s+([A-Za-z_][A-Za-z0-9_]*)(?:\s+as\s+([A-Za-z_][A-Za-z0-9_]*))?\s*;`)
	useStmtPattern      = regexp.MustCompile(`(?ms)^\s*use\s+(.+?);`)
	macroInvokePattern  = regexp.MustCompile(`(?m)\b[A-Za-z_][A-Za-z0-9_]*!\s*(?:\(|\{|\[)`)
	workspaceFieldStart = "members"
)

func NewAdapter() *Adapter {
	return &Adapter{Clock: time.Now}
}

func (a *Adapter) ID() string {
	return "rust"
}

func (a *Adapter) Aliases() []string {
	return []string{"rs", "cargo"}
}

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	repoPath = shared.DefaultRepoPath(repoPath)

	detection := language.Detection{}
	roots := make(map[string]struct{})
	workspaceOnlyRoot, err := applyRustRootSignals(repoPath, &detection, roots)
	if err != nil {
		return language.Detection{}, err
	}

	visited := 0
	err = filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return walkRustDetectionEntry(path, entry, repoPath, workspaceOnlyRoot, roots, &detection, &visited)
	})
	if err != nil && err != fs.SkipAll {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func applyRustRootSignals(repoPath string, detection *language.Detection, roots map[string]struct{}) (bool, error) {
	workspaceOnlyRoot := false
	cargoTomlPath := filepath.Join(repoPath, cargoTomlName)
	if _, err := os.Stat(cargoTomlPath); err == nil {
		detection.Matched = true
		detection.Confidence += 60

		meta, _, parseErr := parseCargoManifest(cargoTomlPath, repoPath)
		if parseErr != nil {
			return false, parseErr
		}
		if meta.HasPackage {
			roots[repoPath] = struct{}{}
		}
		if len(meta.WorkspaceMembers) > 0 {
			workspaceOnlyRoot = !meta.HasPackage
			for _, member := range meta.WorkspaceMembers {
				addWorkspaceMemberRoot(repoPath, member, roots)
			}
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}

	cargoLockPath := filepath.Join(repoPath, cargoLockName)
	if _, err := os.Stat(cargoLockPath); err == nil {
		detection.Matched = true
		detection.Confidence += 20
		if !workspaceOnlyRoot {
			roots[repoPath] = struct{}{}
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}

	return workspaceOnlyRoot, nil
}

func addWorkspaceMemberRoot(repoPath, member string, roots map[string]struct{}) {
	member = strings.TrimSpace(member)
	if member == "" {
		return
	}
	pattern := filepath.Join(repoPath, member)
	candidates, err := filepath.Glob(pattern)
	if err != nil {
		return
	}
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		info, statErr := os.Stat(candidate)
		if statErr != nil || !info.IsDir() {
			continue
		}
		manifestPath := filepath.Join(candidate, cargoTomlName)
		if _, manifestErr := os.Stat(manifestPath); manifestErr != nil {
			continue
		}
		if !isSubPath(repoPath, candidate) {
			continue
		}
		roots[candidate] = struct{}{}
	}
}

func walkRustDetectionEntry(path string, entry fs.DirEntry, repoPath string, workspaceOnlyRoot bool, roots map[string]struct{}, detection *language.Detection, visited *int) error {
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}

	(*visited)++
	if *visited > maxDetectionFiles {
		return fs.SkipAll
	}

	name := strings.ToLower(entry.Name())
	switch name {
	case strings.ToLower(cargoTomlName):
		detection.Matched = true
		detection.Confidence += 12
		dir := filepath.Dir(path)
		if workspaceOnlyRoot && samePath(dir, repoPath) {
			return nil
		}
		roots[dir] = struct{}{}
	case strings.ToLower(cargoLockName):
		detection.Matched = true
		detection.Confidence += 6
	}

	if strings.EqualFold(filepath.Ext(path), ".rs") {
		detection.Matched = true
		detection.Confidence += 2
	}
	return nil
}

func (a *Adapter) Detect(ctx context.Context, repoPath string) (bool, error) {
	return shared.DetectMatched(ctx, repoPath, a.DetectWithConfidence)
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

	manifestPaths, depLookup, renamedAliases, warnings, err := collectManifestData(repoPath)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, warnings...)

	scan, err := scanRepo(ctx, repoPath, manifestPaths, depLookup, renamedAliases)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, scan.Warnings...)

	dependencies, dependencyWarnings := buildRequestedRustDependencies(req, scan)
	result.Dependencies = dependencies
	result.Warnings = append(result.Warnings, dependencyWarnings...)
	result.Summary = report.ComputeSummary(result.Dependencies)
	return result, nil
}

func collectManifestData(repoPath string) ([]string, map[string]dependencyInfo, map[string][]string, []string, error) {
	manifestPaths, warnings, err := discoverManifestPaths(repoPath)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	lookup := make(map[string]dependencyInfo)
	renamed := make(map[string]map[string]struct{})
	for _, manifestPath := range manifestPaths {
		_, deps, parseErr := parseCargoManifest(manifestPath, repoPath)
		if parseErr != nil {
			return nil, nil, nil, nil, parseErr
		}
		warnings = mergeDependencyLookup(lookup, renamed, deps, warnings)
	}

	renamedByDep := make(map[string][]string, len(renamed))
	for dependency, aliases := range renamed {
		renamedByDep[dependency] = shared.SortedKeys(aliases)
	}
	return manifestPaths, lookup, renamedByDep, dedupeWarnings(warnings), nil
}

func mergeDependencyLookup(lookup map[string]dependencyInfo, renamed map[string]map[string]struct{}, deps map[string]dependencyInfo, warnings []string) []string {
	for alias, info := range deps {
		if existing, ok := lookup[alias]; ok {
			warnings = handleExistingDependencyAlias(lookup, alias, existing, info, warnings)
			continue
		}
		lookup[alias] = info
		trackRenamedDependencyAlias(renamed, alias, info)
	}
	return warnings
}

func handleExistingDependencyAlias(lookup map[string]dependencyInfo, alias string, existing, incoming dependencyInfo, warnings []string) []string {
	if existing.Canonical != incoming.Canonical {
		warnings = append(warnings, fmt.Sprintf("ambiguous dependency alias %q maps to multiple crates; using %q", alias, existing.Canonical))
	}
	if existing.LocalPath && !incoming.LocalPath {
		lookup[alias] = incoming
	}
	return warnings
}

func trackRenamedDependencyAlias(renamed map[string]map[string]struct{}, alias string, info dependencyInfo) {
	if !info.Renamed {
		return
	}
	if _, ok := renamed[info.Canonical]; !ok {
		renamed[info.Canonical] = make(map[string]struct{})
	}
	renamed[info.Canonical][alias] = struct{}{}
}

func discoverManifestPaths(repoPath string) ([]string, []string, error) {
	rootManifest := filepath.Join(repoPath, cargoTomlName)
	if _, err := os.Stat(rootManifest); err == nil {
		return discoverFromRootManifest(repoPath, rootManifest)
	} else if !os.IsNotExist(err) {
		return nil, nil, err
	}
	return discoverManifestsByWalk(repoPath)
}

func discoverFromRootManifest(repoPath, rootManifest string) ([]string, []string, error) {
	meta, _, parseErr := parseCargoManifest(rootManifest, repoPath)
	if parseErr != nil {
		return nil, nil, parseErr
	}
	paths := make([]string, 0, 1+len(meta.WorkspaceMembers))
	warnings := make([]string, 0)
	if meta.HasPackage || len(meta.WorkspaceMembers) == 0 {
		paths = append(paths, rootManifest)
	}
	for _, member := range meta.WorkspaceMembers {
		memberManifests, warning := resolveWorkspaceMemberManifestPaths(repoPath, member)
		if warning != "" {
			warnings = append(warnings, warning)
		}
		paths = append(paths, memberManifests...)
	}
	return uniquePaths(paths), dedupeWarnings(warnings), nil
}

func resolveWorkspaceMemberManifestPaths(repoPath, member string) ([]string, string) {
	memberRoots := resolveWorkspaceMembers(repoPath, member)
	if len(memberRoots) == 0 {
		return nil, fmt.Sprintf("workspace member pattern %q did not resolve to a Cargo.toml", member)
	}
	paths := make([]string, 0, len(memberRoots))
	for _, root := range memberRoots {
		paths = append(paths, filepath.Join(root, cargoTomlName))
	}
	return paths, ""
}

func discoverManifestsByWalk(repoPath string) ([]string, []string, error) {
	paths := make([]string, 0)
	warnings := make([]string, 0)
	count := 0
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(entry.Name(), cargoTomlName) {
			return nil
		}
		count++
		if count > maxManifestCount {
			return fs.SkipAll
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil && err != fs.SkipAll {
		return nil, nil, err
	}
	if count > maxManifestCount {
		warnings = append(warnings, "cargo manifest discovery capped at 256 manifests")
	}
	if len(paths) == 0 {
		warnings = append(warnings, "no Cargo.toml files found for analysis")
	}
	return uniquePaths(paths), dedupeWarnings(warnings), nil
}

func resolveWorkspaceMembers(repoPath, pattern string) []string {
	glob := filepath.Join(repoPath, pattern)
	matches, err := filepath.Glob(glob)
	if err != nil {
		return nil
	}
	roots := make(map[string]struct{})
	for _, match := range matches {
		match = filepath.Clean(match)
		info, statErr := os.Stat(match)
		if statErr != nil || !info.IsDir() {
			continue
		}
		if !isSubPath(repoPath, match) {
			continue
		}
		manifest := filepath.Join(match, cargoTomlName)
		if _, manifestErr := os.Stat(manifest); manifestErr != nil {
			continue
		}
		roots[match] = struct{}{}
	}
	return shared.SortedKeys(roots)
}

func parseCargoManifest(manifestPath, repoPath string) (manifestMeta, map[string]dependencyInfo, error) {
	content, err := safeio.ReadFileUnder(repoPath, manifestPath)
	if err != nil {
		return manifestMeta{}, nil, err
	}
	return parseCargoManifestContent(string(content)), parseCargoDependencies(string(content)), nil
}

func parseCargoManifestContent(content string) manifestMeta {
	meta := manifestMeta{}
	inWorkspaceMembers := false
	consumeTomlContent(
		content,
		func(section string) {
			inWorkspaceMembers = false
			markManifestPackageSection(section, &meta)
		},
		func(section, clean string) {
			inWorkspaceMembers = parseWorkspaceMembersLine(clean, section, inWorkspaceMembers, &meta)
		},
	)
	meta.WorkspaceMembers = dedupeStrings(meta.WorkspaceMembers)
	return meta
}

func parseTomlSectionName(clean string) (string, bool) {
	match := tablePattern.FindStringSubmatch(clean)
	if len(match) != 2 {
		return "", false
	}
	return strings.ToLower(strings.TrimSpace(match[1])), true
}

func markManifestPackageSection(section string, meta *manifestMeta) {
	if section == "package" {
		meta.HasPackage = true
	}
}

func parseWorkspaceMembersLine(clean, section string, inWorkspaceMembers bool, meta *manifestMeta) bool {
	if section != "workspace" {
		return false
	}
	if inWorkspaceMembers {
		meta.WorkspaceMembers = append(meta.WorkspaceMembers, extractQuotedStrings(clean)...)
		return !strings.Contains(clean, "]")
	}
	right, ok := workspaceMembersAssignmentValue(clean)
	if !ok {
		return false
	}
	meta.WorkspaceMembers = append(meta.WorkspaceMembers, extractQuotedStrings(right)...)
	return strings.Contains(right, "[") && !strings.Contains(right, "]")
}

func workspaceMembersAssignmentValue(clean string) (string, bool) {
	eq := strings.Index(clean, "=")
	if eq < 0 {
		return "", false
	}
	key := strings.TrimSpace(clean[:eq])
	if key != workspaceFieldStart {
		return "", false
	}
	return strings.TrimSpace(clean[eq+1:]), true
}

func parseCargoDependencies(content string) map[string]dependencyInfo {
	deps := make(map[string]dependencyInfo)
	consumeTomlContent(content, nil, func(section, clean string) {
		addDependencyFromLine(deps, section, clean)
	})
	return deps
}

func consumeTomlContent(content string, onSection func(string), onLine func(section, clean string)) {
	section := ""
	for _, line := range strings.Split(content, "\n") {
		clean := strings.TrimSpace(stripTomlComment(line))
		if clean == "" {
			continue
		}
		if nextSection, isSection := parseTomlSectionName(clean); isSection {
			section = nextSection
			if onSection != nil {
				onSection(section)
			}
			continue
		}
		onLine(section, clean)
	}
}

func addDependencyFromLine(deps map[string]dependencyInfo, section, clean string) {
	if !isDependencySection(section) {
		return
	}
	alias, info, ok := parseDependencyInfo(clean)
	if !ok {
		return
	}
	deps[alias] = info
	ensureCanonicalDependencyAlias(deps, info)
}

func parseDependencyInfo(clean string) (string, dependencyInfo, bool) {
	key, value, ok := parseTomlAssignment(clean)
	if !ok {
		return "", dependencyInfo{}, false
	}
	alias := normalizeDependencyID(key)
	if alias == "" {
		return "", dependencyInfo{}, false
	}
	info := dependencyInfo{Canonical: alias}
	if strings.HasPrefix(strings.TrimSpace(value), "{") {
		applyInlineDependencyFields(value, alias, &info)
	}
	return alias, info, true
}

func applyInlineDependencyFields(value, alias string, info *dependencyInfo) {
	fields := parseInlineFields(value)
	if pkg, ok := fields["package"]; ok {
		info.Canonical = normalizeDependencyID(pkg)
		info.Renamed = info.Canonical != alias
	}
	if pathValue, ok := fields["path"]; ok && strings.TrimSpace(pathValue) != "" {
		info.LocalPath = true
	}
}

func ensureCanonicalDependencyAlias(deps map[string]dependencyInfo, info dependencyInfo) {
	if _, ok := deps[info.Canonical]; ok {
		return
	}
	deps[info.Canonical] = dependencyInfo{
		Canonical: info.Canonical,
		LocalPath: info.LocalPath,
	}
}

func isDependencySection(section string) bool {
	section = strings.ToLower(strings.TrimSpace(section))
	if section == "dependencies" || section == "dev-dependencies" || section == "build-dependencies" {
		return true
	}
	if strings.HasPrefix(section, "target.") {
		return strings.HasSuffix(section, ".dependencies") || strings.HasSuffix(section, ".dev-dependencies") || strings.HasSuffix(section, ".build-dependencies")
	}
	return false
}

func parseTomlAssignment(line string) (string, string, bool) {
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	key := strings.TrimSpace(parts[0])
	value := strings.TrimSpace(parts[1])
	key = strings.Trim(key, `"'`)
	if key == "" || value == "" {
		return "", "", false
	}
	return key, value, true
}

func parseInlineFields(value string) map[string]string {
	fields := make(map[string]string)
	for _, match := range stringFieldPattern.FindAllStringSubmatch(value, -1) {
		if len(match) != 3 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(match[1]))
		fields[key] = strings.TrimSpace(match[2])
	}
	return fields
}

func stripTomlComment(line string) string {
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

func extractQuotedStrings(value string) []string {
	results := make([]string, 0)
	current := strings.Builder{}
	inString := false
	quote := byte(0)
	for i := 0; i < len(value); i++ {
		ch := value[i]
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
			results = append(results, current.String())
			continue
		}
		current.WriteByte(ch)
	}
	return dedupeStrings(results)
}

func scanRepo(ctx context.Context, repoPath string, manifestPaths []string, depLookup map[string]dependencyInfo, renamedAliases map[string][]string) (scanResult, error) {
	result := scanResult{
		UnresolvedImports:   make(map[string]int),
		RenamedAliasesByDep: renamedAliases,
	}
	roots := scanRoots(manifestPaths, repoPath)
	scannedFiles := make(map[string]struct{})
	fileCount := 0
	for _, root := range roots {
		err := scanRepoRoot(ctx, repoPath, root, depLookup, scannedFiles, &fileCount, &result)
		if err != nil && err != fs.SkipAll {
			return scanResult{}, err
		}
	}
	result.Warnings = append(result.Warnings, compileScanWarnings(result)...)
	result.Warnings = dedupeWarnings(result.Warnings)
	return result, nil
}

func scanRoots(manifestPaths []string, repoPath string) []string {
	roots := make([]string, 0, len(manifestPaths))
	for _, manifestPath := range manifestPaths {
		roots = append(roots, filepath.Dir(manifestPath))
	}
	roots = uniquePaths(roots)
	if len(roots) == 0 {
		return []string{repoPath}
	}
	return roots
}

func scanRepoRoot(ctx context.Context, repoPath, root string, depLookup map[string]dependencyInfo, scannedFiles map[string]struct{}, fileCount *int, result *scanResult) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
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
		return scanRepoFileEntry(repoPath, root, path, depLookup, scannedFiles, fileCount, result)
	})
}

func scanRepoFileEntry(repoPath, root, path string, depLookup map[string]dependencyInfo, scannedFiles map[string]struct{}, fileCount *int, result *scanResult) error {
	if !strings.EqualFold(filepath.Ext(path), ".rs") {
		return nil
	}
	if _, ok := scannedFiles[path]; ok {
		return nil
	}
	scannedFiles[path] = struct{}{}

	(*fileCount)++
	if *fileCount > maxDetectionFiles {
		result.SkippedFilesByBoundLimit = true
		return fs.SkipAll
	}
	return scanRustSourceFile(repoPath, root, path, depLookup, result)
}

func compileScanWarnings(result scanResult) []string {
	warnings := make([]string, 0, 4+len(result.UnresolvedImports))
	if len(result.Files) == 0 {
		warnings = append(warnings, "no Rust source files found for analysis")
	}
	if result.SkippedLargeFiles > 0 {
		warnings = append(warnings, fmt.Sprintf("skipped %d Rust files larger than %d bytes", result.SkippedLargeFiles, maxScannableRustFile))
	}
	if result.SkippedFilesByBoundLimit {
		warnings = append(warnings, fmt.Sprintf("Rust source scanning capped at %d files", maxDetectionFiles))
	}
	if result.MacroAmbiguityDetected {
		warnings = append(warnings, "Rust macro invocations detected; static attribution may be partial for macro- and feature-driven paths")
	}
	return append(warnings, summarizeUnresolved(result.UnresolvedImports)...)
}

func scanRustSourceFile(repoPath string, crateRoot string, path string, depLookup map[string]dependencyInfo, result *scanResult) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() > maxScannableRustFile {
		result.SkippedLargeFiles++
		return nil
	}

	content, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		return err
	}
	relativePath, err := filepath.Rel(repoPath, path)
	if err != nil {
		relativePath = path
	}

	imports := parseRustImports(string(content), relativePath, crateRoot, depLookup, result)
	result.Files = append(result.Files, fileScan{
		Path:    relativePath,
		Imports: imports,
		Usage:   shared.CountUsage(content, imports),
	})
	if macroInvokePattern.Match(content) {
		result.MacroAmbiguityDetected = true
	}
	return nil
}

func parseRustImports(content, filePath, crateRoot string, depLookup map[string]dependencyInfo, scan *scanResult) []importBinding {
	imports := parseExternCrateImports(content, filePath, crateRoot, depLookup, scan)
	for _, idx := range useStmtPattern.FindAllStringSubmatchIndex(content, -1) {
		clause, line, column, ok := parseUseStatementIndex(content, idx)
		if !ok {
			continue
		}
		ctx := useImportContext{
			FilePath:  filePath,
			Line:      line,
			Column:    column,
			CrateRoot: crateRoot,
			DepLookup: depLookup,
			Scan:      scan,
		}
		imports = append(imports, appendUseClauseImports(clause, ctx)...)
	}
	return imports
}

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

type useImportContext struct {
	FilePath  string
	Line      int
	Column    int
	CrateRoot string
	DepLookup map[string]dependencyInfo
	Scan      *scanResult
}

func appendUseClauseImports(clause string, ctx useImportContext) []importBinding {
	imports := make([]importBinding, 0)
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

type usePathEntry struct {
	Path     string
	Symbol   string
	Local    string
	Wildcard bool
}

func parseExternCrateImports(content, filePath, crateRoot string, depLookup map[string]dependencyInfo, scan *scanResult) []importBinding {
	lines := strings.Split(content, "\n")
	imports := make([]importBinding, 0)
	for i, line := range lines {
		match := externCratePattern.FindStringSubmatch(line)
		if len(match) < 2 {
			continue
		}
		root := strings.TrimSpace(match[1])
		local := root
		if len(match) >= 3 && strings.TrimSpace(match[2]) != "" {
			local = strings.TrimSpace(match[2])
		}
		dependency := resolveDependency(root, crateRoot, depLookup, scan)
		if dependency == "" {
			continue
		}
		imports = append(imports, importBinding{
			Dependency: dependency,
			Module:     root,
			Name:       root,
			Local:      local,
			Location: report.Location{
				File:   filePath,
				Line:   i + 1,
				Column: shared.FirstContentColumn(line),
			},
		})
	}
	return imports
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
			if depth > 0 {
				depth--
			}
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

func resolveDependency(path string, crateRoot string, depLookup map[string]dependencyInfo, scan *scanResult) string {
	path = strings.TrimSpace(strings.TrimPrefix(path, "::"))
	if path == "" {
		return ""
	}
	root := strings.Split(path, "::")[0]
	normalizedRoot := normalizeDependencyID(root)
	if normalizedRoot == "" {
		return ""
	}
	if rustStdRoots[normalizedRoot] {
		return ""
	}
	if normalizedRoot == "crate" || normalizedRoot == "self" || normalizedRoot == "super" {
		return ""
	}
	if isLocalRustModule(crateRoot, root) {
		return ""
	}

	if info, ok := depLookup[normalizedRoot]; ok {
		if info.LocalPath {
			return ""
		}
		return info.Canonical
	}
	scan.UnresolvedImports[normalizedRoot]++
	return normalizedRoot
}

func isLocalRustModule(crateRoot, root string) bool {
	if crateRoot == "" || root == "" {
		return false
	}
	candidates := []string{
		filepath.Join(crateRoot, "src", root+".rs"),
		filepath.Join(crateRoot, "src", root, "mod.rs"),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return true
		}
	}
	return false
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

func buildRequestedRustDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	minUsageThreshold := resolveMinUsageRecommendationThreshold(req.MinUsagePercentForRecommendations)
	switch {
	case req.Dependency != "":
		dependency := normalizeDependencyID(req.Dependency)
		depReport := buildDependencyReport(dependency, scan, minUsageThreshold)
		return []report.DependencyReport{depReport}, nil
	case req.TopN > 0:
		return buildTopRustDependencies(req.TopN, scan, minUsageThreshold)
	default:
		return nil, []string{"no dependency or top-N target provided"}
	}
}

func buildTopRustDependencies(topN int, scan scanResult, minUsageThreshold int) ([]report.DependencyReport, []string) {
	fileUsages := shared.MapFileUsages(
		scan.Files,
		func(file fileScan) []shared.ImportRecord { return file.Imports },
		func(file fileScan) map[string]int { return file.Usage },
	)
	dependencies := shared.ListDependencies(fileUsages, normalizeDependencyID)
	return shared.BuildTopReports(topN, dependencies, func(dependency string) (report.DependencyReport, []string) {
		return buildDependencyReport(dependency, scan, minUsageThreshold), nil
	})
}

func buildDependencyReport(dependency string, scan scanResult, minUsageThreshold int) report.DependencyReport {
	stats := shared.BuildDependencyStats(
		dependency,
		shared.MapFileUsages(
			scan.Files,
			func(file fileScan) []shared.ImportRecord { return file.Imports },
			func(file fileScan) map[string]int { return file.Usage },
		),
		normalizeDependencyID,
	)
	dep := report.DependencyReport{
		Language:             "rust",
		Name:                 dependency,
		UsedExportsCount:     stats.UsedCount,
		TotalExportsCount:    stats.TotalCount,
		UsedPercent:          stats.UsedPercent,
		EstimatedUnusedBytes: 0,
		TopUsedSymbols:       stats.TopSymbols,
		UsedImports:          stats.UsedImports,
		UnusedImports:        stats.UnusedImports,
	}
	if stats.WildcardImports > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "broad-imports",
			Severity: "medium",
			Message:  "found broad wildcard imports; prefer narrower symbol imports",
		})
		if dep.UsedPercent > 0 && dep.UsedPercent < float64(minUsageThreshold) {
			dep.Recommendations = append(dep.Recommendations, report.Recommendation{
				Code:      "prefer-explicit-imports",
				Priority:  "medium",
				Message:   "Replace wildcard imports with explicit symbol imports for better precision.",
				Rationale: "Explicit imports improve maintainability and reduce over-coupling.",
			})
		}
	}
	if aliases := scan.RenamedAliasesByDep[dependency]; len(aliases) > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "renamed-crate",
			Severity: "low",
			Message:  "crate is imported via alias/package rename in Cargo.toml",
		})
		dep.Recommendations = append(dep.Recommendations, report.Recommendation{
			Code:      "document-crate-rename",
			Priority:  "low",
			Message:   "Document crate rename mappings to avoid attribution confusion.",
			Rationale: "Renamed crates can hide real package identity in usage reports.",
		})
	}
	if scan.MacroAmbiguityDetected && len(dep.UsedImports) > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "macro-ambiguity",
			Severity: "low",
			Message:  "macro-heavy usage may reduce static import attribution precision",
		})
	}
	if dep.TotalExportsCount > 0 && dep.UsedPercent > 0 && dep.UsedPercent < float64(minUsageThreshold) {
		dep.Recommendations = append(dep.Recommendations, report.Recommendation{
			Code:      "reduce-rust-surface-area",
			Priority:  "low",
			Message:   fmt.Sprintf("Only %.1f%% of %q imports appear used; consider tightening imports or lighter alternatives.", dep.UsedPercent, dependency),
			Rationale: "Low observed usage often indicates avoidable dependency surface area.",
		})
	}
	if len(dep.UsedImports) == 0 && len(dep.UnusedImports) > 0 {
		dep.Recommendations = append(dep.Recommendations, report.Recommendation{
			Code:      "remove-unused-dependency",
			Priority:  "high",
			Message:   "No used imports were detected for this dependency; consider removing it.",
			Rationale: "Unused dependencies increase attack and maintenance surface.",
		})
	}
	sort.Slice(dep.RiskCues, func(i, j int) bool { return dep.RiskCues[i].Code < dep.RiskCues[j].Code })
	sort.Slice(dep.Recommendations, func(i, j int) bool {
		left := recommendationPriorityRank(dep.Recommendations[i].Priority)
		right := recommendationPriorityRank(dep.Recommendations[j].Priority)
		if left == right {
			return dep.Recommendations[i].Code < dep.Recommendations[j].Code
		}
		return left < right
	})
	return dep
}

func resolveMinUsageRecommendationThreshold(value *int) int {
	if value != nil {
		return *value
	}
	return thresholds.Defaults().MinUsagePercentForRecommendations
}

func summarizeUnresolved(unresolved map[string]int) []string {
	if len(unresolved) == 0 {
		return nil
	}
	type unresolvedItem struct {
		dep   string
		count int
	}
	items := make([]unresolvedItem, 0, len(unresolved))
	for dep, count := range unresolved {
		items = append(items, unresolvedItem{dep: dep, count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count == items[j].count {
			return items[i].dep < items[j].dep
		}
		return items[i].count > items[j].count
	})
	if len(items) > maxWarningSamples {
		items = items[:maxWarningSamples]
	}
	warnings := make([]string, 0, len(items))
	for _, item := range items {
		warnings = append(warnings, fmt.Sprintf("could not resolve Rust crate alias %q from Cargo manifests", item.dep))
	}
	return warnings
}

func normalizeDependencyID(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.ReplaceAll(value, "_", "-")
	return value
}

func shouldSkipDir(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".idea", "node_modules", "vendor", "target", "dist", "build", ".artifacts":
		return true
	default:
		return false
	}
}

func uniquePaths(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = filepath.Clean(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func dedupeWarnings(warnings []string) []string {
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
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func isSubPath(root, candidate string) bool {
	rootAbs, rootErr := filepath.Abs(root)
	candidateAbs, candidateErr := filepath.Abs(candidate)
	if rootErr != nil || candidateErr != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, candidateAbs)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func samePath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return filepath.Clean(left) == filepath.Clean(right)
	}
	return filepath.Clean(leftAbs) == filepath.Clean(rightAbs)
}

func lastPathSegment(path string) string {
	path = strings.TrimSpace(strings.TrimPrefix(path, "::"))
	if path == "" {
		return ""
	}
	parts := strings.Split(path, "::")
	return strings.TrimSpace(parts[len(parts)-1])
}

func recommendationPriorityRank(priority string) int {
	switch priority {
	case "high":
		return 0
	case "medium":
		return 1
	default:
		return 2
	}
}

var rustStdRoots = map[string]bool{
	"alloc":      true,
	"core":       true,
	"proc-macro": true,
	"std":        true,
	"test":       true,
}
