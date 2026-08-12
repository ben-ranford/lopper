// Package pep440 compares Python package versions using PEP 440 ordering.
package pep440

import (
	"regexp"
	"strings"
)

var pep440VersionRe = regexp.MustCompile(`(?i)^(?:v)?(?:(\d+)!)?(\d+(?:\.\d+)*)(?:[-_.]?(a|b|c|rc|alpha|beta|pre|preview)[-_.]?(\d*))?(?:(?:-(\d+))|(?:[-_.]?(post|rev|r)[-_.]?(\d*)))?(?:[-_.]?(dev)[-_.]?(\d*))?(?:\+([a-z0-9]+(?:[._-][a-z0-9]+)*))?$`)
var pep440LocalSeparatorRe = regexp.MustCompile(`[-_.]+`)

type pep440Version struct {
	epoch      string
	release    []string
	preKind    int
	preNumber  string
	postNumber string
	devNumber  string
	localParts []string
	hasPost    bool
	hasDev     bool
	hasLocal   bool
}

// CompareVersions compares valid Python PEP 440 public and local versions.
// It deliberately rejects values outside the supported PEP 440 grammar so callers
// can retain their unordered-version behavior instead of guessing an ordering.
func CompareVersions(left, right string) (int, bool) {
	leftVersion, ok := parsePEP440Version(left)
	if !ok {
		return 0, false
	}
	rightVersion, ok := parsePEP440Version(right)
	if !ok {
		return 0, false
	}
	return comparePEP440Versions(leftVersion, rightVersion), true
}

func parsePEP440Version(value string) (pep440Version, bool) {
	matches := pep440VersionRe.FindStringSubmatch(strings.TrimSpace(value))
	if len(matches) == 0 {
		return pep440Version{}, false
	}
	version := pep440Version{
		epoch:      normalizedPEP440Number(matches[1]),
		release:    normalizedPEP440Release(matches[2]),
		preNumber:  normalizedPEP440OptionalNumber(matches[4]),
		postNumber: normalizedPEP440OptionalNumber(firstPEP440Value(matches[5], matches[7])),
		devNumber:  normalizedPEP440OptionalNumber(matches[9]),
		hasPost:    matches[5] != "" || matches[6] != "",
		hasDev:     matches[8] != "",
		hasLocal:   matches[10] != "",
	}
	if version.epoch == "" {
		version.epoch = "0"
	}
	switch strings.ToLower(matches[3]) {
	case "a":
		version.preKind = 0
	case "b":
		version.preKind = 1
	case "c", "rc", "pre", "preview":
		version.preKind = 2
	case "alpha":
		version.preKind = 0
	case "beta":
		version.preKind = 1
	default:
		version.preKind = 3
		if version.hasDev && !version.hasPost {
			version.preKind = -1
		}
	}
	if version.hasLocal {
		version.localParts = pep440LocalSeparatorRe.Split(strings.ToLower(matches[10]), -1)
	}
	return version, true
}

func firstPEP440Value(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func comparePEP440Versions(left, right pep440Version) int {
	for _, compare := range []func(pep440Version, pep440Version) int{
		comparePEP440Epoch,
		comparePEP440ReleaseVersion,
		comparePEP440PreRelease,
		comparePEP440PostRelease,
		comparePEP440DevRelease,
		comparePEP440LocalVersion,
	} {
		if cmp := compare(left, right); cmp != 0 {
			return cmp
		}
	}
	return 0
}

func comparePEP440Epoch(left, right pep440Version) int {
	return comparePEP440Numbers(left.epoch, right.epoch)
}

func comparePEP440ReleaseVersion(left, right pep440Version) int {
	return comparePEP440Release(left.release, right.release)
}

func comparePEP440PreRelease(left, right pep440Version) int {
	if left.preKind != right.preKind {
		return comparePEP440Ints(left.preKind, right.preKind)
	}
	if left.preKind >= 3 {
		return 0
	}
	return comparePEP440Numbers(left.preNumber, right.preNumber)
}

func comparePEP440PostRelease(left, right pep440Version) int {
	return comparePEP440OptionalNumber(left.hasPost, left.postNumber, right.hasPost, right.postNumber, 1)
}

func comparePEP440DevRelease(left, right pep440Version) int {
	return comparePEP440OptionalNumber(left.hasDev, left.devNumber, right.hasDev, right.devNumber, -1)
}

func comparePEP440LocalVersion(left, right pep440Version) int {
	return comparePEP440LocalParts(left.localParts, right.localParts)
}

func comparePEP440OptionalNumber(leftPresent bool, left string, rightPresent bool, right string, presentOrder int) int {
	if leftPresent != rightPresent {
		if leftPresent {
			return presentOrder
		}
		return -presentOrder
	}
	if !leftPresent {
		return 0
	}
	return comparePEP440Numbers(left, right)
}

func normalizedPEP440Release(value string) []string {
	parts := strings.Split(value, ".")
	for len(parts) > 1 && normalizedPEP440Number(parts[len(parts)-1]) == "0" {
		parts = parts[:len(parts)-1]
	}
	for index := range parts {
		parts[index] = normalizedPEP440Number(parts[index])
	}
	return parts
}

func normalizedPEP440Number(value string) string {
	trimmed := strings.TrimLeft(value, "0")
	if trimmed == "" && value != "" {
		return "0"
	}
	return trimmed
}

func normalizedPEP440OptionalNumber(value string) string {
	if value == "" {
		return "0"
	}
	return normalizedPEP440Number(value)
}

func comparePEP440Release(left, right []string) int {
	return comparePEP440PartSlices(left, right, comparePEP440Numbers)
}

func comparePEP440Numbers(left, right string) int {
	if len(left) != len(right) {
		return comparePEP440Ints(len(left), len(right))
	}
	return strings.Compare(left, right)
}

func comparePEP440Ints(left, right int) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func comparePEP440LocalParts(left, right []string) int {
	if len(left) == 0 || len(right) == 0 {
		return comparePEP440Ints(len(left), len(right))
	}
	return comparePEP440PartSlices(left, right, comparePEP440LocalPart)
}

func comparePEP440PartSlices(left, right []string, compare func(string, string) int) int {
	commonLength := len(left)
	if len(right) < commonLength {
		commonLength = len(right)
	}
	for index := 0; index < commonLength; index++ {
		if cmp := compare(left[index], right[index]); cmp != 0 {
			return cmp
		}
	}
	return comparePEP440Ints(len(left), len(right))
}

func comparePEP440LocalPart(left, right string) int {
	leftNumeric := isPEP440NumericPart(left)
	rightNumeric := isPEP440NumericPart(right)
	switch {
	case leftNumeric && rightNumeric:
		return comparePEP440Numbers(normalizedPEP440Number(left), normalizedPEP440Number(right))
	case leftNumeric:
		return 1
	case rightNumeric:
		return -1
	default:
		return strings.Compare(left, right)
	}
}

func isPEP440NumericPart(value string) bool {
	return value != "" && strings.Trim(value, "0123456789") == ""
}
