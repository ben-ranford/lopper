package js

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/ben-ranford/lopper/internal/safeio"
)

type entrypointCandidates struct {
	ordered []string
	total   int
}

var openEntrypointRoot = openConstrainedRoot

func loadPackageJSONForSurface(rootPath, depPath string) (pkg packageJSON, warnings []string, err error) {
	validatedDepRoot, err := resolvePinnedRootPath(depPath)
	if err != nil {
		return packageJSON{}, packageReadWarnings(depPath), err
	}
	if rootPath == "" {
		rootPath = validatedDepRoot
	}
	root, validatedRootPath, err := openValidatedRootNoFollow(rootPath)
	if err != nil {
		return packageJSON{}, packageReadWarnings(validatedDepRoot), err
	}
	defer closeReadCloserPreserveErr(root, &err)
	pkgPath := filepath.Join(validatedDepRoot, jsPackageFile)
	relPkgPath, err := relativePathWithinRoot(validatedRootPath, pkgPath)
	if err != nil {
		return packageJSON{}, packageReadWarnings(validatedDepRoot), err
	}
	data, err := safeio.ReadFileWithinRootLimit(root, relPkgPath, jsPackageJSONReadMaxBytes)
	if err != nil {
		return packageJSON{}, packageReadWarnings(validatedDepRoot), err
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return packageJSON{}, []string{"failed to parse dependency package.json"}, err
	}
	return pkg, nil, nil
}

func packageReadWarnings(depPath string) []string {
	return []string{fmt.Sprintf("unable to read %s", filepath.Join(depPath, jsPackageFile))}
}

func collectCandidateEntrypoints(pkg packageJSON, profile runtimeProfile, surface *ExportSurface) entrypointCandidates {
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
	return entrypointCandidates{
		ordered: prioritizedEntrypoints(pkg, profile, entrypoints),
		total:   len(entrypoints),
	}
}

func prioritizedEntrypoints(pkg packageJSON, profile runtimeProfile, entrypoints map[string]struct{}) []string {
	ordered := make([]string, 0, len(entrypoints))
	seen := make(map[string]struct{}, len(entrypoints))
	appendEntry := func(entry string) {
		if _, ok := entrypoints[entry]; !ok {
			return
		}
		if _, ok := seen[entry]; ok {
			return
		}
		seen[entry] = struct{}{}
		ordered = append(ordered, entry)
	}

	for _, entry := range prioritizedExportEntrypoints(pkg.Exports, profile) {
		appendEntry(entry)
	}
	for _, entry := range []string{pkg.Main, pkg.Module, pkg.Types, pkg.Typings, "index.js"} {
		appendEntry(entry)
	}
	for _, entry := range sortedMapKeys(entrypoints) {
		appendEntry(entry)
	}
	return ordered
}

func prioritizedExportEntrypoints(exports any, profile runtimeProfile) []string {
	exportsMap, ok := exports.(map[string]any)
	if !ok || len(exportsMap) == 0 {
		return resolveExportsEntryPaths(exports, profile, "exports", nil)
	}

	if !hasSubpathExportKeys(exportsMap) {
		return resolveExportsEntryPaths(exports, profile, "exports", nil)
	}

	collected := make([]string, 0, len(exportsMap))
	seen := make(map[string]struct{})
	appendPaths := func(paths []string) {
		for _, entry := range paths {
			if _, ok := seen[entry]; ok {
				continue
			}
			seen[entry] = struct{}{}
			collected = append(collected, entry)
		}
	}

	if rootExport, ok := exportsMap["."]; ok {
		paths, _ := resolveExportNode(rootExport, profile, "exports.", nil)
		appendPaths(paths)
	}

	for _, key := range sortedObjectKeys(exportsMap) {
		if key == "." || !isSubpathExportKey(key) {
			continue
		}
		paths, _ := resolveExportNode(exportsMap[key], profile, fmt.Sprintf("exports.%s", key), nil)
		appendPaths(paths)
	}
	return collected
}

func resolveEntrypoints(rootPath, depPath string, candidates entrypointCandidates, surface *ExportSurface) (resolved []string) {
	if candidates.total > maxExportEntrypoints {
		surface.Warnings = append(surface.Warnings, fmt.Sprintf("capped dependency entrypoint resolution at %d candidates", maxExportEntrypoints))
	}

	root, err := openEntrypointRoot(rootPath)
	if err != nil {
		return nil
	}
	defer func() {
		if closeRootAppendWarning(root, &surface.Warnings, "failed to close dependency root after entrypoint resolution") {
			resolved = nil
		}
	}()

	resolved = make([]string, 0, minInt(len(candidates.ordered), maxExportEntrypoints))
	attempts := 0
	for _, entry := range candidates.ordered {
		if attempts >= maxExportEntrypoints || len(resolved) >= maxExportEntrypoints {
			break
		}
		attempts++

		path, ok := resolveEntrypointWithinRoot(root, rootPath, depPath, entry)
		if !ok {
			surface.Warnings = append(surface.Warnings, fmt.Sprintf("entrypoint not found: %s", entry))
			continue
		}
		resolved = append(resolved, path)
	}
	return resolved
}

func parseEntrypointsIntoSurface(rootPath string, resolved []string, surface *ExportSurface) {
	if rootPath == "" {
		return
	}
	root, validatedRootPath, err := openValidatedRootNoFollow(rootPath)
	if err != nil {
		for _, entry := range resolved {
			surface.Warnings = append(surface.Warnings, fmt.Sprintf("failed to read entrypoint: %s", entry))
		}
		return
	}
	defer closeRootAppendWarning(root, &surface.Warnings, "failed to close dependency root after entrypoint parsing")
	parser := newSourceParser()
	seenEntries := make(map[string]struct{})
	for _, entry := range resolved {
		if !trackEntrypoint(surface, seenEntries, entry) {
			continue
		}
		content, readOK := readEntrypointWithinRoot(root, validatedRootPath, entry, surface)
		if !readOK {
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
}

func trackEntrypoint(surface *ExportSurface, seenEntries map[string]struct{}, entry string) bool {
	if _, ok := seenEntries[entry]; ok {
		return false
	}
	seenEntries[entry] = struct{}{}
	surface.EntryPoints = append(surface.EntryPoints, entry)
	return true
}

func readEntrypointWithinRoot(root safeio.Root, validatedRootPath, entry string, surface *ExportSurface) ([]byte, bool) {
	relEntry, err := relativePathWithinRoot(validatedRootPath, entry)
	if err != nil {
		surface.Warnings = append(surface.Warnings, fmt.Sprintf("failed to read entrypoint: %s", entry))
		return nil, false
	}
	content, err := safeio.ReadFileWithinRootLimit(root, relEntry, jsSourceReadMaxBytes)
	if err != nil {
		surface.Warnings = append(surface.Warnings, fmt.Sprintf("failed to read entrypoint: %s", entry))
		return nil, false
	}
	return content, true
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

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
