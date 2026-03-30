package js

import "fmt"

func (r *reExportResolver) hasResolutionCycle(req resolveExportRequest) bool {
	key := req.currentFilePath + "|" + req.requestedExport
	if _, seen := req.visited[key]; !seen {
		return false
	}
	path := append([]string{}, req.localTrail...)
	path = append(path, req.currentFilePath)
	warning := fmt.Sprintf("re-export attribution cycle while resolving %q from %s: %s", req.requestedExport, req.importerPath, stringsJoin(path, " -> "))
	r.warningSet[warning] = struct{}{}
	return true
}

func stringsJoin(items []string, sep string) string {
	if len(items) == 0 {
		return ""
	}
	result := items[0]
	for _, item := range items[1:] {
		result += sep + item
	}
	return result
}
