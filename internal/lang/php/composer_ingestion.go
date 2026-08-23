package php

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

type composerData struct {
	DeclaredDependencies map[string]struct{}
	NamespaceToDep       map[string]string
	LocalNamespaces      map[string]struct{}
	UsageIncomplete      bool
	ShortOpenTags        bool
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
	data.ShortOpenTags = detectPHPShortOpenTags(repoPath)

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

func detectPHPShortOpenTags(repoPath string) bool {
	for _, filename := range []string{".user.ini", "php.ini", ".htaccess"} {
		if phpConfigEnablesShortOpenTags(repoPath, filename) {
			return true
		}
	}
	return false
}

func phpConfigEnablesShortOpenTags(repoPath, filename string) bool {
	path := filepath.Join(repoPath, filename)
	bytes, err := safeio.ReadFileUnderLimit(repoPath, path, maxPHPConfigBytes)
	if err != nil {
		return false
	}
	return parsesShortOpenTagEnabled(string(bytes))
}

func parsesShortOpenTagEnabled(content string) bool {
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "php_value") || strings.HasPrefix(lower, "php_flag") {
			fields := strings.Fields(lower)
			if len(fields) >= 3 && fields[1] == "short_open_tag" {
				return isPHPConfigTruthy(fields[2])
			}
			continue
		}
		key, value, ok := strings.Cut(lower, "=")
		if !ok || strings.TrimSpace(key) != "short_open_tag" {
			continue
		}
		return isPHPConfigTruthy(strings.TrimSpace(value))
	}
	return false
}

func isPHPConfigTruthy(value string) bool {
	if comment := strings.IndexAny(value, ";#"); comment >= 0 {
		value = value[:comment]
	}
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"'`)
	switch value {
	case "1", "on", "true", "yes":
		return true
	default:
		return false
	}
}
