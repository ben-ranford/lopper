package php

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

type composerData struct {
	DeclaredDependencies map[string]struct{}
	NamespaceToDep       map[string]string
	LocalNamespaces      map[string]struct{}
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

type composerResolver struct {
	namespaceToDep map[string]string
	localNamespace map[string]struct{}
	declared       map[string]struct{}
}

func newComposerResolver(data composerData) composerResolver {
	return composerResolver{
		namespaceToDep: data.NamespaceToDep,
		localNamespace: data.LocalNamespaces,
		declared:       data.DeclaredDependencies,
	}
}

func (r *composerResolver) dependencyFromModule(module string) (string, bool) {
	module = normalizeNamespace(module)
	if module == "" {
		return "", false
	}
	if r.isLocalNamespace(module) {
		return "", false
	}
	if dep := r.resolveWithPSR4(module); dep != "" {
		return dep, true
	}
	if dep := r.resolveByNamespaceHeuristic(module); dep != "" {
		return dep, true
	}
	return "", true
}

func (r *composerResolver) isLocalNamespace(module string) bool {
	for namespace := range r.localNamespace {
		if namespace == "" {
			continue
		}
		if module == namespace || strings.HasPrefix(module, namespace+`\`) {
			return true
		}
	}
	return false
}

func (r *composerResolver) resolveWithPSR4(module string) string {
	longest := ""
	selected := ""
	for prefix, dependency := range r.namespaceToDep {
		normalizedPrefix := normalizeNamespace(prefix)
		if normalizedPrefix == "" {
			continue
		}
		if module == normalizedPrefix || strings.HasPrefix(module, normalizedPrefix+`\`) {
			if len(normalizedPrefix) > len(longest) {
				longest = normalizedPrefix
				selected = dependency
			}
		}
	}
	return selected
}

func (r *composerResolver) resolveByNamespaceHeuristic(module string) string {
	parts := strings.Split(module, `\`)
	if len(parts) < 2 {
		return ""
	}
	vendor := strings.ToLower(strings.TrimSpace(parts[0]))
	name := normalizePackagePart(parts[1])
	if vendor == "" || name == "" {
		return ""
	}
	candidate := normalizeDependencyID(vendor + "/" + name)
	if _, ok := r.declared[candidate]; ok {
		return candidate
	}
	return ""
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

	if err := loadComposerLockMappings(repoPath, &data); err != nil {
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
	name = normalizeDependencyID(name)
	if name == "" || name == "php" {
		return "", false
	}
	if strings.HasPrefix(name, "ext-") || strings.HasPrefix(name, "lib-") {
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
	bytes, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return bytes, true, nil
}

func unmarshalRepoJSON(filename string, bytes []byte, dest any) error {
	if err := json.Unmarshal(bytes, dest); err != nil {
		return fmt.Errorf("parse %s: %w", filename, err)
	}
	return nil
}

func normalizeNamespace(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, `\`)
	value = strings.TrimSuffix(value, `\`)
	return value
}
