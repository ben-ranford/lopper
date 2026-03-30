package cpp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	vcpkgManifestFile = "vcpkg.json"
	vcpkgLockFile     = "vcpkg-lock.json"
	conanManifestFile = "conanfile.txt"
	conanLockFile     = "conan.lock"
	maxManifestFiles  = 64
	parseWarningFmt   = "failed to parse %s: %v"
)

type dependencyCatalog struct {
	Declarations map[string]declaredDependency
}

type declaredDependency struct {
	Sources map[string]struct{}
}

type vcpkgManifest struct {
	Dependencies []any `json:"dependencies"`
}

func newDependencyCatalog() dependencyCatalog {
	return dependencyCatalog{Declarations: make(map[string]declaredDependency)}
}

func (c *dependencyCatalog) add(dependency, source string) {
	dependency = normalizeCPPDependencyID(dependency)
	if dependency == "" || source == "" {
		return
	}
	current := c.Declarations[dependency]
	if current.Sources == nil {
		current.Sources = make(map[string]struct{})
	}
	current.Sources[source] = struct{}{}
	c.Declarations[dependency] = current
}

func (c *dependencyCatalog) contains(dependency string) bool {
	_, ok := c.Declarations[normalizeCPPDependencyID(dependency)]
	return ok
}

func (c *dependencyCatalog) list() []string {
	items := make([]string, 0, len(c.Declarations))
	for dependency := range c.Declarations {
		items = append(items, dependency)
	}
	sort.Strings(items)
	return items
}

func (c *dependencyCatalog) sources(dependency string) []string {
	current, ok := c.Declarations[normalizeCPPDependencyID(dependency)]
	if !ok {
		return nil
	}
	items := make([]string, 0, len(current.Sources))
	for source := range current.Sources {
		items = append(items, source)
	}
	sort.Strings(items)
	return items
}

func loadDependencyCatalog(repoPath string) (dependencyCatalog, []string, error) {
	catalog := newDependencyCatalog()
	warnings := make([]string, 0)
	manifestCount := 0

	err := shared.WalkRepoFiles(context.Background(), repoPath, 0, shared.ShouldSkipCommonDir, func(path string, entry fs.DirEntry) error {
		switch filepath.Base(path) {
		case vcpkgManifestFile, vcpkgLockFile, conanManifestFile, conanLockFile:
			manifestCount++
			if manifestCount > maxManifestFiles {
				return fs.SkipAll
			}
		default:
			return nil
		}

		currentWarnings, err := loadDependencyManifest(repoPath, path, &catalog)
		warnings = append(warnings, currentWarnings...)
		return err
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return catalog, warnings, err
	}

	return catalog, dedupeCPPWarnings(warnings), nil
}

func loadDependencyManifest(repoPath, path string, catalog *dependencyCatalog) ([]string, error) {
	relPath := relOrBase(repoPath, path)
	content, err := safeio.ReadFileUnder(repoPath, path)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		return nil, nil
	default:
		return nil, fmt.Errorf("read %s: %w", relPath, err)
	}

	var (
		dependencies []string
		warnings     []string
		source       string
	)

	switch filepath.Base(path) {
	case vcpkgManifestFile:
		dependencies, warnings = parseVcpkgManifest(content)
		source = "vcpkg manifest"
	case vcpkgLockFile:
		dependencies, warnings = parseVcpkgLock(content)
		source = "vcpkg lockfile"
	case conanManifestFile:
		dependencies, warnings = parseConanfileTxt(content)
		source = "conanfile.txt"
	case conanLockFile:
		dependencies, warnings = parseConanLock(content)
		source = "conan.lock"
	default:
		return nil, nil
	}

	for _, dependency := range dependencies {
		catalog.add(dependency, source)
	}
	for i, warning := range warnings {
		warnings[i] = fmt.Sprintf("%s: %s", relPath, warning)
	}
	return warnings, nil
}

func parseVcpkgManifest(content []byte) ([]string, []string) {
	if len(content) == 0 {
		return nil, nil
	}

	var manifest vcpkgManifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		return nil, []string{fmt.Sprintf(parseWarningFmt, vcpkgManifestFile, err)}
	}

	dependencies := make(map[string]struct{})
	for _, item := range manifest.Dependencies {
		addVcpkgDependency(item, dependencies)
	}
	return shared.SortedKeys(dependencies), nil
}

func addVcpkgDependency(value any, out map[string]struct{}) {
	switch current := value.(type) {
	case string:
		if dependency := normalizeCPPDependencyID(current); dependency != "" {
			out[dependency] = struct{}{}
		}
	case map[string]any:
		if name, ok := current["name"].(string); ok {
			if dependency := normalizeCPPDependencyID(name); dependency != "" {
				out[dependency] = struct{}{}
			}
		}
	}
}

func parseVcpkgLock(content []byte) ([]string, []string) {
	return parseJSONDependencyLock(content, vcpkgLockFile, collectVcpkgLockDependencies)
}

func collectVcpkgLockDependencies(value any, out map[string]struct{}) {
	switch current := value.(type) {
	case []any:
		collectDependencySlice(current, out, collectVcpkgLockDependencies)
	case map[string]any:
		collectVcpkgLockMap(current, out)
	}
}

func collectVcpkgLockMap(values map[string]any, out map[string]struct{}) {
	addNormalizedDependency(valueAsString(values["name"]), out)
	for key, item := range values {
		if isVcpkgContainerKey(key) {
			collectVcpkgLockContainer(item, out)
			continue
		}
		collectVcpkgLockDependencies(item, out)
	}
}

func collectVcpkgLockContainer(value any, out map[string]struct{}) {
	switch current := value.(type) {
	case []any:
		collectDependencySlice(current, out, collectVcpkgLockDependencies)
	case map[string]any:
		for name, child := range current {
			addNormalizedDependency(name, out)
			collectVcpkgLockDependencies(child, out)
		}
	}
}

func isVcpkgContainerKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "dependencies", "packages", "ports":
		return true
	default:
		return false
	}
}

func parseConanfileTxt(content []byte) ([]string, []string) {
	if len(content) == 0 {
		return nil, nil
	}

	dependencies := make(map[string]struct{})
	currentSection := ""
	for _, rawLine := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentSection = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		if currentSection != "requires" && currentSection != "test_requires" {
			continue
		}
		if dependency := dependencyFromConanReference(line); dependency != "" {
			dependencies[dependency] = struct{}{}
		}
	}
	return shared.SortedKeys(dependencies), nil
}

func parseConanLock(content []byte) ([]string, []string) {
	return parseJSONDependencyLock(content, conanLockFile, collectConanLockDependencies)
}

func parseJSONDependencyLock(content []byte, filename string, collect func(any, map[string]struct{})) ([]string, []string) {
	if len(content) == 0 {
		return nil, nil
	}

	var payload any
	if err := json.Unmarshal(content, &payload); err != nil {
		return nil, []string{fmt.Sprintf(parseWarningFmt, filename, err)}
	}

	dependencies := make(map[string]struct{})
	collect(payload, dependencies)
	return shared.SortedKeys(dependencies), nil
}

func collectConanLockDependencies(value any, out map[string]struct{}) {
	switch current := value.(type) {
	case []any:
		collectDependencySlice(current, out, collectConanLockDependencies)
	case map[string]any:
		collectConanLockMap(current, out)
	}
}

func collectConanLockMap(values map[string]any, out map[string]struct{}) {
	addConanReference(valueAsString(values["ref"]), out)
	for key, item := range values {
		if isConanRefKey(key) {
			continue
		}
		collectConanLockDependencies(item, out)
	}
}

func dependencyFromConanReference(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	if comment := strings.Index(value, " #"); comment >= 0 {
		value = strings.TrimSpace(value[:comment])
	}
	value = strings.TrimPrefix(value, "&:")

	slash := strings.IndexByte(value, '/')
	if slash <= 0 {
		return ""
	}
	return normalizeCPPDependencyID(value[:slash])
}

func normalizeCPPDependencyID(value string) string {
	value = shared.NormalizeDependencyID(strings.TrimSpace(value))
	value = strings.Trim(value, "\"'")
	return strings.TrimSpace(value)
}

func correlateDeclaredDependency(token string, catalog dependencyCatalog) string {
	token = normalizeCPPDependencyID(token)
	if token == "" || len(catalog.Declarations) == 0 {
		return token
	}
	if catalog.contains(token) {
		return token
	}

	match := ""
	for dependency := range catalog.Declarations {
		if !strings.HasPrefix(dependency, token+"-") && !strings.HasPrefix(dependency, token+"_") {
			continue
		}
		if match != "" {
			return token
		}
		match = dependency
	}
	if match != "" {
		return match
	}
	return token
}

func collectDependencySlice(items []any, out map[string]struct{}, visit func(any, map[string]struct{})) {
	for _, item := range items {
		visit(item, out)
	}
}

func addNormalizedDependency(value string, out map[string]struct{}) {
	if dependency := normalizeCPPDependencyID(value); dependency != "" {
		out[dependency] = struct{}{}
	}
}

func addConanReference(value string, out map[string]struct{}) {
	if dependency := dependencyFromConanReference(value); dependency != "" {
		out[dependency] = struct{}{}
	}
}

func valueAsString(value any) string {
	current, _ := value.(string)
	return current
}

func isConanRefKey(key string) bool {
	return strings.EqualFold(strings.TrimSpace(key), "ref")
}

func dedupeCPPWarnings(warnings []string) []string {
	seen := make(map[string]struct{})
	items := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		warning = strings.TrimSpace(warning)
		if warning == "" {
			continue
		}
		if _, ok := seen[warning]; ok {
			continue
		}
		seen[warning] = struct{}{}
		items = append(items, warning)
	}
	return items
}
