package powershell

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func scanRepo(ctx context.Context, repoPath string) (scanResult, error) {
	scan := newScanResult()

	foundPowerShellFile := false
	err := shared.WalkRepoFiles(ctx, repoPath, maxScanFiles, shouldSkipPowerShellDir, func(path string, entry fs.DirEntry) error {
		processed, scanErr := scanPowerShellFile(repoPath, path, entry, &scan)
		if processed {
			foundPowerShellFile = true
		}
		return scanErr
	})
	if err != nil {
		return scan, err
	}

	if !foundPowerShellFile {
		scan.Warnings = append(scan.Warnings, "no PowerShell files found for analysis")
	}
	if len(scan.DeclaredDependencies) == 0 {
		scan.Warnings = append(scan.Warnings, "no PowerShell module declarations found in .psd1 RequiredModules")
	}
	return scan, nil
}

func newScanResult() scanResult {
	return scanResult{
		DeclaredDependencies: make(map[string]struct{}),
		DeclaredSources:      make(map[string]powerShellDependencySource),
		ImportedDependencies: make(map[string]struct{}),
	}
}

func scanPowerShellFile(repoPath, path string, entry fs.DirEntry, scan *scanResult) (bool, error) {
	ext := strings.ToLower(filepath.Ext(entry.Name()))
	if !isPowerShellSource(ext) {
		return false, nil
	}

	relPath := scanRelativePath(repoPath, path, entry)
	if skip, err := skipLargePowerShellFile(entry, relPath, scan); skip || err != nil {
		return true, err
	}

	content, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		return true, err
	}

	if ext == moduleManifestExt {
		recordDeclaredPowerShellDependencies(scan, relPath, content)
	}
	recordImportedPowerShellDependencies(scan, relPath, content)
	return true, nil
}

func scanRelativePath(repoPath, path string, entry fs.DirEntry) string {
	relPath, err := filepath.Rel(repoPath, path)
	if err != nil {
		relPath = entry.Name()
	}
	return filepath.ToSlash(relPath)
}

func skipLargePowerShellFile(entry fs.DirEntry, relPath string, scan *scanResult) (bool, error) {
	info, err := entry.Info()
	if err != nil {
		return false, nil
	}
	if info.Size() <= maxScannablePowerShellBytes {
		return false, nil
	}

	scan.Warnings = append(scan.Warnings, fmt.Sprintf("skipped large PowerShell file %s (%d bytes)", relPath, info.Size()))
	return true, nil
}

func recordDeclaredPowerShellDependencies(scan *scanResult, relPath string, content []byte) {
	declared, warnings := parseRequiredModules(content, relPath)
	scan.Warnings = append(scan.Warnings, warnings...)
	for _, dependency := range declared {
		scan.DeclaredDependencies[dependency] = struct{}{}
		source := scan.DeclaredSources[dependency]
		source.addManifest(relPath)
		scan.DeclaredSources[dependency] = source
	}
}

func recordImportedPowerShellDependencies(scan *scanResult, relPath string, content []byte) {
	imports, warnings := parsePowerShellImports(content, relPath, scan.DeclaredDependencies)
	scan.Warnings = append(scan.Warnings, warnings...)
	if len(imports) == 0 {
		return
	}

	records := make([]shared.ImportRecord, 0, len(imports))
	for _, imported := range imports {
		dependency := normalizeDependencyID(imported.Record.Dependency)
		imported.Record.Dependency = dependency
		scan.ImportedDependencies[dependency] = struct{}{}
		records = append(records, imported.Record)
	}

	scan.Files = append(scan.Files, fileScan{
		Imports: imports,
		Usage:   shared.CountUsage(content, records),
	})
}

func isPowerShellSource(ext string) bool {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case moduleManifestExt, moduleScriptExt, scriptExt:
		return true
	default:
		return false
	}
}
