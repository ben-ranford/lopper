package golang

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func loadGoModuleInfo(repoPath string) (moduleInfo, error) {
	info := moduleInfo{
		ReplacementImports: make(map[string]string),
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

	finalizeGoModuleInfo(&info)
	return info, nil
}

func loadRootModuleInfo(repoPath string, info *moduleInfo) error {
	if info == nil {
		return nil
	}

	goModPath := filepath.Join(repoPath, goModName)
	content, err := safeio.ReadFileUnder(repoPath, goModPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	modulePath, dependencies, replacements := parseGoMod(content)
	info.ModulePath = modulePath
	info.DeclaredDependencies = dependencies
	info.ReplacementImports = replacements
	info.LocalModulePaths = append(info.LocalModulePaths, modulePath)
	return nil
}

func loadWorkspaceModules(repoPath string, info *moduleInfo) error {
	if info == nil {
		return nil
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
		return nil
	}

	nestedModules, nestedDeps, nestedReplacements, err := discoverNestedModules(repoPath)
	if err != nil {
		return err
	}
	info.LocalModulePaths = append(info.LocalModulePaths, nestedModules...)
	info.DeclaredDependencies = append(info.DeclaredDependencies, nestedDeps...)
	for replacementImport, dependency := range nestedReplacements {
		if _, ok := info.ReplacementImports[replacementImport]; !ok {
			info.ReplacementImports[replacementImport] = dependency
		}
	}
	return nil
}

func finalizeGoModuleInfo(info *moduleInfo) {
	if info == nil {
		return
	}

	info.LocalModulePaths = uniqueStrings(info.LocalModulePaths)
	info.DeclaredDependencies = uniqueStrings(info.DeclaredDependencies)
	sort.Strings(info.LocalModulePaths)
	sort.Strings(info.DeclaredDependencies)
}
