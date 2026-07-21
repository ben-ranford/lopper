package analysis

import (
	"regexp"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/pelletier/go-toml/v2"
)

var (
	exactPythonRequirementPattern = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)\s*(?:\[[A-Za-z0-9._,\s-]+\])?\s*(?:==\s*([^,;\s#*)]+)|\(\s*==\s*([^,;\s#*)]+)\s*\))\s*(?:;.*)?$`)
	exactPythonVersionPattern     = regexp.MustCompile(`(?i)^(?:[0-9]+!)?[0-9]+(?:\.[0-9]+)*(?:[-_.]?(?:a|b|c|rc|alpha|beta|pre|preview)[-_.]?[0-9]*)?(?:(?:-[0-9]+)|(?:[-_.]?(?:post|rev|r)[-_.]?[0-9]*))?(?:[-_.]?dev[-_.]?[0-9]*)?(?:\+[a-z0-9]+(?:[-_.][a-z0-9]+)*)?$`)
)

func collectPyprojectManifestEvidence(repoPath, path string, index identityIndex, warnings *identityWarningCollector) {
	document, ok := readPythonManifestDocument(repoPath, path, warnings)
	if !ok {
		return
	}
	source := relativeIdentitySource(repoPath, path)
	project := pythonManifestTable(document["project"])
	addPythonRequirementPins(index, project["dependencies"], source)
	addPythonRequirementGroupPins(index, document["dependency-groups"], source)

	tool := pythonManifestTable(document["tool"])
	uv := pythonManifestTable(tool["uv"])
	addPythonRequirementPins(index, uv["dev-dependencies"], source)
	addPoetryManifestPins(index, pythonManifestTable(tool["poetry"]), source)
}

func collectPipfileManifestEvidence(repoPath, path string, index identityIndex, warnings *identityWarningCollector) {
	document, ok := readPythonManifestDocument(repoPath, path, warnings)
	if !ok {
		return
	}
	source := relativeIdentitySource(repoPath, path)
	addPythonPackageTablePins(index, document["packages"], source, false)
	addPythonPackageTablePins(index, document["dev-packages"], source, false)
}

func readPythonManifestDocument(repoPath, path string, warnings *identityWarningCollector) (map[string]any, bool) {
	data, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		warnings.addFailure("read", path, identityReadFailed, err)
		return nil, false
	}
	var document map[string]any
	if err := toml.Unmarshal(data, &document); err != nil {
		warnings.addFailure("parse", path, identityParseFailed, err)
		return nil, false
	}
	return document, true
}

func addPoetryManifestPins(index identityIndex, poetry map[string]any, source string) {
	addPythonPackageTablePins(index, poetry["dependencies"], source, true)
	addPythonPackageTablePins(index, poetry["dev-dependencies"], source, true)
	groups := pythonManifestTable(poetry["group"])
	for _, groupName := range sortedPythonManifestKeys(groups) {
		group := pythonManifestTable(groups[groupName])
		if optional, _ := group["optional"].(bool); optional {
			continue
		}
		addPythonPackageTablePins(index, group["dependencies"], source, true)
	}
}

func addPythonRequirementGroupPins(index identityIndex, rawGroups any, source string) {
	groups := pythonManifestTable(rawGroups)
	for _, groupName := range sortedPythonManifestKeys(groups) {
		addPythonRequirementPins(index, groups[groupName], source)
	}
}

func addPythonRequirementPins(index identityIndex, rawRequirements any, source string) {
	for _, requirement := range pythonManifestStrings(rawRequirements) {
		name, version, ok := exactPythonRequirementPin(requirement)
		if ok {
			addPythonManifestEvidence(index, name, version, source)
		}
	}
}

func addPythonPackageTablePins(index identityIndex, rawTable any, source string, allowBareVersion bool) {
	table := pythonManifestTable(rawTable)
	for _, name := range sortedPythonManifestKeys(table) {
		if allowBareVersion && strings.EqualFold(strings.TrimSpace(name), "python") {
			continue
		}
		version, ok := exactPythonPackageVersion(table[name], allowBareVersion)
		if ok {
			addPythonManifestEvidence(index, name, version, source)
		}
	}
}

func exactPythonRequirementPin(requirement string) (string, string, bool) {
	matches := exactPythonRequirementPattern.FindStringSubmatch(requirement)
	if len(matches) != 4 {
		return "", "", false
	}
	versionSpec := matches[2]
	if versionSpec == "" {
		versionSpec = matches[3]
	}
	version, ok := exactPythonVersionSpec("=="+versionSpec, false)
	return matches[1], version, ok
}

func exactPythonPackageVersion(rawValue any, allowBareVersion bool) (string, bool) {
	if value, ok := rawValue.(string); ok {
		return exactPythonVersionSpec(value, allowBareVersion)
	}
	metadata, ok := rawValue.(map[string]any)
	if !ok || pythonManifestDependencyUnsupported(metadata) {
		return "", false
	}
	version, _ := metadata["version"].(string)
	return exactPythonVersionSpec(version, allowBareVersion)
}

func pythonManifestDependencyUnsupported(metadata map[string]any) bool {
	if optional, _ := metadata["optional"].(bool); optional {
		return true
	}
	for _, field := range []string{"file", "git", "path", "ref", "url"} {
		if _, ok := metadata[field]; ok {
			return true
		}
	}
	return false
}

func exactPythonVersionSpec(spec string, allowBareVersion bool) (string, bool) {
	spec = strings.TrimSpace(spec)
	version := spec
	switch {
	case strings.HasPrefix(spec, "==="):
		return "", false
	case strings.HasPrefix(spec, "=="):
		version = strings.TrimSpace(strings.TrimPrefix(spec, "=="))
	case !allowBareVersion:
		return "", false
	}
	if version == "" || !exactPythonVersionPattern.MatchString(version) {
		return "", false
	}
	return version, true
}

func addPythonManifestEvidence(index identityIndex, name, version, source string) {
	addIdentityEvidence(index, identityEvidence{
		Language: "python", Ecosystem: "pypi", Name: name, Version: version,
		Status: identityStatusDeclared, Source: source, Confidence: "high",
	})
}

func pythonManifestTable(rawValue any) map[string]any {
	table, _ := rawValue.(map[string]any)
	return table
}

func pythonManifestStrings(rawValue any) []string {
	switch values := rawValue.(type) {
	case []string:
		return values
	case []any:
		stringsOnly := make([]string, 0, len(values))
		for _, value := range values {
			if item, ok := value.(string); ok {
				stringsOnly = append(stringsOnly, item)
			}
		}
		return stringsOnly
	default:
		return nil
	}
}

func sortedPythonManifestKeys(table map[string]any) []string {
	keys := make([]string, 0, len(table))
	for key := range table {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
