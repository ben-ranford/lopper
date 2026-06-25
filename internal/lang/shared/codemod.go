package shared

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

const CodemodModeSuggestOnly = "suggest-only"

type CodemodSuggestionSpec struct {
	Language          string
	Dependency        string
	File              string
	TargetFile        string
	Line              int
	ImportName        string
	FromModule        string
	ToModule          string
	Original          string
	Replacement       string
	Patch             string
	SafetyReasonCodes []string
	DeleteLine        bool
}

type CodemodSkipSpec struct {
	Language   string
	Dependency string
	File       string
	TargetFile string
	Line       int
	ImportName string
	Module     string
	ReasonCode string
	Message    string
}

func NewCodemodSuggestion(spec CodemodSuggestionSpec) report.CodemodSuggestion {
	targetFile := spec.TargetFile
	if strings.TrimSpace(targetFile) == "" {
		targetFile = spec.File
	}
	return report.CodemodSuggestion{
		Language:          spec.Language,
		Dependency:        spec.Dependency,
		File:              spec.File,
		TargetFile:        targetFile,
		Line:              spec.Line,
		ImportName:        spec.ImportName,
		FromModule:        spec.FromModule,
		ToModule:          spec.ToModule,
		Original:          spec.Original,
		Replacement:       spec.Replacement,
		Patch:             spec.Patch,
		SafetyReasonCodes: cleanCodemodReasonCodes(spec.SafetyReasonCodes),
		DeleteLine:        spec.DeleteLine,
	}
}

func NewCodemodSkip(spec CodemodSkipSpec) report.CodemodSkip {
	targetFile := spec.TargetFile
	if strings.TrimSpace(targetFile) == "" {
		targetFile = spec.File
	}
	return report.CodemodSkip{
		Language:   spec.Language,
		Dependency: spec.Dependency,
		File:       spec.File,
		TargetFile: targetFile,
		Line:       spec.Line,
		ImportName: spec.ImportName,
		Module:     spec.Module,
		ReasonCode: spec.ReasonCode,
		Message:    spec.Message,
	}
}

func BuildSingleLinePatch(file string, line int, oldLine, newLine string) string {
	return strings.Join([]string{fmt.Sprintf("--- a/%s", file), fmt.Sprintf("+++ b/%s", file), fmt.Sprintf("@@ -%d +%d @@", line, line), "-" + oldLine, "+" + newLine}, "\n")
}

func BuildDeleteLinePatch(file string, line int, oldLine string) string {
	return strings.Join([]string{fmt.Sprintf("--- a/%s", file), fmt.Sprintf("+++ b/%s", file), fmt.Sprintf("@@ -%d,1 +%d,0 @@", line, line-1), "-" + oldLine}, "\n")
}

func LoadCodemodSourceLines(repoPath, filePath string, lineCache map[string][]string) ([]string, string, bool) {
	if lines, ok := lineCache[filePath]; ok {
		return lines, "", true
	}
	content, err := safeio.ReadFileUnder(repoPath, filepath.Join(repoPath, filePath))
	if err != nil {
		return nil, fmt.Sprintf("codemod preview skipped for %s: %v", filePath, err), false
	}
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	lineCache[filePath] = lines
	return lines, "", true
}

func cleanCodemodReasonCodes(values []string) []string {
	return DedupeWarnings(values)
}
