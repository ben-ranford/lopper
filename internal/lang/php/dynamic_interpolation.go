package php

import "strings"

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
		if next, dynamic := scanDynamicInterpolationAt(text, offset); next > offset {
			return next, dynamic
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
		if next, dynamic := scanDynamicInterpolationAt(text, offset); next > offset {
			if dynamic {
				return true
			}
			offset = next - 1
		}
	}
	return false
}

// Return the end of the examined expression even when it is not dynamic.
// Nested or unterminated fragments must not rescan the same suffix repeatedly.
func scanDynamicInterpolationAt(text string, offset int) (int, bool) {
	if offset+1 >= len(text) || text[offset] != '{' || text[offset+1] != '$' {
		return offset, false
	}
	end := interpolationExpressionEnd(text, offset)
	return min(end+1, len(text)), hasDynamicInterpolationExpression(text[offset+1 : end])
}

func interpolationExpressionEnd(text string, start int) int {
	depth := 1
	state := phpStateCode
	for offset := start + 1; offset < len(text); {
		if state == phpStateCode {
			switch text[offset] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return offset
				}
			}
		}
		offset = advancePHPCodeState(text, offset, &state)
	}
	return len(text)
}

func hasDynamicInterpolationExpression(expression string) bool {
	state := phpStateCode
	for offset := 0; offset < len(expression); {
		if state == phpStateCode {
			if next, dynamic := scanDynamicInterpolationToken(expression, offset); next > offset {
				if dynamic {
					return true
				}
				offset = next
				continue
			}
		}
		next, dynamic := advancePHPDynamicInterpolation(expression, offset, &state)
		if dynamic {
			return true
		}
		offset = next
	}
	return false
}

// Consume each variable/property chain or identifier once, including failed
// matches. Retrying an anchored pattern at each dollar sign or property in a
// long chain would repeatedly scan the same suffix.
func scanDynamicInterpolationToken(text string, start int) (int, bool) {
	offset := start
	if text[offset] == '$' {
		for {
			for offset < len(text) && text[offset] == '$' {
				offset++
			}
			end := interpolationIdentifierEnd(text, offset)
			if end == offset {
				return offset, false
			}
			offset = end
			if !strings.HasPrefix(text[offset:], "->") {
				break
			}
			offset += 2
		}
		return offset, strings.HasPrefix(strings.TrimLeft(text[offset:], " \t\r\n\f"), "::")
	}
	offset = interpolationIdentifierEnd(text, offset)
	if offset == start {
		return start, false
	}
	switch text[start:offset] {
	case "class_exists", "interface_exists", "trait_exists", "method_exists":
		return offset, strings.HasPrefix(strings.TrimLeft(text[offset:], " \t\r\n\f"), "(")
	default:
		return offset, false
	}
}

func interpolationIdentifierEnd(text string, offset int) int {
	if offset >= len(text) || !isPHPIdentifierByte(text[offset]) || text[offset] == '$' || text[offset] >= '0' && text[offset] <= '9' {
		return offset
	}
	for offset < len(text) && isPHPIdentifierByte(text[offset]) && text[offset] != '$' {
		offset++
	}
	return offset
}
