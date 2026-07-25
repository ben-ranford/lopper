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

func captureRuntimeTraceIfNeeded(ctx context.Context, req Request, repoPath string, analysisRepoPath string, cache *analysisCache, candidates []language.Candidate) ([]string, string, bool, *runtime.Trace, error) {
	tracePath := strings.TrimSpace(req.RuntimeTracePath)
	command := strings.TrimSpace(req.RuntimeTestCommand)
	if command == "" {
		return nil, tracePath, false, nil, nil
	}
	if tracePath == "" {
		tracePath = runtime.DefaultTracePath(repoPath)
	}

	provider := captureProviderForRequest(req, command, candidates)
	pythonRunnerProfiles := req.Features.Enabled(runtime.PythonRunnerProfilesFeature)
	trustedInputDigest, _ := trustedRuntimeTraceInputDigest(cache, repoPath, req.ConfigPath)
	validatedTrace, err := runtime.CaptureValidatedTrace(ctx, runtime.CaptureRequest{
		RepoPath:             repoPath,
		TracePath:            tracePath,
		Command:              command,
		Provider:             provider,
		ReuseIfUnchanged:     shouldReuseRuntimeTrace(cache),
		TrustedInputDigest:   trustedInputDigest,
		PythonRunnerProfiles: pythonRunnerProfiles,
	})
	if err != nil {
		warning := runtimeTraceCommandWarningPrefix + err.Error()
		if req.RuntimeTracePathExplicit {
			return []string{warning}, tracePath, false, nil, nil
		}
		return []string{warning}, "", false, nil, nil
	}

	pythonCaptured := provider == runtime.CaptureProviderPython
	pythonTraceEnabled := req.Features.Enabled(pythonRuntimeTraceFeature) ||
		(pythonCaptured && req.Features.Enabled(pythonRuntimeCaptureFeature))
	if len(supportedRuntimeTraceLanguages(req.Language, pythonTraceEnabled)) == 0 {
		return nil, tracePath, pythonCaptured, nil, nil
	}

	var traceData runtime.Trace
	if validatedTrace == nil {
		traceData, err = runtime.Load(tracePath)
	} else {
		traceData, err = validatedTrace.Load()
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{runtimeTraceMissingWarning}, tracePath, pythonCaptured, nil, nil
		}
		return nil, tracePath, pythonCaptured, nil, err
	}
	if captureRuntimeTraceAfterValidatedLoadHook != nil {
		captureRuntimeTraceAfterValidatedLoadHook()
	}
	return nil, tracePath, pythonCaptured, &traceData, nil
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

func shouldReuseRuntimeTrace(cache *analysisCache) bool {
	if cache == nil {
		return false
	}
	metadata := cache.metadataSnapshot()
	if metadata == nil {
		return false
	}
	return metadata.Enabled && metadata.Hits > 0 && metadata.Misses == 0
}

func trustedRuntimeTraceInputDigest(cache *analysisCache, repoPath string, configPath string) (string, bool) {
	if cache == nil || strings.TrimSpace(repoPath) == "" {
		return "", false
	}
	digest, err := cache.memoizedInputDigest(repoPath, configPath)
	if err != nil || strings.TrimSpace(digest) == "" {
		return "", false
	}
	return digest, true
}
