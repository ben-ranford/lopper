package php

import (
	"regexp"
	"strings"
)

var dynamicInterpolationPattern = regexp.MustCompile(`^\{\$[A-Za-z_][A-Za-z0-9_]*\s*::`)

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
		if dynamicInterpolationPattern.MatchString(text[offset:]) {
			return offset, true
		}
		if strings.HasPrefix(text[offset:], `\{`) {
			// PHP does not treat a backslash before an opening brace as an escape.
			return offset + 1, false
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
		if text[offset] == '\\' && offset+1 < len(text) && text[offset+1] != '{' {
			offset++
			continue
		}
		if dynamicInterpolationPattern.MatchString(text[offset:]) {
			return true
		}
	}
	return false
}
