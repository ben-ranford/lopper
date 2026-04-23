package ruby

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func loadDeclaredDependencies(ctx context.Context, repoPath string, out map[string]struct{}, sources map[string]rubyDependencySource) ([]string, error) {
	if err := loadBundlerDependenciesWithSources(repoPath, out, sources); err != nil {
		return nil, err
	}
	return loadGemspecDependencies(ctx, repoPath, out)
}

func loadGemspecDependencies(ctx context.Context, repoPath string, out map[string]struct{}) ([]string, error) {
	var warnings []string
	err := walkRubyRepoFiles(ctx, repoPath, func(path string, entry fs.DirEntry) error {
		if !strings.EqualFold(filepath.Ext(entry.Name()), gemspecExt) {
			return nil
		}
		content, err := safeio.ReadFileUnder(repoPath, path)
		if err != nil {
			return fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		relPath, relErr := filepath.Rel(repoPath, path)
		if relErr != nil {
			relPath = entry.Name()
		}
		fileWarnings := parseGemspecDependencies(content, filepath.ToSlash(relPath), out)
		warnings = append(warnings, fileWarnings...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return warnings, nil
}

func parseGemspecDependencies(content []byte, filePath string, out map[string]struct{}) []string {
	lines := strings.Split(string(content), "\n")
	var warnings []string
	for index, line := range lines {
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
	return warnings
}
