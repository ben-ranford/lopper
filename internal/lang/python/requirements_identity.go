package python

import (
	"regexp"
	"strings"
)

var requirementIdentityVersionPattern = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)\s*(\[[A-Za-z0-9._,\s-]+\])?\s*==\s*([^;\s#]+)`)

// VisitRequirementIdentityPins visits the name and version prefixes historically
// used for requirements.txt identity evidence. It deliberately does not validate
// versions or require the whole line to match, and preserves duplicate order.
func VisitRequirementIdentityPins(text string, visit func(name, version string)) {
	for _, line := range strings.Split(text, "\n") {
		matches := requirementIdentityVersionPattern.FindStringSubmatch(line)
		if len(matches) == 4 {
			visit(matches[1], matches[3])
		}
	}
}

func compactRequirementsIdentityText(text string) string {
	var compact strings.Builder
	VisitRequirementIdentityPins(text, func(name, version string) {
		compact.WriteString(name)
		compact.WriteString("==")
		compact.WriteString(version)
		compact.WriteByte('\n')
	})
	return compact.String()
}
