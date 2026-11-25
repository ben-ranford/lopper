package shared

type kotlinImportMode byte

const (
	kotlinQuotedString kotlinImportMode = iota
	kotlinRawString
	kotlinTemplateExpression
)

func skipKotlinImportLiteral(content []byte, start int) int {
	mode, index := kotlinImportStringStart(content, start)
	var localModes [8]kotlinImportMode
	modes := append(localModes[:0], mode)
	// Each nested expression or literal consumes input before adding a mode.
	for index < len(content) && len(modes) != 0 {
		if modes[len(modes)-1] == kotlinTemplateExpression {
			index, modes = scanKotlinImportExpression(content, index, modes)
		} else {
			index, modes = scanKotlinImportString(content, index, modes)
		}
	}
	return index
}

func kotlinImportStringStart(content []byte, start int) (kotlinImportMode, int) {
	if hasImportTextBlockDelimiter(content, start) {
		return kotlinRawString, start + 3
	}
	return kotlinQuotedString, start + 1
}

func scanKotlinImportString(content []byte, index int, modes []kotlinImportMode) (int, []kotlinImportMode) {
	mode := modes[len(modes)-1]
	switch {
	case mode == kotlinQuotedString && content[index] == '\\':
		return min(index+2, len(content)), modes
	case hasBytePrefix(content, index, '$', '{'):
		return index + 2, append(modes, kotlinTemplateExpression)
	case hasBytePrefix(content, index, '$', '`'):
		end := afterImportDelimiter(content, index+2, "`\r\n")
		if end > index+3 && content[end-1] == '`' {
			return end, modes
		}
		return index + 1, modes
	case mode == kotlinQuotedString && content[index] == '"':
		return index + 1, modes[:len(modes)-1]
	case mode == kotlinRawString && hasImportTextBlockDelimiter(content, index):
		end := index + 3
		// Extra quotes adjacent to the closing delimiter belong to the raw string.
		for end < len(content) && content[end] == '"' {
			end++
		}
		return end, modes[:len(modes)-1]
	default:
		return index + 1, modes
	}
}

func scanKotlinImportExpression(content []byte, index int, modes []kotlinImportMode) (int, []kotlinImportMode) {
	switch {
	case content[index] == '{':
		return index + 1, append(modes, kotlinTemplateExpression)
	case content[index] == '}':
		return index + 1, modes[:len(modes)-1]
	case content[index] == '"':
		mode, end := kotlinImportStringStart(content, index)
		return end, append(modes, mode)
	case content[index] == '\'':
		return skipPlainImportLiteral(content, index), modes
	case content[index] == '`':
		return afterImportDelimiter(content, index+1, "`\r\n"), modes
	case hasBytePrefix(content, index, '/', '/'):
		return afterImportDelimiter(content, index+2, "\r\n"), modes
	case hasBytePrefix(content, index, '/', '*'):
		return maskImportBlockComment(content, index, true), modes
	default:
		return index + 1, modes
	}
}
