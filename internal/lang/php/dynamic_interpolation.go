package php

import "strings"

const phpInterpolationWhitespace = " \t\r\n\f"

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
		if next, dynamic, _ := dynamicHeredocInterpolation(text, offset); next > offset {
			return next, dynamic
		}
	}
	if *state == phpStateDoubleQuote || *state == phpStateBacktick {
		if next, dynamic, _ := scanDynamicInterpolationAt(text, offset); next > offset {
			return next, dynamic
		}
	}
	return advancePHPCodeState(text, offset, state), false
}

func dynamicHeredocInterpolation(text string, offset int) (int, bool, bool) {
	lineEnd := nextPHPLineEnd(text, offset)
	marker := strings.TrimLeft(text[offset+len("<<<"):lineEnd], " \t")
	label, ok := parseHeredocNowdocLabelAfterMarker(marker)
	if !ok {
		return offset, false, false
	}
	bodyStart := nextPHPLineStart(text, lineEnd)
	bodyEnd, _, found := findHeredocNowdocTerminatorRange(text, bodyStart, label)
	if !found {
		bodyEnd = len(text)
	}
	if strings.HasPrefix(marker, "'") {
		return bodyEnd, false, false
	}
	next, dynamic, closed := scanDynamicHeredocBody(text[bodyStart:bodyEnd])
	return bodyStart + next, dynamic, closed
}

// Collect dynamic cues and close tags together so nested heredocs are not
// recursively scanned twice at each nesting level.
func scanDynamicHeredocBody(text string) (int, bool, bool) {
	dynamic := false
	for offset := 0; offset < len(text); offset++ {
		if text[offset] == '\\' && offset+1 < len(text) {
			offset++
			continue
		}
		if next, found, closed := scanDynamicInterpolationAt(text, offset); next > offset {
			dynamic = dynamic || found
			if closed {
				return next, dynamic, true
			}
			offset = next - 1
		}
	}
	return len(text), dynamic, false
}

// Return the end of the examined expression even when it is not dynamic.
// Nested or unterminated fragments must not rescan the same suffix repeatedly.
// The final result reports a PHP close tag in expression code or a line comment.
func scanDynamicInterpolationAt(text string, offset int) (int, bool, bool) {
	start := phpInterpolationExpressionStart(text, offset)
	if start == offset {
		return offset, false, false
	}
	return scanPHPInterpolationExpression(text, start)
}

func phpInterpolationExpressionStart(text string, offset int) int {
	if strings.HasPrefix(text[offset:], "{$") {
		return offset + 1
	}
	if strings.HasPrefix(text[offset:], "${") {
		return offset + 2
	}
	return offset
}

type interpolationQuoteFrame struct {
	state       phpCodeState
	resumeDepth int
}

// Scan each interpolation expression once. Nested interpolations temporarily
// leave their containing quote and resume it after the matching brace, so
// quotes inside nested comments cannot corrupt the surrounding lexical state.
func scanPHPInterpolationExpression(text string, offset int) (int, bool, bool) {
	depth := 1
	state := phpStateCode
	var quotes []interpolationQuoteFrame
	dynamic := false
	for offset < len(text) {
		next, complete, closed := advancePHPInterpolationExpression(text, offset, &depth, &state, &quotes, &dynamic)
		if complete {
			return next, dynamic, closed
		}
		offset = next
	}
	return len(text), dynamic, false
}

func advancePHPInterpolationExpression(text string, offset int, depth *int, state *phpCodeState, quotes *[]interpolationQuoteFrame, dynamic *bool) (int, bool, bool) {
	if isPHPRegionCloseTagAt(text, offset, *state) {
		return offset, true, true
	}
	if *state != phpStateCode {
		if next, entered := startNestedPHPInterpolation(text, offset, depth, state, quotes); entered {
			return next, false, false
		}
		return advancePHPCodeState(text, offset, state), false, false
	}
	if next, found, closed := scanPHPInterpolationHeredoc(text, offset); next > offset {
		*dynamic = *dynamic || found
		return next, closed, closed
	}
	if next, found := scanDynamicInterpolationToken(text, offset); next > offset {
		*dynamic = *dynamic || found
		return next, false, false
	}
	switch text[offset] {
	case '{':
		(*depth)++
		return offset + 1, false, false
	case '}':
		(*depth)--
		next := offset + 1
		if *depth == 0 {
			return next, true, false
		}
		if len(*quotes) > 0 && *depth == (*quotes)[len(*quotes)-1].resumeDepth {
			*state = (*quotes)[len(*quotes)-1].state
			*quotes = (*quotes)[:len(*quotes)-1]
		}
		return next, false, false
	default:
		return advancePHPCodeState(text, offset, state), false, false
	}
}

func scanPHPInterpolationHeredoc(text string, offset int) (int, bool, bool) {
	if !strings.HasPrefix(text[offset:], "<<<") {
		return offset, false, false
	}
	return dynamicHeredocInterpolation(text, offset)
}

func startNestedPHPInterpolation(text string, offset int, depth *int, state *phpCodeState, quotes *[]interpolationQuoteFrame) (int, bool) {
	if *state != phpStateDoubleQuote && *state != phpStateBacktick {
		return offset, false
	}
	next := phpInterpolationExpressionStart(text, offset)
	if next == offset {
		return offset, false
	}
	*quotes = append(*quotes, interpolationQuoteFrame{state: *state, resumeDepth: *depth})
	(*depth)++
	*state = phpStateCode
	return next, true
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
		return offset, strings.HasPrefix(strings.TrimLeft(text[offset:], phpInterpolationWhitespace), "::")
	}
	offset = interpolationIdentifierEnd(text, offset)
	if offset == start {
		return start, false
	}
	switch text[start:offset] {
	case "new":
		return offset, strings.HasPrefix(strings.TrimLeft(text[offset:], phpInterpolationWhitespace), "$")
	case "class_exists", "interface_exists", "trait_exists", "method_exists":
		return offset, strings.HasPrefix(strings.TrimLeft(text[offset:], phpInterpolationWhitespace), "(")
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
