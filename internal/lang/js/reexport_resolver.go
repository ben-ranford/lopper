package js

import (
	"fmt"
	"slices"
	"strings"
)

const (
	maxReExportResolutionSteps      = 128
	reExportResolutionBudgetWarning = "re-export attribution exceeded resolution budget"
)

type reExportResolver struct {
	filesByPath          map[string]FileScan
	resolveCache         map[string]string
	warningSet           map[string]struct{}
	resolutionSteps      int
	resolutionLimit      int
	resolutionLimited    bool
	resolutionIncomplete bool
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
		filesByPath:     filesByPath,
		resolveCache:    make(map[string]string),
		warningSet:      make(map[string]struct{}),
		resolutionLimit: maxReExportResolutionSteps,
	}
}

func (r *reExportResolver) consumeResolutionBudget() bool {
	limit := r.resolutionLimit
	if limit == 0 {
		limit = maxReExportResolutionSteps
	}
	if r.resolutionSteps < limit {
		r.resolutionSteps++
		return true
	}
	if !r.resolutionLimited {
		r.resolutionLimited = true
		r.resolutionIncomplete = true
		r.warningSet[reExportResolutionBudgetWarning] = struct{}{}
	}
	return false
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
	r.resolutionSteps = 0
	r.resolutionLimited = false

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
