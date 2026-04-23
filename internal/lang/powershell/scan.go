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
	scan := scanResult{
		DeclaredDependencies: make(map[string]struct{}),
		DeclaredSources:      make(map[string]powerShellDependencySource),
		ImportedDependencies: make(map[string]struct{}),
	}

	foundPowerShellFile := false
	err := shared.WalkRepoFiles(ctx, repoPath, maxScanFiles, shouldSkipPowerShellDir, func(path string, entry fs.DirEntry) error {
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if !isPowerShellSource(ext) {
			return nil
		}
		foundPowerShellFile = true

		relPath, relErr := filepath.Rel(repoPath, path)
		if relErr != nil {
			relPath = entry.Name()
		}
		relPath = filepath.ToSlash(relPath)

		info, err := entry.Info()
		if err == nil && info.Size() > maxScannablePowerShellBytes {
			scan.Warnings = append(scan.Warnings, fmt.Sprintf("skipped large PowerShell file %s (%d bytes)", relPath, info.Size()))
			return nil
		}

		content, err := safeio.ReadFileUnder(repoPath, path)
		if err != nil {
			return err
		}

		if ext == moduleManifestExt {
			declared, warnings := parseRequiredModules(content, relPath)
			scan.Warnings = append(scan.Warnings, warnings...)
			for _, dependency := range declared {
				scan.DeclaredDependencies[dependency] = struct{}{}
				source := scan.DeclaredSources[dependency]
				source.addManifest(relPath)
				scan.DeclaredSources[dependency] = source
			}
		}

		imports, parseWarnings := parsePowerShellImports(content, relPath, scan.DeclaredDependencies)
		scan.Warnings = append(scan.Warnings, parseWarnings...)
		if len(imports) == 0 {
			return nil
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
		return nil
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

func isPowerShellSource(ext string) bool {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case moduleManifestExt, moduleScriptExt, scriptExt:
		return true
	default:
		return false
	}
}
