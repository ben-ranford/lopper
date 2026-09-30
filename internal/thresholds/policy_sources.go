package thresholds

// PrependPolicySource gives source the highest precedence and retains the first
// occurrence of every other source in order. Source names are exact identifiers:
// whitespace and empty names are preserved. The result never aliases sources.
func PrependPolicySource(source string, sources []string) []string {
	out := []string{source}
	seen := map[string]struct{}{source: {}}
	for _, item := range sources {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}
