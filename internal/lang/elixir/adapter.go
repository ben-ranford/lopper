package elixir

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type Adapter struct {
	language.AdapterLifecycle
}

const (
	mixExsName     = "mix.exs"
	mixLockName    = "mix.lock"
	maxDetectFiles = 1200
	maxScanFiles   = 2400
)

var (
	importPattern  = regexp.MustCompile(`(?m)^[ \t]*(alias|import|use|require)[ \t]+([A-Z][A-Za-z0-9_]*(?:\.[A-Z][A-Za-z0-9_]*)*)`)
	aliasAsPattern = regexp.MustCompile(`\bas:\s*([A-Z][A-Za-z0-9_]*)\b`)
	appsPathRegex  = regexp.MustCompile(`apps_path:\s*["']([^"']+)["']`)
	quotedDepKey   = regexp.MustCompile(`"([a-z0-9_-]+)"\s*:`)
	depsPattern    = regexp.MustCompile(`\{\s*:([a-zA-Z0-9_]+)\s*,`)
)

func NewAdapter() *Adapter {
	adapter := &Adapter{}
	adapter.AdapterLifecycle = language.NewAdapterLifecycle("elixir", []string{"ex", "mix"}, adapter.DetectWithConfidence)
	return adapter
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
		if umbrella, appsPath := detectUmbrellaAppsPath(content); umbrella {
			umbrellaOnly = true
			if err := addUmbrellaRoots(repoPath, appsPath, roots); err != nil {
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

func detectUmbrellaAppsPath(content []byte) (bool, string) {
	raw := stripElixirComments(content)
	if !strings.Contains(raw, "apps_path:") {
		return false, ""
	}
	matches := appsPathRegex.FindStringSubmatch(raw)
	if len(matches) >= 2 {
		path := strings.TrimSpace(matches[1])
		if path != "" {
			return true, path
		}
	}
	return true, "apps"
}

func stripElixirComments(content []byte) string {
	var stripped strings.Builder
	stripped.Grow(len(content))

	state := elixirCommentState{}

	for i := 0; i < len(content); i++ {
		ch := content[i]

		if state.writeEscaped(&stripped, ch) {
			continue
		}

		if state.writeEscape(&stripped, ch) {
			continue
		}

		if state.writeQuote(&stripped, ch) {
			continue
		}

		if ch == '#' {
			if state.inQuotedString() {
				stripped.WriteByte(ch)
				continue
			}
			i = skipElixirComment(content, i, &stripped)
			continue
		}

		stripped.WriteByte(ch)
	}
	return stripped.String()
}

type elixirCommentState struct {
	inSingleQuote bool
	inDoubleQuote bool
	escaped       bool
}

func (s *elixirCommentState) inQuotedString() bool {
	return s.inSingleQuote || s.inDoubleQuote
}

func (s *elixirCommentState) writeEscaped(out *strings.Builder, ch byte) bool {
	if !s.escaped {
		return false
	}
	out.WriteByte(ch)
	s.escaped = false
	return true
}

func (s *elixirCommentState) writeEscape(out *strings.Builder, ch byte) bool {
	if ch != '\\' || !s.inQuotedString() {
		return false
	}
	out.WriteByte(ch)
	s.escaped = true
	return true
}

func (s *elixirCommentState) writeQuote(out *strings.Builder, ch byte) bool {
	switch ch {
	case '"':
		if !s.inSingleQuote {
			s.inDoubleQuote = !s.inDoubleQuote
		}
	case '\'':
		if !s.inDoubleQuote {
			s.inSingleQuote = !s.inSingleQuote
		}
	default:
		return false
	}
	out.WriteByte(ch)
	return true
}

func skipElixirComment(content []byte, start int, out *strings.Builder) int {
	i := start
	for i < len(content) && content[i] != '\n' {
		i++
	}
	if i < len(content) {
		out.WriteByte('\n')
	}
	return i
}

func addUmbrellaRoots(repoPath string, appsPath string, roots map[string]struct{}) error {
	appsRoot := filepath.Join(repoPath, appsPath)
	if !shared.IsPathWithin(repoPath, appsRoot) {
		return nil
	}
	apps, err := filepath.Glob(filepath.Join(appsRoot, "*"))
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
	sanitized := maskElixirImportSource(content)
	matches := importPattern.FindAllSubmatchIndex(sanitized, -1)
	records := make([]shared.ImportRecord, 0, len(matches))
	for _, idx := range matches {
		keywordStart := idx[2]
		keyword := strings.TrimSpace(string(content[idx[2]:idx[3]]))
		module := strings.TrimSpace(string(content[idx[4]:idx[5]]))
		dependency := dependencyFromModule(module, declared)
		if dependency == "" {
			continue
		}
		line := 1 + strings.Count(string(content[:keywordStart]), "\n")
		local := module
		if parts := strings.Split(module, "."); len(parts) > 0 {
			local = parts[len(parts)-1]
		}
		if keyword == "alias" {
			if aliasLocal := parseAliasLocal(lineBytes(content, keywordStart)); aliasLocal != "" {
				local = aliasLocal
			}
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

type elixirImportMaskState struct {
	inSingleQuote   bool
	inDoubleQuote   bool
	inSingleHeredoc bool
	inDoubleHeredoc bool
	escaped         bool
}

func maskElixirImportSource(content []byte) []byte {
	sanitized := make([]byte, len(content))
	copy(sanitized, content)
	state := elixirImportMaskState{}

	for i := 0; i < len(content); i++ {
		ch := content[i]

		if state.inDoubleHeredoc {
			maskElixirSourceByte(sanitized, i)
			if isElixirTripleQuote(content, i, '"') {
				maskElixirSourceByte(sanitized, i+1)
				maskElixirSourceByte(sanitized, i+2)
				state.inDoubleHeredoc = false
				i += 2
			}
			continue
		}

		if state.inSingleHeredoc {
			maskElixirSourceByte(sanitized, i)
			if isElixirTripleQuote(content, i, '\'') {
				maskElixirSourceByte(sanitized, i+1)
				maskElixirSourceByte(sanitized, i+2)
				state.inSingleHeredoc = false
				i += 2
			}
			continue
		}

		if state.inDoubleQuote {
			maskElixirSourceByte(sanitized, i)
			if state.escaped {
				state.escaped = false
				continue
			}
			switch ch {
			case '\\':
				state.escaped = true
			case '"':
				state.inDoubleQuote = false
			}
			continue
		}

		if state.inSingleQuote {
			maskElixirSourceByte(sanitized, i)
			if state.escaped {
				state.escaped = false
				continue
			}
			switch ch {
			case '\\':
				state.escaped = true
			case '\'':
				state.inSingleQuote = false
			}
			continue
		}

		if isElixirTripleQuote(content, i, '"') {
			maskElixirSourceByte(sanitized, i)
			maskElixirSourceByte(sanitized, i+1)
			maskElixirSourceByte(sanitized, i+2)
			state.inDoubleHeredoc = true
			i += 2
			continue
		}

		if isElixirTripleQuote(content, i, '\'') {
			maskElixirSourceByte(sanitized, i)
			maskElixirSourceByte(sanitized, i+1)
			maskElixirSourceByte(sanitized, i+2)
			state.inSingleHeredoc = true
			i += 2
			continue
		}

		switch ch {
		case '"':
			maskElixirSourceByte(sanitized, i)
			state.inDoubleQuote = true
		case '\'':
			maskElixirSourceByte(sanitized, i)
			state.inSingleQuote = true
		}
	}

	return sanitized
}

func isElixirTripleQuote(content []byte, index int, quote byte) bool {
	return index+2 < len(content) && content[index] == quote && content[index+1] == quote && content[index+2] == quote
}

func maskElixirSourceByte(content []byte, index int) {
	if index < 0 || index >= len(content) {
		return
	}
	if content[index] != '\n' {
		content[index] = ' '
	}
}

func lineBytes(content []byte, start int) []byte {
	if start < 0 || start >= len(content) {
		return nil
	}
	line := content[start:]
	if i := bytes.IndexByte(line, '\n'); i >= 0 {
		return line[:i]
	}
	return line
}

func parseAliasLocal(line []byte) string {
	matches := aliasAsPattern.FindSubmatch(line)
	if len(matches) < 2 {
		return ""
	}
	return strings.TrimSpace(string(matches[1]))
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
