package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/workspace"
)

var normalizeRepoPathFn = workspace.NormalizeRepoPath

func evaluateLockfileDriftPolicy(ctx context.Context, repoPath, policy string) ([]string, error) {
	return evaluateLockfileDriftPolicyWithFeatures(ctx, repoPath, policy, featureflags.Set{})
}

func evaluateLockfileDriftPolicyWithFeatures(ctx context.Context, repoPath, policy string, features featureflags.Set) ([]string, error) {
	normalizedPolicy := strings.TrimSpace(policy)
	if normalizedPolicy == "off" {
		return nil, nil
	}
	failMode := normalizedPolicy == "fail"
	driftWarnings, err := detectLockfileDriftWithFeatures(ctx, repoPath, failMode, features)
	if err != nil && !failMode && errors.Is(err, safeio.ErrFileTooLarge) {
		return append(driftWarnings, oversizedLockfileDriftWarning(err)), nil
	}
	if err != nil || len(driftWarnings) == 0 {
		return driftWarnings, err
	}
	if failMode {
		return driftWarnings, formatLockfileDriftError(driftWarnings)
	}
	return driftWarnings, nil
}

func oversizedLockfileDriftWarning(err error) string {
	return fmt.Sprintf("%sunable to safely inspect manifest during lockfile drift analysis: %v", lockfileDriftWarningPrefix, err)
}

func detectLockfileDrift(ctx context.Context, repoPath string, stopOnFirst bool) ([]string, error) {
	return detectLockfileDriftWithFeatures(ctx, repoPath, stopOnFirst, featureflags.Set{})
}

func detectLockfileDriftWithFeatures(ctx context.Context, repoPath string, stopOnFirst bool, features featureflags.Set) ([]string, error) {
	normalizedPath, err := normalizeRepoPathFn(repoPath)
	if err != nil {
		return nil, err
	}
	rules := activeLockfileRules(features)
	if stopOnFirst {
		return scanLockfileDriftStopOnFirst(ctx, normalizedPath, rules)
	}
	gitContext, err := collectLockfileGitContextFn(ctx, normalizedPath, rules)
	if err != nil {
		return nil, err
	}
	return scanLockfileDrift(ctx, normalizedPath, gitContext, false, rules)
}
