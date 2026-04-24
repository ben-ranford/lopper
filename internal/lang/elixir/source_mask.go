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
