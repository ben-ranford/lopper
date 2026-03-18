package dart

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
)

func collectManifestData(repoPath string) ([]packageManifest, []string, error) {
	manifestPaths, warnings, err := discoverPubspecPaths(repoPath)
	if err != nil {
		return nil, nil, err
	}

	manifests := make([]packageManifest, 0, len(manifestPaths))
	for _, manifestPath := range manifestPaths {
		manifest, manifestWarnings, loadErr := loadPackageManifest(repoPath, manifestPath)
		if loadErr != nil {
			return nil, nil, loadErr
		}
		manifests = append(manifests, manifest)
		warnings = append(warnings, manifestWarnings...)
	}
	return manifests, dedupeWarnings(warnings), nil
}

func discoverPubspecPaths(repoPath string) ([]string, []string, error) {
	paths := make([]string, 0)
	warnings := make([]string, 0)
	count := 0

	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isPubspecFile(entry.Name()) {
			return nil
		}
		count++
		if count > maxManifestCount {
			return fs.SkipAll
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return nil, nil, err
	}

	if count > maxManifestCount {
		warnings = append(warnings, fmt.Sprintf("pubspec manifest discovery capped at %d files", maxManifestCount))
	}
	if len(paths) == 0 {
		warnings = append(warnings, "no pubspec.yaml or pubspec.yml files found for analysis")
	}

	return uniquePaths(paths), dedupeWarnings(warnings), nil
}

func loadPackageManifest(repoPath, manifestPath string) (packageManifest, []string, error) {
	warnings := make([]string, 0)

	manifestData, err := readPubspecManifest(repoPath, manifestPath)
	if err != nil {
		return packageManifest{}, nil, err
	}
	dependencies, hasFlutterSection, hasPluginMetadata, overrideDeps := parsePubspecDependencies(manifestData)
	root := filepath.Dir(manifestPath)

	if len(overrideDeps) > 0 {
		relManifest, relErr := filepath.Rel(repoPath, manifestPath)
		if relErr != nil {
			relManifest = manifestPath
		}
		warnings = append(warnings, fmt.Sprintf("dependency_overrides declared in %s: %s", relManifest, strings.Join(overrideDeps, ", ")))
	}

	lockPath := filepath.Join(root, pubspecLockName)
	hasLock := false
	if _, statErr := os.Stat(lockPath); statErr == nil {
		hasLock = true
		lockData, lockErr := readPubspecLock(repoPath, lockPath)
		if lockErr != nil {
			return packageManifest{}, nil, lockErr
		}
		mergeLockDependencyData(dependencies, lockData.Packages, &hasPluginMetadata)
	} else if os.IsNotExist(statErr) {
		relRoot, relErr := filepath.Rel(repoPath, root)
		if relErr != nil || relRoot == "" {
			relRoot = "."
		}
		warnings = append(warnings, fmt.Sprintf("pubspec.lock not found in %s; resolved package metadata may be partial", relRoot))
	} else {
		return packageManifest{}, nil, statErr
	}

	return packageManifest{
		Root:                     root,
		ManifestPath:             manifestPath,
		Dependencies:             dependencies,
		HasLock:                  hasLock,
		HasFlutterSection:        hasFlutterSection,
		HasFlutterPluginMetadata: hasPluginMetadata,
	}, dedupeWarnings(warnings), nil
}

func readPubspecManifest(repoPath, manifestPath string) (pubspecManifest, error) {
	return shared.ReadYAMLUnderRepo[pubspecManifest](repoPath, manifestPath)
}

func readPubspecLock(repoPath, lockPath string) (pubspecLock, error) {
	return shared.ReadYAMLUnderRepo[pubspecLock](repoPath, lockPath)
}

func parsePubspecDependencies(manifest pubspecManifest) (map[string]dependencyInfo, bool, bool, []string) {
	dependencies := make(map[string]dependencyInfo)
	hasFlutterSection := manifest.Flutter != nil
	hasPluginMetadata := hasPluginMetadataValue(manifest.Flutter)
	overrideDeps := make([]string, 0)

	addDependencySection(dependencies, manifest.Dependencies, "runtime", &hasPluginMetadata)
	addDependencySection(dependencies, manifest.DevDependencies, "dev", &hasPluginMetadata)
	addDependencySection(dependencies, manifest.DependencyOverrides, "override", &hasPluginMetadata)

	for dependency, info := range dependencies {
		if info.Override {
			overrideDeps = append(overrideDeps, dependency)
		}
	}
	sort.Strings(overrideDeps)

	return dependencies, hasFlutterSection, hasPluginMetadata, overrideDeps
}

func addDependencySection(dest map[string]dependencyInfo, section map[string]any, sectionKind string, hasPluginMetadata *bool) {
	for rawName, rawValue := range section {
		dependency := normalizeDependencyID(rawName)
		if dependency == "" {
			continue
		}
		info := dependencyInfoFromSpec(dependency, rawValue, hasPluginMetadata)
		switch sectionKind {
		case "runtime":
			info.Runtime = true
		case "dev":
			info.Dev = true
		case "override":
			info.Override = true
		}
		mergeDependencyInfo(dest, dependency, info)
	}
}

func mergeLockDependencyData(dest map[string]dependencyInfo, packages map[string]pubspecLockPackage, hasPluginMetadata *bool) {
	for rawName, item := range packages {
		dependency, ok := resolveDeclaredLockDependency(dest, rawName, item.Description)
		if !ok {
			continue
		}
		classification := strings.ToLower(strings.TrimSpace(item.Dependency))
		info := dependencyInfo{
			Runtime:    strings.Contains(classification, "main"),
			Dev:        strings.Contains(classification, "dev"),
			Override:   strings.Contains(classification, "overrid"),
			FlutterSDK: strings.EqualFold(strings.TrimSpace(item.Source), "sdk") && lockDescriptionTargetsFlutter(item.Description),
			LocalPath:  strings.EqualFold(strings.TrimSpace(item.Source), "path"),
			PluginLike: isLikelyFlutterPluginPackage(dependency),
			Source:     strings.TrimSpace(item.Source),
			Version:    strings.TrimSpace(item.Version),
		}
		if hasPluginMetadataValue(item.Description) {
			info.PluginLike = true
			if hasPluginMetadata != nil {
				*hasPluginMetadata = true
			}
		}
		mergeDependencyInfo(dest, dependency, info)
	}
}

func resolveDeclaredLockDependency(dest map[string]dependencyInfo, rawName string, description any) (string, bool) {
	candidates := make([]string, 0, 2)
	if lockName := lockPackageName(description); lockName != "" {
		candidates = append(candidates, lockName)
	}
	if dependency := normalizeDependencyID(rawName); dependency != "" {
		candidates = append(candidates, dependency)
	}
	for _, dependency := range candidates {
		if _, ok := dest[dependency]; ok {
			return dependency, true
		}
	}
	return "", false
}

func lockPackageName(description any) string {
	fields, ok := toStringMap(description)
	if !ok {
		return ""
	}
	return normalizeDependencyID(asString(fields["name"]))
}

func lockDescriptionTargetsFlutter(description any) bool {
	if strings.EqualFold(strings.TrimSpace(asString(description)), "flutter") {
		return true
	}
	fields, ok := toStringMap(description)
	if !ok {
		return false
	}
	if strings.EqualFold(asString(fields["name"]), "flutter") {
		return true
	}
	if strings.EqualFold(asString(fields["sdk"]), "flutter") {
		return true
	}
	return false
}

func mergeDependencyInfo(dest map[string]dependencyInfo, dependency string, incoming dependencyInfo) {
	current, ok := dest[dependency]
	if !ok {
		dest[dependency] = incoming
		return
	}
	current.Runtime = current.Runtime || incoming.Runtime
	current.Dev = current.Dev || incoming.Dev
	current.Override = current.Override || incoming.Override
	current.LocalPath = current.LocalPath || incoming.LocalPath
	current.FlutterSDK = current.FlutterSDK || incoming.FlutterSDK
	current.PluginLike = current.PluginLike || incoming.PluginLike
	if current.Source == "" {
		current.Source = incoming.Source
	}
	if current.Version == "" {
		current.Version = incoming.Version
	}
	dest[dependency] = current
}
