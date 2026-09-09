//go:build lockfiledrift_head

package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestLockfileManifestIOFromContextUsesDefaults(t *testing.T) {
	called := false
	customRead := func(string, string) ([]byte, error) {
		called = true
		return []byte("ok"), nil
	}

	ctx := withLockfileManifestIO(context.Background(), lockfileManifestIO{
		readFileUnder: customRead,
	})
	fromContext := lockfileManifestIOFromContext(ctx)
	if fromContext.readFileUnder == nil || fromContext.readFileUnderLimit == nil {
		t.Fatalf("expected context readers to be defaulted, got %#v", fromContext)
	}
	if _, err := fromContext.readFileUnder("", ""); err != nil {
		t.Fatalf("custom manifest reader returned error: %v", err)
	}
	if !called {
		t.Fatal("expected context manifest reader to be preserved")
	}
}

func TestLockfileManifestChangeCandidatePathsWithReadErrorsSkipsRecoverableManifestErrors(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, pyprojectManifestName), oversizedManifestBody("[tool.poetry]\nname = \"demo\"\n", "# filler\n", 8))
	writeFile(t, filepath.Join(repo, poetryLockName), "# lock\n")
	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
	writeFile(t, filepath.Join(repo, lockfileName), "{}\n")

	snapshot, err := readLockfileDirSnapshot(repo, repo)
	if err != nil {
		t.Fatalf("read lockfile snapshot: %v", err)
	}

	readErrors := &lockfileManifestReadErrors{}
	rules := []lockfileRule{
		mustLockfileRule(t, "Poetry", pyprojectManifestName),
		mustLockfileRule(t, "npm", manifestFileName),
	}
	candidates, err := lockfileManifestChangeCandidatePathsWithReadErrors(snapshot, rules, newLockfileManifestCache(snapshot), readErrors)
	if err != nil {
		t.Fatalf("collect candidate paths: %v", err)
	}

	assertCandidatePaths(t, candidates, []string{lockfileName, manifestFileName})
	if len(readErrors.records) != 1 {
		t.Fatalf("expected one recoverable manifest read error, got %#v", readErrors.records)
	}
	if !errors.Is(readErrors.records[0].err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected recoverable size error, got %v", readErrors.records[0].err)
	}
}

func TestPrepareLockfileRulePropagatesDistributedLockfileDiscoveryError(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, dotnetCentralManifest)
	writeFile(t, manifestPath, "<Project></Project>\n")
	manifestInfo, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatalf("stat central manifest: %v", err)
	}

	snapshot := lockfileDirSnapshot{
		repoPath: root,
		path:     filepath.Join(root, "missing"),
		relDir:   ".",
		files: map[string]fs.FileInfo{
			dotnetCentralManifest: manifestInfo,
		},
	}

	_, _, err = prepareLockfileRule(snapshot, mustLockfileRule(t, ".NET", dotnetCentralManifest), newLockfileManifestCache(snapshot), &lockfileManifestReadErrors{})
	if err == nil {
		t.Fatal("expected distributed lockfile discovery error")
	}
}

func TestPrepareLockfileRuleSkipsRecoverableReadErrorsWithoutCollector(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, pyprojectManifestName), oversizedManifestBody("[tool.poetry]\nname = \"demo\"\n", "# filler\n", 8))
	writeFile(t, filepath.Join(repo, poetryLockName), "# lock\n")

	snapshot, err := readLockfileDirSnapshot(repo, repo)
	if err != nil {
		t.Fatalf("read lockfile snapshot: %v", err)
	}

	prepared, candidates, err := prepareLockfileRule(snapshot, mustLockfileRule(t, "Poetry", pyprojectManifestName), newLockfileManifestCache(snapshot), nil)
	if err != nil {
		t.Fatalf("prepare rule with recoverable read error: %v", err)
	}
	if prepared.manifestReadErr != nil {
		t.Fatalf("expected nil collector to skip recording manifest read error, got %v", prepared.manifestReadErr)
	}
	if prepared.replay != nil || prepared.manifestChange != nil {
		t.Fatalf("expected no prepared replay state on recoverable read error, got %#v", prepared)
	}
	if len(candidates) != 0 {
		t.Fatalf("expected no candidate paths on recoverable read error, got %#v", candidates)
	}
}

func TestPrepareLockfileRuleAndReplayDetectStaleLockfileForNonMatchingManifest(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, pyprojectManifestName), "[build-system]\nrequires = [\"setuptools\"]\n")
	writeFile(t, filepath.Join(repo, poetryLockName), "# lock\n")

	snapshot, err := readLockfileDirSnapshot(repo, repo)
	if err != nil {
		t.Fatalf("read lockfile snapshot: %v", err)
	}

	prepared, candidates, err := prepareLockfileRule(snapshot, mustLockfileRule(t, "Poetry", pyprojectManifestName), newLockfileManifestCache(snapshot), &lockfileManifestReadErrors{})
	if err != nil {
		t.Fatalf("prepare stale replay rule: %v", err)
	}
	if prepared.replay == nil {
		t.Fatalf("expected replay state for stale non-matching manifest, got %#v", prepared)
	}
	if len(candidates) != 0 {
		t.Fatalf("expected no candidate paths for stale non-matching manifest, got %#v", candidates)
	}
	if len(prepared.replay.lockfiles) != 1 || prepared.replay.lockfiles[0] != poetryLockName {
		t.Fatalf("expected replay lockfile names to be retained, got %#v", prepared.replay.lockfiles)
	}

	dir := lockfilePreparedDir{repoPath: repo, path: repo, relDir: "."}
	finding, found, err := evaluatePreparedReplayRule(dir, prepared, lockfileGitContext{}, newLockfileManifestCache(snapshot))
	if err != nil {
		t.Fatalf("evaluate prepared replay rule: %v", err)
	}
	if !found {
		t.Fatal("expected stale lockfile finding from prepared replay rule")
	}
	if finding.kind != lockfileDriftStaleLockfile {
		t.Fatalf("expected stale lockfile finding, got %#v", finding)
	}
	if len(finding.lockfiles) != 1 || finding.lockfiles[0].name != poetryLockName {
		t.Fatalf("expected stale finding to retain lockfile names, got %#v", finding.lockfiles)
	}
}

func TestPrepareLockfileManifestChangeCandidatesDropsEmptyDirsAndRules(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "a-empty"), 0o755); err != nil {
		t.Fatalf("mkdir empty dir: %v", err)
	}
	writeFile(t, filepath.Join(repo, "b-replay", pyprojectManifestName), "[build-system]\nrequires = [\"setuptools\"]\n")
	writeFile(t, filepath.Join(repo, "b-replay", poetryLockName), "# lock\n")
	writeFile(t, filepath.Join(repo, "c-candidate", manifestFileName), demoPackageJSON)
	writeFile(t, filepath.Join(repo, "c-candidate", lockfileName), "{}\n")
	writeFile(t, filepath.Join(repo, "d-oversized", pyprojectManifestName), oversizedManifestBody("[tool.poetry]\nname = \"demo\"\n", "# filler\n", 8))
	writeFile(t, filepath.Join(repo, "d-oversized", poetryLockName), "# lock\n")

	prepared, candidates, err := prepareLockfileManifestChangeCandidates(context.Background(), repo, []lockfileRule{
		mustLockfileRule(t, "Poetry", pyprojectManifestName),
		mustLockfileRule(t, "npm", manifestFileName),
	})
	if !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected oversized manifest error, got %v", err)
	}
	assertCandidatePaths(t, candidates, []string{
		filepath.ToSlash(filepath.Join("c-candidate", lockfileName)),
		filepath.ToSlash(filepath.Join("c-candidate", manifestFileName)),
	})
	if prepared == nil {
		t.Fatal("expected prepared scan")
	}
	if len(prepared.dirs) != 3 {
		t.Fatalf("expected only retained prepared dirs, got %#v", prepared.dirs)
	}
	if prepared.dirs[0].relDir != "b-replay" || len(prepared.dirs[0].rules) != 1 || prepared.dirs[0].rules[0].replay == nil {
		t.Fatalf("expected replay-only dir retention, got %#v", prepared.dirs[0])
	}
	if prepared.dirs[1].relDir != "c-candidate" || len(prepared.dirs[1].rules) != 1 || prepared.dirs[1].rules[0].manifestChange == nil {
		t.Fatalf("expected candidate-only dir retention, got %#v", prepared.dirs[1])
	}
	if prepared.dirs[2].relDir != "d-oversized" || len(prepared.dirs[2].rules) != 1 || prepared.dirs[2].rules[0].manifestReadErr == nil {
		t.Fatalf("expected read-error dir retention, got %#v", prepared.dirs[2])
	}
}

func TestPrepareLockfileManifestChangeCandidatesRetainsMinimalReplayState(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "a-oversized", pyprojectManifestName), oversizedManifestBody("[tool.poetry]\nname = \"demo\"\n", "# filler\n", 8))
	writeFile(t, filepath.Join(repo, "a-oversized", poetryLockName), "# lock\n")
	writeFile(t, filepath.Join(repo, "z-drift", manifestFileName), demoPackageJSON)
	writeFile(t, filepath.Join(repo, "z-drift", lockfileName), "{}\n")

	prepared, candidates, err := prepareLockfileManifestChangeCandidates(context.Background(), repo, []lockfileRule{
		mustLockfileRule(t, "Poetry", pyprojectManifestName),
		mustLockfileRule(t, "npm", manifestFileName),
	})
	if !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected oversized manifest error, got %v", err)
	}
	assertCandidatePaths(t, candidates, []string{
		filepath.ToSlash(filepath.Join("z-drift", lockfileName)),
		filepath.ToSlash(filepath.Join("z-drift", manifestFileName)),
	})
	if prepared == nil || len(prepared.dirs) == 0 {
		t.Fatalf("expected prepared replay state, got %#v", prepared)
	}
	assertPreparedReplayStateShape(t, reflect.TypeOf(*prepared), make(map[reflect.Type]struct{}))
}

func TestEvaluatePreparedReplayRuleReturnsNoFindingWithoutReplayInputs(t *testing.T) {
	repo := t.TempDir()
	snapshot := lockfileDirSnapshot{repoPath: repo, path: repo, relDir: "."}
	cache := newLockfileManifestCache(snapshot)
	dir := lockfilePreparedDir{repoPath: repo, path: repo, relDir: "."}

	finding, found, err := evaluatePreparedReplayRule(dir, lockfilePreparedRule{}, lockfileGitContext{}, cache)
	if err != nil {
		t.Fatalf("evaluate replay without state: %v", err)
	}
	if found {
		t.Fatalf("expected no finding without replay state, got found=%v finding=%#v", found, finding)
	}
	if finding.kind != 0 || finding.manifest != "" || finding.relDir != "" || len(finding.lockfiles) != 0 {
		t.Fatalf("expected no finding without replay state, got found=%v finding=%#v", found, finding)
	}

	emptyReplay := lockfilePreparedRule{
		replay: &lockfilePreparedRuleReplay{
			rule: lockfileRule{manager: "custom", manifest: "custom.toml"},
		},
	}
	finding, found, err = evaluatePreparedReplayRule(dir, emptyReplay, lockfileGitContext{}, cache)
	if err != nil {
		t.Fatalf("evaluate replay without manifests or lockfiles: %v", err)
	}
	if found {
		t.Fatalf("expected no finding without replay inputs, got found=%v finding=%#v", found, finding)
	}
	if finding.kind != 0 || finding.manifest != "" || finding.relDir != "" || len(finding.lockfiles) != 0 {
		t.Fatalf("expected no finding without replay inputs, got found=%v finding=%#v", found, finding)
	}
}

func TestDetectLockfileDriftStopOnFirstDoesNotPrewalkPastFinding(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "a-drift", manifestFileName), demoPackageJSON)
	triggerManifest := filepath.Join(repo, "m-trigger", pyprojectManifestName)
	writeFile(t, triggerManifest, "[tool.poetry]\nname = \"trigger\"\n")
	writeFile(t, filepath.Join(repo, "m-trigger", poetryLockName), "# lock\n")
	initGitRepo(t, repo)
	ctx := withLockfileManifestReadError(context.Background(), triggerManifest, fs.ErrPermission)

	warnings, err := evaluateLockfileDriftPolicy(ctx, repo, "fail")
	if !errors.Is(err, ErrLockfileDrift) {
		t.Fatalf("expected early lockfile drift error, got warnings=%#v err=%v", warnings, err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "npm in a-drift") {
		t.Fatalf("expected only the early npm finding, got %#v", warnings)
	}
	if errors.Is(err, fs.ErrPermission) {
		t.Fatalf("expected fail mode to stop before reading the later manifest, got %v", err)
	}
}

func TestDetectLockfileDriftStopOnFirstFlushesGitBatchBeforeLaterWalkError(t *testing.T) {
	repo := t.TempDir()
	earlyManifest := filepath.Join(repo, "a-drift", manifestFileName)
	writeFile(t, earlyManifest, demoPackageJSON)
	writeFile(t, filepath.Join(repo, "a-drift", lockfileName), "{}\n")
	triggerManifest := filepath.Join(repo, "m-trigger", pyprojectManifestName)
	writeFile(t, triggerManifest, "[tool.poetry]\nname = \"trigger\"\n")
	writeFile(t, filepath.Join(repo, "m-trigger", poetryLockName), "# lock\n")
	initGitRepo(t, repo)
	writeFile(t, earlyManifest, demoPackageJSONUpdated)
	writeFile(t, triggerManifest, "[tool.poetry]\nname = \"trigger\"\nversion = \"0.2.0\"\n")
	ctx := withLockfileManifestReadError(context.Background(), triggerManifest, fs.ErrPermission)

	warnings, err := evaluateLockfileDriftPolicy(ctx, repo, "fail")
	if !errors.Is(err, ErrLockfileDrift) {
		t.Fatalf("expected earlier lockfile drift to win over the later walk error, got warnings=%#v err=%v", warnings, err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "npm in a-drift") {
		t.Fatalf("expected only the earlier npm finding, got %#v", warnings)
	}
	if errors.Is(err, fs.ErrPermission) {
		t.Fatalf("expected earlier git finding to win over the later manifest read error, got %v", err)
	}
}

func TestDetectLockfileDriftStopOnFirstFlushesOlderGitBatchBeforeReplayingErrorSnapshot(t *testing.T) {
	repo := t.TempDir()
	earlyManifest := filepath.Join(repo, "a-drift", manifestFileName)
	laterManifest := filepath.Join(repo, "b-mixed", manifestFileName)
	laterPoetryManifest := filepath.Join(repo, "b-mixed", pyprojectManifestName)
	writeFile(t, earlyManifest, demoPackageJSON)
	writeFile(t, filepath.Join(repo, "a-drift", lockfileName), "{}\n")
	writeFile(t, laterManifest, demoPackageJSON)
	writeFile(t, filepath.Join(repo, "b-mixed", lockfileName), "{}\n")
	writeFile(t, laterPoetryManifest, "[tool.poetry]\nname = \"demo\"\n")
	writeFile(t, filepath.Join(repo, "b-mixed", poetryLockName), "# lock\n")
	initGitRepo(t, repo)
	writeFile(t, earlyManifest, demoPackageJSONUpdated)
	writeFile(t, laterManifest, demoPackageJSONUpdatedV2)
	ctx := withLockfileManifestReadError(context.Background(), laterPoetryManifest, fs.ErrPermission)

	warnings, err := evaluateLockfileDriftPolicy(ctx, repo, "fail")
	if !errors.Is(err, ErrLockfileDrift) {
		t.Fatalf("expected earlier buffered drift finding to win, got warnings=%#v err=%v", warnings, err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "npm in a-drift: package.json changed while no matching lockfile changed") {
		t.Fatalf("expected earlier a-drift finding, got %#v", warnings)
	}
	if strings.Contains(warnings[0], "b-mixed") {
		t.Fatalf("expected replay of b-mixed to stay behind older buffered findings, got %#v", warnings)
	}
}

func TestDetectLockfileDriftDetailedGitPreservesReplayOversizedErrorOnLaterCancellation(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "a-replay-oversized", pyprojectManifestName)
	writeFile(t, path, "[build-system]\nrequires = [\"setuptools\"]\n")
	writeFile(t, filepath.Join(repo, "a-replay-oversized", poetryLockName), "# lock\n")
	writeFile(t, filepath.Join(repo, "b-replay-canceled", pyprojectManifestName), "[build-system]\nrequires = [\"setuptools\"]\n")
	writeFile(t, filepath.Join(repo, "b-replay-canceled", poetryLockName), "# lock\n")
	initGitRepo(t, repo)

	baseCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	defaultIO := defaultLockfileManifestIO()
	replayReads := 0
	ctx := withLockfileManifestIO(baseCtx, lockfileManifestIO{
		readFileUnder: defaultIO.readFileUnder,
		readFileUnderLimit: func(rootDir, targetPath string, limit int64) ([]byte, error) {
			if filepath.Clean(targetPath) == filepath.Clean(path) {
				replayReads++
				if replayReads == 2 {
					cancel()
					return nil, safeio.ErrFileTooLarge
				}
			}
			return defaultIO.readFileUnderLimit(rootDir, targetPath, limit)
		},
	})

	result := detectLockfileDriftDetailed(ctx, repo, false, featureflags.Set{})
	if !errors.Is(result.err, safeio.ErrFileTooLarge) || !errors.Is(result.err, context.Canceled) {
		t.Fatalf("expected replay oversized-manifest and cancellation errors, got findings=%#v warnings=%#v err=%v", result.findings, result.orderedWarnings, result.err)
	}
	if replayReads != 2 {
		t.Fatalf("expected first replay manifest to be read during prepare and replay, got %d reads", replayReads)
	}
	if len(result.findings) != 0 {
		t.Fatalf("expected cancellation before later replay findings, got %#v", result.findings)
	}
	if len(result.orderedWarnings) != 1 {
		t.Fatalf("expected preserved oversized warning before cancellation, got %#v", result.orderedWarnings)
	}
	if !strings.Contains(result.orderedWarnings[0], "unable to safely inspect manifest during lockfile drift analysis") {
		t.Fatalf("expected oversized-manifest warning to be preserved, got %#v", result.orderedWarnings)
	}
	if leaves := countMatchingErrorLeaves(result.err, safeio.ErrFileTooLarge); leaves != 1 {
		t.Fatalf("expected one oversized-manifest error leaf, got %d in %v", leaves, result.err)
	}
}

func TestDetectLockfileDriftDetailedGitContinuesAfterReplayOversizedManifestError(t *testing.T) {
	repo := t.TempDir()
	replayManifest := filepath.Join(repo, "a-replay-growth", pyprojectManifestName)
	writeFile(t, replayManifest, "[tool.poetry]\nname = \"demo\"\n")
	writeFile(t, filepath.Join(repo, "z-drift", manifestFileName), demoPackageJSON)
	writeFile(t, filepath.Join(repo, "z-drift", lockfileName), "{}\n")
	initGitRepo(t, repo)
	writeFile(t, filepath.Join(repo, "z-drift", manifestFileName), demoPackageJSONUpdated)

	defaultIO := defaultLockfileManifestIO()
	replayReads := 0
	ctx := withLockfileManifestIO(context.Background(), lockfileManifestIO{
		readFileUnder: defaultIO.readFileUnder,
		readFileUnderLimit: func(rootDir, targetPath string, limit int64) ([]byte, error) {
			if filepath.Clean(targetPath) == filepath.Clean(replayManifest) {
				replayReads++
				if replayReads == 2 {
					return nil, fmt.Errorf("replay manifest grew: %w", safeio.ErrFileTooLarge)
				}
			}
			return defaultIO.readFileUnderLimit(rootDir, targetPath, limit)
		},
	})

	result := detectLockfileDriftDetailed(ctx, repo, false, featureflags.Set{})
	if !errors.Is(result.err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected replay oversized-manifest error to be retained, got findings=%#v warnings=%#v err=%v", result.findings, result.orderedWarnings, result.err)
	}
	if replayReads != 2 {
		t.Fatalf("expected manifest to be read during prepare and replay, got %d reads", replayReads)
	}
	if len(result.findings) != 1 || !strings.Contains(result.findings[0], "z-drift: package.json changed while no matching lockfile changed") {
		t.Fatalf("expected later replay finding to be retained, got %#v", result.findings)
	}
	if len(result.orderedWarnings) != 2 {
		t.Fatalf("expected replay oversized warning plus later finding, got %#v", result.orderedWarnings)
	}
	if !strings.Contains(result.orderedWarnings[0], "unable to safely inspect manifest during lockfile drift analysis") {
		t.Fatalf("expected deterministic oversized-manifest warning first, got %#v", result.orderedWarnings)
	}
	if !strings.Contains(result.orderedWarnings[1], "z-drift: package.json changed while no matching lockfile changed") {
		t.Fatalf("expected later finding after replay oversized warning, got %#v", result.orderedWarnings)
	}
}

func TestEvaluateLockfileDriftPolicyWarnPropagatesCrossPhaseOversizedAndPermissionErrors(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "a-oversized", pyprojectManifestName), oversizedManifestBody("[tool.poetry]\nname = \"demo\"\n", "# filler\n", 8))
	writeFile(t, filepath.Join(repo, "a-oversized", poetryLockName), "# lock\n")
	manifest := filepath.Join(repo, pyprojectManifestName)
	writeFile(t, manifest, "[tool.poetry]\nname = \"demo\"\n")
	writeFile(t, filepath.Join(repo, poetryLockName), "# lock\n")
	ctx := withLockfileManifestReadError(context.Background(), manifest, fs.ErrPermission)

	original := collectLockfileGitContextFn
	collectLockfileGitContextFn = func(context.Context, string, []lockfileRule) (lockfileGitContext, error) {
		return lockfileGitContext{}, fmt.Errorf("collect manifest candidates: %w", safeio.ErrFileTooLarge)
	}
	t.Cleanup(func() { collectLockfileGitContextFn = original })

	warnings, err := evaluateLockfileDriftPolicy(ctx, repo, "warn")
	if !errors.Is(err, safeio.ErrFileTooLarge) || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("expected size and permission errors to remain fatal, got warnings=%#v err=%v", warnings, err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected fatal errors to suppress warnings, got %#v", warnings)
	}
}

func TestEvaluateLockfileDriftPolicyWarnPropagatesCrossDirectoryOversizedAndPermissionErrors(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "a-oversized", pyprojectManifestName), oversizedManifestBody("[tool.poetry]\nname = \"demo\"\n", "# filler\n", 8))
	writeFile(t, filepath.Join(repo, "a-oversized", poetryLockName), "# lock\n")
	permissionManifest := filepath.Join(repo, "z-permission", pyprojectManifestName)
	writeFile(t, permissionManifest, "[tool.poetry]\nname = \"demo\"\n")
	writeFile(t, filepath.Join(repo, "z-permission", poetryLockName), "# lock\n")
	ctx := withLockfileManifestReadError(context.Background(), permissionManifest, fs.ErrPermission)

	warnings, err := evaluateLockfileDriftPolicy(ctx, repo, "warn")
	if !errors.Is(err, safeio.ErrFileTooLarge) || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("expected size and permission errors to remain fatal across directories, got warnings=%#v err=%v", warnings, err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected fatal errors to suppress warnings, got %#v", warnings)
	}
}

func TestEvaluateLockfileDriftPolicyWarnGitCandidateCollectionJoinsOversizedAndPermissionErrors(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "a-oversized", pyprojectManifestName), oversizedManifestBody("[tool.poetry]\nname = \"demo\"\n", "# filler\n", 8))
	writeFile(t, filepath.Join(repo, "a-oversized", poetryLockName), "# lock\n")
	permissionManifest := filepath.Join(repo, "z-permission", pyprojectManifestName)
	writeFile(t, permissionManifest, "[tool.poetry]\nname = \"demo\"\n")
	writeFile(t, filepath.Join(repo, "z-permission", poetryLockName), "# lock\n")
	initGitRepo(t, repo)
	ctx := withLockfileManifestReadError(context.Background(), permissionManifest, fs.ErrPermission)

	warnings, err := evaluateLockfileDriftPolicy(ctx, repo, "warn")
	if !errors.Is(err, safeio.ErrFileTooLarge) || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("expected Git candidate collection to retain size and permission errors, got warnings=%#v err=%v", warnings, err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected fatal Git candidate collection errors to suppress warnings, got %#v", warnings)
	}
}

func TestDetectLockfileDriftStopOnFirstPropagatesFinalBatchEvaluationError(t *testing.T) {
	repo := t.TempDir()
	manifest := filepath.Join(repo, pyprojectManifestName)
	writeFile(t, manifest, "[tool.poetry]\nname = \"demo\"\n")
	writeFile(t, filepath.Join(repo, poetryLockName), "# lock\n")
	initGitRepo(t, repo)
	writeFile(t, manifest, "[tool.poetry]\nname = \"demo\"\nversion = \"0.2.0\"\n")
	ctx := withLockfileManifestReadError(context.Background(), manifest, fs.ErrPermission)

	_, err := detectLockfileDrift(ctx, repo, true)
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("expected final batch evaluation error, got %v", err)
	}
}

func TestDetectLockfileDriftPythonMatcherReadError(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, pyprojectManifestName), "[tool.poetry]\nname = \"demo\"\nversion = \"0.1.0\"\n")
	writeFile(t, filepath.Join(repo, poetryLockName), "metadata = {}\n")
	initGitRepo(t, repo)
	manifestPath := filepath.Join(repo, pyprojectManifestName)
	ctx := withLockfileManifestReadError(context.Background(), manifestPath, fs.ErrPermission)

	for _, stopOnFirst := range []bool{false, true} {
		_, err := detectLockfileDrift(ctx, repo, stopOnFirst)
		if err == nil {
			t.Fatalf("expected read error with stopOnFirst=%v", stopOnFirst)
		}
		if !strings.Contains(err.Error(), "read pyproject.toml for tool.poetry lockfile drift detection") {
			t.Fatalf("expected matcher read error context with stopOnFirst=%v, got %v", stopOnFirst, err)
		}
	}
}

func TestDetectLockfileDriftSkipsPresenceOnlyManifestReads(t *testing.T) {
	repo := t.TempDir()
	manifestPath := filepath.Join(repo, manifestFileName)
	writeFile(t, manifestPath, "{}\n")
	ctx := withLockfileManifestReadError(context.Background(), manifestPath, fs.ErrPermission)

	warnings, err := detectLockfileDrift(ctx, repo, false)
	if err != nil {
		t.Fatalf(detectLockfileDriftFmt, err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], manifestFileName) || !strings.Contains(warnings[0], "package-lock.json") {
		t.Fatalf("expected missing lockfile warning for package.json without reading it, got %#v", warnings)
	}
}

func withLockfileManifestIO(ctx context.Context, io lockfileManifestIO) context.Context {
	return context.WithValue(ctx, lockfileManifestIOContextKey{}, lockfileManifestIOWithDefaults(io))
}

func withLockfileManifestReadError(ctx context.Context, path string, err error) context.Context {
	path = filepath.Clean(path)
	defaultIO := defaultLockfileManifestIO()
	testIO := lockfileManifestIO{
		readFileUnder: func(rootDir, targetPath string) ([]byte, error) {
			if filepath.Clean(targetPath) == path {
				return nil, err
			}
			return defaultIO.readFileUnder(rootDir, targetPath)
		},
		readFileUnderLimit: func(rootDir, targetPath string, limit int64) ([]byte, error) {
			if filepath.Clean(targetPath) == path {
				return nil, err
			}
			return defaultIO.readFileUnderLimit(rootDir, targetPath, limit)
		},
	}
	return withLockfileManifestIO(ctx, testIO)
}

func assertPreparedReplayStateShape(t *testing.T, typ reflect.Type, seen map[reflect.Type]struct{}) {
	t.Helper()

	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == nil {
		return
	}
	if _, ok := seen[typ]; ok {
		return
	}
	seen[typ] = struct{}{}

	if typ == reflect.TypeOf(lockfileManifestCache{}) || typ == reflect.TypeOf(cachedManifestRead{}) {
		t.Fatalf("prepared replay state must not retain manifest caches, found %v", typ)
	}
	if typ == reflect.TypeOf([]byte(nil)) {
		t.Fatalf("prepared replay state must not retain manifest bodies, found %v", typ)
	}
	if typ == reflect.TypeOf((*fs.FileInfo)(nil)).Elem() {
		t.Fatalf("prepared replay state must not retain fs.FileInfo snapshots, found %v", typ)
	}

	switch typ.Kind() {
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			assertPreparedReplayStateShape(t, typ.Field(i).Type, seen)
		}
	case reflect.Slice, reflect.Array, reflect.Pointer:
		assertPreparedReplayStateShape(t, typ.Elem(), seen)
	case reflect.Map:
		assertPreparedReplayStateShape(t, typ.Key(), seen)
		assertPreparedReplayStateShape(t, typ.Elem(), seen)
	}
}

func TestDotnetProjectLockfileIndexCoversFallbackAndCachedScopes(t *testing.T) {
	var missing *dotnetProjectLockfileIndex
	lockfiles, err := missing.lockfilesUnder(".")
	if err != nil || lockfiles != nil {
		t.Fatalf("expected nil index to return no lockfiles, got %#v, %v", lockfiles, err)
	}

	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "src", dotnetProjectManifest), "<Project></Project>\n")
	writeFile(t, filepath.Join(repo, "src", dotnetLockfileName), "{}\n")
	index := &dotnetProjectLockfileIndex{repoPath: repo}
	lockfiles, err = index.lockfilesUnder(".")
	if err != nil {
		t.Fatalf("read fallback index: %v", err)
	}
	if len(lockfiles) != 1 || lockfiles[0].name != "src/"+dotnetLockfileName {
		t.Fatalf("expected fallback lockfile, got %#v", lockfiles)
	}

	scoped := &dotnetProjectLockfileIndex{
		repoPath: repo,
		scoped:   true,
		scopedLockfilesByScope: map[string][]presentLockfile{
			"src": {{name: dotnetLockfileName}},
		},
	}
	lockfiles, err = scoped.lockfilesUnder("src")
	if err != nil {
		t.Fatalf("read cached scoped lockfiles: %v", err)
	}
	if len(lockfiles) != 1 || lockfiles[0].name != dotnetLockfileName {
		t.Fatalf("expected cached scoped lockfile, got %#v", lockfiles)
	}
}

func TestFindDotnetProjectLockfilesContextStopsBeforeWalk(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := findDotnetProjectLockfilesContext(ctx, t.TempDir())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled lockfile walk, got %v", err)
	}
}

func TestDotnetProjectLockfileIndexPropagatesScopedDiscoveryError(t *testing.T) {
	wantErr := errors.New("lockfile discovery failed")
	index := &dotnetProjectLockfileIndex{
		repoPath: t.TempDir(),
		scoped:   true,
		findLockfiles: func(string) ([]presentLockfile, error) {
			return nil, wantErr
		},
	}
	_, err := index.lockfilesUnder("nested")
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected scoped discovery error, got %v", err)
	}
}

func TestDotnetProjectLockfileIndexDoesNotUseSiblingCachedScope(t *testing.T) {
	index := &dotnetProjectLockfileIndex{
		scopedLockfilesByScope: map[string][]presentLockfile{
			"alpha": {{name: "src/" + dotnetLockfileName}},
		},
	}
	if lockfiles, ok := index.lockfilesFromCachedAncestor("alpha-other"); ok || lockfiles != nil {
		t.Fatalf("expected sibling scope to miss ancestor cache, got %#v", lockfiles)
	}
}

func TestFindDotnetProjectLockfilesContextAcceptsNilContext(t *testing.T) {
	lockfiles, err := findDotnetProjectLockfilesContext(nil, t.TempDir())
	if err != nil || len(lockfiles) != 0 {
		t.Fatalf("expected empty nil-context walk, got %#v, %v", lockfiles, err)
	}
}

func TestDotnetProjectLockfileHelpersReturnFilesystemErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := findDotnetProjectLockfilesContext(context.Background(), missing); err == nil {
		t.Fatal("expected missing scan root error")
	}
	if _, err := dirContainsDotnetProjectManifest(missing); err == nil {
		t.Fatal("expected missing project directory error")
	}
	if _, err := dotnetProjectLockfileRelativePath("\x00", missing); err == nil {
		t.Fatal("expected invalid root path error")
	}
}

func TestDirContainsDotnetProjectManifestSkipsNestedEntries(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}
	writeFile(t, filepath.Join(dir, dotnetProjectManifest), "<Project></Project>\n")
	hasProject, err := dirContainsDotnetProjectManifest(dir)
	if err != nil || !hasProject {
		t.Fatalf("expected root project after nested directory, got %v, %v", hasProject, err)
	}
}

func TestPrepareLockfileManifestChangeCandidatesBoundsNestedDistributedLockfileStorage(t *testing.T) {
	rules := []lockfileRule{mustLockfileRule(t, ".NET", dotnetCentralManifest)}
	smallAllocs, smallBytes := measurePreparedDistributedLockfileStorage(t, newCompactNestedDotnetCentralLockfileRepo(t, 64), rules)
	largeAllocs, largeBytes := measurePreparedDistributedLockfileStorage(t, newCompactNestedDotnetCentralLockfileRepo(t, 256), rules)
	t.Logf("nested distributed lockfile preparation: depth 64=%.0f allocs/%d bytes, depth 256=%.0f allocs/%d bytes", smallAllocs, smallBytes, largeAllocs, largeBytes)
	if largeAllocs > smallAllocs*8 {
		t.Fatalf("expected bounded nested distributed lockfile allocations, got depth 64=%.0f depth 256=%.0f", smallAllocs, largeAllocs)
	}
	if largeBytes > smallBytes*8+4<<20 {
		t.Fatalf("expected bounded nested distributed lockfile bytes, got depth 64=%d depth 256=%d", smallBytes, largeBytes)
	}
}

func newCompactNestedDotnetCentralLockfileRepo(t *testing.T, depth int) string {
	t.Helper()
	repo := t.TempDir()
	dir := repo
	manifest := "<Project><ItemGroup><PackageVersion Include=\"Newtonsoft.Json\" Version=\"13.0.3\" /></ItemGroup></Project>\n"
	for range depth {
		writeFile(t, filepath.Join(dir, dotnetCentralManifest), manifest)
		writeFile(t, filepath.Join(dir, "src", dotnetProjectManifest), "<Project></Project>\n")
		writeFile(t, filepath.Join(dir, "src", dotnetLockfileName), "{}\n")
		dir = filepath.Join(dir, "n")
	}
	return repo
}

func measurePreparedDistributedLockfileStorage(t *testing.T, repo string, rules []lockfileRule) (float64, uint64) {
	t.Helper()
	allocs := testing.AllocsPerRun(3, func() {
		prepared, _, err := prepareLockfileManifestChangeCandidates(context.Background(), repo, rules)
		if err != nil {
			t.Fatalf("prepare lockfile manifest changes: %v", err)
		}
		runtime.KeepAlive(prepared)
	})
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	prepared, _, err := prepareLockfileManifestChangeCandidates(context.Background(), repo, rules)
	if err != nil {
		t.Fatalf("prepare lockfile manifest changes: %v", err)
	}
	runtime.KeepAlive(prepared)
	runtime.ReadMemStats(&after)
	return allocs, after.TotalAlloc - before.TotalAlloc
}

func TestPreparedDistributedLockfileRangesReuseRepositoryEntries(t *testing.T) {
	index := &dotnetProjectLockfileIndex{
		repoPath: ".",
		lockfiles: []presentLockfile{
			{name: "alpha/src/" + dotnetLockfileName},
			{name: "beta/src/" + dotnetLockfileName},
		},
		initialized: true,
	}
	rootRange, err := index.lockfileRangeUnder(".")
	if err != nil {
		t.Fatalf("root lockfile range: %v", err)
	}
	alphaRange, err := index.lockfileRangeUnder("alpha")
	if err != nil {
		t.Fatalf("alpha lockfile range: %v", err)
	}
	emptyRange, err := index.lockfileRangeUnder("missing")
	if err != nil {
		t.Fatalf("missing lockfile range: %v", err)
	}
	if rootRange.start != 0 || rootRange.end != 2 || alphaRange.start != 0 || alphaRange.end != 1 || emptyRange.start != emptyRange.end {
		t.Fatalf("unexpected ranges: root=%#v alpha=%#v empty=%#v", rootRange, alphaRange, emptyRange)
	}

	prepared := &lockfilePreparedScan{dirs: []lockfilePreparedDir{{rules: []lockfilePreparedRule{
		{manifestChange: &lockfilePreparedManifestChange{distributed: &rootRange}},
		{manifestChange: &lockfilePreparedManifestChange{distributed: &alphaRange}},
	}}}}
	candidates := appendPreparedDistributedLockfileCandidates([]string{"Directory.Packages.props"}, map[string]struct{}{"Directory.Packages.props": {}}, prepared)
	if want := []string{"Directory.Packages.props", "alpha/src/" + dotnetLockfileName, "beta/src/" + dotnetLockfileName}; !reflect.DeepEqual(candidates, want) {
		t.Fatalf("expected merged distributed candidates %#v, got %#v", want, candidates)
	}

	change := lockfilePreparedManifestChange{distributed: &alphaRange}
	if preparedManifestChangeHasChangedLockfile(change, map[string]struct{}{}, nil) {
		t.Fatal("expected unchanged alpha lockfile range")
	}
	changedFiles := map[string]struct{}{"alpha/src/" + dotnetLockfileName: {}}
	prefixes, err := preparedDistributedLockfileChangePrefixes(context.Background(), prepared, changedFiles)
	if err != nil {
		t.Fatalf("build distributed change prefixes: %v", err)
	}
	if !preparedManifestChangeHasChangedLockfile(change, changedFiles, prefixes) {
		t.Fatal("expected changed distributed lockfile to suppress manifest warning")
	}
	manifestChange := lockfilePreparedManifestChange{
		rule:        mustLockfileRule(t, ".NET", dotnetCentralManifest),
		relDir:      "alpha",
		manifests:   []lockfilePreparedManifestRef{{name: dotnetCentralManifest, relPath: "alpha/" + dotnetCentralManifest}},
		distributed: &alphaRange,
	}
	finding, found := evaluatePreparedManifestChangeWithChanges(manifestChange, lockfileGitContext{hasGitContext: true, changedFiles: map[string]struct{}{"alpha/" + dotnetCentralManifest: {}}}, nil)
	if !found || finding.lockfiles != nil {
		t.Fatalf("expected distributed manifest finding without retained lockfiles, got %#v found=%v", finding, found)
	}
	_, found = evaluatePreparedManifestChangeWithChanges(manifestChange, lockfileGitContext{hasGitContext: true, changedFiles: map[string]struct{}{"alpha/" + dotnetCentralManifest: {}, "alpha/src/" + dotnetLockfileName: {}}}, nil)
	if found {
		t.Fatal("expected changed distributed lockfile to suppress manifest finding")
	}
}

func TestPrepareLockfileRuleHandlesDistributedLockfileRangeOutcomes(t *testing.T) {
	repo := t.TempDir()
	manifestPath := filepath.Join(repo, dotnetCentralManifest)
	writeFile(t, manifestPath, "<Project></Project>\n")
	manifestInfo, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatalf("stat central manifest: %v", err)
	}
	rule := mustLockfileRule(t, ".NET", dotnetCentralManifest)

	t.Run("discovery error", func(t *testing.T) {
		snapshot := lockfileDirSnapshot{
			repoPath: repo, path: repo, relDir: ".", files: map[string]fs.FileInfo{dotnetCentralManifest: manifestInfo},
			dotnetProjectLockfiles: &dotnetProjectLockfileIndex{repoPath: repo, findLockfiles: func(string) ([]presentLockfile, error) {
				return nil, errors.New("distributed discovery failed")
			}},
		}
		_, _, gotErr := prepareLockfileRule(snapshot, rule, newLockfileManifestCache(snapshot), nil)
		if gotErr == nil || !strings.Contains(gotErr.Error(), "distributed discovery failed") {
			t.Fatalf("expected distributed discovery error, got %v", gotErr)
		}
	})

	t.Run("manifest matcher error", func(t *testing.T) {
		snapshot := lockfileDirSnapshot{
			repoPath: repo, path: repo, relDir: ".", files: map[string]fs.FileInfo{dotnetCentralManifest: manifestInfo},
			dotnetProjectLockfiles: &dotnetProjectLockfileIndex{repoPath: repo, lockfiles: []presentLockfile{{name: "src/" + dotnetLockfileName}}, initialized: true},
		}
		matcherErr := errors.New("matcher failed")
		failing := rule
		failing.manifestMatcher = func(string, string) (bool, error) { return false, matcherErr }
		_, _, gotErr := prepareLockfileRule(snapshot, failing, newLockfileManifestCache(snapshot), nil)
		if !errors.Is(gotErr, matcherErr) {
			t.Fatalf("expected matcher error, got %v", gotErr)
		}
	})

	t.Run("nonmatching manifest", func(t *testing.T) {
		snapshot := lockfileDirSnapshot{
			repoPath: repo, path: repo, relDir: ".", files: map[string]fs.FileInfo{dotnetCentralManifest: manifestInfo},
			dotnetProjectLockfiles: &dotnetProjectLockfileIndex{repoPath: repo, lockfiles: []presentLockfile{{name: "src/" + dotnetLockfileName}}, initialized: true},
		}
		nonmatching := rule
		nonmatching.manifestMatcher = func(string, string) (bool, error) { return false, nil }
		prepared, candidates, gotErr := prepareLockfileRule(snapshot, nonmatching, newLockfileManifestCache(snapshot), nil)
		if gotErr != nil || prepared.manifestChange != nil || len(candidates) != 0 {
			t.Fatalf("expected nonmatching distributed manifest to be omitted, got %#v %#v %v", prepared, candidates, gotErr)
		}
	})

	if got := appendPreparedDistributedLockfileCandidates(nil, map[string]struct{}{}, nil); got != nil {
		t.Fatalf("expected nil preparation to leave candidates nil, got %#v", got)
	}
	if got, err := (*dotnetProjectLockfileIndex)(nil).lockfileRangeUnder("."); err != nil || got.index != nil {
		t.Fatalf("expected nil index range, got %#v %v", got, err)
	}
}

func TestPrepareLockfileRuleRetainsMissingDistributedLockfileWarning(t *testing.T) {
	repo := t.TempDir()
	manifestPath := filepath.Join(repo, dotnetCentralManifest)
	writeFile(t, manifestPath, "<Project></Project>\n")
	manifestInfo, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatalf("stat central manifest: %v", err)
	}
	snapshot := lockfileDirSnapshot{
		repoPath: repo, path: repo, relDir: ".", files: map[string]fs.FileInfo{dotnetCentralManifest: manifestInfo},
		dotnetProjectLockfiles: &dotnetProjectLockfileIndex{repoPath: repo, lockfiles: nil, initialized: true},
	}
	prepared, candidates, err := prepareLockfileRule(snapshot, mustLockfileRule(t, ".NET", dotnetCentralManifest), newLockfileManifestCache(snapshot), nil)
	if err != nil || prepared.replay == nil || len(candidates) != 0 {
		t.Fatalf("expected missing distributed lockfile replay, got %#v %#v %v", prepared, candidates, err)
	}
}

type cancelAfterFirstCheckContext struct{ checks int }

func (c *cancelAfterFirstCheckContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *cancelAfterFirstCheckContext) Done() <-chan struct{}       { return nil }
func (c *cancelAfterFirstCheckContext) Value(any) any               { return nil }
func (c *cancelAfterFirstCheckContext) Err() error {
	c.checks++
	if c.checks > 1 {
		return context.Canceled
	}
	return nil
}

func TestPreparedDistributedLockfileChangePrefixesRespectCancellation(t *testing.T) {
	index := &dotnetProjectLockfileIndex{lockfiles: []presentLockfile{{name: "src/one/" + dotnetLockfileName}, {name: "src/two/" + dotnetLockfileName}}}
	rangeValue := dotnetProjectLockfileRange{index: index, end: 2}
	prepared := &lockfilePreparedScan{dirs: []lockfilePreparedDir{{rules: []lockfilePreparedRule{{manifestChange: &lockfilePreparedManifestChange{distributed: &rangeValue}}}}}}
	if _, err := preparedDistributedLockfileChangePrefixes(nil, prepared, map[string]struct{}{index.lockfiles[0].name: {}}); err != nil {
		t.Fatalf("expected nil context to use background context, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := preparedDistributedLockfileChangePrefixes(ctx, prepared, map[string]struct{}{index.lockfiles[0].name: {}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled prefix preparation, got %v", err)
	}
	if _, err := distributedLockfileChangePrefix(nil, index, map[string]struct{}{}); err != nil {
		t.Fatalf("expected nil context prefix traversal to succeed, got %v", err)
	}
	if _, err := distributedLockfileChangePrefix(&cancelAfterFirstCheckContext{}, index, map[string]struct{}{index.lockfiles[0].name: {}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation during prefix traversal, got %v", err)
	}
	if _, err := preparedDistributedLockfileChangePrefixes(&cancelAfterFirstCheckContext{}, prepared, map[string]struct{}{index.lockfiles[0].name: {}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected prefix traversal error to propagate, got %v", err)
	}
	result := scanPreparedLockfileDrift(ctx, lockfileGitContext{preparedScan: prepared, changedFiles: map[string]struct{}{index.lockfiles[0].name: {}}}, nil)
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("expected canceled prepared scan, got %v", result.err)
	}
	if ok := (&lockfileDriftResult{}).appendPreparedRule(lockfilePreparedDir{}, lockfilePreparedRule{manifestChange: &lockfilePreparedManifestChange{manifests: []lockfilePreparedManifestRef{{name: dotnetCentralManifest, relPath: dotnetCentralManifest}}}}, lockfileGitContext{hasGitContext: true}, newLockfileManifestCache(lockfileDirSnapshot{}), nil); !ok {
		t.Fatal("expected unchanged prepared rule to continue")
	}
}

func TestDistributedLockfilePreparationPreservesDiscoveryAndRecoverableErrors(t *testing.T) {
	if io := lockfileManifestIOFromContext(nil); io.readFileUnder == nil || io.readFileUnderLimit == nil {
		t.Fatalf("expected default manifest IO, got %#v", io)
	}
	repo := t.TempDir()
	manifestPath := filepath.Join(repo, dotnetCentralManifest)
	writeFile(t, manifestPath, "<Project></Project>\n")
	info, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := lockfileDirSnapshot{repoPath: repo, path: repo, relDir: ".", files: map[string]fs.FileInfo{dotnetCentralManifest: info}, dotnetProjectLockfiles: &dotnetProjectLockfileIndex{repoPath: repo, findLockfiles: func(string) ([]presentLockfile, error) { return nil, errors.New("walk failed") }}}
	rule := mustLockfileRule(t, ".NET", dotnetCentralManifest)
	if _, err := lockfileManifestChangeCandidatePathsForRule(snapshot, rule, newLockfileManifestCache(snapshot)); err == nil {
		t.Fatal("expected distributed candidate discovery error")
	}
	snapshot.dotnetProjectLockfiles = &dotnetProjectLockfileIndex{repoPath: repo, lockfiles: []presentLockfile{{name: "src/" + dotnetLockfileName}}, initialized: true}
	recoverable := rule
	recoverable.manifestMatcher = func(string, string) (bool, error) { return false, safeio.ErrFileTooLarge }
	prepared, _, err := prepareLockfileRule(snapshot, recoverable, newLockfileManifestCache(snapshot), &lockfileManifestReadErrors{})
	if err != nil || prepared.manifestReadErr == nil {
		t.Fatalf("expected recoverable manifest preparation, got %#v %v", prepared, err)
	}
}
