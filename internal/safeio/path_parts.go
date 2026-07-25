package safeio

import "strings"

func nonDotPathParts(path, separator string) []string {
	rawParts := strings.Split(path, separator)
	parts := rawParts[:0]
	for _, part := range rawParts {
		if part == "" || part == "." {
			continue
		}
		parts = append(parts, part)
	}
	return parts
}
