package shared

import "strings"

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

var commonAdditionalSkippedDirectories = map[string]bool{
	".cache": true,
	".hg":    true,
	".next":  true,
	".svn":   true,
	"out":    true,
	"target": true,
}

func ShouldSkipCommonDir(name string) bool {
	return ShouldSkipDir(strings.ToLower(name), commonAdditionalSkippedDirectories)
}
