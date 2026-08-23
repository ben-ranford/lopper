package php

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

var errPHPShortOpenTagConfigWalkLimit = errors.New("php short_open_tag config discovery limit exceeded")

type composerData struct {
	DeclaredDependencies map[string]struct{}
	NamespaceToDep       map[string]string
	LocalNamespaces      map[string]struct{}
	UsageIncomplete      bool
	ShortOpenTags        bool
	ShortOpenTagPolicy   phpShortOpenTagPolicy
}

type composerManifest struct {
	Name        string            `json:"name"`
	Require     map[string]string `json:"require"`
	RequireDev  map[string]string `json:"require-dev"`
	Autoload    composerAutoload  `json:"autoload"`
	AutoloadDev composerAutoload  `json:"autoload-dev"`
}

type composerAutoload struct {
	PSR4 map[string]any `json:"psr-4"`
}

type composerLock struct {
	Packages    []composerPackage `json:"packages"`
	PackagesDev []composerPackage `json:"packages-dev"`
}

type composerPackage struct {
	Name     string           `json:"name"`
	Autoload composerAutoload `json:"autoload"`
}

func loadComposerData(repoPath string) (composerData, []string, error) {
	return loadComposerDataWithContext(context.Background(), repoPath)
}

func loadComposerDataWithContext(ctx context.Context, repoPath string) (composerData, []string, error) {
	return loadComposerDataWithContextAndExcludedDirectories(ctx, repoPath, nil)
}

func loadComposerDataWithContextAndExcludedDirectories(ctx context.Context, repoPath string, excludedDirectories map[string]struct{}) (composerData, []string, error) {
	data := composerData{
		DeclaredDependencies: make(map[string]struct{}),
		NamespaceToDep:       make(map[string]string),
		LocalNamespaces:      make(map[string]struct{}),
	}
	warnings := make([]string, 0)

	manifest, hasManifest, err := readComposerManifest(repoPath)
	if err != nil {
		return data, nil, err
	}
	if !hasManifest {
		warnings = append(warnings, "composer.json not found in analysis root")
	}
	if hasManifest {
		collectDeclaredDependencies(manifest, data.DeclaredDependencies)
		collectLocalNamespaces(manifest, data.LocalNamespaces)
	}
	shortOpenTagPolicy, shortOpenTagWarnings, err := detectPHPShortOpenTagsWithExcludedDirectories(ctx, repoPath, excludedDirectories)
	if err != nil {
		return data, nil, err
	}
	data.ShortOpenTags = shortOpenTagPolicy.anyEnabled()
	data.ShortOpenTagPolicy = shortOpenTagPolicy
	if shortOpenTagPolicy.discoveryIncomplete {
		data.UsageIncomplete = true
	}
	warnings = append(warnings, shortOpenTagWarnings...)

	if err := loadComposerLockMappings(repoPath, &data); isPureOversizedFileError(err) {
		data.UsageIncomplete = true
		warnings = append(warnings, fmt.Sprintf("skipped composer.lock because it exceeds %d bytes", maxComposerLockBytes))
	} else if err != nil {
		return data, nil, err
	}
	return data, warnings, nil
}

func readComposerManifest(repoPath string) (composerManifest, bool, error) {
	bytes, found, err := readOptionalRepoFile(repoPath, composerJSONName)
	if err != nil {
		return composerManifest{}, false, err
	}
	if !found {
		return composerManifest{}, false, nil
	}
	manifest := composerManifest{}
	if err := unmarshalRepoJSON(composerJSONName, bytes, &manifest); err != nil {
		return composerManifest{}, false, err
	}
	return manifest, true, nil
}

func collectDeclaredDependencies(manifest composerManifest, out map[string]struct{}) {
	for name := range manifest.Require {
		if dep, ok := normalizeComposerDependency(name); ok {
			out[dep] = struct{}{}
		}
	}
	for name := range manifest.RequireDev {
		if dep, ok := normalizeComposerDependency(name); ok {
			out[dep] = struct{}{}
		}
	}
}

func collectLocalNamespaces(manifest composerManifest, out map[string]struct{}) {
	for namespace := range manifest.Autoload.PSR4 {
		out[normalizeNamespace(namespace)] = struct{}{}
	}
	for namespace := range manifest.AutoloadDev.PSR4 {
		out[normalizeNamespace(namespace)] = struct{}{}
	}
}

func normalizeComposerDependency(name string) (string, bool) {
	return NormalizeComposerDependency(name)
}

// NormalizeComposerDependency normalizes installable Composer package names.
func NormalizeComposerDependency(name string) (string, bool) {
	name = normalizeDependencyID(name)
	switch name {
	case "", "php", "hhvm", "composer", "composer-plugin-api", "composer-runtime-api":
		return "", false
	}
	if !strings.Contains(name, "/") && (strings.HasPrefix(name, "php-") || strings.HasPrefix(name, "ext-") || strings.HasPrefix(name, "lib-")) {
		return "", false
	}
	return name, true
}

func loadComposerLockMappings(repoPath string, data *composerData) error {
	bytes, found, err := readOptionalRepoFile(repoPath, composerLockName)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	lock := composerLock{}
	if err := unmarshalRepoJSON(composerLockName, bytes, &lock); err != nil {
		return err
	}
	for _, pkg := range append(lock.Packages, lock.PackagesDev...) {
		dep := normalizeDependencyID(pkg.Name)
		if dep == "" {
			continue
		}
		for namespace := range pkg.Autoload.PSR4 {
			normalized := normalizeNamespace(namespace)
			if normalized == "" {
				continue
			}
			data.NamespaceToDep[normalized] = dep
		}
	}
	return nil
}

func readOptionalRepoFile(repoPath, filename string) ([]byte, bool, error) {
	path := filepath.Join(repoPath, filename)
	bytes, err := safeio.ReadFileUnderLimit(repoPath, path, composerInputByteLimit(filename))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return bytes, true, nil
}

func composerInputByteLimit(filename string) int64 {
	if filename == composerLockName {
		return maxComposerLockBytes
	}
	return maxComposerManifestBytes
}

func unmarshalRepoJSON(filename string, bytes []byte, dest any) error {
	if err := json.Unmarshal(bytes, dest); err != nil {
		return fmt.Errorf("parse %s: %w", filename, err)
	}
	return nil
}

func isPureOversizedFileError(err error) bool {
	return shared.IsPureSentinelError(err, safeio.ErrFileTooLarge)
}

type phpShortOpenTagPolicy struct {
	dirSettings         map[string]phpShortOpenTagDirSetting
	incompleteDirs      map[string]int
	discoveryIncomplete bool
}

type phpShortOpenTagDirSetting struct {
	enabled  bool
	priority int
}

func detectPHPShortOpenTagsWithExcludedDirectories(ctx context.Context, repoPath string, excludedDirectories map[string]struct{}) (phpShortOpenTagPolicy, []string, error) {
	policy := phpShortOpenTagPolicy{
		dirSettings:    make(map[string]phpShortOpenTagDirSetting),
		incompleteDirs: make(map[string]int),
	}
	warnings := make([]string, 0)
	root := filepath.Clean(repoPath)
	state := phpShortOpenTagConfigWalkState{root: root, excludedDirectories: excludedDirectories}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		return state.scan(ctx, path, entry, walkErr, &policy, &warnings)
	})
	if errors.Is(err, errPHPShortOpenTagConfigWalkLimit) {
		policy.discoveryIncomplete = true
		warnings = append(warnings, phpShortOpenTagConfigWalkLimitWarning())
		return policy, warnings, nil
	}
	if err != nil {
		return phpShortOpenTagPolicy{}, nil, err
	}
	return policy, warnings, nil
}

type phpShortOpenTagConfigWalkState struct {
	root                string
	excludedDirectories map[string]struct{}
	visited             int
}

func (s *phpShortOpenTagConfigWalkState) scan(ctx context.Context, path string, entry fs.DirEntry, walkErr error, policy *phpShortOpenTagPolicy, warnings *[]string) error {
	if err := shared.WalkContextErr(ctx, walkErr); err != nil {
		return err
	}
	if path != s.root && (isExcludedDirectory(s.excludedDirectories, path) || entry.IsDir() && (shouldSkipDir(entry.Name()) || hasComposerManifest(path))) {
		return filepath.SkipDir
	}
	if path != s.root {
		s.visited++
		if s.visited > maxPHPConfigWalkEntries {
			return errPHPShortOpenTagConfigWalkLimit
		}
	}
	return scanPHPShortOpenTagConfigEntry(s.root, path, entry, nil, policy, warnings)
}

func phpShortOpenTagConfigWalkLimitWarning() string {
	return fmt.Sprintf("PHP short_open_tag config discovery stopped after %d traversal entries to keep analysis bounded; dependency usage may be incomplete", maxPHPConfigWalkEntries)
}

func scanPHPShortOpenTagConfigEntry(root, path string, entry fs.DirEntry, walkErr error, policy *phpShortOpenTagPolicy, warnings *[]string) error {
	if walkErr != nil {
		return walkErr
	}
	if entry.IsDir() {
		return scanPHPShortOpenTagConfigDir(root, path, entry)
	}
	priority := phpConfigFilePriority(entry.Name())
	if priority == 0 {
		return nil
	}
	return scanPHPShortOpenTagConfigFile(root, path, priority, policy, warnings)
}

func scanPHPShortOpenTagConfigDir(root, path string, entry fs.DirEntry) error {
	if path == root {
		return nil
	}
	if shouldSkipDir(entry.Name()) || hasComposerManifest(path) {
		return filepath.SkipDir
	}
	return nil
}

func scanPHPShortOpenTagConfigFile(root, path string, priority int, policy *phpShortOpenTagPolicy, warnings *[]string) error {
	enabled, found, incomplete, err := phpConfigShortOpenTagSetting(root, path)
	if isPureOversizedFileError(err) {
		policy.setIncompleteDir(filepath.Dir(path), priority)
		*warnings = append(*warnings, phpShortOpenTagConfigOversizedWarning(root, path))
		return nil
	}
	if err != nil {
		return err
	}
	if incomplete {
		policy.setIncompleteDir(filepath.Dir(path), priority)
		*warnings = append(*warnings, phpShortOpenTagConfigUnresolvedWarning(root, path))
		return nil
	}
	if found {
		policy.setDirSetting(filepath.Dir(path), enabled, priority)
	}
	return nil
}

func phpShortOpenTagConfigOversizedWarning(root, path string) string {
	relPath, err := filepath.Rel(root, path)
	if err != nil {
		relPath = path
	}
	return fmt.Sprintf("skipped PHP short_open_tag config %s because it exceeds %d bytes", relPath, maxPHPConfigBytes)
}

func phpShortOpenTagConfigUnresolvedWarning(root, path string) string {
	relPath, err := filepath.Rel(root, path)
	if err != nil {
		relPath = path
	}
	return fmt.Sprintf("could not resolve PHP short_open_tag config %s; dependency usage may be incomplete", relPath)
}

func (p *phpShortOpenTagPolicy) anyEnabled() bool {
	for _, setting := range p.dirSettings {
		if setting.enabled {
			return true
		}
	}
	return false
}

func (p *phpShortOpenTagPolicy) hasSettings() bool {
	return len(p.dirSettings) > 0 || len(p.incompleteDirs) > 0
}

func (p *phpShortOpenTagPolicy) enabledForFile(path string) bool {
	dir := filepath.Dir(filepath.Clean(path))
	for {
		if setting, ok := p.dirSettings[dir]; ok {
			return setting.enabled
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

func (p *phpShortOpenTagPolicy) incompleteForFile(path string) bool {
	dir := filepath.Dir(filepath.Clean(path))
	for {
		setting, hasSetting := p.dirSettings[dir]
		incompletePriority, hasIncompleteSetting := p.incompleteDirs[dir]
		if hasIncompleteSetting && (!hasSetting || incompletePriority >= setting.priority) {
			return true
		}
		if hasSetting {
			return false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

func (p *phpShortOpenTagPolicy) setIncompleteDir(dir string, priority int) {
	if existing, ok := p.incompleteDirs[dir]; ok && existing > priority {
		return
	}
	p.incompleteDirs[dir] = priority
}

func (p *phpShortOpenTagPolicy) setDirSetting(dir string, enabled bool, priority int) {
	if existing, ok := p.dirSettings[dir]; ok && existing.priority > priority {
		return
	}
	p.dirSettings[dir] = phpShortOpenTagDirSetting{enabled: enabled, priority: priority}
}

func phpConfigFilePriority(filename string) int {
	switch filename {
	case "php.ini":
		return 1
	case ".user.ini":
		return 2
	case ".htaccess":
		return 3
	default:
		return 0
	}
}

func phpConfigShortOpenTagSetting(repoPath, path string) (bool, bool, bool, error) {
	bytes, err := safeio.ReadFileUnderLimit(repoPath, path, maxPHPConfigBytes)
	if err != nil {
		if os.IsNotExist(err) {
			return false, false, false, nil
		}
		return false, false, false, err
	}
	enabled, found, incomplete := parseShortOpenTagSetting(string(bytes))
	return enabled, found, incomplete, nil
}

func parsesShortOpenTagEnabled(content string) bool {
	enabled, found, _ := parseShortOpenTagSetting(content)
	return found && enabled
}

func parseShortOpenTagSetting(content string) (bool, bool, bool) {
	enabled := false
	found := false
	incomplete := false
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "php_value") || strings.HasPrefix(lower, "php_flag") {
			fields := strings.Fields(lower)
			if len(fields) >= 3 && fields[1] == "short_open_tag" {
				enabled, found, incomplete = phpConfigBooleanSetting(fields[2])
			}
			continue
		}
		key, value, ok := strings.Cut(lower, "=")
		if !ok || strings.TrimSpace(key) != "short_open_tag" {
			continue
		}
		enabled, found, incomplete = phpConfigBooleanSetting(value)
	}
	return enabled, found, incomplete
}

func phpConfigBooleanSetting(value string) (bool, bool, bool) {
	if comment := strings.IndexAny(value, ";#"); comment >= 0 {
		value = value[:comment]
	}
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"'`)
	switch value {
	case "1", "on", "true", "yes":
		return true, true, false
	case "", "0", "off", "false", "no", "none":
		return false, true, false
	default:
		return false, false, true
	}
}
