package analysis

import (
	"context"
	"strings"

	"github.com/ben-ranford/lopper/internal/runtime"
)

const runtimeTraceCommandWarningPrefix = "runtime trace command failed; continuing with static analysis: "

func captureRuntimeTraceIfNeeded(ctx context.Context, req Request, repoPath string, cache *analysisCache) ([]string, string) {
	tracePath := strings.TrimSpace(req.RuntimeTracePath)
	command := strings.TrimSpace(req.RuntimeTestCommand)
	if command == "" {
		return nil, tracePath
	}
	if tracePath == "" {
		tracePath = runtime.DefaultTracePath(repoPath)
	}

	if err := runtime.Capture(ctx, runtime.CaptureRequest{
		RepoPath:         repoPath,
		TracePath:        tracePath,
		Command:          command,
		ReuseIfUnchanged: shouldReuseRuntimeTrace(cache),
	}); err != nil {
		warning := runtimeTraceCommandWarningPrefix + err.Error()
		if req.RuntimeTracePathExplicit {
			return []string{warning}, tracePath
		}
		return []string{warning}, ""
	}
	return nil, tracePath
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
