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
	scan, err := scanDynamicLoaderUsage(depRoot, entrypoints)
	if err != nil {
		return nil, err
	}
	if scan.count == 0 && scan.skippedOversized == 0 {
		return nil, nil
	}

	return &report.RiskCue{
		Code:     riskCodeDynamicLoader,
		Severity: "medium",
		Message:  dynamicLoaderRiskMessage(scan),
	}, nil
}

func detectDynamicLoaderUsage(depRoot string, entrypoints []string) (int, []string, error) {
	scan, err := scanDynamicLoaderUsage(depRoot, entrypoints)
	if err != nil {
		return 0, nil, err
	}
	return scan.count, scan.samples, nil
}

type dynamicLoaderScanResult struct {
	count            int
	samples          []string
	skippedOversized int
}

func scanDynamicLoaderUsage(depRoot string, entrypoints []string) (dynamicLoaderScanResult, error) {
	result := dynamicLoaderScanResult{samples: make([]string, 0, 3)}
	for _, entry := range entrypoints {
		entryScan, err := scanDynamicLoaderUsageInEntrypoint(depRoot, entry)
		if err != nil {
			return dynamicLoaderScanResult{}, err
		}
		result.count += entryScan.count
		result.skippedOversized += entryScan.skippedOversized
		remaining := 3 - len(result.samples)
		if remaining > len(entryScan.samples) {
			remaining = len(entryScan.samples)
		}
		result.samples = append(result.samples, entryScan.samples[:remaining]...)
	}

	return result, nil
}

func dynamicLoaderRiskMessage(scan dynamicLoaderScanResult) string {
	if scan.count == 0 {
		return fmt.Sprintf("dynamic loader usage could not be verified in %d oversized dependency entrypoint(s)", scan.skippedOversized)
	}

	msg := fmt.Sprintf("dynamic require/import usage found in %d dependency entrypoint location(s)", scan.count)
	if len(scan.samples) > 0 {
		msg = fmt.Sprintf("%s (%s)", msg, strings.Join(scan.samples, ", "))
	}
	if scan.skippedOversized > 0 {
		msg = fmt.Sprintf("%s; dynamic loader usage could not be verified in %d oversized dependency entrypoint(s)", msg, scan.skippedOversized)
	}
	return msg
}

func scanDynamicLoaderUsageInEntrypoint(depRoot, entry string) (dynamicLoaderScanResult, error) {
	if !isLikelyCodeAsset(entry) {
		return dynamicLoaderScanResult{}, nil
	}
	content, err := safeio.ReadFileUnderLimit(depRoot, entry, maxScannableJSFile)
	if err != nil {
		if errors.Is(err, safeio.ErrFileTooLarge) {
			return dynamicLoaderScanResult{skippedOversized: 1}, nil
		}
		return dynamicLoaderScanResult{}, err
	}

	result := dynamicLoaderScanResult{samples: make([]string, 0, 3)}
	for idx, line := range strings.Split(string(content), "\n") {
		if hasDynamicCall(line, "require(") || hasDynamicCall(line, "import(") {
			result.count++
			if len(result.samples) < 3 {
				result.samples = append(result.samples, fmt.Sprintf("%s:%d", filepath.Base(entry), idx+1))
			}
		}
	}
	return result, nil
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
