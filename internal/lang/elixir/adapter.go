package elixir

import (
	"context"
	"fmt"
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
	mixExsName     = "mix.exs"
	mixLockName    = "mix.lock"
	maxDetectFiles = 1200
	maxScanFiles   = 2400
)

var (
	importPattern = regexp.MustCompile(`(?m)^\s*(?:alias|import|use|require)\s+([A-Z][A-Za-z0-9_]*(?:\.[A-Z][A-Za-z0-9_]*)*)`)
	quotedDepKey  = regexp.MustCompile(`"([a-z0-9_-]+)"\s*:`)
	depsPattern   = regexp.MustCompile(`\{\s*:([a-zA-Z0-9_]+)\s*,`)
)

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
	detection, err := a.DetectWithConfidence(ctx, repoPath)
	if err != nil {
		return false, err
	}
	return detection.Matched, nil
}

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	repoPath = shared.DefaultRepoPath(repoPath)
	detection, roots := newDetectionState()

	umbrellaOnly, err := detectFromRootFiles(repoPath, &detection, roots)
	if err != nil {
		return language.Detection{}, err
	}
	err = shared.WalkRepoFiles(ctx, repoPath, maxDetectFiles, shouldSkipDir, func(path string, _ os.DirEntry) error {
		switch strings.ToLower(filepath.Base(path)) {
		case mixExsName:
			detection.Matched = true
			detection.Confidence += 10
			dir := filepath.Dir(path)
			if umbrellaOnly && samePath(dir, repoPath) {
				return nil
			}
			roots[dir] = struct{}{}
		case mixLockName:
			detection.Matched = true
			detection.Confidence += 8
		default:
			ext := strings.ToLower(filepath.Ext(path))
			if ext == ".ex" || ext == ".exs" {
				detection.Matched = true
				detection.Confidence += 2
			}
		}
		return nil
	})
	if err != nil {
		return language.Detection{}, err
	}
	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func newDetectionState() (language.Detection, map[string]struct{}) {
	return language.Detection{}, make(map[string]struct{})
}

func detectFromRootFiles(repoPath string, detection *language.Detection, roots map[string]struct{}) (bool, error) {
	umbrellaOnly := false
	mixPath := filepath.Join(repoPath, mixExsName)
	if _, err := os.Stat(mixPath); err == nil {
		detection.Matched = true
		detection.Confidence += 55
		content, readErr := safeio.ReadFileUnder(repoPath, mixPath)
		if readErr != nil {
			return false, readErr
		}
		if strings.Contains(string(content), "apps_path:") {
			umbrellaOnly = true
			if err := addUmbrellaRoots(repoPath, roots); err != nil {
				return false, err
			}
		} else {
			roots[repoPath] = struct{}{}
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if _, err := os.Stat(filepath.Join(repoPath, mixLockName)); err == nil {
		detection.Matched = true
		detection.Confidence += 20
		if !umbrellaOnly {
			roots[repoPath] = struct{}{}
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	return umbrellaOnly, nil
}

func addUmbrellaRoots(repoPath string, roots map[string]struct{}) error {
	apps, err := filepath.Glob(filepath.Join(repoPath, "apps", "*"))
	if err != nil {
		return err
	}
	for _, app := range apps {
		info, statErr := os.Stat(app)
		if statErr != nil || !info.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(app, mixExsName)); err == nil {
			roots[filepath.Clean(app)] = struct{}{}
		}
	}
	return nil
}

func (a *Adapter) Analyse(ctx context.Context, req language.Request) (report.Report, error) {
	repoPath, err := workspace.NormalizeRepoPath(req.RepoPath)
	if err != nil {
		return report.Report{}, err
	}
	declared, err := loadDeclaredDependencies(repoPath)
	if err != nil {
		return report.Report{}, err
	}
	scan, err := scanElixirRepo(ctx, repoPath, declared)
	if err != nil {
		return report.Report{}, err
	}
	dependencies, warnings := buildRequestedDependencies(req, scan)
	return report.Report{
		GeneratedAt:   a.Clock(),
		RepoPath:      repoPath,
		Dependencies:  dependencies,
		Warnings:      warnings,
		Summary:       report.ComputeSummary(dependencies),
		SchemaVersion: report.SchemaVersion,
	}, nil
}

type scanResult struct {
	files    []shared.FileUsage
	declared map[string]struct{}
}

func scanElixirRepo(ctx context.Context, repoPath string, declared map[string]struct{}) (scanResult, error) {
	result := scanResult{declared: declared}
	err := shared.WalkRepoFiles(ctx, repoPath, maxScanFiles, shouldSkipDir, func(path string, _ os.DirEntry) error {
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".ex" && ext != ".exs" {
			return nil
		}
		content, err := safeio.ReadFileUnder(repoPath, path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(repoPath, path)
		if err != nil {
			relative = path
		}
		imports := parseImports(content, relative, declared)
		result.files = append(result.files, shared.FileUsage{
			Imports: imports,
			Usage:   shared.CountUsage(content, imports),
		})
		return nil
	})
	return result, err
}

func parseImports(content []byte, filePath string, declared map[string]struct{}) []shared.ImportRecord {
	matches := importPattern.FindAllSubmatchIndex(content, -1)
	records := make([]shared.ImportRecord, 0, len(matches))
	for _, idx := range matches {
		module := strings.TrimSpace(string(content[idx[2]:idx[3]]))
		dependency := dependencyFromModule(module, declared)
		if dependency == "" {
			continue
		}
		line := 1 + strings.Count(string(content[:idx[0]]), "\n")
		local := module
		if parts := strings.Split(module, "."); len(parts) > 0 {
			local = parts[len(parts)-1]
		}
		records = append(records, shared.ImportRecord{
			Dependency: dependency,
			Module:     module,
			Name:       module,
			Local:      local,
			Location:   report.Location{File: filePath, Line: line, Column: 1},
		})
	}
	return records
}

func dependencyFromModule(module string, declared map[string]struct{}) string {
	root := strings.Split(module, ".")[0]
	normalized := normalizeDependencyID(camelToSnake(root))
	if normalized == "" {
		return ""
	}
	if _, ok := declared[normalized]; ok {
		return normalized
	}
	alt := strings.ReplaceAll(normalized, "_", "-")
	if _, ok := declared[alt]; ok {
		return alt
	}
	return ""
}

func loadDeclaredDependencies(repoPath string) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	readAndCollect := func(path string, collect func([]byte)) error {
		content, err := safeio.ReadFileUnder(repoPath, path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		collect(content)
		return nil
	}
	if err := readAndCollect(filepath.Join(repoPath, mixLockName), func(content []byte) {
		for _, m := range quotedDepKey.FindAllSubmatch(content, -1) {
			result[normalizeDependencyID(string(m[1]))] = struct{}{}
		}
	}); err != nil {
		return nil, err
	}
	if err := readAndCollect(filepath.Join(repoPath, mixExsName), func(content []byte) {
		for _, m := range depsPattern.FindAllSubmatch(content, -1) {
			result[normalizeDependencyID(string(m[1]))] = struct{}{}
		}
	}); err != nil {
		return nil, err
	}
	return result, nil
}

func buildRequestedDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	buildReport := func(dep string) (report.DependencyReport, []string) {
		stats := shared.BuildDependencyStats(dep, scan.files, normalizeDependencyID)
		warnings := []string(nil)
		if !stats.HasImports {
			warnings = []string{fmt.Sprintf("no imports found for dependency %q", dep)}
		}
		return report.DependencyReport{
			Language:             "elixir",
			Name:                 dep,
			UsedExportsCount:     stats.UsedCount,
			TotalExportsCount:    stats.TotalCount,
			UsedPercent:          stats.UsedPercent,
			TopUsedSymbols:       stats.TopSymbols,
			UsedImports:          stats.UsedImports,
			UnusedImports:        stats.UnusedImports,
			EstimatedUnusedBytes: 0,
		}, warnings
	}
	topBuilder := func(topN int, _ scanResult, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
		set := make(map[string]struct{})
		for dep := range scan.declared {
			set[dep] = struct{}{}
		}
		for _, dep := range shared.ListDependencies(scan.files, normalizeDependencyID) {
			set[dep] = struct{}{}
		}
		return shared.BuildTopReports(topN, shared.SortedKeys(set), buildReport, weights)
	}
	buildDependency := func(dep string, _ scanResult) (report.DependencyReport, []string) {
		return buildReport(dep)
	}
	return shared.BuildRequestedDependenciesWithWeights(req, scan, normalizeDependencyID, buildDependency, resolveWeights, topBuilder)
}

func resolveWeights(value *report.RemovalCandidateWeights) report.RemovalCandidateWeights {
	if value == nil {
		return report.DefaultRemovalCandidateWeights()
	}
	return report.NormalizeRemovalCandidateWeights(*value)
}

func normalizeDependencyID(value string) string {
	return strings.ReplaceAll(shared.NormalizeDependencyID(value), "_", "-")
}

func camelToSnake(value string) string {
	var b strings.Builder
	runes := []rune(value)
	for i, r := range runes {
		if unicode.IsUpper(r) && i > 0 && (unicode.IsLower(runes[i-1]) || (i+1 < len(runes) && unicode.IsLower(runes[i+1]))) {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

func shouldSkipDir(name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case "_build", "deps", ".elixir_ls":
		return true
	default:
		return shared.ShouldSkipCommonDir(lower)
	}
}

func samePath(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}
