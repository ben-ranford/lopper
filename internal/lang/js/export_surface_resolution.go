package js

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func loadPackageJSONForSurface(depPath string) (packageJSON, []string, error) {
	pkgPath := filepath.Join(depPath, "package.json")
	data, err := safeio.ReadFileUnder(depPath, pkgPath)
	if err != nil {
		return packageJSON{}, []string{fmt.Sprintf("unable to read %s", pkgPath)}, err
	}
	var pkg packageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return packageJSON{}, []string{"failed to parse dependency package.json"}, err
	}
	return pkg, nil, nil
}

func collectCandidateEntrypoints(pkg packageJSON, profile runtimeProfile, surface *ExportSurface) map[string]struct{} {
	entrypoints := make(map[string]struct{})
	if pkg.Exports != nil {
		resolved := resolveExportsEntryPaths(pkg.Exports, profile, "exports", surface)
		for _, entry := range resolved {
			addEntrypoint(entrypoints, entry)
		}
		if len(resolved) > 0 {
			surface.Warnings = append(surface.Warnings, fmt.Sprintf("info: resolved exports using runtime profile %q", profile.name))
		} else {
			surface.Warnings = append(surface.Warnings, fmt.Sprintf("no exports resolved for runtime profile %q; falling back to legacy entrypoints", profile.name))
		}
	}
	if len(entrypoints) == 0 {
		addEntrypoint(entrypoints, pkg.Main)
		addEntrypoint(entrypoints, pkg.Module)
		addEntrypoint(entrypoints, pkg.Types)
		addEntrypoint(entrypoints, pkg.Typings)
	}
	if len(entrypoints) == 0 {
		addEntrypoint(entrypoints, "index.js")
	}
	return entrypoints
}

func resolveEntrypoints(depPath string, entrypoints map[string]struct{}, surface *ExportSurface) []string {
	resolved := make([]string, 0, len(entrypoints))
	for entry := range entrypoints {
		path, ok := resolveEntrypoint(depPath, entry)
		if !ok {
			surface.Warnings = append(surface.Warnings, fmt.Sprintf("entrypoint not found: %s", entry))
			continue
		}
		resolved = append(resolved, path)
	}
	return resolved
}

func parseEntrypointsIntoSurface(depPath string, resolved []string, surface *ExportSurface) {
	parser := newSourceParser()
	seenEntries := make(map[string]struct{})
	for _, entry := range resolved {
		if _, ok := seenEntries[entry]; ok {
			continue
		}
		seenEntries[entry] = struct{}{}

		content, err := safeio.ReadFileUnder(depPath, entry)
		if err != nil {
			surface.Warnings = append(surface.Warnings, fmt.Sprintf("failed to read entrypoint: %s", entry))
			continue
		}
		tree, err := parser.Parse(context.Background(), entry, content)
		if err != nil {
			surface.Warnings = append(surface.Warnings, fmt.Sprintf("failed to parse entrypoint: %s", entry))
			continue
		}
		if tree != nil {
			addCollectedExports(surface, collectExportNames(tree, content))
		}
	}
	for entry := range seenEntries {
		surface.EntryPoints = append(surface.EntryPoints, entry)
	}
}

func addCollectedExports(surface *ExportSurface, names []string) {
	for _, name := range names {
		if name == "*" {
			surface.IncludesWildcard = true
			continue
		}
		surface.Names[name] = struct{}{}
	}
}
