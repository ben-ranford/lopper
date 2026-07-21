package analysis

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	cargoCratesIOGitIndex = "registry+https://github.com/rust-lang/crates.io-index"
	cargoCratesIOIndex    = "registry+https://index.crates.io/"
	cargoCratesIOSparse   = "sparse+https://index.crates.io/"
)

type cargoLockDocument struct {
	Packages []cargoLockedPackage `toml:"package"`
}

type cargoLockedPackage struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
	Source  string `toml:"source"`
}

type cargoDependencyDeclaration struct {
	lookupName  string
	packageName string
	requirement string
	inherited   bool
}

type cargoManifestModel struct {
	path                  string
	hasWorkspace          bool
	packageWorkspace      string
	workspaceMembers      []string
	workspaceExcludes     []string
	directDependencies    []cargoDependencyDeclaration
	workspaceDependencies map[string]cargoDependencyDeclaration
}

type cargoLockDependencyIndex map[string][]cargoDependencyDeclaration

func collectCargoIdentityEvidenceFromSnapshot(repoPath string, index identityIndex, snapshot identityManifestSnapshot, warnings *identityWarningCollector) {
	manifests := collectCargoManifestModels(repoPath, snapshot.cargoManifestFiles, warnings)
	lockPaths := make(map[string]struct{}, len(snapshot.cargoLockFiles))
	for _, lockPath := range snapshot.cargoLockFiles {
		lockPaths[filepath.Clean(lockPath)] = struct{}{}
	}
	directByLock := make(map[string]cargoLockDependencyIndex, len(lockPaths))
	for _, manifest := range manifests {
		owner := cargoOwningManifest(repoPath, manifest, manifests)
		if owner == nil {
			continue
		}
		lockPath := filepath.Join(filepath.Dir(owner.path), cargoLockFileName)
		if _, ok := lockPaths[lockPath]; !ok {
			continue
		}
		if directByLock[lockPath] == nil {
			directByLock[lockPath] = cargoLockDependencyIndex{}
		}
		for _, dependency := range resolveCargoManifestDependencies(manifest, owner) {
			key := normalizeCargoIdentityLookupName(dependency.packageName)
			directByLock[lockPath][key] = append(directByLock[lockPath][key], dependency)
		}
	}
	for _, lockPath := range snapshot.cargoLockFiles {
		collectCargoLockIdentityEvidence(repoPath, lockPath, index, directByLock[filepath.Clean(lockPath)], warnings)
	}
}

func collectCargoManifestModels(repoPath string, paths []string, warnings *identityWarningCollector) map[string]*cargoManifestModel {
	manifests := make(map[string]*cargoManifestModel, len(paths))
	for _, manifestPath := range paths {
		model := collectCargoManifestModel(repoPath, manifestPath, warnings)
		if model != nil {
			manifests[filepath.Clean(manifestPath)] = model
		}
	}
	return manifests
}

func collectCargoManifestModel(repoPath, path string, warnings *identityWarningCollector) *cargoManifestModel {
	data, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		warnings.addFailure("read", path, identityReadFailed, err)
		return nil
	}
	var document map[string]any
	if err := toml.Unmarshal(data, &document); err != nil {
		warnings.addFailure("parse", path, identityParseFailed, err)
		return nil
	}
	model := &cargoManifestModel{
		path:                  filepath.Clean(path),
		directDependencies:    cargoDirectDependencyDeclarations(document),
		workspaceDependencies: map[string]cargoDependencyDeclaration{},
	}
	if packageTable, ok := document["package"].(map[string]any); ok {
		model.packageWorkspace, _ = packageTable["workspace"].(string)
		model.packageWorkspace = strings.TrimSpace(model.packageWorkspace)
	}
	if workspace, ok := document["workspace"].(map[string]any); ok {
		model.hasWorkspace = true
		model.workspaceMembers = cargoStringSlice(workspace["members"])
		model.workspaceExcludes = cargoStringSlice(workspace["exclude"])
		model.workspaceDependencies = cargoWorkspaceDependencyDeclarations(workspace["dependencies"])
	}
	return model
}

func cargoDirectDependencyDeclarations(document map[string]any) []cargoDependencyDeclaration {
	declarations := make([]cargoDependencyDeclaration, 0)
	appendCargoDependencyTable(&declarations, document["dependencies"], true)
	appendCargoDependencyTable(&declarations, document["dev-dependencies"], true)
	appendCargoDependencyTable(&declarations, document["build-dependencies"], true)
	if targets, ok := document["target"].(map[string]any); ok {
		for _, rawTarget := range targets {
			target, ok := rawTarget.(map[string]any)
			if !ok {
				continue
			}
			appendCargoDependencyTable(&declarations, target["dependencies"], true)
			appendCargoDependencyTable(&declarations, target["dev-dependencies"], true)
			appendCargoDependencyTable(&declarations, target["build-dependencies"], true)
		}
	}
	return declarations
}

func cargoWorkspaceDependencyDeclarations(rawTable any) map[string]cargoDependencyDeclaration {
	table, ok := rawTable.(map[string]any)
	if !ok {
		return map[string]cargoDependencyDeclaration{}
	}
	catalog := make(map[string]cargoDependencyDeclaration, len(table))
	for alias, rawDependency := range table {
		declaration, ok := cargoDependencyDeclarationFor(alias, rawDependency, false)
		if !ok {
			continue
		}
		catalog[normalizeCargoIdentityLookupName(alias)] = declaration
	}
	return catalog
}

func appendCargoDependencyTable(declarations *[]cargoDependencyDeclaration, rawTable any, allowInherited bool) {
	table, ok := rawTable.(map[string]any)
	if !ok {
		return
	}
	aliases := make([]string, 0, len(table))
	for alias := range table {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		if declaration, ok := cargoDependencyDeclarationFor(alias, table[alias], allowInherited); ok {
			*declarations = append(*declarations, declaration)
		}
	}
}

func cargoDependencyDeclarationFor(alias string, rawDependency any, allowInherited bool) (cargoDependencyDeclaration, bool) {
	alias = strings.TrimSpace(alias)
	lookupName := normalizeCargoIdentityLookupName(alias)
	if lookupName == "" {
		return cargoDependencyDeclaration{}, false
	}
	if requirement, ok := rawDependency.(string); ok {
		requirement = strings.TrimSpace(requirement)
		if requirement == "" {
			return cargoDependencyDeclaration{}, false
		}
		return cargoDependencyDeclaration{lookupName: lookupName, packageName: alias, requirement: requirement}, true
	}
	dependency, ok := rawDependency.(map[string]any)
	if !ok {
		return cargoDependencyDeclaration{}, false
	}
	if inherited, _ := dependency["workspace"].(bool); inherited {
		if !allowInherited {
			return cargoDependencyDeclaration{}, false
		}
		return cargoDependencyDeclaration{lookupName: lookupName, inherited: true}, true
	}
	if cargoDependencyUsesUnsupportedSource(dependency) {
		return cargoDependencyDeclaration{}, false
	}
	requirement, _ := dependency["version"].(string)
	requirement = strings.TrimSpace(requirement)
	if requirement == "" {
		return cargoDependencyDeclaration{}, false
	}
	packageName, _ := dependency["package"].(string)
	packageName = strings.TrimSpace(packageName)
	if packageName == "" {
		packageName = alias
	}
	return cargoDependencyDeclaration{
		lookupName:  normalizeCargoIdentityLookupName(packageName),
		packageName: packageName,
		requirement: requirement,
	}, true
}

func cargoDependencyUsesUnsupportedSource(dependency map[string]any) bool {
	for _, field := range []string{"path", "git", "registry", "registry-index"} {
		if _, ok := dependency[field]; ok {
			return true
		}
	}
	return false
}

func resolveCargoManifestDependencies(manifest, owner *cargoManifestModel) []cargoDependencyDeclaration {
	resolved := make([]cargoDependencyDeclaration, 0, len(manifest.directDependencies))
	for _, dependency := range manifest.directDependencies {
		if !dependency.inherited {
			resolved = append(resolved, dependency)
			continue
		}
		catalogDependency, ok := owner.workspaceDependencies[dependency.lookupName]
		if !ok {
			continue
		}
		catalogDependency.lookupName = dependency.lookupName
		resolved = append(resolved, catalogDependency)
	}
	return resolved
}

func collectCargoLockIdentityEvidence(repoPath, path string, index identityIndex, direct cargoLockDependencyIndex, warnings *identityWarningCollector) {
	data, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		warnings.addFailure("read", path, identityReadFailed, err)
		return
	}
	var document cargoLockDocument
	if err := toml.Unmarshal(data, &document); err != nil {
		warnings.addFailure("parse", path, identityParseFailed, err)
		return
	}
	source := relativeIdentitySource(repoPath, path)
	packagesByName := make(map[string][]cargoLockedPackage, len(document.Packages))
	for _, pkg := range document.Packages {
		name := normalizeCargoIdentityLookupName(pkg.Name)
		packagesByName[name] = append(packagesByName[name], pkg)
	}
	for name, dependencies := range direct {
		packages := packagesByName[name]
		for _, dependency := range dependencies {
			compatible, unambiguous := compatibleCratesIOCargoPackages(packages, dependency.requirement)
			if !unambiguous {
				continue
			}
			for _, pkg := range compatible {
				addCargoIdentityEvidence(index, dependency.lookupName, pkg.Name, pkg.Version, source)
			}
		}
	}
}

func compatibleCratesIOCargoPackages(packages []cargoLockedPackage, requirement string) ([]cargoLockedPackage, bool) {
	compatible := make([]cargoLockedPackage, 0, len(packages))
	for _, pkg := range packages {
		if !cargoVersionSatisfiesRequirement(pkg.Version, requirement) {
			continue
		}
		if !isCratesIOCargoSource(pkg.Source) {
			return nil, false
		}
		compatible = append(compatible, pkg)
	}
	return compatible, true
}

func addCargoIdentityEvidence(index identityIndex, lookupName, packageName, version, source string) {
	item := identityEvidence{
		Language:   "rust",
		Ecosystem:  "cargo",
		Name:       strings.TrimSpace(packageName),
		Version:    strings.TrimSpace(version),
		Status:     identityStatusResolved,
		Source:     filepath.ToSlash(strings.TrimSpace(source)),
		Confidence: "high",
	}
	addIdentityEvidence(index, item)
	lookupKey := identityKey(item.Language, lookupName)
	packageKey := identityKey(item.Language, item.Name)
	if lookupKey != packageKey && !hasEquivalentIdentityEvidence(index[lookupKey], item) {
		index[lookupKey] = append(index[lookupKey], item)
	}
}

func isCratesIOCargoSource(source string) bool {
	normalized := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(source)), "/")
	for _, allowed := range []string{cargoCratesIOGitIndex, cargoCratesIOIndex, cargoCratesIOSparse} {
		if normalized == strings.TrimSuffix(allowed, "/") {
			return true
		}
	}
	return false
}

func normalizeCargoIdentityLookupName(name string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "_", "-")
}

func cargoStringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
			values = append(values, strings.TrimSpace(text))
		}
	}
	return values
}
