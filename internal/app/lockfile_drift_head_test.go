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
	"strings"
	"testing"

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
