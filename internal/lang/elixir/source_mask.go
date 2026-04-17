package elixir

type elixirImportMaskState struct {
	inSingleQuote   bool
	inDoubleQuote   bool
	inSingleHeredoc bool
	inDoubleHeredoc bool
	escaped         bool
}

func maskElixirImportSource(content []byte) []byte {
	sanitized := make([]byte, len(content))
	copy(sanitized, content)
	state := elixirImportMaskState{}

	for i := 0; i < len(content); i++ {
		i += maskElixirSourceAt(content, sanitized, i, &state)
	}

	return sanitized
}

func maskElixirSourceAt(content []byte, sanitized []byte, index int, state *elixirImportMaskState) int {
	switch {
	case state.inDoubleHeredoc:
		return maskElixirHeredocByte(content, sanitized, index, state, '"')
	case state.inSingleHeredoc:
		return maskElixirHeredocByte(content, sanitized, index, state, '\'')
	case state.inDoubleQuote:
		return maskElixirQuotedByte(content, sanitized, index, state, '"')
	case state.inSingleQuote:
		return maskElixirQuotedByte(content, sanitized, index, state, '\'')
	default:
		return startElixirMaskedRegion(content, sanitized, index, state)
	}
}

func maskElixirHeredocByte(content []byte, sanitized []byte, index int, state *elixirImportMaskState, quote byte) int {
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

func maskElixirQuotedByte(content []byte, sanitized []byte, index int, state *elixirImportMaskState, quote byte) int {
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

func startElixirMaskedRegion(content []byte, sanitized []byte, index int, state *elixirImportMaskState) int {
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
