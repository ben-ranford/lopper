package js

import "strings"

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

func (r *reExportResolver) resolveExportCandidate(req resolveExportRequest, binding ReExportBinding, visited map[string]struct{}) (exportOrigin, bool) {
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

func normalizeRequestedExport(sourceExport, requestedExport string) string {
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

func (r *reExportResolver) dependencyExportOrigin(req resolveExportRequest, module, exportName string) exportOrigin {
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
