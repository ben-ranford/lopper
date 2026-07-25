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
	cue, _, err := buildDynamicLoaderRiskCueWithWarnings(depRoot, entrypoints)
	return cue, err
}

func buildDynamicLoaderRiskCueWithWarnings(depRoot string, entrypoints []string) (cue *report.RiskCue, warnings []string, err error) {
	root, validatedDepRoot, err := openValidatedRootNoFollow(depRoot)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()
	return buildDynamicLoaderRiskCueWithinRootWithWarnings(root, validatedDepRoot, entrypoints)
}

func buildDynamicLoaderRiskCueWithinRootWithWarnings(root safeio.Root, depRoot string, entrypoints []string) (*report.RiskCue, []string, error) {
	dynamicCount, samples, skippedLargeFiles, err := detectDynamicLoaderUsageWithinRoot(root, depRoot, entrypoints)
	if err != nil {
		return nil, nil, err
	}
	var warnings []string
	if skippedLargeFiles > 0 {
		warnings = append(warnings, fmt.Sprintf("skipped %d JS/TS file(s) above %d bytes during dynamic loader scan", skippedLargeFiles, jsSourceReadMaxBytes))
	}
	if dynamicCount == 0 {
		return nil, warnings, nil
	}

	msg := fmt.Sprintf("dynamic require/import usage found in %d dependency entrypoint location(s)", dynamicCount)
	if len(samples) > 0 {
		msg = fmt.Sprintf("%s (%s)", msg, strings.Join(samples, ", "))
	}

	return &report.RiskCue{
		Code:     riskCodeDynamicLoader,
		Severity: "medium",
		Message:  msg,
	}, warnings, nil
}

func detectDynamicLoaderUsage(depRoot string, entrypoints []string) (count int, samples []string, skippedLargeFiles int, err error) {
	root, validatedDepRoot, err := openValidatedRootNoFollow(depRoot)
	if err != nil {
		return 0, nil, 0, err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()
	return detectDynamicLoaderUsageWithinRoot(root, validatedDepRoot, entrypoints)
}

func detectDynamicLoaderUsageWithinRoot(root safeio.Root, depRoot string, entrypoints []string) (int, []string, int, error) {
	count := 0
	samples := make([]string, 0, 3)
	skippedLargeFiles := 0

	for _, entry := range entrypoints {
		entryCount, entrySamples, skipped, err := scanDynamicLoaderEntrypoint(root, depRoot, entry)
		if err != nil {
			return 0, nil, 0, err
		}
		count += entryCount
		skippedLargeFiles += skipped
		for _, sample := range entrySamples {
			if len(samples) >= 3 {
				break
			}
			samples = append(samples, sample)
		}
	}

	return count, samples, skippedLargeFiles, nil
}

func scanDynamicLoaderEntrypoint(root safeio.Root, depRoot, entry string) (int, []string, int, error) {
	if !isLikelyCodeAsset(entry) {
		return 0, nil, 0, nil
	}
	relEntry, err := relativePathWithinRoot(depRoot, entry)
	if err != nil {
		return 0, nil, 0, err
	}
	content, err := safeio.ReadFileWithinRootLimit(root, relEntry, jsSourceReadMaxBytes)
	if err != nil {
		if errors.Is(err, safeio.ErrFileTooLarge) {
			return 0, nil, 1, nil
		}
		return 0, nil, 0, err
	}
	count, samples := findDynamicLoaderCalls(entry, content)
	return count, samples, 0, nil
}

func findDynamicLoaderCalls(entry string, content []byte) (int, []string) {
	count := 0
	samples := make([]string, 0, 3)
	for idx, line := range strings.Split(string(content), "\n") {
		if !hasDynamicCall(line, "require(") && !hasDynamicCall(line, "import(") {
			continue
		}
		count++
		if len(samples) < 3 {
			samples = append(samples, fmt.Sprintf("%s:%d", filepath.Base(entry), idx+1))
		}
	}
	return count, samples
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
