package gitexec

// ValidObjectID reports whether value is a full SHA-1 or SHA-256 Git object ID.
// Hexadecimal digits may be lowercase or uppercase; abbreviations and refs are rejected.
func ValidObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}
