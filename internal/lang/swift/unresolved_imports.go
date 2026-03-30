package swift

import (
	"fmt"
	"sort"
	"strings"
)

func appendUnresolvedImportWarning(scan *scanResult, unresolved map[string]int, catalog dependencyCatalog) {
	if len(unresolved) == 0 {
		return
	}
	scan.Warnings = append(scan.Warnings, unresolvedImportWarning(unresolved, catalog))
}

func trackUnresolvedImport(unresolved map[string]int, module string, catalog dependencyCatalog) {
	if shouldTrackUnresolvedImport(module, catalog) {
		unresolved[module]++
	}
}

func unresolvedImportWarning(unresolved map[string]int, catalog dependencyCatalog) string {
	type unresolvedEntry struct {
		Module string
		Count  int
	}

	entries := make([]unresolvedEntry, 0, len(unresolved))
	for module, count := range unresolved {
		entries = append(entries, unresolvedEntry{Module: module, Count: count})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Count == entries[j].Count {
			return entries[i].Module < entries[j].Module
		}
		return entries[i].Count > entries[j].Count
	})

	samples := make([]string, 0, maxWarningSamples)
	for index, item := range entries {
		if index >= maxWarningSamples {
			break
		}
		samples = append(samples, fmt.Sprintf("%s (%d)", item.Module, item.Count))
	}
	if len(entries) > maxWarningSamples {
		samples = append(samples, fmt.Sprintf("+%d more", len(entries)-maxWarningSamples))
	}

	message := "could not map some Swift imports to known Swift dependencies"
	if catalog.HasCocoaPods {
		message += "; CocoaPods module mapping may be incomplete"
	}
	return message + ": " + strings.Join(samples, ", ")
}

func shouldTrackUnresolvedImport(module string, catalog dependencyCatalog) bool {
	if len(catalog.Dependencies) == 0 {
		return false
	}
	key := lookupKey(module)
	if key == "" {
		return false
	}
	if _, ok := catalog.LocalModules[key]; ok {
		return false
	}
	if _, ok := standardSwiftSymbols[key]; ok {
		return false
	}
	return true
}
