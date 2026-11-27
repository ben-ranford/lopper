package swift

import (
	"bytes"
	"iter"
	"strings"
)

type swiftStatement struct {
	text   string
	line   int
	offset int
}

// Split only after masking literals and comments so offsets refer to the original source.
func swiftStatements(content []byte) iter.Seq[swiftStatement] {
	return func(yield func(swiftStatement) bool) {
		row := 1
		for line := range strings.SplitSeq(blankSwiftStringsAndComments(content), "\n") {
			offset := 0
			for part := range strings.SplitSeq(line, ";") {
				if !yield(swiftStatement{text: part, line: row, offset: offset}) {
					return
				}
				offset += len(part) + 1
			}
			row++
		}
	}
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
	if !state.escaped && matchesSwiftStringDelimiter(content, index, state.rawHashCount, state.multiline) {
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
	if !state.escaped {
		if end := swiftStringEscapeEnd(content, index, state.rawHashCount); end > index {
			state.escaped = true
			builder.WriteString(strings.Repeat(" ", end-index))
			return end
		}
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

	if content[index] == '/' {
		if end := swiftBareRegexEnd(content, index); end > index {
			blankSwiftLiteral(content[index:end], builder)
			return end
		}
	}

	if content[index] == '#' {
		hashes := 1
		for index+hashes < len(content) && content[index+hashes] == '#' {
			hashes++
		}
		if index+hashes < len(content) && content[index+hashes] == '/' {
			return blankSwiftExtendedRegex(content, index, hashes, builder)
		}
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

// A bare slash may open a regex in expression position, not after an operand.
func swiftBareRegexPosition(content []byte, start int) bool {
	pos := start
	for pos > 0 && swiftHorizontalSpace(content[pos-1]) {
		pos--
	}
	if pos == 0 {
		return true
	}
	switch content[pos-1] {
	case '\n', '\r', '=', '(', '[', '{', ',', ':', ';', '?', '!', '&', '|':
		return true
	}
	end := pos
	for pos > 0 && swiftRegexContextWordByte(content[pos-1]) {
		pos--
	}
	switch string(content[pos:end]) {
	case "return", "throw", "case", "in":
		return true
	default:
		return false
	}
}

func swiftRegexContextWordByte(ch byte) bool {
	return ch >= 0x80 || ch == '_' || ('0' <= ch && ch <= '9') ||
		('A' <= ch && ch <= 'Z') || ('a' <= ch && ch <= 'z')
}

func swiftHorizontalSpace(ch byte) bool {
	return ch == ' ' || ch == '\t'
}

func swiftBareRegexEnd(content []byte, start int) int {
	first := start + 1
	if first >= len(content) || swiftHorizontalSpace(content[first]) || !swiftBareRegexPosition(content, start) {
		return start
	}
	groups, classes := 0, 0
	for pos := first; pos < len(content); pos++ {
		switch content[pos] {
		case '\n', '\r':
			return start
		case '\\':
			if pos+1 == len(content) || content[pos+1] == '\n' || content[pos+1] == '\r' {
				return start
			}
			pos++
		case '[':
			classes++
		case ']':
			if classes > 0 {
				classes--
			}
		case '(':
			if classes == 0 {
				groups++
			}
		case ')':
			if classes == 0 {
				if groups == 0 {
					return start
				}
				groups--
			}
		case '/':
			if pos == first || groups != 0 || classes != 0 || swiftHorizontalSpace(content[pos-1]) {
				return start
			}
			return pos + 1
		}
	}
	return start
}

// Swift's extended regex delimiters use matching runs of pound signs, even across lines.
func blankSwiftExtendedRegex(content []byte, start, hashes int, builder *strings.Builder) int {
	end := start + hashes + 1
	// A newline after optional horizontal whitespace opens a multiline literal.
	limit := len(content)
	body := bytes.TrimLeft(content[end:], " \t")
	if len(body) > 0 && body[0] != '\n' && body[0] != '\r' {
		if newline := bytes.IndexAny(content[end:], "\r\n"); newline >= 0 {
			limit = end + newline
		}
	}
	for end < limit {
		ch := content[end]
		end++
		if ch == '\\' && end < limit {
			end++ // An escaped slash cannot close the pattern.
			continue
		}
		if ch != '/' {
			continue
		}
		hashStart := end
		for end < limit && content[end] == '#' {
			end++
		}
		if end-hashStart == hashes {
			break
		}
	}
	blankSwiftLiteral(content[start:end], builder)
	return end
}

func blankSwiftLiteral(content []byte, builder *strings.Builder) {
	for _, ch := range content {
		if ch == '\n' || ch == '\r' {
			builder.WriteByte(ch)
		} else {
			builder.WriteByte(' ')
		}
	}
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
	if !state.escaped && matchesSwiftStringDelimiter(content, index, state.rawHashCount, state.multiline) {
		delimiterLen := swiftStringDelimiterLength(state.rawHashCount, state.multiline)
		builder.Write(content[index : index+delimiterLen])
		resetSwiftStringScanState(state)
		return index + delimiterLen
	}

	if !state.escaped {
		if end := swiftStringEscapeEnd(content, index, state.rawHashCount); end > index {
			state.escaped = true
			builder.Write(content[index:end])
			return end
		}
	}
	state.escaped = false
	builder.WriteByte(content[index])
	return index + 1
}

// A raw string escape must start with a backslash and the delimiter's hash count.
func swiftStringEscapeEnd(content []byte, index, hashes int) int {
	end := index + 1 + hashes
	if content[index] != '\\' || end >= len(content) {
		return index
	}
	for _, ch := range content[index+1 : end] {
		if ch != '#' {
			return index
		}
	}
	return end
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

func matchesSwiftStringDelimiter(content []byte, index, rawHashCount int, multiline bool) bool {
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
