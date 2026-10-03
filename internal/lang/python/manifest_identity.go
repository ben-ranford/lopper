package python

import (
	"regexp"
	"strings"
)

var (
	exactPythonRequirementPattern = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)\s*(?:\[[A-Za-z0-9._,\s-]+\])?\s*(?:==\s*([^,;\s#*)]+)|\(\s*==\s*([^,;\s#*)]+)\s*\))\s*(?:;.*)?$`)
	exactPythonVersionPattern     = regexp.MustCompile(`(?i)^(?:[0-9]+!)?[0-9]+(?:\.[0-9]+)*(?:[-_.]?(?:a|b|c|rc|alpha|beta|pre|preview)[-_.]?[0-9]*)?(?:(?:-[0-9]+)|(?:[-_.]?(?:post|rev|r)[-_.]?[0-9]*))?(?:[-_.]?dev[-_.]?[0-9]*)?(?:\+[a-z0-9]+(?:[-_.][a-z0-9]+)*)?$`)
)

// ExactManifestRequirementPin returns the exact dependency name and version accepted by manifest identity enrichment.
func ExactManifestRequirementPin(requirement string) (string, string, bool) {
	matches := exactPythonRequirementPattern.FindStringSubmatch(requirement)
	if len(matches) != 4 {
		return "", "", false
	}
	versionSpec := matches[2]
	if versionSpec == "" {
		versionSpec = matches[3]
	}
	version, ok := ExactManifestVersionSpec("=="+versionSpec, false)
	return matches[1], version, ok
}

// ExactManifestPackageVersion reads a supported manifest package declaration without changing its existing version rules.
func ExactManifestPackageVersion(rawValue any, allowBareVersion bool) (string, bool) {
	if value, ok := rawValue.(string); ok {
		return ExactManifestVersionSpec(value, allowBareVersion)
	}
	metadata, ok := rawValue.(map[string]any)
	if !ok || ManifestDependencyUnsupported(metadata) {
		return "", false
	}
	version, _ := metadata["version"].(string)
	return ExactManifestVersionSpec(version, allowBareVersion)
}

// ManifestDependencyUnsupported reports optional or direct-source declarations excluded from manifest identity evidence.
func ManifestDependencyUnsupported(metadata map[string]any) bool {
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

// ExactManifestVersionSpec recognizes the version syntax historically accepted by manifest identity enrichment.
func ExactManifestVersionSpec(spec string, allowBareVersion bool) (string, bool) {
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
