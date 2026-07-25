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
		traceData, missing, err := loadRuntimeTraceSnapshot(resolvedTracePath)
		if err != nil {
			return outcome, err
		}
		outcome.traceFinalized = true
		if missing {
			outcome.warnings = []string{runtimeTraceMissingWarning}
			return outcome, nil
		}
		if captureRuntimeTraceAfterValidatedLoadHook != nil {
			captureRuntimeTraceAfterValidatedLoadHook()
		}
		outcome.trace = traceData
		return outcome, nil
	}
	outcome.captureAttempted = true
	outcome.traceFinalized = true

	var explicitTraceSnapshot *runtime.Trace
	explicitTraceMissing := false
	var explicitTraceErr error
	if req.RuntimeTracePathExplicit {
		explicitTraceSnapshot, explicitTraceMissing, explicitTraceErr = loadRuntimeTraceSnapshot(resolvedTracePath)
		if explicitTraceErr != nil {
			return outcome, explicitTraceErr
		}
	}

	provider := captureProviderForRequest(req, command, candidates)
	pythonRunnerProfiles := req.Features.Enabled(runtime.PythonRunnerProfilesFeature)
	captureResult, err := runtime.CaptureValidatedTrace(ctx, runtime.CaptureRequest{
		RepoPath:             repoPath,
		TracePath:            resolvedTracePath,
		Command:              command,
		Provider:             provider,
		PythonRunnerProfiles: pythonRunnerProfiles,
	})
	if captureResult.TracePath != "" {
		outcome.tracePath = captureResult.TracePath
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return outcome, err
		}
		if explicitTraceSnapshot != nil {
			outcome.warnings = []string{runtimeTraceCommandWarningPrefix + err.Error()}
			outcome.trace = explicitTraceSnapshot
			return outcome, nil
		}
		if explicitTraceMissing {
			outcome.warnings = []string{runtimeTraceCommandWarningPrefix + err.Error(), runtimeTraceMissingWarning}
			return outcome, nil
		}
		outcome.warnings = []string{runtimeTraceCommandWarningPrefix + err.Error()}
		return outcome, nil
	}

	outcome.pythonCaptured = provider == runtime.CaptureProviderPython
	pythonTraceEnabled := req.Features.Enabled(pythonRuntimeTraceFeature) ||
		(outcome.pythonCaptured && req.Features.Enabled(pythonRuntimeCaptureFeature))
	if len(supportedRuntimeTraceLanguages(req.Language, pythonTraceEnabled)) == 0 {
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
	if captureRuntimeTraceAfterValidatedLoadHook != nil {
		captureRuntimeTraceAfterValidatedLoadHook()
	}
	outcome.trace = &traceData
	return outcome, nil
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
