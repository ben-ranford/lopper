package csvsanitize

func EscapeLeadingFormula(value string) string {
	if value == "" {
		return value
	}

	switch value[0] {
	case '=', '+', '-', '@':
		return "'" + value
	default:
		return value
	}
}

func EscapeLeadingFormulaRow(values []string) []string {
	sanitized := make([]string, len(values))
	for i, value := range values {
		sanitized[i] = EscapeLeadingFormula(value)
	}
	return sanitized
}
