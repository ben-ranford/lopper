package js

import (
	"fmt"
	"path/filepath"
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

type resolveExportRequest struct {
	importerPath    string
	currentFilePath string
	requestedExport string
	dependency      string
	visited         map[string]struct{}
	localTrail      []string
}

type exportOrigin struct {
	dependencyModule string
	dependencyExport string
	localTrail       []string
}

func (r *reExportResolver) resolveExportOrigin(req resolveExportRequest) (exportOrigin, bool) {
	if r.hasResolutionCycle(req) {
		return exportOrigin{}, false
	}

	file, visited, ok := r.prepareExportResolution(req)
	if !ok {
		return exportOrigin{}, false
	}

	for _, binding := range selectReExportCandidates(file.ReExports, req.requestedExport) {
		origin, ok := r.resolveExportCandidate(req, binding, visited)
		if ok {
			return origin, true
		}
	}

	return exportOrigin{}, false
}

func (r *reExportResolver) hasResolutionCycle(req resolveExportRequest) bool {
	key := req.currentFilePath + "|" + req.requestedExport
	if _, seen := req.visited[key]; !seen {
		return false
	}
	path := append([]string{}, req.localTrail...)
	path = append(path, req.currentFilePath)
	warning := fmt.Sprintf(
		"re-export attribution cycle while resolving %q from %s: %s",
		req.requestedExport,
		req.importerPath,
		strings.Join(path, " -> "),
	)
	r.warningSet[warning] = struct{}{}
	return true
}

func (r *reExportResolver) prepareExportResolution(req resolveExportRequest) (FileScan, map[string]struct{}, bool) {
	file, ok := r.filesByPath[req.currentFilePath]
	if !ok {
		return FileScan{}, nil, false
	}
	visited := cloneVisitedSet(req.visited)
	visited[req.currentFilePath+"|"+req.requestedExport] = struct{}{}
	return file, visited, true
}

func selectReExportCandidates(bindings []ReExportBinding, requestedExport string) []ReExportBinding {
	candidates := make([]ReExportBinding, 0)
	for _, binding := range bindings {
		if binding.ExportName == requestedExport {
			candidates = append(candidates, binding)
		}
	}
	if len(candidates) > 0 {
		return candidates
	}
	for _, binding := range bindings {
		if binding.ExportName == "*" {
			candidates = append(candidates, binding)
		}
	}
	return candidates
}

func (r *reExportResolver) resolveExportCandidate(
	req resolveExportRequest,
	binding ReExportBinding,
	visited map[string]struct{},
) (exportOrigin, bool) {
	nextExport := normalizeRequestedExport(binding.SourceExportName, req.requestedExport)
	if matchesDependency(binding.SourceModule, req.dependency) {
		return r.dependencyExportOrigin(req, binding.SourceModule, nextExport), true
	}
	if !isLocalModuleSpecifier(binding.SourceModule) {
		return exportOrigin{}, false
	}

	nextPath, ok := r.resolveLocalModule(req.currentFilePath, binding.SourceModule)
	if !ok {
		return exportOrigin{}, false
	}
	return r.resolveExportOrigin(resolveExportRequest{
		importerPath:    req.importerPath,
		currentFilePath: nextPath,
		requestedExport: nextExport,
		dependency:      req.dependency,
		visited:         visited,
		localTrail:      appendTrail(req.localTrail, req.currentFilePath),
	})
}

func normalizeRequestedExport(sourceExport string, requestedExport string) string {
	if sourceExport == "*" {
		return requestedExport
	}
	return sourceExport
}

func appendTrail(trail []string, current string) []string {
	next := append([]string{}, trail...)
	next = append(next, current)
	return next
}

func (r *reExportResolver) dependencyExportOrigin(req resolveExportRequest, module string, exportName string) exportOrigin {
	return exportOrigin{
		dependencyModule: module,
		dependencyExport: exportName,
		localTrail:       appendTrail(req.localTrail, req.currentFilePath),
	}
}

func cloneVisitedSet(src map[string]struct{}) map[string]struct{} {
	cloned := make(map[string]struct{}, len(src))
	for key := range src {
		cloned[key] = struct{}{}
	}
	return cloned
}

func isLocalModuleSpecifier(module string) bool {
	return strings.HasPrefix(module, ".")
}

func (r *reExportResolver) resolveLocalModule(importerPath string, module string) (string, bool) {
	key := normalizeModulePath(importerPath) + "\x00" + module
	if value, ok := r.resolveCache[key]; ok {
		if value == "" {
			return "", false
		}
		return value, true
	}

	base := filepath.Dir(normalizeModulePath(importerPath))
	target := normalizeModulePath(filepath.Join(base, module))
	for _, candidate := range localModuleCandidates(target) {
		if _, ok := r.filesByPath[candidate]; ok {
			r.resolveCache[key] = candidate
			return candidate, true
		}
	}

	r.resolveCache[key] = ""
	return "", false
}

func localModuleCandidates(path string) []string {
	normalized := normalizeModulePath(path)
	candidates := make([]string, 0, 24)
	candidates = append(candidates, normalized)
	base := strings.TrimSuffix(normalized, filepath.Ext(normalized))
	if base != normalized {
		candidates = append(candidates, base)
	}

	extensions := []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"}
	for _, ext := range extensions {
		candidates = append(candidates, base+ext)
		candidates = append(candidates, filepath.Join(base, "index"+ext))
	}

	unique := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		normalizedCandidate := normalizeModulePath(candidate)
		if _, ok := seen[normalizedCandidate]; ok {
			continue
		}
		seen[normalizedCandidate] = struct{}{}
		unique = append(unique, normalizedCandidate)
	}
	return unique
}

func normalizeModulePath(path string) string {
	return filepath.ToSlash(filepath.Clean(path))
}
