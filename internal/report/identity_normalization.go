package report

import (
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

var pypiPackageSeparatorRe = regexp.MustCompile(`[-_.]+`)
var purlTypeRe = regexp.MustCompile(`^[a-z][a-z0-9-.]+$`)

func CanonicalPackageEcosystem(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "packagist", "composer":
		return "composer"
	case "crates.io", "crates", "cargo":
		return "cargo"
	case "ruby", "rubygems", "gem":
		return "gem"
	case "elixir", "hex":
		return "hex"
	default:
		return normalized
	}
}

func CanonicalPackageNameForEcosystem(ecosystem, value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return ""
	}
	switch CanonicalPackageEcosystem(ecosystem) {
	case "pypi":
		return pypiPackageSeparatorRe.ReplaceAllString(normalized, "-")
	default:
		return normalized
	}
}

func CanonicalPURL(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || !strings.HasPrefix(strings.ToLower(trimmed), "pkg:") {
		return trimmed
	}
	purlType, path, version, suffix, ok := splitCanonicalPURL(trimmed)
	if !ok {
		return value
	}
	return "pkg:" + purlType + canonicalizePURLPath(purlType, path) + canonicalizePURLVersion(version) + suffix
}

func VersionlessCanonicalPURL(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || !strings.HasPrefix(strings.ToLower(trimmed), "pkg:") {
		return trimmed
	}
	purl := CanonicalPURL(value)
	if purl == "" {
		return ""
	}
	purlType, path, version, suffix, ok := splitCanonicalPURL(purl)
	if !ok || version == "" {
		return purl
	}
	return "pkg:" + purlType + path + suffix
}

func CompareSemanticVersions(left, right string) (int, bool) {
	normalizedLeft, okLeft := normalizeSemanticVersion(left)
	normalizedRight, okRight := normalizeSemanticVersion(right)
	if !okLeft || !okRight {
		return 0, false
	}
	return semver.Compare(normalizedLeft, normalizedRight), true
}

func normalizeSemanticVersion(value string) (string, bool) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "", false
	}
	switch normalized[0] {
	case 'v':
	case 'V':
		normalized = "v" + normalized[1:]
	default:
		normalized = "v" + normalized
	}
	if !semver.IsValid(normalized) {
		return "", false
	}
	return normalized, true
}

func splitCanonicalPURL(value string) (purlType, path, version, suffix string, ok bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || !strings.HasPrefix(strings.ToLower(trimmed), "pkg:") {
		return "", "", "", "", false
	}
	remainder := trimmed[len("pkg:"):]
	slash := strings.IndexByte(remainder, '/')
	if slash <= 0 {
		return "", "", "", "", false
	}
	purlType = CanonicalPackageEcosystem(strings.TrimSpace(remainder[:slash]))
	if purlType == "" || !purlTypeRe.MatchString(purlType) {
		return "", "", "", "", false
	}
	rest := remainder[slash:]
	suffixStart := len(rest)
	for _, index := range []int{strings.IndexByte(rest, '?'), strings.IndexByte(rest, '#')} {
		if index >= 0 && index < suffixStart {
			suffixStart = index
		}
	}
	pathAndVersion := rest[:suffixStart]
	suffix = rest[suffixStart:]
	path, version = splitPURLPathVersion(purlType, pathAndVersion)
	if !isValidPURLPath(purlType, path) || !isValidPURLVersion(version) || !isValidPURLSuffix(suffix) {
		return "", "", "", "", false
	}
	return purlType, path, version, suffix, true
}

func splitPURLPathVersion(purlType, pathAndVersion string) (path, version string) {
	at := strings.LastIndexByte(pathAndVersion, '@')
	if at < 0 || at+1 >= len(pathAndVersion) {
		return pathAndVersion, ""
	}
	candidatePath := pathAndVersion[:at]
	if !isValidPURLPath(purlType, candidatePath) {
		return pathAndVersion, ""
	}
	return candidatePath, pathAndVersion[at+1:]
}

func canonicalizePURLPath(purlType, path string) string {
	if path == "" {
		return ""
	}
	segments, ok := decodedPURLPathSegments(purlType, path)
	if !ok {
		return path
	}
	switch purlType {
	case "npm":
		if len(segments) == 1 && !strings.HasPrefix(segments[0], "@") {
			return "/" + escapePURLSegment(segments[0])
		}
		if len(segments) == 2 && strings.HasPrefix(segments[0], "@") {
			return "/" + escapeNPMScopeSegment(segments[0]) + "/" + escapePURLSegment(segments[1])
		}
		return path
	case "pypi":
		if len(segments) != 1 {
			return path
		}
		return "/" + escapePURLSegment(CanonicalPackageNameForEcosystem(purlType, segments[0]))
	default:
		escaped := make([]string, 0, len(segments))
		for _, segment := range segments {
			escaped = append(escaped, escapePURLSegment(segment))
		}
		return "/" + strings.Join(escaped, "/")
	}
}

func canonicalizePURLVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return ""
	}
	decoded, err := url.PathUnescape(version)
	if err != nil {
		return "@" + version
	}
	return "@" + escapePURLSegment(decoded)
}

func decodedPURLPathSegments(purlType, path string) ([]string, bool) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(path), "/")
	if trimmed == "" {
		return nil, false
	}
	rawSegments := strings.Split(trimmed, "/")
	segments := make([]string, 0, len(rawSegments))
	for _, rawSegment := range rawSegments {
		if rawSegment == "" {
			return nil, false
		}
		decoded, ok := decodePURLPathSegment(rawSegment)
		if !ok {
			return nil, false
		}
		var appended bool
		segments, appended = appendDecodedPURLPathSegments(segments, purlType, decoded)
		if !appended {
			return nil, false
		}
	}
	return segments, len(segments) > 0
}

func decodePURLPathSegment(rawSegment string) (string, bool) {
	decoded, err := url.PathUnescape(rawSegment)
	if err != nil {
		return "", false
	}
	return decoded, true
}

func appendDecodedPURLPathSegments(segments []string, purlType, decoded string) ([]string, bool) {
	if purlType != "composer" || !strings.Contains(decoded, "/") {
		return append(segments, decoded), true
	}
	for _, composerSegment := range strings.Split(decoded, "/") {
		if composerSegment == "" {
			return nil, false
		}
		segments = append(segments, composerSegment)
	}
	return segments, true
}

func isValidPURLPath(purlType, path string) bool {
	segments, ok := decodedPURLPathSegments(purlType, path)
	if !ok {
		return false
	}
	switch purlType {
	case "npm":
		switch len(segments) {
		case 1:
			return !strings.HasPrefix(segments[0], "@")
		case 2:
			return strings.HasPrefix(segments[0], "@")
		default:
			return false
		}
	case "pypi":
		return len(segments) == 1
	default:
		return len(segments) > 0
	}
}

func PURLVersion(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || !strings.HasPrefix(strings.ToLower(trimmed), "pkg:") {
		return ""
	}
	_, _, version, _, ok := splitCanonicalPURL(CanonicalPURL(value))
	if !ok {
		return ""
	}
	return strings.TrimSpace(version)
}

func isValidPURLVersion(version string) bool {
	if version == "" {
		return true
	}
	_, err := url.PathUnescape(version)
	return err == nil
}

func isValidPURLSuffix(suffix string) bool {
	if suffix == "" {
		return true
	}
	_, err := url.PathUnescape(suffix)
	return err == nil
}

func escapePURLSegment(value string) string {
	return strings.ReplaceAll(url.PathEscape(strings.TrimSpace(value)), "+", "%2B")
}

func escapeNPMScopeSegment(value string) string {
	return strings.ReplaceAll(escapePURLSegment(value), "@", "%40")
}
