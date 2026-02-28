package elixir

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type Adapter struct {
	Clock func() time.Time
}

const (
	mixExsName      = "mix.exs"
	mixLockName     = "mix.lock"
	maxDetectFiles  = 1536
	maxScanFiles    = 3072
	maxScannableMix = 2 * 1024 * 1024
)

var (
	importPattern = regexp.MustCompile(`(?m)^\s*(?:alias|import|use|require)\s+([A-Z][A-Za-z0-9_]*(?:\.[A-Z][A-Za-z0-9_]*)*)`)
	lockKeyQuoted = regexp.MustCompile(`"([a-z0-9_-]+)"\s*:`)
	lockKeyAtom   = regexp.MustCompile(`:([a-z0-9_]+)\s*=>`)
	depsPattern   = regexp.MustCompile(`\{\s*:([a-zA-Z0-9_]+)\s*,`)
)

var elixirSkippedDirs = map[string]bool{
	"_build":     true,
	"deps":       true,
	".elixir_ls": true,
}

func NewAdapter() *Adapter {
	return &Adapter{Clock: time.Now}
}

func (a *Adapter) ID() string {
	return "elixir"
}

func (a *Adapter) Aliases() []string {
	return []string{"ex", "mix"}
}

func (a *Adapter) Detect(ctx context.Context, repoPath string) (bool, error) {
	return shared.DetectMatched(ctx, repoPath, a.DetectWithConfidence)
}

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	repoPath = shared.DefaultRepoPath(repoPath)
	detection := language.Detection{}
	roots := make(map[string]struct{})

	umbrellaOnly, err := applyMixRootSignals(repoPath, &detection, roots)
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
		return walkElixirDetectionEntry(path, entry, repoPath, umbrellaOnly, roots, &detection, &visited)
	})
	if err != nil && err != fs.SkipAll {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func applyMixRootSignals(repoPath string, detection *language.Detection, roots map[string]struct{}) (bool, error) {
	umbrellaOnly := false
	mixPath := filepath.Join(repoPath, mixExsName)
	if _, err := os.Stat(mixPath); err == nil {
		detection.Matched = true
		detection.Confidence += 58
		umbrella, readErr := isUmbrellaMixProject(repoPath, mixPath)
		if readErr != nil {
			return false, readErr
		}
		umbrellaOnly = umbrella
		if umbrella {
			if err := addUmbrellaRoots(repoPath, roots); err != nil {
				return false, err
			}
		} else {
			roots[repoPath] = struct{}{}
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}

	lockPath := filepath.Join(repoPath, mixLockName)
	if _, err := os.Stat(lockPath); err == nil {
		detection.Matched = true
		detection.Confidence += 22
		if !umbrellaOnly {
			roots[repoPath] = struct{}{}
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	return umbrellaOnly, nil
}

func addUmbrellaRoots(repoPath string, roots map[string]struct{}) error {
	pattern := filepath.Join(repoPath, "apps", "*")
	candidates, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		info, statErr := os.Stat(candidate)
		if statErr != nil || !info.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(candidate, mixExsName)); err != nil {
			continue
		}
		roots[candidate] = struct{}{}
	}
	return nil
}

func walkElixirDetectionEntry(path string, entry fs.DirEntry, repoPath string, umbrellaOnly bool, roots map[string]struct{}, detection *language.Detection, visited *int) error {
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}
	(*visited)++
	if *visited > maxDetectFiles {
		return fs.SkipAll
	}
	switch strings.ToLower(entry.Name()) {
	case mixExsName:
		detection.Matched = true
		detection.Confidence += 12
		dir := filepath.Dir(path)
		if umbrellaOnly && samePath(dir, repoPath) {
			return nil
		}
		roots[dir] = struct{}{}
	case mixLockName:
		detection.Matched = true
		detection.Confidence += 8
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".ex" || ext == ".exs" {
		detection.Matched = true
		detection.Confidence += 2
	}
	return nil
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

	declared, err := loadDeclaredDependencies(repoPath)
	if err != nil {
		return report.Report{}, err
	}
	scan, err := scanRepo(ctx, repoPath, declared)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, scan.Warnings...)

	dependencies, warnings := buildRequestedElixirDependencies(req, scan)
	result.Dependencies = dependencies
	result.Warnings = append(result.Warnings, warnings...)
	result.Summary = report.ComputeSummary(result.Dependencies)
	return result, nil
}

type fileScan struct {
	Imports []shared.ImportRecord
	Usage   map[string]int
}

type scanResult struct {
	Files    []fileScan
	Warnings []string
	Declared map[string]struct{}
}

func scanRepo(ctx context.Context, repoPath string, declared map[string]struct{}) (scanResult, error) {
	result := scanResult{Declared: declared}
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
		if visited > maxScanFiles {
			return fs.SkipAll
		}
		return scanElixirFile(repoPath, path, &result)
	})
	if err != nil && err != fs.SkipAll {
		return result, err
	}
	if len(result.Files) == 0 {
		result.Warnings = append(result.Warnings, "no Elixir source files found for analysis")
	}
	return result, nil
}

func scanElixirFile(repoPath, path string, result *scanResult) error {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".ex" && ext != ".exs" {
		return nil
	}
	content, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		return err
	}
	if len(content) > maxScannableMix {
		result.Warnings = append(result.Warnings, "skipping large Elixir source file: "+filepath.Base(path))
		return nil
	}
	relativePath, err := filepath.Rel(repoPath, path)
	if err != nil {
		relativePath = path
	}
	imports := parseImports(content, relativePath, result.Declared)
	result.Files = append(result.Files, fileScan{
		Imports: imports,
		Usage:   shared.CountUsage(content, imports),
	})
	return nil
}

func parseImports(content []byte, filePath string, declared map[string]struct{}) []shared.ImportRecord {
	matches := importPattern.FindAllSubmatchIndex(content, -1)
	records := make([]shared.ImportRecord, 0, len(matches))
	for _, idx := range matches {
		module := string(content[idx[2]:idx[3]])
		module = strings.TrimSpace(module)
		dependency := dependencyFromModule(module, declared)
		if dependency == "" {
			continue
		}
		local := localAlias(module)
		line := 1 + strings.Count(string(content[:idx[0]]), "\n")
		records = append(records, shared.ImportRecord{
			Dependency: dependency,
			Module:     module,
			Name:       module,
			Local:      local,
			Location: report.Location{
				File:   filePath,
				Line:   line,
				Column: 1,
			},
		})
	}
	return records
}

func dependencyFromModule(module string, declared map[string]struct{}) string {
	parts := strings.Split(module, ".")
	if len(parts) == 0 || parts[0] == "" {
		return ""
	}
	root := parts[0]
	candidates := []string{
		normalizeDependencyID(root),
		normalizeDependencyID(camelToSnake(root)),
		normalizeDependencyID(strings.ReplaceAll(camelToSnake(root), "_", "-")),
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if len(declared) == 0 {
			return candidate
		}
		if _, ok := declared[candidate]; ok {
			return candidate
		}
	}
	return ""
}

func camelToSnake(value string) string {
	if value == "" {
		return ""
	}
	var b strings.Builder
	for i, r := range value {
		if unicode.IsUpper(r) && i > 0 {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

func localAlias(module string) string {
	parts := strings.Split(module, ".")
	return parts[len(parts)-1]
}

func buildRequestedElixirDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	fileUsages := shared.MapFileUsages(scan.Files, func(file fileScan) []shared.ImportRecord { return file.Imports }, func(file fileScan) map[string]int { return file.Usage })
	build := func(dep string) (report.DependencyReport, []string) {
		stats := shared.BuildDependencyStats(dep, fileUsages, normalizeDependencyID)
		warnings := make([]string, 0)
		if !stats.HasImports {
			warnings = append(warnings, fmt.Sprintf("no imports found for dependency %q", dep))
		}
		return report.DependencyReport{
			Language:             "elixir",
			Name:                 dep,
			UsedExportsCount:     stats.UsedCount,
			TotalExportsCount:    stats.TotalCount,
			UsedPercent:          stats.UsedPercent,
			EstimatedUnusedBytes: 0,
			TopUsedSymbols:       stats.TopSymbols,
			UsedImports:          stats.UsedImports,
			UnusedImports:        stats.UnusedImports,
		}, warnings
	}
	topBuilder := func(topN int, _ scanResult, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
		set := make(map[string]struct{})
		for dep := range scan.Declared {
			set[dep] = struct{}{}
		}
		for _, dep := range shared.ListDependencies(fileUsages, normalizeDependencyID) {
			set[dep] = struct{}{}
		}
		deps := shared.SortedKeys(set)
		return shared.BuildTopReports(topN, deps, build, weights)
	}
	return shared.BuildRequestedDependenciesWithWeights(req, scan, normalizeDependencyID, func(dep string, _ scanResult) (report.DependencyReport, []string) {
		return build(dep)
	}, resolveRemovalCandidateWeights, topBuilder)
}

func resolveRemovalCandidateWeights(value *report.RemovalCandidateWeights) report.RemovalCandidateWeights {
	if value == nil {
		return report.DefaultRemovalCandidateWeights()
	}
	return report.NormalizeRemovalCandidateWeights(*value)
}

func loadDeclaredDependencies(repoPath string) (map[string]struct{}, error) {
	declared := make(map[string]struct{})
	lockPath := filepath.Join(repoPath, mixLockName)
	if content, err := safeio.ReadFileUnder(repoPath, lockPath); err == nil {
		for _, matches := range lockKeyQuoted.FindAllSubmatch(content, -1) {
			declared[normalizeDependencyID(string(matches[1]))] = struct{}{}
		}
		for _, matches := range lockKeyAtom.FindAllSubmatch(content, -1) {
			declared[normalizeDependencyID(strings.ReplaceAll(string(matches[1]), "_", "-"))] = struct{}{}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	mixPath := filepath.Join(repoPath, mixExsName)
	if content, err := safeio.ReadFileUnder(repoPath, mixPath); err == nil {
		for _, matches := range depsPattern.FindAllSubmatch(content, -1) {
			declared[normalizeDependencyID(strings.ReplaceAll(string(matches[1]), "_", "-"))] = struct{}{}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return declared, nil
}

func isUmbrellaMixProject(repoPath, mixPath string) (bool, error) {
	content, err := safeio.ReadFileUnder(repoPath, mixPath)
	if err != nil {
		return false, err
	}
	return strings.Contains(string(content), "apps_path:"), nil
}

func samePath(left, right string) bool {
	lc := filepath.Clean(left)
	rc := filepath.Clean(right)
	return lc == rc
}

func normalizeDependencyID(value string) string {
	return strings.ReplaceAll(shared.NormalizeDependencyID(value), "_", "-")
}

func shouldSkipDir(name string) bool {
	return shared.ShouldSkipDir(strings.ToLower(name), elixirSkippedDirs)
}
