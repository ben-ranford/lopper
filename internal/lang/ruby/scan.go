package ruby

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func scanRepo(ctx context.Context, repoPath string) (scanResult, error) {
	scan := scanResult{
		DeclaredDependencies: make(map[string]struct{}),
		DeclaredSources:      make(map[string]rubyDependencySource),
		ImportedDependencies: make(map[string]struct{}),
	}

	declWarnings, err := loadDeclaredDependencies(ctx, repoPath, scan.DeclaredDependencies, scan.DeclaredSources)
	if err != nil {
		return scan, err
	}
	scan.Warnings = append(scan.Warnings, declWarnings...)
	if len(scan.DeclaredDependencies) == 0 {
		scan.Warnings = append(scan.Warnings, "no gem declarations found in Gemfile, Gemfile.lock, or .gemspec files")
	}

	foundRuby := false
	err = walkRubyRepoFiles(ctx, repoPath, func(path string, entry fs.DirEntry) error {
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".rb") {
			return nil
		}
		content, err := safeio.ReadFileUnder(repoPath, path)
		if err != nil {
			return err
		}
		relPath, relErr := filepath.Rel(repoPath, path)
		if relErr != nil {
			relPath = entry.Name()
		}
		imports := parseRequires(content, relPath, scan.DeclaredDependencies)
		for _, imported := range imports {
			scan.ImportedDependencies[imported.Dependency] = struct{}{}
		}
		scan.Files = append(scan.Files, fileScan{
			Imports: imports,
			Usage:   shared.CountUsage(content, imports),
		})
		foundRuby = true
		return nil
	})
	if err != nil {
		return scan, err
	}
	if !foundRuby {
		scan.Warnings = append(scan.Warnings, "no Ruby files found for analysis")
	}
	return scan, nil
}

func parseRequires(content []byte, filePath string, declared map[string]struct{}) []importBinding {
	return shared.ParseImportLines(content, filePath, func(line string, index int) []shared.ImportRecord {
		line = shared.StripLineComment(line, "#")
		matches := requirePattern.FindStringSubmatch(line)
		if len(matches) != 3 {
			return nil
		}
		if strings.TrimSpace(matches[1]) != "" {
			return nil
		}
		module := strings.TrimSpace(matches[2])
		dependency := dependencyFromRequire(module, declared)
		if dependency == "" {
			return nil
		}
		name := module
		if slash := strings.LastIndex(name, "/"); slash >= 0 && slash+1 < len(name) {
			name = name[slash+1:]
		}
		if name == "" {
			name = dependency
		}
		return []shared.ImportRecord{{
			Dependency: dependency,
			Module:     module,
			Name:       name,
			Local:      name,
			Wildcard:   false,
			Location:   shared.LocationFromLine(filePath, index, line),
		}}
	})
}

func dependencyFromRequire(module string, declared map[string]struct{}) string {
	if module == "" {
		return ""
	}
	if strings.HasPrefix(module, ".") || strings.HasPrefix(module, "/") {
		return ""
	}
	normalizedModule := normalizeDependencyID(module)
	if _, ok := declared[normalizedModule]; ok {
		return normalizedModule
	}
	root := normalizedModule
	if slash := strings.Index(root, "/"); slash >= 0 {
		root = root[:slash]
	}
	if _, ok := declared[root]; ok {
		return root
	}
	return ""
}
