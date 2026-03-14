package kotlinandroid

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	repoPath = shared.DefaultRepoPath(repoPath)

	detection := language.Detection{}
	roots := make(map[string]struct{})
	androidSpecificSignal := false
	if err := applyKotlinAndroidRootSignals(repoPath, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	const maxFiles = 1200
	visited := 0
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return walkKotlinAndroidDetectionEntry(path, entry, roots, &detection, &visited, maxFiles, &androidSpecificSignal)
	})
	if err != nil && err != fs.SkipAll {
		return language.Detection{}, err
	}

	if !androidSpecificSignal {
		detection.Matched = false
		clear(roots)
	}
	pruneKotlinAndroidRoots(repoPath, roots)
	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func walkKotlinAndroidDetectionEntry(path string, entry fs.DirEntry, roots map[string]struct{}, detection *language.Detection, visited *int, maxFiles int, androidSpecificSignal *bool) error {
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}
	(*visited)++
	if *visited > maxFiles {
		return fs.SkipAll
	}
	updateKotlinAndroidDetection(path, entry, roots, detection, androidSpecificSignal)
	return nil
}

func applyKotlinAndroidRootSignals(repoPath string, detection *language.Detection, roots map[string]struct{}) error {
	signals := []shared.RootSignal{
		{Name: buildGradleName, Confidence: 45},
		{Name: buildGradleKTSName, Confidence: 45},
		{Name: settingsGradleName, Confidence: 30},
		{Name: settingsGradleKTS, Confidence: 30},
		{Name: gradleLockfileName, Confidence: 25},
	}
	return shared.ApplyRootSignals(repoPath, signals, detection, roots)
}

func updateKotlinAndroidDetection(path string, entry fs.DirEntry, roots map[string]struct{}, detection *language.Detection, androidSpecificSignal *bool) {
	name := strings.ToLower(entry.Name())
	switch name {
	case buildGradleName, buildGradleKTSName:
		detection.Matched = true
		detection.Confidence += 12
		roots[filepath.Dir(path)] = struct{}{}
		if buildFileSignalsAndroidPlugin(path) {
			markAndroidSpecificDetection(detection, androidSpecificSignal)
		}
	case settingsGradleName, settingsGradleKTS:
		detection.Matched = true
		detection.Confidence += 8
		roots[filepath.Dir(path)] = struct{}{}
	case gradleLockfileName:
		detection.Matched = true
		detection.Confidence += 10
		roots[filepath.Dir(path)] = struct{}{}
	case androidManifestName:
		markAndroidSpecificDetection(detection, androidSpecificSignal)
		if root := androidManifestModuleRoot(path); root != "" {
			roots[root] = struct{}{}
		} else {
			roots[filepath.Dir(path)] = struct{}{}
		}
	}

	switch strings.ToLower(filepath.Ext(path)) {
	case ".java", ".kt":
		detection.Matched = true
		detection.Confidence += 2
		if root := sourceLayoutModuleRoot(path); root != "" {
			roots[root] = struct{}{}
		}
	}
}

func markAndroidSpecificDetection(detection *language.Detection, androidSpecificSignal *bool) {
	detection.Matched = true
	detection.Confidence += 18
	if androidSpecificSignal != nil {
		*androidSpecificSignal = true
	}
}

func buildFileSignalsAndroidPlugin(path string) bool {
	content, err := safeio.ReadFile(path)
	if err != nil {
		return false
	}
	buildFile := strings.ToLower(string(content))
	for _, marker := range androidBuildPluginMarkers {
		if strings.Contains(buildFile, marker) {
			return true
		}
	}
	return false
}

func androidManifestModuleRoot(path string) string {
	normalized := filepath.ToSlash(filepath.Clean(path))
	if normalized == "" {
		return ""
	}
	parts := strings.Split(normalized, "/")
	if len(parts) < 4 {
		return ""
	}
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] != "src" || parts[i+1] != "main" {
			continue
		}
		if strings.ToLower(parts[i+2]) != "androidmanifest.xml" {
			continue
		}
		if i == 0 {
			return ""
		}
		root := strings.Join(parts[:i], "/")
		if root == "" {
			return ""
		}
		return filepath.FromSlash(root)
	}
	return ""
}

func sourceLayoutModuleRoot(path string) string {
	normalized := filepath.ToSlash(filepath.Clean(path))
	if normalized == "" {
		return ""
	}
	parts := strings.Split(normalized, "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] != "src" || parts[i+1] != "main" {
			continue
		}
		if !isAndroidSourceLayout(parts[i+2]) {
			continue
		}
		root := strings.Join(parts[:i], "/")
		if root == "" {
			return ""
		}
		return filepath.FromSlash(root)
	}
	return ""
}

func isAndroidSourceLayout(name string) bool {
	return name == "java" || name == "kotlin"
}

func pruneKotlinAndroidRoots(repoPath string, roots map[string]struct{}) {
	if len(roots) <= 1 {
		return
	}
	repoPath = filepath.Clean(repoPath)
	if _, ok := roots[repoPath]; !ok {
		return
	}
	hasNestedModuleRoot := false
	for root := range roots {
		if filepath.Clean(root) == repoPath {
			continue
		}
		if !isSubPath(repoPath, root) {
			continue
		}
		if !hasGradleBuildFile(root) {
			continue
		}
		hasNestedModuleRoot = true
		break
	}
	if !hasNestedModuleRoot {
		return
	}
	if shouldKeepRepoRootForPackageAnalysis(repoPath) {
		return
	}
	delete(roots, repoPath)
}

func shouldKeepRepoRootForPackageAnalysis(repoPath string) bool {
	if !hasGradleBuildFile(repoPath) {
		return false
	}
	if hasRootGradleDependencyDeclarations(repoPath) {
		return true
	}
	return hasRootSourceLayout(repoPath)
}

func hasRootGradleDependencyDeclarations(repoPath string) bool {
	for _, fileName := range []string{buildGradleName, buildGradleKTSName} {
		path := filepath.Join(repoPath, fileName)
		content, err := safeio.ReadFileUnder(repoPath, path)
		if err != nil {
			continue
		}
		if len(parseGradleDependencyContent(string(content))) > 0 {
			return true
		}
	}
	return false
}

func hasRootSourceLayout(repoPath string) bool {
	srcRoot := filepath.Join(repoPath, "src")
	found := false
	err := filepath.WalkDir(srcRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if isSourceFile(path) {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	if err != nil && err != fs.SkipAll {
		return false
	}
	return found
}

func hasGradleBuildFile(root string) bool {
	for _, fileName := range []string{buildGradleName, buildGradleKTSName} {
		path := filepath.Join(root, fileName)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			return true
		}
	}
	return false
}

func isSubPath(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if rel == "." || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
