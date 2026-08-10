package shared

import "regexp"

const (
	JVMImportMatchGroups       = 4
	jvmImportIdentifierPattern = "(?:[A-Za-z_][A-Za-z0-9_]*|`[A-Za-z_][A-Za-z0-9_]*`)"
)

var jvmImportPattern = regexp.MustCompile(`^\s*import\s+(?:static\s+)?(` + jvmImportIdentifierPattern + `(?:\.` + jvmImportIdentifierPattern + `)*)(\.\*)?(?:\s+as\s+(` + jvmImportIdentifierPattern + `))?\s*;?\s*$`)

var kotlinHardKeywords = map[string]struct{}{
	"as": {}, "break": {}, "class": {}, "continue": {}, "do": {}, "else": {}, "false": {},
	"for": {}, "fun": {}, "if": {}, "in": {}, "interface": {}, "is": {}, "null": {}, "object": {},
	"package": {}, "return": {}, "super": {}, "this": {}, "throw": {}, "true": {}, "try": {},
	"typealias": {}, "typeof": {}, "val": {}, "var": {}, "when": {}, "while": {},
}

// MatchJVMImport recognizes Java imports plus Kotlin escaped identifier and
// alias segments. JVM and Kotlin Android scanners share this grammar.
func MatchJVMImport(line string) []string {
	return jvmImportPattern.FindStringSubmatch(line)
}

// IsKotlinEscapedKeyword reports whether a backticked Kotlin local cannot be
// referenced without backticks. Legal bare identifiers remain usable either way.
func IsKotlinEscapedKeyword(local string) bool {
	if len(local) < 3 || local[0] != '`' || local[len(local)-1] != '`' {
		return false
	}
	_, ok := kotlinHardKeywords[local[1:len(local)-1]]
	return ok
}
