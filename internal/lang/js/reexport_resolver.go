package js

import (
	"fmt"
	"slices"
	"strings"
)

type reExportResolver struct {
	filesByPath  map[string]FileScan
	resolveCache map[string]string
	warningSet   map[string]struct{}
}

type resolvedAttribution struct {
	Module     string
	ExportName string
	Provenance string
}

func newReExportResolver(scanResult ScanResult) *reExportResolver {
	filesByPath := make(map[string]FileScan, len(scanResult.Files))
	for _, file := range scanResult.Files {
		filesByPath[normalizeModulePath(file.Path)] = file
	}
	return &reExportResolver{
		filesByPath:  filesByPath,
		resolveCache: make(map[string]string),
		warningSet:   make(map[string]struct{}),
	}
}

func (r *reExportResolver) warnings() []string {
	warnings := make([]string, 0, len(r.warningSet))
	for warning := range r.warningSet {
		warnings = append(warnings, warning)
	}
	slices.Sort(warnings)
	return warnings
}

func (r *reExportResolver) resolveImportAttribution(importerPath string, imp ImportBinding, dependency string) (resolvedAttribution, bool) {
	if !isLocalModuleSpecifier(imp.Module) {
		return resolvedAttribution{}, false
	}
	if imp.Kind != ImportNamed && imp.Kind != ImportDefault {
		return resolvedAttribution{}, false
	}

	resolvedModule, ok := r.resolveLocalModule(importerPath, imp.Module)
	if !ok {
		return resolvedAttribution{}, false
	}

	requestedExport := imp.ExportName
	if imp.Kind == ImportDefault {
		requestedExport = "default"
	}

	origin, ok := r.resolveExportOrigin(resolveExportRequest{
		importerPath:    normalizeModulePath(importerPath),
		currentFilePath: resolvedModule,
		requestedExport: requestedExport,
		dependency:      dependency,
		visited:         map[string]struct{}{},
		localTrail:      []string{},
	})
	if !ok {
		return resolvedAttribution{}, false
	}

	path := make([]string, 0, len(origin.localTrail)+2)
	path = append(path, normalizeModulePath(importerPath))
	path = append(path, origin.localTrail...)
	path = append(path, fmt.Sprintf("%s#%s", origin.dependencyModule, origin.dependencyExport))

	return resolvedAttribution{
		Module:     origin.dependencyModule,
		ExportName: origin.dependencyExport,
		Provenance: strings.Join(path, " -> "),
	}, true
}
