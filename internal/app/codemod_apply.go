package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/workspace"
)

const (
	codemodModeApply          = "apply"
	codemodApplyStatusApplied = "applied"
	codemodApplyStatusSkipped = "skipped"
	codemodApplyStatusFailed  = "failed"
	codemodRollbackDir        = ".artifacts/lopper-codemod-backups"
)

type preparedCodemodFile struct {
	file       string
	absPath    string
	original   string
	updated    string
	patchCount int
	mode       os.FileMode
}

type codemodRollbackArtifact struct {
	GeneratedAt time.Time               `json:"generatedAt"`
	Dependency  string                  `json:"dependency"`
	Files       []codemodRollbackRecord `json:"files"`
}

type codemodRollbackRecord struct {
	File    string `json:"file"`
	Mode    uint32 `json:"mode"`
	Content string `json:"content"`
}

type codemodApplyPhaseContext struct {
	repoPath   string
	dependency string
	codemod    *report.CodemodReport
}

type codemodApplyPreparationPhase struct {
	skipResults   []report.CodemodApplyResult
	preparedFiles []preparedCodemodFile
	failedResults []report.CodemodApplyResult
}

type codemodApplyExecutionPhase struct {
	backupPath     string
	appliedResults []report.CodemodApplyResult
	failedResults  []report.CodemodApplyResult
}

func applyCodemodIfNeeded(ctx context.Context, reportData report.Report, repoPath string, req AnalyseRequest, now time.Time) (report.Report, error) {
	if !req.ApplyCodemod {
		return reportData, nil
	}

	phaseContext, shouldApply, err := beginCodemodApplyPhase(&reportData, repoPath, req.Dependency)
	if err != nil {
		return reportData, err
	}
	if !shouldApply {
		return reportData, nil
	}

	preparation := prepareCodemodApplyPhase(phaseContext)
	execution, err := executeCodemodApplyPhase(phaseContext, preparation.preparedFiles, preparation.failedResults, now)
	if err != nil {
		return reportData, err
	}

	results := finalizeCodemodApplyPhase(phaseContext.codemod, preparation.skipResults, execution.appliedResults, execution.failedResults, execution.backupPath)
	if phaseContext.codemod.Apply.FailedFiles > 0 {
		return reportData, codemodApplyError(results)
	}
	return reportData, nil
}

func beginCodemodApplyPhase(reportData *report.Report, repoPath, dependency string) (codemodApplyPhaseContext, bool, error) {
	normalizedRepoPath, err := normalizeRepoPathForCodemod(repoPath)
	if err != nil {
		return codemodApplyPhaseContext{}, false, err
	}

	codemod := findCodemodReport(reportData, dependency)
	if codemod == nil {
		return codemodApplyPhaseContext{}, false, nil
	}
	codemod.Mode = codemodModeApply

	return codemodApplyPhaseContext{
		repoPath:   normalizedRepoPath,
		dependency: dependency,
		codemod:    codemod,
	}, true, nil
}

func prepareCodemodApplyPhase(phaseContext codemodApplyPhaseContext) codemodApplyPreparationPhase {
	preparedFiles, failedResults := prepareCodemodFiles(phaseContext.repoPath, phaseContext.codemod.Suggestions)
	return codemodApplyPreparationPhase{
		skipResults:   buildCodemodSkipResults(phaseContext.codemod.Skips),
		preparedFiles: preparedFiles,
		failedResults: failedResults,
	}
}

func executeCodemodApplyPhase(phaseContext codemodApplyPhaseContext, preparedFiles []preparedCodemodFile, failedResults []report.CodemodApplyResult, now time.Time) (codemodApplyExecutionPhase, error) {
	execution := codemodApplyExecutionPhase{
		failedResults: failedResults,
	}
	if len(preparedFiles) == 0 {
		return execution, nil
	}

	backupPath, err := writeCodemodRollbackArtifact(phaseContext.repoPath, phaseContext.dependency, preparedFiles, now)
	if err != nil {
		return codemodApplyExecutionPhase{}, fmt.Errorf("write codemod rollback artifact: %w", err)
	}

	execution.backupPath = backupPath
	execution.appliedResults, execution.failedResults = applyPreparedCodemodFiles(phaseContext.repoPath, preparedFiles, failedResults)
	return execution, nil
}

func finalizeCodemodApplyPhase(codemod *report.CodemodReport, skipResults, appliedResults, failedResults []report.CodemodApplyResult, backupPath string) []report.CodemodApplyResult {
	results := make([]report.CodemodApplyResult, 0, len(skipResults)+len(appliedResults)+len(failedResults))
	results = append(results, skipResults...)
	results = append(results, appliedResults...)
	results = append(results, failedResults...)
	sortCodemodApplyResults(results)

	codemod.Apply = &report.CodemodApplyReport{
		AppliedFiles:   countUniqueResultFiles(results, codemodApplyStatusApplied),
		AppliedPatches: countResultPatches(results, codemodApplyStatusApplied),
		SkippedFiles:   countUniqueResultFiles(results, codemodApplyStatusSkipped),
		SkippedPatches: countResultPatches(results, codemodApplyStatusSkipped),
		FailedFiles:    countUniqueResultFiles(results, codemodApplyStatusFailed),
		FailedPatches:  countResultPatches(results, codemodApplyStatusFailed),
		BackupPath:     backupPath,
		Results:        results,
	}
	return results
}

func validateCodemodApplyPreconditions(ctx context.Context, repoPath string, req AnalyseRequest) error {
	if !req.ApplyCodemod {
		return nil
	}
	normalizedRepoPath, err := normalizeRepoPathForCodemod(repoPath)
	if err != nil {
		return err
	}
	return ensureCleanWorktreeForCodemod(ctx, normalizedRepoPath, req.AllowDirty)
}

func normalizeRepoPathForCodemod(repoPath string) (string, error) {
	if strings.TrimSpace(repoPath) == "" {
		return "", fmt.Errorf("repo path is required")
	}
	return workspace.NormalizeRepoPath(repoPath)
}

func ensureCleanWorktreeForCodemod(ctx context.Context, repoPath string, allowDirty bool) error {
	if allowDirty {
		return nil
	}
	changed, hasGitContext, err := gitChangedFiles(ctx, repoPath)
	if err != nil {
		return err
	}
	if !hasGitContext || len(changed) == 0 {
		return nil
	}

	files := make([]string, 0, len(changed))
	for file := range changed {
		files = append(files, file)
	}
	sort.Strings(files)
	if len(files) > 5 {
		files = append(files[:5], fmt.Sprintf("+%d more", len(files)-5))
	}
	return fmt.Errorf("%w: detected uncommitted changes in %s; commit/stash them first or pass --allow-dirty", ErrDirtyWorktree, strings.Join(files, ", "))
}

func findCodemodReport(reportData *report.Report, dependency string) *report.CodemodReport {
	if reportData == nil {
		return nil
	}
	if strings.TrimSpace(dependency) != "" {
		for i := range reportData.Dependencies {
			if reportData.Dependencies[i].Name == dependency {
				return reportData.Dependencies[i].Codemod
			}
		}
	}
	for i := range reportData.Dependencies {
		if reportData.Dependencies[i].Codemod != nil {
			return reportData.Dependencies[i].Codemod
		}
	}
	return nil
}

func buildCodemodSkipResults(skips []report.CodemodSkip) []report.CodemodApplyResult {
	if len(skips) == 0 {
		return nil
	}
	grouped := make(map[string][]string)
	for _, skip := range skips {
		grouped[skip.File] = append(grouped[skip.File], skip.ReasonCode)
	}

	files := make([]string, 0, len(grouped))
	for file := range grouped {
		files = append(files, file)
	}
	sort.Strings(files)

	results := make([]report.CodemodApplyResult, 0, len(files))
	for _, file := range files {
		reasons := uniqueSortedStrings(grouped[file])
		results = append(results, report.CodemodApplyResult{
			File:       file,
			Status:     codemodApplyStatusSkipped,
			PatchCount: len(grouped[file]),
			Message:    "reason codes: " + strings.Join(reasons, ", "),
		})
	}
	return results
}

func prepareCodemodFiles(repoPath string, suggestions []report.CodemodSuggestion) ([]preparedCodemodFile, []report.CodemodApplyResult) {
	grouped := make(map[string][]report.CodemodSuggestion)
	for _, suggestion := range suggestions {
		grouped[suggestion.File] = append(grouped[suggestion.File], suggestion)
	}

	files := make([]string, 0, len(grouped))
	for file := range grouped {
		files = append(files, file)
	}
	sort.Strings(files)

	prepared := make([]preparedCodemodFile, 0, len(files))
	failures := make([]report.CodemodApplyResult, 0)
	for _, file := range files {
		fileSuggestions := append([]report.CodemodSuggestion{}, grouped[file]...)
		sort.Slice(fileSuggestions, func(i, j int) bool {
			if fileSuggestions[i].Line != fileSuggestions[j].Line {
				return fileSuggestions[i].Line < fileSuggestions[j].Line
			}
			return fileSuggestions[i].ImportName < fileSuggestions[j].ImportName
		})

		absPath, err := resolveCodemodFilePath(repoPath, file)
		if err != nil {
			failures = append(failures, report.CodemodApplyResult{File: file, Status: codemodApplyStatusFailed, PatchCount: len(fileSuggestions), Message: err.Error()})
			continue
		}
		content, err := safeio.ReadFileUnder(repoPath, absPath)
		if err != nil {
			failures = append(failures, report.CodemodApplyResult{File: file, Status: codemodApplyStatusFailed, PatchCount: len(fileSuggestions), Message: err.Error()})
			continue
		}
		info, err := os.Stat(absPath)
		if err != nil {
			failures = append(failures, report.CodemodApplyResult{File: file, Status: codemodApplyStatusFailed, PatchCount: len(fileSuggestions), Message: err.Error()})
			continue
		}
		updated, err := applySuggestionsToContent(string(content), fileSuggestions)
		if err != nil {
			failures = append(failures, report.CodemodApplyResult{File: file, Status: codemodApplyStatusFailed, PatchCount: len(fileSuggestions), Message: err.Error()})
			continue
		}
		prepared = append(prepared, preparedCodemodFile{
			file:       file,
			absPath:    absPath,
			original:   string(content),
			updated:    updated,
			patchCount: len(fileSuggestions),
			mode:       info.Mode().Perm(),
		})
	}
	return prepared, failures
}

func applySuggestionsToContent(content string, suggestions []report.CodemodSuggestion) (string, error) {
	lineSeparator := "\n"
	if strings.Contains(content, "\r\n") {
		lineSeparator = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	for _, suggestion := range suggestions {
		if suggestion.Line <= 0 || suggestion.Line > len(lines) {
			return "", fmt.Errorf("line %d is out of range for %s", suggestion.Line, suggestion.File)
		}
		if lines[suggestion.Line-1] != suggestion.Original {
			return "", fmt.Errorf("source line mismatch at %s:%d", suggestion.File, suggestion.Line)
		}
		lines[suggestion.Line-1] = suggestion.Replacement
	}
	return strings.Join(lines, lineSeparator), nil
}

func applyPreparedCodemodFiles(repoPath string, prepared []preparedCodemodFile, failures []report.CodemodApplyResult) ([]report.CodemodApplyResult, []report.CodemodApplyResult) {
	applied := make([]report.CodemodApplyResult, 0, len(prepared))
	for _, item := range prepared {
		if err := writeFileAtomically(repoPath, item.absPath, item.updated, item.mode); err != nil {
			failures = append(failures, report.CodemodApplyResult{
				File:       item.file,
				Status:     codemodApplyStatusFailed,
				PatchCount: item.patchCount,
				Message:    err.Error(),
			})
			continue
		}
		applied = append(applied, report.CodemodApplyResult{
			File:       item.file,
			Status:     codemodApplyStatusApplied,
			PatchCount: item.patchCount,
		})
	}
	return applied, failures
}

func writeCodemodRollbackArtifact(repoPath, dependency string, prepared []preparedCodemodFile, now time.Time) (relativePath string, err error) {
	if len(prepared) == 0 {
		return "", nil
	}
	root, err := os.OpenRoot(repoPath)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	if err := root.MkdirAll(codemodRollbackDir, 0o750); err != nil {
		return "", err
	}

	fileName := fmt.Sprintf("%s-%d.json", sanitizeArtifactName(dependency), now.UTC().UnixNano())
	relativePath = filepath.Join(codemodRollbackDir, fileName)
	absPath := filepath.Join(repoPath, relativePath)

	payload := codemodRollbackArtifact{
		GeneratedAt: now.UTC(),
		Dependency:  dependency,
		Files:       make([]codemodRollbackRecord, 0, len(prepared)),
	}
	for _, item := range prepared {
		payload.Files = append(payload.Files, codemodRollbackRecord{
			File:    item.file,
			Mode:    uint32(item.mode),
			Content: item.original,
		})
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeFileAtomically(repoPath, absPath, string(data)+"\n", 0o600); err != nil {
		return "", err
	}
	return filepath.ToSlash(relativePath), nil
}

func writeFileAtomically(repoPath, path, content string, mode os.FileMode) error {
	return safeio.WriteFileUnder(repoPath, path, []byte(content), mode)
}

func resolveCodemodFilePath(repoPath, relativePath string) (string, error) {
	if strings.TrimSpace(relativePath) == "" {
		return "", fmt.Errorf("empty codemod file path")
	}
	if filepath.IsAbs(relativePath) {
		return "", fmt.Errorf("codemod file path must be relative: %s", relativePath)
	}

	cleaned := filepath.Clean(relativePath)
	absPath := filepath.Join(repoPath, cleaned)
	rel, err := filepath.Rel(repoPath, absPath)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("codemod file path escapes repository: %s", relativePath)
	}
	return absPath, nil
}

func countUniqueResultFiles(results []report.CodemodApplyResult, status string) int {
	files := make(map[string]struct{})
	for _, result := range results {
		if result.Status != status {
			continue
		}
		files[result.File] = struct{}{}
	}
	return len(files)
}

func countResultPatches(results []report.CodemodApplyResult, status string) int {
	total := 0
	for _, result := range results {
		if result.Status != status {
			continue
		}
		total += result.PatchCount
	}
	return total
}

func codemodApplyError(results []report.CodemodApplyResult) error {
	failures := make([]string, 0)
	for _, result := range results {
		if result.Status != codemodApplyStatusFailed {
			continue
		}
		failures = append(failures, fmt.Sprintf("%s: %s", result.File, result.Message))
	}
	if len(failures) == 0 {
		return ErrCodemodApplyFailed
	}
	return fmt.Errorf("%w: %s", ErrCodemodApplyFailed, strings.Join(failures, "; "))
}

func sortCodemodApplyResults(results []report.CodemodApplyResult) {
	statusOrder := map[string]int{
		codemodApplyStatusApplied: 0,
		codemodApplyStatusSkipped: 1,
		codemodApplyStatusFailed:  2,
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].File != results[j].File {
			return results[i].File < results[j].File
		}
		if statusOrder[results[i].Status] != statusOrder[results[j].Status] {
			return statusOrder[results[i].Status] < statusOrder[results[j].Status]
		}
		return results[i].PatchCount < results[j].PatchCount
	})
}

func uniqueSortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		unique = append(unique, trimmed)
	}
	sort.Strings(unique)
	return unique
}

func sanitizeArtifactName(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "codemod"
	}
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", " ", "-")
	sanitized := replacer.Replace(trimmed)
	sanitized = strings.Trim(sanitized, "-.")
	if sanitized == "" {
		return "codemod"
	}
	return sanitized
}
