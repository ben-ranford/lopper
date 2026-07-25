package analysis

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/runtime"
)

const runtimeTraceCommandWarningPrefix = "runtime trace command failed; continuing with static analysis: "
const runtimeTraceMissingWarning = "runtime trace file not found; continuing with static analysis"

var captureRuntimeTraceAfterValidatedLoadHook func()

type runtimeTraceCaptureOutcome struct {
	warnings         []string
	tracePath        string
	captureAttempted bool
	pythonCaptured   bool
	trace            *runtime.Trace
	traceFinalized   bool
}

func captureRuntimeTraceIfNeeded(ctx context.Context, req Request, repoPath string, candidates []language.Candidate) (runtimeTraceCaptureOutcome, error) {
	outcome := runtimeTraceCaptureOutcome{tracePath: strings.TrimSpace(req.RuntimeTracePath)}
	command := strings.TrimSpace(req.RuntimeTestCommand)
	if command == "" && !req.RuntimeTracePathExplicit {
		return outcome, nil
	}

	resolvedTracePath, err := runtime.ResolveTracePathForRepo(repoPath, outcome.tracePath)
	if err != nil {
		return outcome, err
	}
	outcome.tracePath = resolvedTracePath

	if command == "" {
		return finalizeRuntimeTraceWithoutCommand(outcome, resolvedTracePath)
	}

	outcome.captureAttempted = true
	outcome.traceFinalized = true
	fallback, err := loadExplicitRuntimeTraceFallback(req.RuntimeTracePathExplicit, resolvedTracePath)
	if err != nil {
		return outcome, err
	}

	provider := captureProviderForRequest(req, command, candidates)
	captureResult, err := runRuntimeTraceCapture(ctx, req, repoPath, resolvedTracePath, command, provider)
	if captureResult.TracePath != "" {
		outcome.tracePath = captureResult.TracePath
	}
	if err != nil {
		return handleRuntimeTraceCaptureError(outcome, fallback, err)
	}

	outcome.pythonCaptured = provider == runtime.CaptureProviderPython
	return finalizeCapturedRuntimeTrace(outcome, req, captureResult)
}

func loadRuntimeTraceSnapshot(tracePath string) (*runtime.Trace, bool, error) {
	traceData, err := runtime.LoadValidatedTrace(tracePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, true, nil
		}
		return nil, false, err
	}
	return &traceData, false, nil
}

type explicitRuntimeTraceFallback struct {
	trace   *runtime.Trace
	missing bool
}

func finalizeRuntimeTraceWithoutCommand(outcome runtimeTraceCaptureOutcome, resolvedTracePath string) (runtimeTraceCaptureOutcome, error) {
	traceData, missing, err := loadRuntimeTraceSnapshot(resolvedTracePath)
	if err != nil {
		return outcome, err
	}
	outcome.traceFinalized = true
	if missing {
		outcome.warnings = []string{runtimeTraceMissingWarning}
		return outcome, nil
	}
	runRuntimeTraceValidatedLoadHook()
	outcome.trace = traceData
	return outcome, nil
}

func loadExplicitRuntimeTraceFallback(explicit bool, resolvedTracePath string) (explicitRuntimeTraceFallback, error) {
	if !explicit {
		return explicitRuntimeTraceFallback{}, nil
	}
	traceData, missing, err := loadRuntimeTraceSnapshot(resolvedTracePath)
	if err != nil {
		return explicitRuntimeTraceFallback{}, err
	}
	return explicitRuntimeTraceFallback{trace: traceData, missing: missing}, nil
}

func runRuntimeTraceCapture(ctx context.Context, req Request, repoPath string, resolvedTracePath string, command string, provider runtime.CaptureProvider) (runtime.CaptureResult, error) {
	return runtime.CaptureValidatedTrace(ctx, runtime.CaptureRequest{
		RepoPath:             repoPath,
		TracePath:            resolvedTracePath,
		Command:              command,
		Provider:             provider,
		PythonRunnerProfiles: req.Features.Enabled(runtime.PythonRunnerProfilesFeature),
	})
}

func handleRuntimeTraceCaptureError(outcome runtimeTraceCaptureOutcome, fallback explicitRuntimeTraceFallback, err error) (runtimeTraceCaptureOutcome, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return outcome, err
	}
	warnings := []string{runtimeTraceCommandWarningPrefix + err.Error()}
	if fallback.trace != nil {
		outcome.warnings = warnings
		outcome.trace = fallback.trace
		return outcome, nil
	}
	if fallback.missing {
		warnings = append(warnings, runtimeTraceMissingWarning)
	}
	outcome.warnings = warnings
	return outcome, nil
}

func finalizeCapturedRuntimeTrace(outcome runtimeTraceCaptureOutcome, req Request, captureResult runtime.CaptureResult) (runtimeTraceCaptureOutcome, error) {
	if !supportsCapturedRuntimeTrace(req, outcome.pythonCaptured) {
		return outcome, nil
	}
	if !captureResult.TraceProduced {
		outcome.warnings = []string{runtimeTraceMissingWarning}
		return outcome, nil
	}
	traceData, err := captureResult.Snapshot.Load()
	if err != nil {
		return outcome, err
	}
	runRuntimeTraceValidatedLoadHook()
	outcome.trace = &traceData
	return outcome, nil
}

func supportsCapturedRuntimeTrace(req Request, pythonCaptured bool) bool {
	pythonTraceEnabled := req.Features.Enabled(pythonRuntimeTraceFeature) ||
		(pythonCaptured && req.Features.Enabled(pythonRuntimeCaptureFeature))
	return len(supportedRuntimeTraceLanguages(req.Language, pythonTraceEnabled)) > 0
}

func runRuntimeTraceValidatedLoadHook() {
	if captureRuntimeTraceAfterValidatedLoadHook != nil {
		captureRuntimeTraceAfterValidatedLoadHook()
	}
}

func captureProviderForRequest(req Request, command string, candidates []language.Candidate) runtime.CaptureProvider {
	if !req.Features.Enabled(pythonRuntimeCaptureFeature) || !hasPythonRuntimeCandidate(req.Language, candidates) {
		return runtime.CaptureProviderNode
	}
	commandOptions := runtime.CommandOptions{PythonRunnerProfiles: req.Features.Enabled(runtime.PythonRunnerProfilesFeature)}
	if isExplicitPythonLanguage(req.Language) || runtime.IsPythonTestCommand(command, commandOptions) || hasOnlyPythonRuntimeCandidate(candidates) {
		return runtime.CaptureProviderPython
	}
	return runtime.CaptureProviderNode
}

func hasPythonRuntimeCandidate(languageID string, candidates []language.Candidate) bool {
	if isExplicitPythonLanguage(languageID) {
		return true
	}
	for _, candidate := range candidates {
		if candidate.Adapter != nil && normalizeAdapterID(candidate.Adapter.ID()) == "python" {
			return true
		}
	}
	return false
}

func hasOnlyPythonRuntimeCandidate(candidates []language.Candidate) bool {
	if len(candidates) != 1 || candidates[0].Adapter == nil {
		return false
	}
	return normalizeAdapterID(candidates[0].Adapter.ID()) == "python"
}

func isExplicitPythonLanguage(languageID string) bool {
	switch strings.TrimSpace(strings.ToLower(languageID)) {
	case "python", "py":
		return true
	default:
		return false
	}
}
