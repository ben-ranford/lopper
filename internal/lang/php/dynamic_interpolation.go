package php

import (
	"regexp"
	"strings"
)

var dynamicInterpolationExpressionPattern = regexp.MustCompile(`^(?:\$+[A-Za-z_][A-Za-z0-9_]*(?:->\$*[A-Za-z_][A-Za-z0-9_]*)*\s*::|\b(?:class_exists|interface_exists|trait_exists|method_exists)\s*\()`)

func hasPHPDynamicInterpolation(text string) bool {
	state := phpStateCode
	for offset := 0; offset < len(text); {
		next, dynamic := advancePHPDynamicInterpolation(text, offset, &state)
		if dynamic {
			return true
		}
		offset = next
	}
	return false
}

func advancePHPDynamicInterpolation(text string, offset int, state *phpCodeState) (int, bool) {
	if *state == phpStateCode && strings.HasPrefix(text[offset:], "<<<") {
		if next, dynamic := dynamicHeredocInterpolation(text, offset); next > offset {
			return next, dynamic
		}
	}
	if *state == phpStateDoubleQuote || *state == phpStateBacktick {
		if hasDynamicInterpolationAt(text, offset) {
			return offset, true
		}
	}
	return advancePHPCodeState(text, offset, state), false
}

func dynamicHeredocInterpolation(text string, offset int) (int, bool) {
	lineEnd := nextPHPLineEnd(text, offset)
	marker := strings.TrimLeft(text[offset+len("<<<"):lineEnd], " \t")
	label, ok := parseHeredocNowdocLabelAfterMarker(marker)
	if !ok {
		return offset, false
	}
	bodyStart := nextPHPLineStart(text, lineEnd)
	bodyEnd, _, found := findHeredocNowdocTerminatorRange(text, bodyStart, label)
	if !found {
		bodyEnd = len(text)
	}
	if strings.HasPrefix(marker, "'") {
		return bodyEnd, false
	}
	return bodyEnd, hasDynamicHeredocBody(text[bodyStart:bodyEnd])
}

func hasDynamicHeredocBody(text string) bool {
	for offset := 0; offset < len(text); offset++ {
		if text[offset] == '\\' && offset+1 < len(text) {
			offset++
			continue
		}
		if hasDynamicInterpolationAt(text, offset) {
			return true
		}
	}
	return false
}

func hasDynamicInterpolationAt(text string, offset int) bool {
	if offset+1 >= len(text) || text[offset] != '{' || text[offset+1] != '$' {
		return false
	}
	end := interpolationExpressionEnd(text, offset)
	return hasDynamicInterpolationExpression(text[offset+1 : end])
}

func interpolationExpressionEnd(text string, start int) int {
	depth := 1
	var quote byte
	for offset := start + 1; offset < len(text); offset++ {
		if quote != 0 {
			if text[offset] == '\\' {
				offset++
				continue
			}
			if text[offset] == quote {
				quote = 0
			}
			continue
		}
		switch text[offset] {
		case '\'', '"', '`':
			quote = text[offset]
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return offset
			}
		}
	}
	return len(text)
}

func hasDynamicInterpolationExpression(expression string) bool {
	for offset := 0; offset < len(expression); {
		if quote := expression[offset]; quote == '\'' || quote == '"' || quote == '`' {
			offset = skipPHPStringLiteral(expression, offset, quote)
			continue
		}
		if dynamicInterpolationExpressionPattern.MatchString(expression[offset:]) {
			return true
		}
		offset++
	}
	return false
}

func skipPHPStringLiteral(text string, start int, quote byte) int {
	for offset := start + 1; offset < len(text); offset++ {
		if text[offset] == '\\' {
			offset++
			continue
		}
		if text[offset] == quote {
			return offset + 1
		}
	}
	return len(text)
}
