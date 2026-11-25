package shared

type kotlinImportMode byte

const (
	kotlinQuotedString kotlinImportMode = iota
	kotlinRawString
	kotlinTemplateExpression
)

type kotlinImportFrame struct {
	mode                 kotlinImportMode
	interpolationDollars int
}

func skipKotlinImportLiteral(content []byte, start int) int {
	frame, index := kotlinImportStringStart(content, start)
	var localFrames [8]kotlinImportFrame
	frames := append(localFrames[:0], frame)
	// Each nested expression or literal consumes input before adding a mode.
	for index < len(content) && len(frames) != 0 {
		if frames[len(frames)-1].mode == kotlinTemplateExpression {
			index, frames = scanKotlinImportExpression(content, index, frames)
		} else {
			index, frames = scanKotlinImportString(content, index, frames)
		}
	}
	return index
}

func kotlinImportStringStart(content []byte, start int) (kotlinImportFrame, int) {
	prefix := start
	for prefix > 0 && content[prefix-1] == '$' {
		prefix--
	}
	frame := kotlinImportFrame{mode: kotlinQuotedString, interpolationDollars: max(1, start-prefix)}
	if hasImportTextBlockDelimiter(content, start) {
		frame.mode = kotlinRawString
		return frame, start + 3
	}
	return frame, start + 1
}

func scanKotlinImportString(content []byte, index int, frames []kotlinImportFrame) (int, []kotlinImportFrame) {
	frame := frames[len(frames)-1]
	switch {
	case frame.mode == kotlinQuotedString && content[index] == '\\':
		return min(index+2, len(content)), frames
	case content[index] == '$':
		return scanKotlinImportInterpolation(content, index, frames)
	case frame.mode == kotlinQuotedString && content[index] == '"':
		return index + 1, frames[:len(frames)-1]
	case frame.mode == kotlinRawString && hasImportTextBlockDelimiter(content, index):
		end := index + 3
		// Extra quotes adjacent to the closing delimiter belong to the raw string.
		for end < len(content) && content[end] == '"' {
			end++
		}
		return end, frames[:len(frames)-1]
	default:
		return index + 1, frames
	}
}

func scanKotlinImportInterpolation(content []byte, start int, frames []kotlinImportFrame) (int, []kotlinImportFrame) {
	next := start + 1
	for next < len(content) && content[next] == '$' {
		next++
	}
	if next == len(content) || next-start < frames[len(frames)-1].interpolationDollars {
		return next, frames
	}
	switch content[next] {
	case '{':
		return next + 1, append(frames, kotlinImportFrame{mode: kotlinTemplateExpression})
	case '`':
		end := afterImportDelimiter(content, next+1, "`\r\n")
		if end > next+2 && content[end-1] == '`' {
			return end, frames
		}
	}
	return next, frames
}

func scanKotlinImportExpression(content []byte, index int, frames []kotlinImportFrame) (int, []kotlinImportFrame) {
	switch {
	case content[index] == '{':
		return index + 1, append(frames, kotlinImportFrame{mode: kotlinTemplateExpression})
	case content[index] == '}':
		return index + 1, frames[:len(frames)-1]
	case content[index] == '"':
		frame, end := kotlinImportStringStart(content, index)
		return end, append(frames, frame)
	case content[index] == '\'':
		return skipPlainImportLiteral(content, index), frames
	case content[index] == '`':
		return afterImportDelimiter(content, index+1, "`\r\n"), frames
	case hasBytePrefix(content, index, '/', '/'):
		return afterImportDelimiter(content, index+2, "\r\n"), frames
	case hasBytePrefix(content, index, '/', '*'):
		return maskImportBlockComment(content, index, true), frames
	default:
		return index + 1, frames
	}
}
