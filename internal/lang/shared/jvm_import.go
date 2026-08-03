package shared

import "regexp"

const (
	JVMImportMatchGroups           = 4
	kotlinEscapedIdentifierPattern = "`[^`\\r\\n]+`"
)

var jvmImportPattern = regexp.MustCompile(`^\s*import\s+(?:static\s+)?((?:[A-Za-z_][A-Za-z0-9_]*|` + kotlinEscapedIdentifierPattern + `)(?:\.(?:[A-Za-z_][A-Za-z0-9_]*|` + kotlinEscapedIdentifierPattern + `))*)(\.\*)?(?:\s+as\s+([A-Za-z_][A-Za-z0-9_]*|` + kotlinEscapedIdentifierPattern + `))?\s*;?\s*$`)

// MatchJVMImport recognizes Java imports plus Kotlin escaped identifier and
// alias segments. JVM and Kotlin Android scanners share this grammar.
func MatchJVMImport(line string) []string {
	return jvmImportPattern.FindStringSubmatch(line)
}
