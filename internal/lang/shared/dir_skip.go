package shared

var baselineSkipDirectories = map[string]bool{
	".git":         true,
	".idea":        true,
	"node_modules": true,
	"dist":         true,
	"build":        true,
	"vendor":       true,
}

func ShouldSkipDir(name string, languageSpecific map[string]bool) bool {
	if baselineSkipDirectories[name] {
		return true
	}
	return languageSpecific[name]
}
