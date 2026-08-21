package golang

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
	"golang.org/x/mod/modfile"
)

var errNilModuleInfo = errors.New("module info is nil")

func loadGoModuleInfoWithOptions(repoPath string, options moduleLoadOptions) (moduleInfo, error) {
	info := moduleInfo{
		ReplacementImports:         make(map[string]string),
		VendoredImportDependencies: make(map[string]string),
		VendoredDependencies:       make(map[string]vendoredDependencyMetadata),
	}

	if err := loadRootModuleInfo(repoPath, &info); err != nil {
		return moduleInfo{}, err
	}
	if err := loadWorkspaceModules(repoPath, &info); err != nil {
		return moduleInfo{}, err
	}
	if err := loadNestedModules(repoPath, &info); err != nil {
		return moduleInfo{}, err
	}
	if err := loadVendoredMetadata(repoPath, options, &info); err != nil {
		return moduleInfo{}, err
	}

	if err := finalizeGoModuleInfo(&info); err != nil {
		return moduleInfo{}, err
	}
	return info, nil
}

func loadGoModuleInfo(repoPath string, opts ...moduleLoadOptions) (moduleInfo, error) {
	options := moduleLoadOptions{}
	if len(opts) > 0 {
		options = opts[0]
	}
	return loadGoModuleInfoWithOptions(repoPath, options)
}

func loadRootModuleInfo(repoPath string, info *moduleInfo) error {
	if info == nil {
		return errNilModuleInfo
	}

	goModPath := filepath.Join(repoPath, goModName)
	exists, err := manifestPathExists(goModPath)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	content, err := safeio.ReadFileUnderLimit(repoPath, goModPath, maxGoModBytes)
	if isPureGoModSizeLimit(err) {
		info.RootGoModTooLarge = true
		if modulePath, pathErr := readOversizedRootModulePath(goModPath); pathErr != nil {
			return pathErr
		} else if modulePath != "" {
			info.ModulePath = modulePath
			info.LocalModulePaths = append(info.LocalModulePaths, modulePath)
		}
		return nil
	}
	if err != nil {
		return err
	}

	modulePath, dependencies, replacements := parseGoMod(content)
	info.ModulePath = modulePath
	info.DeclaredDependencies = dependencies
	info.ReplacementImports = replacements
	info.LocalModulePaths = append(info.LocalModulePaths, modulePath)
	return nil
}

func readOversizedRootModulePath(goModPath string) (modulePath string, err error) {
	file, err := safeio.OpenFile(goModPath)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	content, err := io.ReadAll(io.LimitReader(file, maxGoModRootModulePathProbeBytes))
	if err != nil {
		return "", err
	}
	return modfile.ModulePath(content), nil
}

func isPureGoModSizeLimit(err error) bool {
	return shared.IsPureSentinelError(err, safeio.ErrFileTooLarge)
}

func loadWorkspaceModules(repoPath string, info *moduleInfo) error {
	if info == nil {
		return errNilModuleInfo
	}

	workModules, err := loadGoWorkLocalModules(repoPath)
	if err != nil {
		return err
	}
	info.LocalModulePaths = append(info.LocalModulePaths, workModules...)
	return nil
}

func loadNestedModules(repoPath string, info *moduleInfo) error {
	if info == nil {
		return errNilModuleInfo
	}

	workspaceModuleDirs, err := workspaceRootModuleDirs(repoPath, *info)
	if err != nil {
		return err
	}
	info.WorkspaceModuleExclusions = normalizedDirSet(workspaceModuleDirs)

	scanNestedDirs, err := nestedModuleDirs(repoPath, workspaceModuleDirs)
	if err != nil {
		return err
	}
	info.NestedModuleDirs = normalizedDirSet(scanNestedDirs)

	metadataNestedDirs := unionDirSet(info.NestedModuleDirs, info.WorkspaceModuleExclusions)
	nestedModules, nestedDeps, nestedReplacements, oversizedModuleDirs, err := discoverNestedModulesFromDirs(repoPath, metadataNestedDirs)
	if err != nil {
		return err
	}
	info.LocalModulePaths = append(info.LocalModulePaths, nestedModules...)
	info.DeclaredDependencies = append(info.DeclaredDependencies, nestedDeps...)
	info.OversizedModuleDirs = normalizedDirSet(oversizedModuleDirs)
	for replacementImport, dependency := range nestedReplacements {
		if _, ok := info.ReplacementImports[replacementImport]; !ok {
			info.ReplacementImports[replacementImport] = dependency
		}
	}
	return nil
}

func loadVendoredMetadata(repoPath string, options moduleLoadOptions, info *moduleInfo) error {
	if info == nil || !options.EnableVendoredProvenance {
		return nil
	}
	metadata, err := loadVendoredModuleMetadata(repoPath)
	if err != nil {
		return err
	}
	info.VendoredProvenanceEnabled = metadata.ManifestFound
	info.VendoringWarnings = append(info.VendoringWarnings, metadata.Warnings...)
	if !metadata.ManifestFound {
		return nil
	}
	for importPrefix, dependency := range metadata.ImportToDependency {
		if _, ok := info.VendoredImportDependencies[importPrefix]; !ok {
			info.VendoredImportDependencies[importPrefix] = normalizeDependencyID(dependency)
		}
	}
	for dependency, item := range metadata.Dependencies {
		info.VendoredDependencies[dependency] = item
	}
	return nil
}

func finalizeGoModuleInfo(info *moduleInfo) error {
	if info == nil {
		return errNilModuleInfo
	}

	info.LocalModulePaths = uniqueStrings(info.LocalModulePaths)
	info.DeclaredDependencies = uniqueStrings(info.DeclaredDependencies)
	sort.Strings(info.LocalModulePaths)
	sort.Strings(info.DeclaredDependencies)
	info.VendoringWarnings = append(info.VendoringWarnings, oversizedModuleWarnings(info.OversizedModuleDirs)...)
	sort.Strings(info.VendoringWarnings)
	return nil
}

func oversizedModuleWarnings(dirs map[string]struct{}) []string {
	if len(dirs) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("skipped Go source dependency attribution for %d module(s) because go.mod exceeds %d bytes", len(dirs), maxGoModBytes)}
}
