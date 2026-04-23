package ruby

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

type bundlerDeclaration struct {
	dependency string
	kind       string
	signal     string
}

func loadBundlerDependencies(repoPath string, out map[string]struct{}) error {
	return loadBundlerDependenciesWithSources(repoPath, out, nil)
}

func loadBundlerDependenciesWithSources(repoPath string, out map[string]struct{}, sources map[string]rubyDependencySource) error {
	if err := loadGemfileDependencies(repoPath, out, sources); err != nil {
		return err
	}
	return loadGemfileLockDependencies(repoPath, out, sources)
}

func loadGemfileDependencies(repoPath string, out map[string]struct{}, sources map[string]rubyDependencySource) error {
	content, err := readBundlerFile(repoPath, gemfileName)
	if err != nil {
		return err
	}
	if len(content) == 0 {
		return nil
	}
	applyBundlerDeclarations(out, sources, parseGemfileDeclarations(content))
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
	for _, dependency := range parseGemfileLockDeclarations(content) {
		out[dependency] = struct{}{}
	}
	parseGemfileLockSourceAttribution(content, out, sources)
	return nil
}

func parseGemfileDeclarations(content []byte) []bundlerDeclaration {
	declarations := make([]bundlerDeclaration, 0)
	for _, line := range strings.Split(string(content), "\n") {
		dependency, kind, ok := parseGemfileDependencyLine(line)
		if !ok {
			continue
		}
		declarations = append(declarations, bundlerDeclaration{
			dependency: dependency,
			kind:       kind,
			signal:     gemfileName,
		})
	}
	return declarations
}

func parseGemfileLockDeclarations(content []byte) []string {
	dependencies := make([]string, 0)
	for _, line := range strings.Split(string(content), "\n") {
		matches := gemSpecPattern.FindStringSubmatch(line)
		if len(matches) != 2 {
			continue
		}
		if dependency := normalizeDependencyID(matches[1]); dependency != "" {
			dependencies = append(dependencies, dependency)
		}
	}
	return dependencies
}

func applyBundlerDeclarations(out map[string]struct{}, sources map[string]rubyDependencySource, declarations []bundlerDeclaration) {
	for _, declaration := range declarations {
		addRubyDependency(out, sources, declaration.dependency, declaration.kind, declaration.signal)
	}
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
	applyBundlerDeclarations(out, sources, parseGemfileLockSourceDeclarations(content))
}

func parseGemfileLockSourceDeclarations(content []byte) []bundlerDeclaration {
	state := gemfileLockSourceAttributionState{}
	for _, rawLine := range strings.Split(string(content), "\n") {
		applyGemfileLockSourceAttributionLine(rawLine, &state)
	}
	return state.declarations
}

type gemfileLockSourceAttributionState struct {
	currentKind  string
	inSpecs      bool
	declarations []bundlerDeclaration
}

func applyGemfileLockSourceAttributionLine(rawLine string, state *gemfileLockSourceAttributionState) {
	line := strings.TrimRight(rawLine, "\r")
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return
	}
	if !isGemfileLockTopLevelLine(line) {
		applyGemfileLockDependencyEntry(line, state)
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

func applyGemfileLockDependencyEntry(line string, state *gemfileLockSourceAttributionState) {
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
	state.declarations = append(state.declarations, bundlerDeclaration{
		dependency: normalizeDependencyID(matches[1]),
		kind:       state.currentKind,
		signal:     gemfileLockName,
	})
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
