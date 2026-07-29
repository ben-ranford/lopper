package js

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func buildDynamicLoaderRiskCue(depRoot string, entrypoints []string) (*report.RiskCue, error) {
	dynamicCount, samples, err := detectDynamicLoaderUsage(depRoot, entrypoints)
	if err != nil {
		return nil, err
	}
	if dynamicCount == 0 {
		return nil, nil
	}

	msg := fmt.Sprintf("dynamic require/import usage found in %d dependency entrypoint location(s)", dynamicCount)
	if len(samples) > 0 {
		msg = fmt.Sprintf("%s (%s)", msg, strings.Join(samples, ", "))
	}

	return &report.RiskCue{
		Code:     riskCodeDynamicLoader,
		Severity: "medium",
		Message:  msg,
	}, nil
}

func detectDynamicLoaderUsage(depRoot string, entrypoints []string) (int, []string, error) {
	count := 0
	samples := make([]string, 0, 3)

	for _, entry := range entrypoints {
		entryCount, entrySamples, err := detectDynamicLoaderUsageInEntrypoint(depRoot, entry)
		if err != nil {
			return 0, nil, err
		}
		count += entryCount
		remaining := 3 - len(samples)
		if remaining > len(entrySamples) {
			remaining = len(entrySamples)
		}
		samples = append(samples, entrySamples[:remaining]...)
	}

	return count, samples, nil
}

func detectDynamicLoaderUsageInEntrypoint(depRoot, entry string) (int, []string, error) {
	if !isLikelyCodeAsset(entry) {
		return 0, nil, nil
	}
	content, err := safeio.ReadFileUnderLimit(depRoot, entry, maxScannableJSFile)
	if err != nil {
		if errors.Is(err, safeio.ErrFileTooLarge) {
			return 0, nil, nil
		}
		return 0, nil, err
	}

	count := 0
	samples := make([]string, 0, 3)
	for idx, line := range strings.Split(string(content), "\n") {
		if hasDynamicCall(line, "require(") || hasDynamicCall(line, "import(") {
			count++
			if len(samples) < 3 {
				samples = append(samples, fmt.Sprintf("%s:%d", filepath.Base(entry), idx+1))
			}
		}
	}
	return count, samples, nil
}

func hasDynamicCall(line, token string) bool {
	search := line
	for {
		pos := strings.Index(search, token)
		if pos < 0 {
			return false
		}
		if isCommented(search[:pos]) {
			return false
		}
		if pos > 0 && isIdentifierByte(search[pos-1]) {
			search = search[pos+len(token):]
			continue
		}
		next := firstNonSpaceByte(search[pos+len(token):])
		if next != '\'' && next != '"' && next != '`' {
			return true
		}
		search = search[pos+len(token):]
	}
}

func isCommented(prefix string) bool {
	var state commentScanState
	for i := 0; i < len(prefix); i++ {
		if state.step(prefix, i) {
			return true
		}
	}

	return false
}

type commentScanState struct {
	delimiter byte
	escaped   bool
}

func (s *commentScanState) step(value string, index int) bool {
	ch := value[index]
	if s.escaped {
		s.escaped = false
		return false
	}
	if s.delimiter != 0 {
		s.stepQuoted(ch)
		return false
	}
	if isStringDelimiter(ch) {
		s.delimiter = ch
		return false
	}
	return ch == '/' && index+1 < len(value) && value[index+1] == '/'
}

func (s *commentScanState) stepQuoted(ch byte) {
	if ch == '\\' {
		s.escaped = true
		return
	}
	if ch == s.delimiter {
		s.delimiter = 0
	}
}

func isStringDelimiter(ch byte) bool {
	return ch == '\'' || ch == '"' || ch == '`'
}

func isIdentifierByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '$'
}

func firstNonSpaceByte(value string) byte {
	for i := 0; i < len(value); i++ {
		if value[i] != ' ' && value[i] != '\t' && value[i] != '\r' {
			return value[i]
		}
	}
	return 0
}
