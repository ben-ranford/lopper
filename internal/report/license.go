package report

import "sort"

const (
	licenseSourceUnknown = "unknown"
)

func NormalizeDependencyLicenses(dependencies []DependencyReport) {
	for i := range dependencies {
		if dependencies[i].License != nil {
			continue
		}
		dependencies[i].License = &DependencyLicense{
			Source:     licenseSourceUnknown,
			Confidence: "low",
			Unknown:    true,
		}
	}
}

func ApplyLicensePolicy(dependencies []DependencyReport, denyList []string) {
	normalizedDeny := normalizeDenyList(denyList)
	if len(normalizedDeny) == 0 {
		for i := range dependencies {
			if dependencies[i].License == nil {
				continue
			}
			dependencies[i].License.Denied = false
		}
		return
	}

	for i := range dependencies {
		license := dependencies[i].License
		if license == nil {
			continue
		}
		license.Denied = spdxExpressionContainsDenied(license.SPDX, normalizedDeny)
	}
}

func CountDeniedLicenses(dependencies []DependencyReport) int {
	count := 0
	for _, dep := range dependencies {
		if dep.License != nil && dep.License.Denied {
			count++
		}
	}
	return count
}

func normalizeDenyList(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	normalized := make(map[string]struct{}, len(values))
	for _, value := range values {
		id := normalizeSPDXID(value)
		if id == "" {
			continue
		}
		normalized[id] = struct{}{}
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func normalizeSPDXID(value string) string {
	out := make([]rune, 0, len(value))
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			out = append(out, r-'a'+'A')
		case r >= 'A' && r <= 'Z':
			out = append(out, r)
		case r >= '0' && r <= '9':
			out = append(out, r)
		case r == '-', r == '.', r == '+':
			out = append(out, r)
		default:
			// Skip separators and punctuation.
		}
	}
	return string(out)
}

func spdxExpressionContainsDenied(expression string, deny map[string]struct{}) bool {
	if len(deny) == 0 {
		return false
	}
	token := make([]rune, 0, len(expression))
	flush := func() bool {
		if len(token) == 0 {
			return false
		}
		id := normalizeSPDXID(string(token))
		token = token[:0]
		if id == "" {
			return false
		}
		_, denied := deny[id]
		return denied
	}

	for _, r := range expression {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-',
			r == '.',
			r == '+':
			token = append(token, r)
		default:
			if flush() {
				return true
			}
		}
	}
	return flush()
}

func SortedDenyList(values []string) []string {
	seen := normalizeDenyList(values)
	if len(seen) == 0 {
		return nil
	}
	items := make([]string, 0, len(seen))
	for value := range seen {
		items = append(items, value)
	}
	sort.Strings(items)
	return items
}
