package ruby

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func loadDeclaredDependencies(ctx context.Context, repoPath string, out map[string]struct{}, sources map[string]rubyDependencySource) ([]string, []report.CoverageGap, error) {
	if err := loadBundlerDependenciesWithSources(repoPath, out, sources); err != nil {
		return nil, nil, err
	}
	return loadGemspecDependencies(ctx, repoPath, out)
}

func loadGemspecDependencies(ctx context.Context, repoPath string, out map[string]struct{}) ([]string, []report.CoverageGap, error) {
	var (
		warnings     []string
		coverageGaps []report.CoverageGap
	)
	err := walkRubyRepoFiles(ctx, repoPath, func(path string, entry fs.DirEntry) error {
		if !strings.EqualFold(filepath.Ext(entry.Name()), gemspecExt) {
			return nil
		}
		relPath, relErr := filepath.Rel(repoPath, path)
		if relErr != nil {
			relPath = entry.Name()
		}
		displayPath := filepath.ToSlash(relPath)
		content, err := safeio.ReadFileUnderLimit(repoPath, path, maxGemspecBytes)
		if shared.IsPureSentinelError(err, safeio.ErrFileTooLarge) {
			warning := fmt.Sprintf("skipped %s because it exceeds %d bytes", displayPath, maxGemspecBytes)
			warnings = append(warnings, warning)
			coverageGaps = append(coverageGaps, report.CoverageGap{
				Code:     report.CoverageGapRubyOversizedGemspec,
				Language: "ruby",
				Path:     displayPath,
				Evidence: []string{warning},
			})
			return nil
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		fileWarnings := parseGemspecDependencies(content, displayPath, out)
		warnings = append(warnings, fileWarnings...)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return warnings, coverageGaps, nil
}

func parseGemspecDependencies(content []byte, filePath string, out map[string]struct{}) []string {
	var warnings []string
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), maxGemspecBytes+1)
	for index := 0; scanner.Scan(); index++ {
		line := scanner.Text()
		line = shared.StripLineComment(line, "#")
		if !gemspecDependencyLineSignal.MatchString(line) {
			continue
		}
		matches := gemspecDependencyPattern.FindStringSubmatch(line)
		if len(matches) != 2 {
			warnings = append(warnings, fmt.Sprintf("could not confidently parse gemspec dependency declaration in %s:%d", filePath, index+1))
			continue
		}
		if dependency := normalizeDependencyID(matches[1]); dependency != "" {
			out[dependency] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		warnings = append(warnings, fmt.Sprintf("could not scan gemspec dependency declarations in %s: %v", filePath, err))
	}
	return warnings
}
