package elixir

type elixirSourceMaskState struct {
	inSingleQuote   bool
	inDoubleQuote   bool
	inSingleHeredoc bool
	inDoubleHeredoc bool
	escaped         bool
}

func sanitizeElixirSource(content []byte) []byte {
	sanitized := make([]byte, len(content))
	copy(sanitized, content)
	state := elixirSourceMaskState{}

	for i := 0; i < len(content); i++ {
		i += sanitizeElixirSourceAt(content, sanitized, i, &state)
	}

	return sanitized
}

// MaskSource replaces strings, heredocs, sigils, and comments while preserving layout.
func MaskSource(content []byte) []byte {
	masked := sanitizeElixirSource(content)
	maskElixirSigils(content, masked)
	for index := 0; index < len(content); index++ {
		if content[index] != '#' || masked[index] != '#' {
			continue
		}
		for index < len(content) && content[index] != '\n' {
			maskElixirSourceByte(masked, index)
			index++
		}
	}
	return masked
}

func maskElixirSigils(content, masked []byte) {
	for index := 0; index < len(content); index++ {
		end, ok := elixirSigilEnd(content, masked, index)
		if !ok {
			continue
		}
		for position := index; position <= end; position++ {
			maskElixirSourceByte(masked, position)
		}
		index = end
	}
}

func elixirSigilEnd(content, masked []byte, start int) (int, bool) {
	if start+2 >= len(content) || masked[start] != '~' || !isElixirSigilLetter(content[start+1]) {
		return 0, false
	}
	opening := content[start+2]
	closing, paired, ok := elixirSigilDelimiter(opening)
	if !ok || masked[start+2] != opening {
		return 0, false
	}
	return findElixirSigilEnd(content, start+2, opening, closing, paired), true
}

func findElixirSigilEnd(content []byte, delimiterIndex int, opening, closing byte, paired bool) int {
	depth := 1
	escaped := false
	for index := delimiterIndex + 1; index < len(content); index++ {
		switch {
		case escaped:
			escaped = false
		case content[index] == '\\':
			escaped = true
		case paired && content[index] == opening:
			depth++
		case content[index] == closing:
			depth--
			if depth == 0 {
				return index
			}
		}
	}
	return len(content) - 1
}

func elixirSigilDelimiter(opening byte) (byte, bool, bool) {
	switch opening {
	case '(':
		return ')', true, true
	case '[':
		return ']', true, true
	case '{':
		return '}', true, true
	case '<':
		return '>', true, true
	case '/', '|':
		return opening, false, true
	default:
		return 0, false, false
	}
}

func isElixirSigilLetter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func sanitizeElixirSourceAt(content []byte, sanitized []byte, index int, state *elixirSourceMaskState) int {
	switch {
	case state.inDoubleHeredoc:
		return sanitizeElixirHeredocByte(content, sanitized, index, state, '"')
	case state.inSingleHeredoc:
		return sanitizeElixirHeredocByte(content, sanitized, index, state, '\'')
	case state.inDoubleQuote:
		return sanitizeElixirQuotedByte(content, sanitized, index, state, '"')
	case state.inSingleQuote:
		return sanitizeElixirQuotedByte(content, sanitized, index, state, '\'')
	default:
		return startElixirSanitizedRegion(content, sanitized, index, state)
	}
}

func sanitizeElixirHeredocByte(content []byte, sanitized []byte, index int, state *elixirSourceMaskState, quote byte) int {
	maskElixirSourceByte(sanitized, index)
	if !isElixirTripleQuote(content, index, quote) {
		return 0
	}
	maskElixirSourceByte(sanitized, index+1)
	maskElixirSourceByte(sanitized, index+2)
	if quote == '"' {
		state.inDoubleHeredoc = false
	} else {
		state.inSingleHeredoc = false
	}
	return 2
}

func sanitizeElixirQuotedByte(content []byte, sanitized []byte, index int, state *elixirSourceMaskState, quote byte) int {
	maskElixirSourceByte(sanitized, index)
	if state.escaped {
		state.escaped = false
		return 0
	}

	switch content[index] {
	case '\\':
		state.escaped = true
	case quote:
		if quote == '"' {
			state.inDoubleQuote = false
		} else {
			state.inSingleQuote = false
		}
	}
	return 0
}

func startElixirSanitizedRegion(content []byte, sanitized []byte, index int, state *elixirSourceMaskState) int {
	if isElixirTripleQuote(content, index, '"') {
		maskElixirSourceByte(sanitized, index)
		maskElixirSourceByte(sanitized, index+1)
		maskElixirSourceByte(sanitized, index+2)
		state.inDoubleHeredoc = true
		return 2
	}
	if isElixirTripleQuote(content, index, '\'') {
		maskElixirSourceByte(sanitized, index)
		maskElixirSourceByte(sanitized, index+1)
		maskElixirSourceByte(sanitized, index+2)
		state.inSingleHeredoc = true
		return 2
	}

	switch content[index] {
	case '"':
		maskElixirSourceByte(sanitized, index)
		state.inDoubleQuote = true
	case '\'':
		maskElixirSourceByte(sanitized, index)
		state.inSingleQuote = true
	}
	return 0
}

func isElixirTripleQuote(content []byte, index int, quote byte) bool {
	return index+2 < len(content) && content[index] == quote && content[index+1] == quote && content[index+2] == quote
}

func maskElixirSourceByte(content []byte, index int) {
	if index < 0 || index >= len(content) {
		return
	}
	if content[index] != '\n' {
		content[index] = ' '
	}
}
