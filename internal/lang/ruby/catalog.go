package ruby

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func loadBundlerDependencies(repoPath string, out map[string]struct{}) error {
	return loadBundlerDependenciesWithSources(repoPath, out, nil)
}

func loadBundlerDependenciesWithSources(repoPath string, out map[string]struct{}, sources map[string]rubyDependencySource) error {
	if err := loadGemfileDependencies(repoPath, out, sources); err != nil {
		return err
	}
	return loadGemfileLockDependencies(repoPath, out, sources)
}

func loadDeclaredDependencies(ctx context.Context, repoPath string, out map[string]struct{}, sources map[string]rubyDependencySource) ([]string, error) {
	if err := loadBundlerDependenciesWithSources(repoPath, out, sources); err != nil {
		return nil, err
	}
	return loadGemspecDependencies(ctx, repoPath, out)
}

func loadGemfileDependencies(repoPath string, out map[string]struct{}, sources map[string]rubyDependencySource) error {
	content, err := readBundlerFile(repoPath, gemfileName)
	if err != nil {
		return err
	}
	if len(content) == 0 {
		return nil
	}
	for _, line := range strings.Split(string(content), "\n") {
		dependency, kind, ok := parseGemfileDependencyLine(line)
		if !ok {
			continue
		}
		addRubyDependency(out, sources, dependency, kind, gemfileName)
	}
	return nil
}

func loadGemfileLockDependencies(repoPath string, out map[string]struct{}, sources map[string]rubyDependencySource) error {
	content, err := readBundlerFile(repoPath, gemfileLockName)
	if err != nil {
		return err
	}
	if len(content) == 0 {
		return nil
	}
	for _, line := range strings.Split(string(content), "\n") {
		matches := gemSpecPattern.FindStringSubmatch(line)
		if len(matches) != 2 {
			continue
		}
		if dependency := normalizeDependencyID(matches[1]); dependency != "" {
			out[dependency] = struct{}{}
		}
	}
	parseGemfileLockSourceAttribution(content, out, sources)
	return nil
}

func parseGemfileDependencyLine(line string) (string, string, bool) {
	line = shared.StripLineComment(line, "#")
	matches := gemDeclarationPattern.FindStringSubmatch(line)
	if len(matches) != 2 {
		return "", "", false
	}
	dependency := normalizeDependencyID(matches[1])
	if dependency == "" {
		return "", "", false
	}
	kind := rubyDependencySourceRubygems
	switch {
	case gemPathOptionPattern.MatchString(line):
		kind = rubyDependencySourcePath
	case gemGitOptionPattern.MatchString(line):
		kind = rubyDependencySourceGit
	}
	return dependency, kind, true
}

func parseGemfileLockSourceAttribution(content []byte, out map[string]struct{}, sources map[string]rubyDependencySource) {
	if sources == nil || len(content) == 0 {
		return
	}
	state := gemfileLockSourceAttributionState{}
	for _, rawLine := range strings.Split(string(content), "\n") {
		applyGemfileLockSourceAttributionLine(rawLine, &state, out, sources)
	}
}

type gemfileLockSourceAttributionState struct {
	currentKind string
	inSpecs     bool
}

func applyGemfileLockSourceAttributionLine(rawLine string, state *gemfileLockSourceAttributionState, out map[string]struct{}, sources map[string]rubyDependencySource) {
	line := strings.TrimRight(rawLine, "\r")
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return
	}
	if !isGemfileLockTopLevelLine(line) {
		applyGemfileLockDependencyEntry(line, state, out, sources)
		return
	}

	state.currentKind = parseGemfileLockSection(trimmed)
	state.inSpecs = false
}

func isGemfileLockTopLevelLine(line string) bool {
	return !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t")
}

func parseGemfileLockSection(line string) string {
	switch line {
	case rubyGemfileSectionGem:
		return rubyDependencySourceRubygems
	case rubyGemfileSectionGit:
		return rubyDependencySourceGit
	case rubyGemfileSectionPath:
		return rubyDependencySourcePath
	default:
		return ""
	}
}

func applyGemfileLockDependencyEntry(line string, state *gemfileLockSourceAttributionState, out map[string]struct{}, sources map[string]rubyDependencySource) {
	trimmed := strings.TrimSpace(line)
	if trimmed == rubyGemfileSpecsSection {
		state.inSpecs = state.currentKind != ""
		return
	}
	if !state.inSpecs {
		return
	}
	matches := gemTopLevelSpecPattern.FindStringSubmatch(line)
	if len(matches) != 2 {
		return
	}
	addRubyDependency(out, sources, normalizeDependencyID(matches[1]), state.currentKind, gemfileLockName)
}

func addRubyDependency(out map[string]struct{}, sources map[string]rubyDependencySource, dependency, kind, signal string) {
	if dependency == "" {
		return
	}
	if out != nil {
		out[dependency] = struct{}{}
	}
	if sources == nil {
		return
	}
	info := sources[dependency]
	switch kind {
	case rubyDependencySourceRubygems:
		info.Rubygems = true
	case rubyDependencySourceGit:
		info.Git = true
	case rubyDependencySourcePath:
		info.Path = true
	}
	switch signal {
	case gemfileName:
		info.DeclaredGemfile = true
	case gemfileLockName:
		info.DeclaredLock = true
	}
	sources[dependency] = info
}

func readBundlerFile(repoPath, filename string) ([]byte, error) {
	targetPath := filepath.Join(repoPath, filename)
	content, err := safeio.ReadFileUnder(repoPath, targetPath)
	switch {
	case err == nil:
		return content, nil
	case errors.Is(err, os.ErrNotExist):
		return nil, nil
	default:
		return nil, fmt.Errorf("read %s: %w", filename, err)
	}
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
