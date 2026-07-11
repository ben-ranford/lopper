package analysis

import (
	"context"
	"strings"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/runtime"
)

const runtimeTraceCommandWarningPrefix = "runtime trace command failed; continuing with static analysis: "

func captureRuntimeTraceIfNeeded(ctx context.Context, req Request, repoPath string, cache *analysisCache, candidates []language.Candidate) ([]string, string, bool) {
	tracePath := strings.TrimSpace(req.RuntimeTracePath)
	command := strings.TrimSpace(req.RuntimeTestCommand)
	if command == "" {
		return nil, tracePath, false
	}
	if tracePath == "" {
		tracePath = runtime.DefaultTracePath(repoPath)
	}

	provider := captureProviderForRequest(req, command, candidates)
	if err := runtime.Capture(ctx, runtime.CaptureRequest{
		RepoPath:         repoPath,
		TracePath:        tracePath,
		Command:          command,
		Provider:         provider,
		ReuseIfUnchanged: shouldReuseRuntimeTrace(cache),
	}); err != nil {
		warning := runtimeTraceCommandWarningPrefix + err.Error()
		if req.RuntimeTracePathExplicit {
			return []string{warning}, tracePath, false
		}
		return []string{warning}, "", false
	}
	return nil, tracePath, provider == runtime.CaptureProviderPython
}

func captureProviderForRequest(req Request, command string, candidates []language.Candidate) runtime.CaptureProvider {
	if !req.Features.Enabled(pythonRuntimeCaptureFeature) || !hasPythonRuntimeCandidate(req.Language, candidates) {
		return runtime.CaptureProviderNode
	}
	if isExplicitPythonLanguage(req.Language) || runtime.IsPythonTestCommand(command) || hasOnlyPythonRuntimeCandidate(candidates) {
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
