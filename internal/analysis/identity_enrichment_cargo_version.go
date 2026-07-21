package analysis

import (
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

type cargoPartialVersion struct {
	normalized string
	major      int
	minor      int
	patch      int
	components int
}

type cargoRequirementOperator uint8

const (
	cargoRequirementCaret cargoRequirementOperator = iota
	cargoRequirementExact
	cargoRequirementGreater
	cargoRequirementGreaterEqual
	cargoRequirementLess
	cargoRequirementLessEqual
	cargoRequirementTilde
	cargoRequirementWildcard
)

type cargoRequirementClause struct {
	operator cargoRequirementOperator
	version  cargoPartialVersion
}

func cargoVersionSatisfiesRequirement(version, requirement string) bool {
	locked, ok := parseCargoPartialVersion(version)
	if !ok || locked.components != 3 {
		return false
	}
	requirement = strings.TrimSpace(requirement)
	if requirement == "" {
		return false
	}
	clauses := make([]cargoRequirementClause, 0, strings.Count(requirement, ",")+1)
	for _, rawClause := range strings.Split(requirement, ",") {
		clause, ok := parseCargoRequirementClause(rawClause)
		if !ok || !cargoRequirementClauseMatches(clause, locked) {
			return false
		}
		clauses = append(clauses, clause)
	}
	if semver.Prerelease(locked.normalized) == "" {
		return true
	}
	for _, clause := range clauses {
		if cargoRequirementActivatesPrerelease(clause, locked) {
			return true
		}
	}
	return false
}

func parseCargoRequirementClause(value string) (cargoRequirementClause, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return cargoRequirementClause{}, false
	}
	operator := cargoRequirementCaret
	explicitOperator := false
	for _, candidate := range []struct {
		prefix   string
		operator cargoRequirementOperator
	}{
		{prefix: ">=", operator: cargoRequirementGreaterEqual},
		{prefix: "<=", operator: cargoRequirementLessEqual},
		{prefix: ">", operator: cargoRequirementGreater},
		{prefix: "<", operator: cargoRequirementLess},
		{prefix: "=", operator: cargoRequirementExact},
		{prefix: "^", operator: cargoRequirementCaret},
		{prefix: "~", operator: cargoRequirementTilde},
	} {
		if strings.HasPrefix(value, candidate.prefix) {
			operator = candidate.operator
			explicitOperator = true
			value = strings.TrimSpace(strings.TrimPrefix(value, candidate.prefix))
			break
		}
	}
	if wildcardVersion, detected, valid := parseCargoWildcardVersion(value); detected {
		if explicitOperator || !valid {
			return cargoRequirementClause{}, false
		}
		return cargoRequirementClause{operator: cargoRequirementWildcard, version: wildcardVersion}, true
	}
	version, ok := parseCargoPartialVersion(value)
	if !ok || (version.components != 3 && semver.Prerelease(version.normalized) != "") {
		return cargoRequirementClause{}, false
	}
	return cargoRequirementClause{operator: operator, version: version}, true
}

func parseCargoWildcardVersion(value string) (cargoPartialVersion, bool, bool) {
	parts := strings.Split(strings.TrimSpace(value), ".")
	wildcardIndex := -1
	for index, part := range parts {
		if part == "*" || strings.EqualFold(part, "x") {
			if wildcardIndex == -1 {
				wildcardIndex = index
			}
			continue
		}
		if wildcardIndex != -1 {
			return cargoPartialVersion{}, true, false
		}
	}
	if wildcardIndex == -1 {
		return cargoPartialVersion{}, false, false
	}
	if len(parts) > 3 {
		return cargoPartialVersion{}, true, false
	}
	if wildcardIndex == 0 {
		return cargoPartialVersion{}, true, len(parts) == 1
	}
	version, ok := parseCargoPartialVersion(strings.Join(parts[:wildcardIndex], "."))
	return version, true, ok
}

func cargoRequirementClauseMatches(clause cargoRequirementClause, locked cargoPartialVersion) bool {
	switch clause.operator {
	case cargoRequirementExact, cargoRequirementWildcard:
		return cargoVersionsMatchExact(clause.version, locked)
	case cargoRequirementGreater:
		return cargoVersionGreaterThanPartial(clause.version, locked)
	case cargoRequirementGreaterEqual:
		return cargoVersionsMatchExact(clause.version, locked) || cargoVersionGreaterThanPartial(clause.version, locked)
	case cargoRequirementLess:
		return cargoVersionLessThanPartial(clause.version, locked)
	case cargoRequirementLessEqual:
		return cargoVersionsMatchExact(clause.version, locked) || cargoVersionLessThanPartial(clause.version, locked)
	case cargoRequirementTilde:
		return cargoVersionMatchesTilde(clause.version, locked)
	case cargoRequirementCaret:
		return cargoVersionMatchesCaret(clause.version, locked)
	default:
		return false
	}
}

func cargoVersionsMatchExact(required, locked cargoPartialVersion) bool {
	if required.components >= 1 && locked.major != required.major {
		return false
	}
	if required.components >= 2 && locked.minor != required.minor {
		return false
	}
	if required.components >= 3 && locked.patch != required.patch {
		return false
	}
	return semver.Prerelease(locked.normalized) == semver.Prerelease(required.normalized)
}

func cargoVersionGreaterThanPartial(required, locked cargoPartialVersion) bool {
	if locked.major != required.major {
		return locked.major > required.major
	}
	if required.components < 2 {
		return false
	}
	if locked.minor != required.minor {
		return locked.minor > required.minor
	}
	if required.components < 3 {
		return false
	}
	return semver.Compare(locked.normalized, required.normalized) > 0
}

func cargoVersionLessThanPartial(required, locked cargoPartialVersion) bool {
	if locked.major != required.major {
		return locked.major < required.major
	}
	if required.components < 2 {
		return false
	}
	if locked.minor != required.minor {
		return locked.minor < required.minor
	}
	if required.components < 3 {
		return false
	}
	return semver.Compare(locked.normalized, required.normalized) < 0
}

func cargoVersionMatchesTilde(required, locked cargoPartialVersion) bool {
	if locked.major != required.major {
		return false
	}
	if required.components == 1 {
		return true
	}
	if locked.minor != required.minor {
		return false
	}
	return required.components == 2 || semver.Compare(locked.normalized, required.normalized) >= 0
}

func cargoVersionMatchesCaret(required, locked cargoPartialVersion) bool {
	if locked.major != required.major {
		return false
	}
	if required.components == 1 {
		return true
	}
	if required.major > 0 {
		if locked.minor != required.minor {
			return locked.minor > required.minor
		}
		return required.components == 2 || semver.Compare(locked.normalized, required.normalized) >= 0
	}
	if locked.minor != required.minor {
		return false
	}
	if required.components == 2 {
		return true
	}
	if required.minor > 0 {
		return semver.Compare(locked.normalized, required.normalized) >= 0
	}
	return locked.patch == required.patch && semver.Compare(locked.normalized, required.normalized) >= 0
}

func cargoRequirementActivatesPrerelease(clause cargoRequirementClause, locked cargoPartialVersion) bool {
	required := clause.version
	return required.components == 3 &&
		required.major == locked.major &&
		required.minor == locked.minor &&
		required.patch == locked.patch &&
		semver.Prerelease(required.normalized) != ""
}

func parseCargoPartialVersion(value string) (cargoPartialVersion, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return cargoPartialVersion{}, false
	}
	coreEnd := len(value)
	for _, separator := range []string{"-", "+"} {
		if index := strings.Index(value, separator); index >= 0 && index < coreEnd {
			coreEnd = index
		}
	}
	parts := strings.Split(value[:coreEnd], ".")
	if len(parts) == 0 || len(parts) > 3 {
		return cargoPartialVersion{}, false
	}
	major, minor, patch := 0, 0, 0
	for index, part := range parts {
		component, err := strconv.Atoi(part)
		if err != nil || component < 0 {
			return cargoPartialVersion{}, false
		}
		switch index {
		case 0:
			major = component
		case 1:
			minor = component
		case 2:
			patch = component
		}
	}
	normalized := cargoVersion(major, minor, patch) + value[coreEnd:]
	if !semver.IsValid(normalized) {
		return cargoPartialVersion{}, false
	}
	return cargoPartialVersion{
		normalized: normalized,
		major:      major,
		minor:      minor,
		patch:      patch,
		components: len(parts),
	}, true
}

func cargoVersion(major, minor, patch int) string {
	return "v" + strconv.Itoa(major) + "." + strconv.Itoa(minor) + "." + strconv.Itoa(patch)
}
