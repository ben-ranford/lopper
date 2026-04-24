package swift

import "strings"

func swiftSymbolScanLines(content []byte) []string {
	return strings.Split(blankSwiftStringsAndComments(content), "\n")
}

func blankSwiftStringsAndComments(content []byte) string {
	builder := strings.Builder{}
	builder.Grow(len(content))

	state := swiftStringScanState{}
	for index := 0; index < len(content); {
		if state.inString {
			index = consumeSwiftStringContent(content, index, &builder, &state)
			continue
		}
		if state.blockDepth > 0 {
			index = consumeSwiftBlockCommentContent(content, index, &builder, &state)
			continue
		}
		index = consumeSwiftCodeContent(content, index, &builder, &state)
	}
	return builder.String()
}

func blankSwiftCommentsPreservingStrings(content string) string {
	builder := strings.Builder{}
	builder.Grow(len(content))

	bytes := []byte(content)
	state := swiftStringScanState{}
	for index := 0; index < len(bytes); {
		if state.inString {
			index = consumeSwiftStringContentPreservingContent(bytes, index, &builder, &state)
			continue
		}
		if state.blockDepth > 0 {
			index = consumeSwiftBlockCommentContent(bytes, index, &builder, &state)
			continue
		}
		if startsSwiftLineComment(bytes, index) {
			index = blankSwiftLineComment(bytes, index, &builder)
			continue
		}
		if startsSwiftBlockComment(bytes, index) {
			state.blockDepth = 1
			builder.WriteString("  ")
			index += 2
			continue
		}
		hashCount, nextIndex, isMultiline, ok := detectSwiftStringStart(bytes, index)
		if ok {
			builder.WriteString(content[index:nextIndex])
			state.inString = true
			state.multiline = isMultiline
			state.rawHashCount = hashCount
			state.escaped = false
			index = nextIndex
			continue
		}

		builder.WriteByte(bytes[index])
		index++
	}
	return builder.String()
}

func consumeSwiftStringContent(content []byte, index int, builder *strings.Builder, state *swiftStringScanState) int {
	if matchesSwiftStringDelimiter(content, index, state.rawHashCount, state.multiline) {
		delimiterLen := swiftStringDelimiterLength(state.rawHashCount, state.multiline)
		builder.WriteString(strings.Repeat(" ", delimiterLen))
		resetSwiftStringScanState(state)
		return index + delimiterLen
	}

	ch := content[index]
	if ch == '\n' {
		builder.WriteByte('\n')
		state.escaped = false
		return index + 1
	}
	if ch == '\\' && !state.multiline && state.rawHashCount == 0 && !state.escaped {
		state.escaped = true
		builder.WriteByte(' ')
		return index + 1
	}

	state.escaped = false
	builder.WriteByte(' ')
	return index + 1
}

func consumeSwiftCodeContent(content []byte, index int, builder *strings.Builder, state *swiftStringScanState) int {
	if startsSwiftLineComment(content, index) {
		return blankSwiftLineComment(content, index, builder)
	}
	if startsSwiftBlockComment(content, index) {
		state.blockDepth = 1
		builder.WriteString("  ")
		return index + 2
	}

	hashCount, nextIndex, isMultiline, ok := detectSwiftStringStart(content, index)
	if ok {
		builder.WriteString(strings.Repeat(" ", nextIndex-index))
		state.inString = true
		state.multiline = isMultiline
		state.rawHashCount = hashCount
		state.escaped = false
		return nextIndex
	}

	builder.WriteByte(content[index])
	return index + 1
}

func resetSwiftStringScanState(state *swiftStringScanState) {
	state.inString = false
	state.multiline = false
	state.rawHashCount = 0
	state.escaped = false
}

func consumeSwiftBlockCommentContent(content []byte, index int, builder *strings.Builder, state *swiftStringScanState) int {
	if startsSwiftBlockComment(content, index) {
		state.blockDepth++
		builder.WriteString("  ")
		return index + 2
	}
	if endsSwiftBlockComment(content, index) {
		state.blockDepth--
		builder.WriteString("  ")
		return index + 2
	}
	if content[index] == '\n' {
		builder.WriteByte('\n')
		return index + 1
	}
	builder.WriteByte(' ')
	return index + 1
}

func consumeSwiftStringContentPreservingContent(content []byte, index int, builder *strings.Builder, state *swiftStringScanState) int {
	if matchesSwiftStringDelimiter(content, index, state.rawHashCount, state.multiline) {
		delimiterLen := swiftStringDelimiterLength(state.rawHashCount, state.multiline)
		builder.Write(content[index : index+delimiterLen])
		resetSwiftStringScanState(state)
		return index + delimiterLen
	}

	ch := content[index]
	builder.WriteByte(ch)
	if ch == '\n' {
		state.escaped = false
		return index + 1
	}
	if ch == '\\' && !state.multiline && state.rawHashCount == 0 && !state.escaped {
		state.escaped = true
		return index + 1
	}

	state.escaped = false
	return index + 1
}

func detectSwiftStringStart(content []byte, index int) (int, int, bool, bool) {
	cursor := index
	for cursor < len(content) && content[cursor] == '#' {
		cursor++
	}
	if cursor >= len(content) || content[cursor] != '"' {
		return 0, index, false, false
	}
	hashCount := cursor - index
	if cursor+2 < len(content) && content[cursor+1] == '"' && content[cursor+2] == '"' {
		return hashCount, cursor + 3, true, true
	}
	return hashCount, cursor + 1, false, true
}

func matchesSwiftStringDelimiter(content []byte, index int, rawHashCount int, multiline bool) bool {
	delimiterLen := swiftStringDelimiterLength(rawHashCount, multiline)
	if index+delimiterLen > len(content) {
		return false
	}
	quoteCount := 1
	if multiline {
		quoteCount = 3
	}
	for offset := 0; offset < quoteCount; offset++ {
		if content[index+offset] != '"' {
			return false
		}
	}
	for offset := 0; offset < rawHashCount; offset++ {
		if content[index+quoteCount+offset] != '#' {
			return false
		}
	}
	return true
}

func swiftStringDelimiterLength(rawHashCount int, multiline bool) int {
	quoteCount := 1
	if multiline {
		quoteCount = 3
	}
	return quoteCount + rawHashCount
}

func startsSwiftLineComment(content []byte, index int) bool {
	return index+1 < len(content) && content[index] == '/' && content[index+1] == '/'
}

func startsSwiftBlockComment(content []byte, index int) bool {
	return index+1 < len(content) && content[index] == '/' && content[index+1] == '*'
}

func endsSwiftBlockComment(content []byte, index int) bool {
	return index+1 < len(content) && content[index] == '*' && content[index+1] == '/'
}

func blankSwiftLineComment(content []byte, index int, builder *strings.Builder) int {
	for index < len(content) && content[index] != '\n' {
		builder.WriteByte(' ')
		index++
	}
	return index
}
