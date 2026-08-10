package shared

import "regexp"

const (
	JVMImportMatchGroups       = 4
	jvmImportIdentifierPattern = "(?:[A-Za-z_][A-Za-z0-9_]*|`[A-Za-z_][A-Za-z0-9_]*`)"
)

var jvmImportPattern = regexp.MustCompile(`^\s*import\s+(?:static\s+)?(` + jvmImportIdentifierPattern + `(?:\.` + jvmImportIdentifierPattern + `)*)(\.\*)?(?:\s+as\s+(` + jvmImportIdentifierPattern + `))?\s*;?\s*$`)

// MatchJVMImport recognizes Java imports plus Kotlin escaped identifier and
// alias segments. JVM and Kotlin Android scanners share this grammar.
func MatchJVMImport(line string) []string {
	return jvmImportPattern.FindStringSubmatch(line)
}
