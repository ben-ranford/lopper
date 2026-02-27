package app

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type lockfileRule struct {
	manager   string
	manifest  string
	lockfiles []string
	remedy    string
}

var lockfileRules = []lockfileRule{
	{manager: "npm", manifest: "package.json", lockfiles: []string{"package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb"}, remedy: "run your package manager install command and commit the updated manifest and lockfile"},
	{manager: "Composer", manifest: "composer.json", lockfiles: []string{"composer.lock"}, remedy: "run composer update --lock (or composer install) and commit the updated files"},
	{manager: "Cargo", manifest: "Cargo.toml", lockfiles: []string{"Cargo.lock"}, remedy: "run cargo generate-lockfile (or cargo build) and commit the updated files"},
	{manager: "Go modules", manifest: "go.mod", lockfiles: []string{"go.sum"}, remedy: "run go mod tidy and commit the updated files"},
	{manager: "Pipenv", manifest: "Pipfile", lockfiles: []string{"Pipfile.lock"}, remedy: "run pipenv lock and commit the updated files"},
	{manager: "Poetry", manifest: "pyproject.toml", lockfiles: []string{"poetry.lock"}, remedy: "run poetry lock and commit the updated files"},
}

func evaluateLockfileDriftPolicy(repoPath, policy string) ([]string, error) {
	if strings.TrimSpace(policy) == "off" {
		return nil, nil
	}
	driftWarnings, err := detectLockfileDrift(repoPath)
	if err != nil || len(driftWarnings) == 0 {
		return driftWarnings, err
	}
	if strings.TrimSpace(policy) == "fail" {
		return driftWarnings, fmt.Errorf("%w: %s", ErrLockfileDrift, driftWarnings[0])
	}
	return driftWarnings, nil
}

func detectLockfileDrift(repoPath string) ([]string, error) {
	normalizedPath, err := workspace.NormalizeRepoPath(repoPath)
	if err != nil {
		return nil, err
	}
	warnings := make([]string, 0, 2)
	err = filepath.WalkDir(normalizedPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		if path != normalizedPath && shared.ShouldSkipCommonDir(entry.Name()) {
			return filepath.SkipDir
		}
		fileInfos, readErr := readDirectoryFiles(path)
		if readErr != nil {
			return nil
		}
		for _, rule := range lockfileRules {
			warnings = append(warnings, detectDriftForRule(normalizedPath, path, fileInfos, rule)...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return warnings, nil
}

func readDirectoryFiles(path string) (map[string]fs.FileInfo, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	files := make(map[string]fs.FileInfo, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		files[entry.Name()] = info
	}
	return files, nil
}

func detectDriftForRule(repoPath, dir string, files map[string]fs.FileInfo, rule lockfileRule) []string {
	manifestInfo, hasManifest := files[rule.manifest]
	lockfiles := findRuleLockfiles(files, rule.lockfiles)
	relDir := relativeDir(repoPath, dir)
	if hasManifest && len(lockfiles) == 0 {
		return []string{
			fmt.Sprintf("lockfile drift detected for %s in %s: %s exists but no matching lockfile (%s) was found; %s", rule.manager, relDir, rule.manifest, strings.Join(rule.lockfiles, ", "), rule.remedy),
		}
	}
	if !hasManifest && len(lockfiles) > 0 {
		return []string{
			fmt.Sprintf("lockfile drift detected for %s in %s: %s exists without %s; remove stale lockfile or restore the manifest", rule.manager, relDir, lockfiles[0].name, rule.manifest),
		}
	}
	if !hasManifest || len(lockfiles) == 0 {
		return nil
	}
	newest := lockfiles[0]
	for _, lockfile := range lockfiles[1:] {
		if lockfile.info.ModTime().After(newest.info.ModTime()) {
			newest = lockfile
		}
	}
	if manifestInfo.ModTime().After(newest.info.ModTime()) {
		return []string{
			fmt.Sprintf("lockfile drift detected for %s in %s: %s is newer than %s; %s", rule.manager, relDir, rule.manifest, newest.name, rule.remedy),
		}
	}
	return nil
}

type presentLockfile struct {
	name string
	info fs.FileInfo
}

func findRuleLockfiles(files map[string]fs.FileInfo, names []string) []presentLockfile {
	lockfiles := make([]presentLockfile, 0, len(names))
	for _, name := range names {
		info, ok := files[name]
		if !ok {
			continue
		}
		lockfiles = append(lockfiles, presentLockfile{name: name, info: info})
	}
	return lockfiles
}

func relativeDir(repoPath, dir string) string {
	relative, err := filepath.Rel(repoPath, dir)
	if err != nil {
		return dir
	}
	if relative == "." {
		return "."
	}
	return relative
}
