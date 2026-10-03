package analysis

import (
	"sort"
	"strings"

	pythonlang "github.com/ben-ranford/lopper/internal/lang/python"
)

func collectPyprojectManifestEvidence(repoPath, path string, index identityIndex, warnings *identityWarningCollector) {
	document, ok := readPythonManifestDocument(repoPath, path, warnings)
	if !ok {
		return
	}
	collectPyprojectManifestEvidenceDocument(repoPath, path, index, document)
}

func collectPyprojectManifestEvidenceDocument(repoPath, path string, index identityIndex, document map[string]any) {
	source := relativeIdentitySource(repoPath, path)
	project := pythonManifestTable(document["project"])
	addPythonRequirementPins(index, project["dependencies"], source)
	addPythonOptionalRequirementPins(index, project["optional-dependencies"], source)
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
	collectPipfileManifestEvidenceDocument(repoPath, path, index, document)
}

func collectPipfileManifestEvidenceDocument(repoPath, path string, index identityIndex, document map[string]any) {
	source := relativeIdentitySource(repoPath, path)
	addPythonPackageTablePins(index, document["packages"], source, false)
	addPythonPackageTablePins(index, document["dev-packages"], source, false)
}

func readPythonManifestDocument(repoPath, path string, warnings *identityWarningCollector) (map[string]any, bool) {
	document, err := pythonlang.ReadPackagingDocument(repoPath, path)
	if err != nil {
		kind := identityReadFailed
		if document.FailureStage == "parse" {
			kind = identityParseFailed
		}
		warnings.addFailure(document.FailureStage, path, kind, err)
		return nil, false
	}
	return document.Document, true
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

func addPythonOptionalRequirementPins(index identityIndex, rawGroups any, source string) {
	addPythonRequirementGroupPins(index, rawGroups, source)
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
	return pythonlang.ExactManifestRequirementPin(requirement)
}

func exactPythonPackageVersion(rawValue any, allowBareVersion bool) (string, bool) {
	return pythonlang.ExactManifestPackageVersion(rawValue, allowBareVersion)
}

func exactPythonVersionSpec(spec string, allowBareVersion bool) (string, bool) {
	return pythonlang.ExactManifestVersionSpec(spec, allowBareVersion)
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
	return pythonlang.ManifestRequirementStrings(rawValue)
}

func sortedPythonManifestKeys(table map[string]any) []string {
	keys := make([]string, 0, len(table))
	for key := range table {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
