package swift

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func loadPodManifestData(repoPath string, catalog *dependencyCatalog) (bool, []string, error) {
	manifestPath := filepath.Join(repoPath, podManifestName)
	content, err := safeio.ReadFileUnder(repoPath, manifestPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("read %s: %w", podManifestName, err)
	}

	declarations := parsePodDeclarations(content)
	warnings := make([]string, 0, 2)
	ambiguousModules := make(map[string]struct{})
	for _, declaration := range declarations {
		depID := normalizeDependencyID(declaration)
		if depID == "" {
			continue
		}
		ensureDeclaredDependencyForManager(catalog, depID, cocoaPodsManager)
		addPodMappings(catalog, depID, declaration, ambiguousModules)
	}
	if len(declarations) == 0 {
		warnings = append(warnings, "no pod declarations found in Podfile")
	}
	if warning := cocoaPodsAmbiguityWarning(ambiguousModules); warning != "" {
		warnings = append(warnings, warning)
	}
	return true, warnings, nil
}

func loadPodLockData(repoPath string, catalog *dependencyCatalog) (bool, []string, error) {
	lockPath := filepath.Join(repoPath, podLockName)
	content, err := safeio.ReadFileUnder(repoPath, lockPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("read %s: %w", podLockName, err)
	}

	entries, err := parsePodLockEntries(content)
	if err != nil {
		return false, nil, fmt.Errorf("parse %s: %w", podLockName, err)
	}

	warnings := make([]string, 0, 2)
	ambiguousModules := make(map[string]struct{})
	if len(entries) == 0 {
		warnings = append(warnings, "no pods found in Podfile.lock")
	}
	for _, entry := range entries {
		depID := normalizeDependencyID(entry.Name)
		if depID == "" {
			continue
		}
		source := strings.TrimSpace(entry.Source)
		if source == "" {
			source = podLockName
		}
		ensureResolvedDependencyForManager(catalog, depID, entry.Version, "", source, cocoaPodsManager)
		addPodMappings(catalog, depID, entry.Name, ambiguousModules)
	}
	if warning := cocoaPodsAmbiguityWarning(ambiguousModules); warning != "" {
		warnings = append(warnings, warning)
	}
	return true, warnings, nil
}

func parsePodDeclarations(content []byte) []string {
	lines := strings.Split(string(content), "\n")
	declarations := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(shared.StripLineComment(line, "#"))
		if line == "" {
			continue
		}
		matches := podDeclarationPattern.FindStringSubmatch(line)
		if len(matches) != 2 {
			continue
		}
		if name := strings.TrimSpace(matches[1]); name != "" {
			declarations = append(declarations, name)
		}
	}
	return dedupeStrings(declarations)
}

func parsePodLockEntries(content []byte) ([]podLockEntry, error) {
	doc := podLockDocument{}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return nil, err
	}

	entries := make([]podLockEntry, 0, len(doc.Pods))
	for _, raw := range doc.Pods {
		entries = append(entries, podLockEntriesFromRaw(raw, doc)...)
	}
	return dedupePodLockEntries(entries), nil
}

func podLockEntriesFromRaw(raw any, doc podLockDocument) []podLockEntry {
	specs := podLockSpecs(raw)
	entries := make([]podLockEntry, 0, len(specs))
	for _, spec := range specs {
		entry := podLockEntryFromSpec(spec, doc)
		if entry.Name != "" {
			entries = append(entries, entry)
		}
	}
	return entries
}

func podLockSpecs(raw any) []string {
	switch value := raw.(type) {
	case string:
		return []string{value}
	case map[string]any:
		specs := make([]string, 0, len(value))
		for key := range value {
			specs = append(specs, key)
		}
		return specs
	case map[any]any:
		specs := make([]string, 0, len(value))
		for rawKey := range value {
			key, ok := rawKey.(string)
			if ok {
				specs = append(specs, key)
			}
		}
		return specs
	default:
		return nil
	}
}

func podLockEntryFromSpec(spec string, doc podLockDocument) podLockEntry {
	name, version := parsePodSpec(spec)
	if name == "" {
		return podLockEntry{}
	}
	return podLockEntry{
		Name:    name,
		Version: version,
		Source:  podLockSource(doc, name),
	}
}

func parsePodSpec(spec string) (string, string) {
	spec = strings.TrimSpace(strings.TrimSuffix(spec, ":"))
	if spec == "" {
		return "", ""
	}
	open := strings.LastIndex(spec, "(")
	if open < 0 || !strings.HasSuffix(spec, ")") {
		return spec, ""
	}
	name := strings.TrimSpace(spec[:open])
	version := strings.TrimSpace(spec[open+1 : len(spec)-1])
	return name, version
}

func podLockSource(doc podLockDocument, podName string) string {
	for _, sourceMap := range []map[string]map[string]any{doc.CheckoutOptions, doc.ExternalSources} {
		if source := lookupPodSource(sourceMap, podName); source != "" {
			return source
		}
	}
	return ""
}

func lookupPodSource(sourceMap map[string]map[string]any, podName string) string {
	if len(sourceMap) == 0 {
		return ""
	}
	for _, candidate := range podSourceCandidates(podName) {
		if options, ok := sourceMap[candidate]; ok {
			if source := extractPodSource(options); source != "" {
				return source
			}
		}
	}
	return ""
}

func extractPodSource(options map[string]any) string {
	for _, key := range []string{":git", ":path", ":podspec", ":http"} {
		if value, ok := options[key]; ok {
			if source := strings.TrimSpace(fmt.Sprint(value)); source != "" {
				return source
			}
		}
	}
	return ""
}

func podSourceCandidates(podName string) []string {
	name := strings.TrimSpace(podName)
	if name == "" {
		return nil
	}
	base := podBaseName(name)
	if base == "" || base == name {
		return []string{name}
	}
	return []string{name, base}
}

func addPodMappings(catalog *dependencyCatalog, depID string, podName string, ambiguousModules map[string]struct{}) {
	mapAlias(catalog, depID, depID)
	for _, alias := range podAliasCandidates(podName) {
		mapAlias(catalog, alias, depID)
	}
	for _, module := range podModuleCandidates(podName) {
		if setLookupWithStatus(catalog.ModuleToDependency, lookupKey(module), normalizeDependencyID(depID)) {
			ambiguousModules[module] = struct{}{}
		}
	}
}

func podAliasCandidates(podName string) []string {
	parts := podNameParts(podName)
	candidates := []string{strings.TrimSpace(podName)}
	if len(parts) > 0 {
		candidates = append(candidates, parts[0])
	}
	if len(parts) > 1 {
		candidates = append(candidates, strings.Join(parts, ""))
	}
	return dedupeStrings(candidates)
}

func podModuleCandidates(podName string) []string {
	candidates := append([]string{}, podAliasCandidates(podName)...)
	tokens := podModuleTokens(podName)
	if len(tokens) > 1 && strings.EqualFold(tokens[len(tokens)-1], "sdk") {
		candidates = append(candidates, strings.Join(tokens[:len(tokens)-1], ""))
	}
	return dedupeStrings(candidates)
}

func podNameParts(podName string) []string {
	rawParts := strings.Split(strings.TrimSpace(podName), "/")
	parts := make([]string, 0, len(rawParts))
	for _, part := range rawParts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		parts = append(parts, part)
	}
	return parts
}

func podBaseName(podName string) string {
	parts := podNameParts(podName)
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func podModuleTokens(podName string) []string {
	return strings.FieldsFunc(strings.TrimSpace(podName), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func dedupePodLockEntries(entries []podLockEntry) []podLockEntry {
	if len(entries) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(entries))
	result := make([]podLockEntry, 0, len(entries))
	for _, entry := range entries {
		name := normalizeDependencyID(entry.Name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool {
		return normalizeDependencyID(result[i].Name) < normalizeDependencyID(result[j].Name)
	})
	return result
}

func cocoaPodsAmbiguityWarning(ambiguousModules map[string]struct{}) string {
	return formatAmbiguousModuleWarning("CocoaPods", ambiguousModules)
}

func formatAmbiguousModuleWarning(manager string, ambiguousModules map[string]struct{}) string {
	aliases := shared.SortedKeys(ambiguousModules)
	if len(aliases) == 0 {
		return ""
	}
	samples := aliases
	if len(samples) > maxWarningSamples {
		samples = samples[:maxWarningSamples]
	}
	message := "ambiguous " + manager + " module mapping for inferred aliases: " + strings.Join(samples, ", ")
	if len(aliases) > maxWarningSamples {
		message += fmt.Sprintf(", +%d more", len(aliases)-maxWarningSamples)
	}
	return message
}
