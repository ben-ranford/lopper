package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/workspace"
)

var normalizeRepoPathFn = workspace.NormalizeRepoPath

type lockfileDriftResult struct {
	findings        []string
	orderedWarnings []string
	err             error
}

func evaluateLockfileDriftPolicy(ctx context.Context, repoPath, policy string) ([]string, error) {
	return evaluateLockfileDriftPolicyWithFeatures(ctx, repoPath, policy, featureflags.Set{})
}

func evaluateLockfileDriftPolicyWithFeatures(ctx context.Context, repoPath, policy string, features featureflags.Set) ([]string, error) {
	normalizedPolicy := strings.TrimSpace(policy)
	if normalizedPolicy == "off" {
		return nil, nil
	}
	failMode := normalizedPolicy == "fail"
	result := detectLockfileDriftDetailed(ctx, repoPath, failMode, features)
	if result.err != nil && !failMode && isPureLockfileManifestReadSizeError(result.err) {
		return result.orderedWarnings, nil
	}
	if result.err != nil || len(result.findings) == 0 {
		return result.findings, result.err
	}
	if failMode {
		return result.findings, formatLockfileDriftError(result.findings)
	}
	return result.findings, nil
}

func oversizedLockfileDriftWarning(err error) string {
	return fmt.Sprintf("%sunable to safely inspect manifest during lockfile drift analysis: %v", lockfileDriftWarningPrefix, err)
}

func detectLockfileDrift(ctx context.Context, repoPath string, stopOnFirst bool) ([]string, error) {
	return detectLockfileDriftWithFeatures(ctx, repoPath, stopOnFirst, featureflags.Set{})
}

func detectLockfileDriftWithFeatures(ctx context.Context, repoPath string, stopOnFirst bool, features featureflags.Set) ([]string, error) {
	result := detectLockfileDriftDetailed(ctx, repoPath, stopOnFirst, features)
	return result.findings, result.err
}

func detectLockfileDriftDetailed(ctx context.Context, repoPath string, stopOnFirst bool, features featureflags.Set) lockfileDriftResult {
	normalizedPath, err := normalizeRepoPathFn(repoPath)
	if err != nil {
		return lockfileDriftResult{err: err}
	}
	rules := activeLockfileRules(features)
	if stopOnFirst {
		findings, scanErr := scanLockfileDriftStopOnFirst(ctx, normalizedPath, rules)
		return lockfileDriftResult{
			findings:        findings,
			orderedWarnings: findings,
			err:             scanErr,
		}
	}
	gitContext, candidateErr := collectLockfileGitContextFn(ctx, normalizedPath, rules)
	if candidateErr != nil && !isPureLockfileManifestReadSizeError(candidateErr) {
		return lockfileDriftResult{err: candidateErr}
	}

	result := scanLockfileDriftDetailed(ctx, normalizedPath, gitContext, false, rules)
	if gitContext.preparedScan != nil {
		return result
	}
	if candidateErr != nil {
		// Injected collectors cannot provide source coordinates; production Git
		// collection returns a prepared scan and emits the error during replay.
		result.orderedWarnings = append(result.orderedWarnings, oversizedLockfileDriftWarning(candidateErr))
		result.err = errors.Join(candidateErr, result.err)
	}
	return result
}
