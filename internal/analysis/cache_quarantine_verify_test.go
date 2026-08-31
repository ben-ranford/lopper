package analysis

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestConditionallyRemoveAnalysisCacheChildBranches(t *testing.T) {
	t.Run("nil child info noops", testConditionallyRemoveAnalysisCacheChildNilInfo)
	t.Run("missing current child noops", testConditionallyRemoveAnalysisCacheChildMissingCurrent)
	t.Run("post-verification replacement is restored and reported", testConditionallyRemoveAnalysisCacheChildRestoresReplacementRace)
	t.Run("pre-quarantine destination race preserves both entries", testConditionallyRemoveAnalysisCacheChildPreservesPreRenameQuarantineRace)
	t.Run("lstat and rename errors are joined", testConditionallyRemoveAnalysisCacheChildJoinsLstatAndRenameErrors)
	t.Run("failed quarantine removal does not overwrite replacement", testConditionallyRemoveAnalysisCacheChildRemoveFailurePreservesReplacement)
	t.Run("replacement reservation is not removed after quarantine cleanup", testConditionallyRemoveAnalysisCacheChildPreservesSwappedReservation)
	t.Run("successful rollback removes reservation directories", testConditionallyRemoveAnalysisCacheChildCleansReservations)
	t.Run("survives a reservation close error after a verified quarantine", testConditionallyRemoveAnalysisCacheChildSurvivesReservationCloseError)
}

func testConditionallyRemoveAnalysisCacheChildSurvivesReservationCloseError(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	closeErr := errors.New("reservation close failed")
	wrappedRoot := &onceCloseErrorOnOpenRootAnalysisCacheRoot{
		Root: root,
		name: ".lopper-cache-rollback-keys-0",
		err:  closeErr,
	}

	if err := conditionallyRemoveAnalysisCacheChild(wrappedRoot, cacheKeysDirName, childInfo); err != nil {
		t.Fatalf("conditionally remove cache child despite reservation close error: %v", err)
	}
	assertAnalysisCachePathAbsent(t, childPath)
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, ".lopper-cache-rollback-keys-0"))
}

func testConditionallyRemoveAnalysisCacheChildNilInfo(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	if err := conditionallyRemoveAnalysisCacheChild(root, cacheKeysDirName, nil); err != nil {
		t.Fatalf("conditionally remove without identity: %v", err)
	}
}

func testConditionallyRemoveAnalysisCacheChildMissingCurrent(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	_, childInfo := createAnalysisCacheChild(t, repo, "created-child")

	if err := conditionallyRemoveAnalysisCacheChild(root, cacheKeysDirName, childInfo); err != nil {
		t.Fatalf("conditionally remove missing child: %v", err)
	}
}

func testConditionallyRemoveAnalysisCacheChildRestoresReplacementRace(t *testing.T) {
	repo := t.TempDir()
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	renamedChildPath := filepath.Join(repo, "created-child")
	root := openAnalysisCacheTestRoot(t, repo)
	wrappedRoot := &lstatSwapAnalysisCacheRoot{
		t:                t,
		Root:             root,
		name:             cacheKeysDirName,
		childPath:        childPath,
		renamedChildPath: renamedChildPath,
	}

	err := conditionallyRemoveAnalysisCacheChild(wrappedRoot, cacheKeysDirName, childInfo)
	if err == nil || !strings.Contains(err.Error(), "rollback target changed while quarantining") {
		t.Fatalf("expected replacement race to be reported, got %v", err)
	}
	assertAnalysisCacheSameFile(t, childPath, wrappedRoot.replacementInfo)
	assertAnalysisCacheDirExists(t, renamedChildPath)
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, ".lopper-cache-rollback-keys-0", cacheKeysDirName))
}

func testConditionallyRemoveAnalysisCacheChildPreservesPreRenameQuarantineRace(t *testing.T) {
	repo := t.TempDir()
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	root := openAnalysisCacheTestRoot(t, repo)
	quarantineName := filepath.Join(".lopper-cache-rollback-keys-0", cacheKeysDirName)
	wrappedRoot := &renameNoReplaceRaceAnalysisCacheRoot{
		Root:    root,
		t:       t,
		repo:    repo,
		oldName: cacheKeysDirName,
		newName: quarantineName,
	}

	err := conditionallyRemoveAnalysisCacheChild(wrappedRoot, cacheKeysDirName, childInfo)
	if err != nil {
		t.Fatalf("conditionally remove after occupied quarantine retry: %v", err)
	}
	assertAnalysisCachePathAbsent(t, childPath)
	assertAnalysisCacheSameFile(t, filepath.Join(repo, quarantineName), wrappedRoot.replacementInfo)
}

func testConditionallyRemoveAnalysisCacheChildJoinsLstatAndRenameErrors(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	lstatErr := errors.New("lstat child failed")
	renameErr := errors.New("rename child failed")

	err := conditionallyRemoveAnalysisCacheChild(
		&renameErrorAnalysisCacheRoot{
			Root: &lstatErrorAnalysisCacheRoot{Root: root, name: cacheKeysDirName, err: lstatErr},
			name: cacheKeysDirName,
			err:  renameErr,
		},
		cacheKeysDirName,
		childInfo,
	)
	if !errors.Is(err, lstatErr) {
		t.Fatalf("expected lstat error to be preserved, got %v", err)
	}
	if !errors.Is(err, renameErr) {
		t.Fatalf("expected rename error to be preserved, got %v", err)
	}
	assertAnalysisCacheDirExists(t, childPath)
}

func testConditionallyRemoveAnalysisCacheChildRemoveFailurePreservesReplacement(t *testing.T) {
	repo := t.TempDir()
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	root := openAnalysisCacheTestRoot(t, repo)
	removeErr := errors.New("remove quarantine failed")
	wrappedRoot := &removeQuarantineCreatesReplacementAnalysisCacheRoot{
		Root:            root,
		t:               t,
		repo:            repo,
		reservationName: ".lopper-cache-rollback-keys-0",
		name:            cacheKeysDirName,
		err:             removeErr,
	}

	err := conditionallyRemoveAnalysisCacheChild(wrappedRoot, cacheKeysDirName, childInfo)
	if !errors.Is(err, removeErr) {
		t.Fatalf("expected failed quarantine remove to be preserved, got %v", err)
	}
	assertAnalysisCacheSameFile(t, childPath, wrappedRoot.replacementInfo)
	assertAnalysisCacheDirExists(t, filepath.Join(repo, ".lopper-cache-rollback-keys-0", cacheKeysDirName))
}

func testConditionallyRemoveAnalysisCacheChildPreservesSwappedReservation(t *testing.T) {
	repo := t.TempDir()
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	root := openAnalysisCacheTestRoot(t, repo)
	reservationName := ".lopper-cache-rollback-keys-0"
	quarantineName := filepath.Join(reservationName, cacheKeysDirName)
	wrappedRoot := &removeQuarantineSwapsReservationAnalysisCacheRoot{
		Root:            root,
		t:               t,
		repo:            repo,
		quarantineName:  quarantineName,
		reservationName: reservationName,
	}

	if err := conditionallyRemoveAnalysisCacheChild(wrappedRoot, cacheKeysDirName, childInfo); err != nil {
		t.Fatalf("conditionally remove cache child: %v", err)
	}
	// The reservation is swapped out from under the rollback the moment it is
	// first pinned (renameAnalysisCacheChildIntoReservation re-verifies
	// identity immediately before the rename), so the child is never moved
	// into the replacement -- it stays exactly where it started, and neither
	// the replacement reservation nor the original (now moved aside) one is
	// touched, matching the mock's Remove Fatalf guard against unsafe
	// pathname-only removal.
	assertAnalysisCacheDirExists(t, childPath)
	assertAnalysisCacheSameFile(t, filepath.Join(repo, reservationName), wrappedRoot.replacementInfo)
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, "replacement-reservation", cacheKeysDirName))
}

func testConditionallyRemoveAnalysisCacheChildCleansReservations(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)

	for attempt := 0; attempt < 17; attempt++ {
		childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
		if err := conditionallyRemoveAnalysisCacheChild(root, cacheKeysDirName, childInfo); err != nil {
			t.Fatalf("conditionally remove cache child attempt %d: %v", attempt, err)
		}
		assertAnalysisCachePathAbsent(t, childPath)
		assertAnalysisCachePathAbsent(t, filepath.Join(repo, ".lopper-cache-rollback-keys-0"))
	}
}

func TestQuarantineAnalysisCacheChildBranches(t *testing.T) {
	t.Run("nil child info noops", testQuarantineAnalysisCacheChildNilInfo)
	t.Run("missing child during rename noops", testQuarantineAnalysisCacheChildMissing)
	t.Run("retries quarantine name collisions", testQuarantineAnalysisCacheChildRetriesNameCollisions)
	t.Run("leaves child quarantined and reports quarantine lstat failure", testQuarantineAnalysisCacheChildReportsLstatFailure)
	t.Run("retries when occupied destination reports a different rename error", testQuarantineAnalysisCacheChildRetriesOccupiedDestination)
	t.Run("does not replace occupied quarantine directory", testQuarantineAnalysisCacheChildPreservesOccupiedDirectory)
	t.Run("does not replace occupied quarantine child directory", testQuarantineAnalysisCacheChildPreservesOccupiedChildDirectory)
	t.Run("reports reserve exhaustion after repeated collisions", testQuarantineAnalysisCacheChildReportsReserveExhaustion)
	t.Run("skips quarantine for a directory populated by another initializer", testQuarantineAnalysisCacheChildSkipsNonEmptyDirectory)
	t.Run("reports reservation open failure", testQuarantineAnalysisCacheChildReportsReservationOpenFailure)
	t.Run("joins rename and reservation close errors", testQuarantineAnalysisCacheChildJoinsRenameAndCloseErrors)
}

func testQuarantineAnalysisCacheChildReportsReservationOpenFailure(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	_, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	openErr := errors.New("reservation open failed")

	quarantineName, err := quarantineAnalysisCacheChild(&openRootErrorAnalysisCacheRoot{Root: root, err: openErr}, cacheKeysDirName, childInfo)
	if !errors.Is(err, openErr) {
		t.Fatalf("expected reservation open error, got %v", err)
	}
	if quarantineName != "" {
		t.Fatalf("expected reservation open failure to skip quarantine, got %q", quarantineName)
	}
}

func testQuarantineAnalysisCacheChildJoinsRenameAndCloseErrors(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	_, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	renameErr := errors.New("rename into reservation failed")
	closeErr := errors.New("reservation close failed")
	renameFailingRoot := &renameErrorAnalysisCacheRoot{Root: root, name: cacheKeysDirName, err: renameErr}
	wrappedRoot := &onceCloseErrorOnOpenRootAnalysisCacheRoot{
		Root: renameFailingRoot,
		name: ".lopper-cache-rollback-keys-0",
		err:  closeErr,
	}

	_, err := quarantineAnalysisCacheChild(wrappedRoot, cacheKeysDirName, childInfo)
	if !errors.Is(err, renameErr) {
		t.Fatalf("expected rename error to be preserved, got %v", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected reservation close error to be preserved, got %v", err)
	}
}

func testQuarantineAnalysisCacheChildSkipsNonEmptyDirectory(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	if err := os.WriteFile(filepath.Join(childPath, "adopted-by-another-initializer"), []byte("live cache data"), 0o600); err != nil {
		t.Fatalf("populate child before rollback: %v", err)
	}

	quarantineName, err := quarantineAnalysisCacheChild(root, cacheKeysDirName, childInfo)
	if err != nil {
		t.Fatalf("quarantine populated child: %v", err)
	}
	if quarantineName != "" {
		t.Fatalf("expected populated child to skip quarantine, got %q", quarantineName)
	}
	assertAnalysisCacheDirExists(t, childPath)
	if _, err := os.Stat(filepath.Join(childPath, "adopted-by-another-initializer")); err != nil {
		t.Fatalf("expected other initializer's data to remain at its expected path: %v", err)
	}
}

func testQuarantineAnalysisCacheChildNilInfo(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)

	quarantineName, err := quarantineAnalysisCacheChild(root, cacheKeysDirName, nil)
	if err != nil {
		t.Fatalf("quarantine nil child info: %v", err)
	}
	if quarantineName != "" {
		t.Fatalf("expected nil child info to skip quarantine, got %q", quarantineName)
	}
}

func testQuarantineAnalysisCacheChildMissing(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	_, childInfo := createAnalysisCacheChild(t, repo, "created-child")

	quarantineName, err := quarantineAnalysisCacheChild(root, cacheKeysDirName, childInfo)
	if err != nil {
		t.Fatalf("quarantine missing child: %v", err)
	}
	if quarantineName != "" {
		t.Fatalf("expected missing child to skip quarantine, got %q", quarantineName)
	}
}

func testQuarantineAnalysisCacheChildRetriesNameCollisions(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	_, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)

	quarantineName, err := quarantineAnalysisCacheChild(
		&renameErrExistOnceAnalysisCacheRoot{Root: root, name: cacheKeysDirName},
		cacheKeysDirName,
		childInfo,
	)
	if err != nil {
		t.Fatalf("quarantine retry: %v", err)
	}
	assertAnalysisCacheQuarantineSuffix(t, quarantineName, "-1")
	if err := root.Remove(quarantineName); err != nil {
		t.Fatalf("remove retried quarantine: %v", err)
	}
}

func testQuarantineAnalysisCacheChildReportsLstatFailure(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	lstatErr := errors.New("quarantine lstat failed")

	quarantineName, err := quarantineAnalysisCacheChild(
		&lstatErrorAnalysisCacheRoot{Root: root, name: filepath.Join(".lopper-cache-rollback-keys-0", cacheKeysDirName), err: lstatErr},
		cacheKeysDirName,
		childInfo,
	)
	if !errors.Is(err, lstatErr) {
		t.Fatalf("expected quarantine lstat error to be reported, got %v", err)
	}
	if quarantineName != "" {
		t.Fatalf("expected failed quarantine verification not to return a quarantine name, got %q", quarantineName)
	}
	assertAnalysisCacheDirExists(t, childPath)
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, ".lopper-cache-rollback-keys-0", cacheKeysDirName))
}

func testQuarantineAnalysisCacheChildRetriesOccupiedDestination(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	_, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	if err := os.WriteFile(filepath.Join(repo, ".lopper-cache-rollback-keys-0"), []byte("occupied"), 0o600); err != nil {
		t.Fatalf("seed occupied quarantine destination: %v", err)
	}

	quarantineName, err := quarantineAnalysisCacheChild(root, cacheKeysDirName, childInfo)
	if err != nil {
		t.Fatalf("quarantine retry after occupied destination: %v", err)
	}
	assertAnalysisCacheQuarantineSuffix(t, quarantineName, "-1")
	if err := root.Remove(quarantineName); err != nil {
		t.Fatalf("remove retried quarantine: %v", err)
	}
}

func testQuarantineAnalysisCacheChildPreservesOccupiedDirectory(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	_, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	occupiedPath := filepath.Join(repo, ".lopper-cache-rollback-keys-0")
	if err := os.Mkdir(occupiedPath, 0o750); err != nil {
		t.Fatalf("seed occupied quarantine directory: %v", err)
	}
	occupiedInfo, err := os.Lstat(occupiedPath)
	if err != nil {
		t.Fatalf("stat occupied quarantine directory: %v", err)
	}

	quarantineName, err := quarantineAnalysisCacheChild(root, cacheKeysDirName, childInfo)
	if err != nil {
		t.Fatalf("quarantine retry after occupied directory: %v", err)
	}
	assertAnalysisCacheQuarantineSuffix(t, quarantineName, "-1")
	assertAnalysisCacheSameFile(t, occupiedPath, occupiedInfo)
	if err := root.Remove(quarantineName); err != nil {
		t.Fatalf("remove retried quarantine: %v", err)
	}
}

func testQuarantineAnalysisCacheChildPreservesOccupiedChildDirectory(t *testing.T) {
	repo := t.TempDir()
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	root := openAnalysisCacheTestRoot(t, repo)
	wrappedRoot := &quarantineDestinationRaceAnalysisCacheRoot{
		Root: root,
		t:    t,
		repo: repo,
		name: cacheKeysDirName,
	}

	quarantineName, err := quarantineAnalysisCacheChild(wrappedRoot, cacheKeysDirName, childInfo)
	if err == nil {
		t.Fatalf("expected occupied quarantine child directory to fail without replacement, got name=%q err=%v", quarantineName, err)
	}
	assertAnalysisCacheSameFile(t, filepath.Join(repo, ".lopper-cache-rollback-keys-0", cacheKeysDirName), wrappedRoot.replacementInfo)
	assertAnalysisCacheDirExists(t, childPath)
}

func TestReserveAnalysisCacheQuarantineCleansCreatedReservationWhenLstatFails(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	reservationName := ".lopper-cache-rollback-keys-0"
	lstatErr := errors.New("reservation lstat failed")

	_, retry, err := reserveAnalysisCacheQuarantine(
		&lstatErrorAnalysisCacheRoot{Root: root, name: reservationName, err: lstatErr},
		reservationName,
		filepath.Join(reservationName, cacheKeysDirName),
	)
	if retry {
		t.Fatal("expected reservation lstat failure not to retry")
	}
	if !errors.Is(err, lstatErr) {
		t.Fatalf("expected reservation lstat error to be preserved, got %v", err)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, reservationName))
}

func TestReserveAnalysisCacheQuarantineReturnsMkdirFailure(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	reservationName := ".lopper-cache-rollback-keys-0"
	mkdirErr := errors.New("reservation mkdir failed")

	_, retry, err := reserveAnalysisCacheQuarantine(
		&mkdirErrorAnalysisCacheRoot{Root: root, name: reservationName, err: mkdirErr},
		reservationName,
		filepath.Join(reservationName, cacheKeysDirName),
	)
	if retry {
		t.Fatal("expected mkdir failure not to retry")
	}
	if !errors.Is(err, mkdirErr) {
		t.Fatalf("expected mkdir error, got %v", err)
	}
}

func TestReserveAnalysisCacheQuarantineAbortsWhenTokenEntropyFails(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	reservationName := ".lopper-cache-rollback-keys-0"
	entropyErr := errors.New("entropy source failed")

	original := quarantineTokenEntropySource
	quarantineTokenEntropySource = func([]byte) (int, error) { return 0, entropyErr }
	t.Cleanup(func() { quarantineTokenEntropySource = original })

	_, retry, err := reserveAnalysisCacheQuarantine(
		root,
		reservationName,
		filepath.Join(reservationName, cacheKeysDirName),
	)
	if retry {
		t.Fatal("expected token entropy failure not to retry")
	}
	if !errors.Is(err, entropyErr) {
		t.Fatalf("expected token entropy error to be preserved, got %v", err)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, reservationName))
}

func TestReserveAnalysisCacheQuarantinePreservesSwappedReservationWhenLstatFails(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	reservationName := ".lopper-cache-rollback-keys-0"
	lstatErr := errors.New("reservation lstat failed")
	wrappedRoot := &reservationLstatSwapAfterOpenAnalysisCacheRoot{
		Root: root,
		t:    t,
		repo: repo,
		name: reservationName,
		err:  lstatErr,
	}

	_, retry, err := reserveAnalysisCacheQuarantine(
		wrappedRoot,
		reservationName,
		filepath.Join(reservationName, cacheKeysDirName),
	)
	if retry {
		t.Fatal("expected reservation lstat failure not to retry")
	}
	if !errors.Is(err, lstatErr) {
		t.Fatalf("expected reservation lstat error to be preserved, got %v", err)
	}
	assertAnalysisCacheSameFile(t, filepath.Join(repo, reservationName), wrappedRoot.replacementInfo)
	assertAnalysisCacheDirExists(t, filepath.Join(repo, "moved-reservation"))
}

func TestReserveAnalysisCacheQuarantineCleansCreatedReservationWhenOwnerWriteFails(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	reservationName := ".lopper-cache-rollback-keys-0"
	quarantineName := filepath.Join(reservationName, cacheKeysDirName)
	ownerName := filepath.Join(reservationName, analysisCacheQuarantineOwnerFile)
	writeErr := errors.New("owner token write failed")

	_, retry, err := reserveAnalysisCacheQuarantine(
		&writeErrorAnalysisCacheRoot{Root: root, name: ownerName, err: writeErr},
		reservationName,
		quarantineName,
	)
	if retry {
		t.Fatal("expected owner write failure not to retry")
	}
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected owner write error to be preserved, got %v", err)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, ownerName))
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, reservationName))
}

func testQuarantineAnalysisCacheChildReportsReserveExhaustion(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	_, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)

	_, err := quarantineAnalysisCacheChild(
		&renameErrExistAnalysisCacheRoot{Root: root, name: cacheKeysDirName},
		cacheKeysDirName,
		childInfo,
	)
	if err == nil || !strings.Contains(err.Error(), "unable to reserve rollback quarantine") {
		t.Fatalf("expected reserve exhaustion error, got %v", err)
	}
}

func TestRestoreMovedAnalysisCacheReplacementPreservesPreRenameTargetRace(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	reservationName := ".lopper-cache-rollback-keys-0"
	quarantineName := filepath.Join(reservationName, cacheKeysDirName)
	quarantinePath := filepath.Join(repo, quarantineName)
	if err := os.Mkdir(filepath.Join(repo, reservationName), 0o700); err != nil {
		t.Fatalf("create reservation: %v", err)
	}
	if err := os.Mkdir(quarantinePath, 0o750); err != nil {
		t.Fatalf("create quarantined replacement: %v", err)
	}
	movedInfo, err := os.Lstat(quarantinePath)
	if err != nil {
		t.Fatalf("stat quarantined replacement: %v", err)
	}
	wrappedRoot := &renameNoReplaceRaceAnalysisCacheRoot{
		Root:    root,
		t:       t,
		repo:    repo,
		oldName: quarantineName,
		newName: cacheKeysDirName,
	}
	reservationRoot, err := safeio.OpenRoot(filepath.Join(repo, reservationName))
	if err != nil {
		t.Fatalf("open reservation root: %v", err)
	}
	defer func() {
		if err := reservationRoot.Close(); err != nil {
			t.Fatalf("close reservation root: %v", err)
		}
	}()

	err = restoreMovedAnalysisCacheReplacement(
		wrappedRoot,
		reservationRoot,
		newAnalysisCacheQuarantineReservation(reservationName, quarantineName, "test-token"),
		cacheKeysDirName,
		movedInfo,
	)
	if err == nil || !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected occupied restore target to be reported, got %v", err)
	}
	assertAnalysisCacheSameFile(t, filepath.Join(repo, cacheKeysDirName), wrappedRoot.replacementInfo)
	assertAnalysisCacheSameFile(t, quarantinePath, movedInfo)
}

func TestRestoreMovedAnalysisCacheReplacementBranches(t *testing.T) {
	t.Run("nil moved info noops", func(t *testing.T) {
		repo := t.TempDir()
		root := openAnalysisCacheTestRoot(t, repo)
		if err := restoreMovedAnalysisCacheReplacement(root, root, analysisCacheQuarantineReservation{}, cacheKeysDirName, nil); err != nil {
			t.Fatalf("restore nil moved info: %v", err)
		}
	})

	t.Run("occupied target fails", func(t *testing.T) {
		repo := t.TempDir()
		root := openAnalysisCacheTestRoot(t, repo)
		_, movedInfo := createAnalysisCacheChild(t, repo, "moved-child")
		createAnalysisCacheChild(t, repo, cacheKeysDirName)

		err := restoreMovedAnalysisCacheReplacement(root, root, analysisCacheQuarantineReservation{}, cacheKeysDirName, movedInfo)
		if err == nil || !strings.Contains(err.Error(), "restore target occupied") {
			t.Fatalf("expected occupied restore target error, got %v", err)
		}
	})

	t.Run("target lstat failure is preserved", func(t *testing.T) {
		repo := t.TempDir()
		root := openAnalysisCacheTestRoot(t, repo)
		_, movedInfo := createAnalysisCacheChild(t, repo, "moved-child")
		lstatErr := errors.New("restore target lstat failed")

		wrappedRoot := &lstatErrorAnalysisCacheRoot{Root: root, name: cacheKeysDirName, err: lstatErr}
		err := restoreMovedAnalysisCacheReplacement(wrappedRoot, wrappedRoot, analysisCacheQuarantineReservation{}, cacheKeysDirName, movedInfo)
		if !errors.Is(err, lstatErr) {
			t.Fatalf("expected target lstat error, got %v", err)
		}
	})

	t.Run("changed restored identity fails", func(t *testing.T) {
		repo := t.TempDir()
		root := openAnalysisCacheTestRoot(t, repo)
		reservationName := ".lopper-cache-rollback-keys-0"
		quarantineName := filepath.Join(reservationName, cacheKeysDirName)
		quarantinePath := filepath.Join(repo, quarantineName)
		if err := os.MkdirAll(filepath.Join(repo, reservationName), 0o700); err != nil {
			t.Fatalf("create reservation: %v", err)
		}
		if err := os.Mkdir(quarantinePath, 0o750); err != nil {
			t.Fatalf("create quarantined child: %v", err)
		}
		_, differentInfo := createAnalysisCacheChild(t, repo, "different-child")
		reservationRoot := openAnalysisCacheTestRoot(t, filepath.Join(repo, reservationName))

		err := restoreMovedAnalysisCacheReplacement(root, reservationRoot, newAnalysisCacheQuarantineReservation(reservationName, quarantineName, "token"), cacheKeysDirName, differentInfo)
		if err == nil || !strings.Contains(err.Error(), "changed while restoring") {
			t.Fatalf("expected changed restored identity error, got %v", err)
		}
	})
}

func TestRestoreMovedAnalysisCacheReplacementRestoresIdentityAndOwnerToken(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	reservationName := ".lopper-cache-rollback-keys-0"
	quarantineName := filepath.Join(reservationName, cacheKeysDirName)
	quarantinePath := filepath.Join(repo, quarantineName)
	reservation := createAnalysisCacheQuarantineReservationWithOwner(t, repo, root, reservationName, quarantineName, "owned-token")
	if err := os.Mkdir(quarantinePath, 0o750); err != nil {
		t.Fatalf("create quarantined replacement: %v", err)
	}
	movedInfo, err := os.Lstat(quarantinePath)
	if err != nil {
		t.Fatalf("stat quarantined replacement: %v", err)
	}
	reservationRoot := openAnalysisCacheTestRoot(t, filepath.Join(repo, reservationName))

	if err := restoreMovedAnalysisCacheReplacement(root, reservationRoot, reservation, cacheKeysDirName, movedInfo); err != nil {
		t.Fatalf("restore moved replacement: %v", err)
	}

	assertAnalysisCacheSameFile(t, filepath.Join(repo, cacheKeysDirName), movedInfo)
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, reservation.ownerName))
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, reservationName))
}

func setupQuarantinedChildForVerification(t *testing.T) (repo string, root safeio.Root, reservation analysisCacheQuarantineReservation, reservationName string, childInfo fs.FileInfo) {
	t.Helper()
	repo = t.TempDir()
	root = openAnalysisCacheTestRoot(t, repo)
	reservationName = ".lopper-cache-rollback-keys-0"
	quarantineName := filepath.Join(reservationName, cacheKeysDirName)
	reservation = createAnalysisCacheQuarantineReservationWithOwner(t, repo, root, reservationName, quarantineName, "owned-token")
	quarantinePath := filepath.Join(repo, quarantineName)
	if err := os.Mkdir(quarantinePath, 0o750); err != nil {
		t.Fatalf("create quarantined child: %v", err)
	}
	info, err := os.Lstat(quarantinePath)
	if err != nil {
		t.Fatalf("stat quarantined child: %v", err)
	}
	return repo, root, reservation, reservationName, info
}

func TestVerifyAnalysisCacheQuarantineRestoresChildPopulatedDuringTheRace(t *testing.T) {
	repo, root, reservation, reservationName, childInfo := setupQuarantinedChildForVerification(t)

	// Simulate: the candidate was empty when last checked and got renamed
	// into quarantine, but another initializer added an entry to it in the
	// narrow window between that check and the rename landing.
	quarantinePath := filepath.Join(repo, reservation.quarantineName)
	if err := os.WriteFile(filepath.Join(quarantinePath, "adopted-by-another-initializer"), []byte("live cache data"), 0o600); err != nil {
		t.Fatalf("populate quarantined child: %v", err)
	}

	reservationRoot := openAnalysisCacheTestRoot(t, filepath.Join(repo, reservationName))
	if _, _, err := verifyAnalysisCacheQuarantine(root, reservationRoot, reservation, cacheKeysDirName, childInfo); err == nil {
		t.Fatal("expected a quarantine child populated during the race to be reported")
	}

	restoredPath := filepath.Join(repo, cacheKeysDirName)
	assertAnalysisCacheDirExists(t, restoredPath)
	if _, err := os.Stat(filepath.Join(restoredPath, "adopted-by-another-initializer")); err != nil {
		t.Fatalf("expected other initializer's data to survive the restore: %v", err)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, reservationName))
}

func TestVerifyAnalysisCacheQuarantineRestoresChildWhenEmptinessCheckFails(t *testing.T) {
	repo, root, reservation, reservationName, childInfo := setupQuarantinedChildForVerification(t)
	emptyCheckErr := errors.New("emptiness check lstat denied")
	reservationRoot := openAnalysisCacheTestRoot(t, filepath.Join(repo, reservationName))
	// The first Lstat call for cacheKeysDirName is verifyAnalysisCacheQuarantine's
	// own identity check, which must succeed; only the second (from
	// analysisCacheChildIsEmpty's re-check) should fail.
	wrappedReservationRoot := &secondLstatErrorAnalysisCacheRoot{Root: reservationRoot, name: cacheKeysDirName, err: emptyCheckErr}

	_, _, verifyErr := verifyAnalysisCacheQuarantine(root, wrappedReservationRoot, reservation, cacheKeysDirName, childInfo)
	if !errors.Is(verifyErr, emptyCheckErr) {
		t.Fatalf("expected emptiness-check error to be preserved, got %v", verifyErr)
	}

	restoredPath := filepath.Join(repo, cacheKeysDirName)
	assertAnalysisCacheDirExists(t, restoredPath)
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, reservationName))
}

func TestVerifyAnalysisCacheQuarantineUsesPinnedReservationAcrossASwap(t *testing.T) {
	repo, root, reservation, reservationName, childInfo := setupQuarantinedChildForVerification(t)
	reservationRoot := openAnalysisCacheTestRoot(t, filepath.Join(repo, reservationName))

	// Simulate: another same-user process renames the reservation away and
	// installs an empty replacement in between the rename landing and this
	// verification call -- reservationRoot is the same open handle
	// renameAnalysisCacheChildIntoReservation already pinned, so it still
	// refers to the original directory (and the quarantined child inside
	// it) by descriptor, unaffected by its name changing.
	if err := os.Rename(filepath.Join(repo, reservationName), filepath.Join(repo, "displaced-reservation")); err != nil {
		t.Fatalf("move reservation aside: %v", err)
	}
	if err := os.Mkdir(filepath.Join(repo, reservationName), 0o700); err != nil {
		t.Fatalf("create replacement reservation: %v", err)
	}

	// A path-based lookup for the quarantined child from the outer root
	// would now resolve into the empty replacement and find nothing --
	// confirming what the pre-fix code would have hit.
	if _, err := root.Lstat(reservation.quarantineName); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected a path-based lookup to now miss the quarantined child, got info err=%v", err)
	}

	verified, _, err := verifyAnalysisCacheQuarantine(root, reservationRoot, reservation, cacheKeysDirName, childInfo)
	if err != nil {
		t.Fatalf("expected verification through the pinned reservation to succeed despite the path-level swap: %v", err)
	}
	if verified.quarantineName != reservation.quarantineName {
		t.Fatalf("expected the original reservation to be confirmed, got %+v", verified)
	}
}

func TestRemoveAnalysisCacheQuarantineRestoresChildWhenRemovalFindsItNonEmpty(t *testing.T) {
	repo, root, reservation, reservationName, _ := setupQuarantinedChildForVerification(t)

	// Simulate: verifyAnalysisCacheQuarantine's post-rename emptiness check
	// already passed, but another initializer added an entry to the
	// quarantined child before this separate cleanup call runs.
	quarantinePath := filepath.Join(repo, reservation.quarantineName)
	if err := os.WriteFile(filepath.Join(quarantinePath, "adopted-by-another-initializer"), []byte("live cache data"), 0o600); err != nil {
		t.Fatalf("populate quarantined child: %v", err)
	}

	if err := removeAnalysisCacheQuarantine(root, reservation); err == nil {
		t.Fatal("expected non-empty quarantine removal to be reported")
	}

	restoredPath := filepath.Join(repo, cacheKeysDirName)
	assertAnalysisCacheDirExists(t, restoredPath)
	if _, err := os.Stat(filepath.Join(restoredPath, "adopted-by-another-initializer")); err != nil {
		t.Fatalf("expected other initializer's data to survive the restore: %v", err)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, reservationName))
}

func TestRemoveAnalysisCacheQuarantineRemovesReservationDirectoryWhenPathLstatFails(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	reservationName := ".lopper-cache-rollback-keys-0"
	quarantineName := filepath.Join(reservationName, cacheKeysDirName)
	reservation := createAnalysisCacheQuarantineReservationWithOwner(t, repo, root, reservationName, quarantineName, "owned-token")
	if err := os.Mkdir(filepath.Join(repo, quarantineName), 0o750); err != nil {
		t.Fatalf("create quarantined child: %v", err)
	}
	lstatErr := errors.New("reservation path lstat failed")

	err := removeAnalysisCacheQuarantine(
		&lstatErrorAnalysisCacheRoot{Root: root, name: reservationName, err: lstatErr},
		reservation,
	)
	if err != nil {
		t.Fatalf("remove quarantine with reservation path lstat failure: %v", err)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, reservationName))
}

func TestRemoveAnalysisCacheQuarantinePreservesRemoveAndCloseErrors(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	reservationName := ".lopper-cache-rollback-keys-0"
	quarantineName := filepath.Join(reservationName, cacheKeysDirName)
	reservation := createAnalysisCacheQuarantineReservationWithOwner(t, repo, root, reservationName, quarantineName, "owned-token")
	reservationRoot := openQuarantinedChildReservationRoot(t, repo, reservationName, quarantineName)
	removeErr := errors.New("remove owner failed")
	closeErr := errors.New("close reservation failed")

	err := removeAnalysisCacheQuarantine(
		&openNamedRootAnalysisCacheRoot{
			Root: root,
			name: reservationName,
			root: &closeErrorAnalysisCacheRoot{
				Root: &exactRemoveErrorAnalysisCacheRoot{Root: reservationRoot, name: analysisCacheQuarantineOwnerFile, err: removeErr},
				err:  closeErr,
			},
		},
		reservation,
	)
	if !errors.Is(err, removeErr) {
		t.Fatalf("expected owner remove error, got %v", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected reservation close error, got %v", err)
	}
}

func TestRemoveAnalysisCacheQuarantinePreservesCloseError(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	reservationName := ".lopper-cache-rollback-keys-0"
	quarantineName := filepath.Join(reservationName, cacheKeysDirName)
	reservation := createAnalysisCacheQuarantineReservationWithOwner(t, repo, root, reservationName, quarantineName, "owned-token")
	reservationRoot := openQuarantinedChildReservationRoot(t, repo, reservationName, quarantineName)
	closeErr := errors.New("close reservation failed")

	err := removeAnalysisCacheQuarantine(
		&openNamedRootAnalysisCacheRoot{
			Root: root,
			name: reservationName,
			root: &closeErrorAnalysisCacheRoot{Root: reservationRoot, err: closeErr},
		},
		reservation,
	)
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected reservation close error, got %v", err)
	}
}

func TestRemoveAnalysisCacheQuarantineReservationBranches(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	if err := removeAnalysisCacheQuarantineReservation(root, newAnalysisCacheQuarantineReservation(".", "keys", "token")); err != nil {
		t.Fatalf("remove root reservation marker: %v", err)
	}
	if err := removeAnalysisCacheQuarantineReservation(root, newAnalysisCacheQuarantineReservation("not-rollback", filepath.Join("not-rollback", "keys"), "token")); err != nil {
		t.Fatalf("remove non-rollback reservation: %v", err)
	}
	reservationName := ".lopper-cache-rollback-keys-0"
	reservation := newAnalysisCacheQuarantineReservation(reservationName, filepath.Join(reservationName, cacheKeysDirName), "owned-token")
	if err := os.Mkdir(filepath.Join(repo, reservationName), 0o700); err != nil {
		t.Fatalf("create reservation: %v", err)
	}
	if err := removeAnalysisCacheQuarantineReservation(root, reservation); err != nil {
		t.Fatalf("remove unowned reservation: %v", err)
	}
	if err := writeAnalysisCacheQuarantineOwner(root, reservation); err != nil {
		t.Fatalf("write owner token: %v", err)
	}
	removeErr := errors.New("remove owner failed")
	err := removeAnalysisCacheQuarantineReservation(
		&removeErrorAnalysisCacheRoot{Root: root, name: reservation.ownerName, err: removeErr},
		reservation,
	)
	if !errors.Is(err, removeErr) {
		t.Fatalf("expected owner remove error, got %v", err)
	}

	if err := os.Remove(filepath.Join(repo, reservation.ownerName)); err != nil {
		t.Fatalf("remove owner file after injected failure: %v", err)
	}
	if err := os.Rename(filepath.Join(repo, reservationName), filepath.Join(repo, "moved-reservation")); err != nil {
		t.Fatalf("move owned reservation: %v", err)
	}
	if err := os.Mkdir(filepath.Join(repo, reservationName), 0o700); err != nil {
		t.Fatalf("create replacement reservation: %v", err)
	}
	if err := writeAnalysisCacheQuarantineOwner(root, reservation); err != nil {
		t.Fatalf("write replacement owner token: %v", err)
	}
	if err := removeAnalysisCacheQuarantineReservation(root, reservation); err != nil {
		t.Fatalf("changed reservation cleanup: %v", err)
	}
	assertAnalysisCacheDirExists(t, filepath.Join(repo, reservationName))
}

func TestFinishConditionalAnalysisCacheRemovalBranches(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	lstatErr := errors.New("rollback lstat failed")
	quarantineErr := errors.New("rollback quarantine failed")

	err := finishConditionalAnalysisCacheRemoval(root, analysisCacheQuarantineReservation{}, lstatErr, quarantineErr)
	if !errors.Is(err, lstatErr) {
		t.Fatalf("expected lstat error to be joined, got %v", err)
	}
	if !errors.Is(err, quarantineErr) {
		t.Fatalf("expected quarantine error to be joined, got %v", err)
	}

	if err := finishConditionalAnalysisCacheRemoval(root, analysisCacheQuarantineReservation{}, nil, nil); err != nil {
		t.Fatalf("empty reservation cleanup: %v", err)
	}

	reservation := newAnalysisCacheQuarantineReservation(".lopper-cache-rollback-missing-0", filepath.Join(".lopper-cache-rollback-missing-0", "missing"), "token")
	if err := finishConditionalAnalysisCacheRemoval(root, reservation, lstatErr, nil); !errors.Is(err, lstatErr) {
		t.Fatalf("expected missing reservation cleanup to preserve lstat error, got %v", err)
	}
}

func TestFinishConditionalAnalysisCacheRemovalPropagatesOwnershipLossWhenTargetStillExists(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	reservationName := ".lopper-cache-rollback-keys-0"
	quarantineName := filepath.Join(reservationName, cacheKeysDirName)
	reservation := newAnalysisCacheQuarantineReservation(reservationName, quarantineName, "original-token")
	if err := os.Mkdir(filepath.Join(repo, reservationName), 0o700); err != nil {
		t.Fatalf("create reservation: %v", err)
	}
	info, err := os.Lstat(filepath.Join(repo, reservationName))
	if err != nil {
		t.Fatalf("stat reservation: %v", err)
	}
	reservation.info = info
	if err := os.WriteFile(filepath.Join(repo, reservation.ownerName), []byte("replaced-by-another-process"), 0o600); err != nil {
		t.Fatalf("write replaced owner marker: %v", err)
	}
	if err := os.Mkdir(filepath.Join(repo, quarantineName), 0o750); err != nil {
		t.Fatalf("create quarantined child: %v", err)
	}

	err = finishConditionalAnalysisCacheRemoval(root, reservation, nil, nil)
	if err == nil {
		t.Fatal("expected ownership loss with the quarantined child still present to be reported")
	}
	// rollbackCreatedAnalysisCacheChild treats any error matching
	// os.ErrNotExist from this call as "nothing left to roll back" and
	// discards it -- the underlying failure here (identity/owner mismatch,
	// not the quarantined target being gone) happens to be built from an
	// os.ErrNotExist-wrapping error internally, so it must not itself match
	// errors.Is(_, os.ErrNotExist), or that caller would silently swallow
	// exactly the failure this test exists to catch.
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ownership-loss error not to match os.ErrNotExist (callers would discard it), got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(repo, quarantineName)); err != nil {
		t.Fatalf("expected quarantined child to remain untouched, stat err=%v", err)
	}
}

func TestIgnoreAnalysisCacheOccupiedReservationCleanupBranches(t *testing.T) {
	if err := ignoreAnalysisCacheOccupiedReservationCleanup(nil); err != nil {
		t.Fatalf("nil cleanup error: %v", err)
	}
	if err := ignoreAnalysisCacheOccupiedReservationCleanup(&os.PathError{Op: "remove", Path: "dir", Err: syscall.ENOTEMPTY}); err != nil {
		t.Fatalf("expected non-empty directory cleanup error to be ignored, got %v", err)
	}
	cleanupErr := errors.New("cleanup failed")
	if err := ignoreAnalysisCacheOccupiedReservationCleanup(cleanupErr); !errors.Is(err, cleanupErr) {
		t.Fatalf("expected cleanup error to be preserved, got %v", err)
	}
}

func TestRemoveCreatedAnalysisCacheQuarantineReservationBranches(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	if err := removeCreatedAnalysisCacheQuarantineReservation(root, newAnalysisCacheQuarantineReservation(".", "keys", "token")); err != nil {
		t.Fatalf("remove created root reservation marker: %v", err)
	}
	if err := removeCreatedAnalysisCacheQuarantineReservation(root, newAnalysisCacheQuarantineReservation("not-rollback", filepath.Join("not-rollback", "keys"), "token")); err != nil {
		t.Fatalf("remove created non-rollback reservation: %v", err)
	}
	if err := removeCreatedAnalysisCacheQuarantineReservation(root, newAnalysisCacheQuarantineReservation(".lopper-cache-rollback-keys-0", filepath.Join(".lopper-cache-rollback-keys-0", cacheKeysDirName), "token")); err != nil {
		t.Fatalf("remove created reservation without identity: %v", err)
	}

	reservationName := ".lopper-cache-rollback-keys-0"
	reservation := createAnalysisCacheQuarantineReservationWithOwner(t, repo, root, reservationName, filepath.Join(reservationName, cacheKeysDirName), "owned-token")
	removeErr := errors.New("remove owner failed")
	if err := removeCreatedAnalysisCacheQuarantineReservation(&removeErrorAnalysisCacheRoot{Root: root, name: reservation.ownerName, err: removeErr}, reservation); !errors.Is(err, removeErr) {
		t.Fatalf("expected owner remove error, got %v", err)
	}
	if err := os.Remove(filepath.Join(repo, reservation.ownerName)); err != nil {
		t.Fatalf("remove owner file after injected failure: %v", err)
	}
	removeDirErr := errors.New("remove reservation directory failed")
	if err := removeCreatedAnalysisCacheQuarantineReservation(&exactRemoveErrorAnalysisCacheRoot{Root: root, name: reservation.name, err: removeDirErr}, reservation); !errors.Is(err, removeDirErr) {
		t.Fatalf("expected reservation directory remove error, got %v", err)
	}
	if err := removeCreatedAnalysisCacheQuarantineReservation(root, reservation); err != nil {
		t.Fatalf("remove created reservation: %v", err)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, reservationName))
}

func TestOpenOwnedAnalysisCacheQuarantineReservationBranches(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	reservationName := ".lopper-cache-rollback-keys-0"
	quarantineName := filepath.Join(reservationName, cacheKeysDirName)
	empty := newAnalysisCacheQuarantineReservation(reservationName, quarantineName, "")
	if _, err := openOwnedAnalysisCacheQuarantineReservation(root, empty); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected empty token reservation to be hidden, got %v", err)
	}

	reservation := createAnalysisCacheQuarantineReservationWithOwner(t, repo, root, reservationName, quarantineName, "owned-token")

	openErr := errors.New("open reservation failed")
	if opened, err := openOwnedAnalysisCacheQuarantineReservation(&openNamedRootErrorAnalysisCacheRoot{Root: root, name: reservationName, err: openErr}, reservation); !errors.Is(err, openErr) || opened != nil {
		t.Fatalf("expected reservation open error, opened=%#v err=%v", opened, err)
	}

	lstatErr := errors.New("reservation lstat failed")
	reservationRoot, err := safeio.OpenRoot(filepath.Join(repo, reservationName))
	if err != nil {
		t.Fatalf("open reservation root: %v", err)
	}
	if opened, err := openOwnedAnalysisCacheQuarantineReservation(&openNamedRootAnalysisCacheRoot{Root: root, name: reservationName, root: &lstatErrorAnalysisCacheRoot{Root: reservationRoot, name: ".", err: lstatErr}}, reservation); !errors.Is(err, lstatErr) || opened != nil {
		t.Fatalf("expected reservation lstat error, opened=%#v err=%v", opened, err)
	}

	wrongToken := reservation
	wrongToken.ownerToken = "different-token"
	if opened, err := openOwnedAnalysisCacheQuarantineReservation(root, wrongToken); !errors.Is(err, os.ErrNotExist) || opened != nil {
		t.Fatalf("expected wrong owner token to be hidden, opened=%#v err=%v", opened, err)
	}

	opened, err := openOwnedAnalysisCacheQuarantineReservation(root, reservation)
	if err != nil {
		t.Fatalf("open owned reservation: %v", err)
	}
	if err := opened.Close(); err != nil {
		t.Fatalf("close owned reservation: %v", err)
	}
}

func TestAnalysisCacheQuarantineHelperPredicates(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	if isAnalysisCacheNonEmptyDirectoryError(errors.New("directory not empty")) != true {
		t.Fatal("expected text non-empty directory error to match")
	}
	if isAnalysisCacheNonEmptyDirectoryError(errors.New("permission denied")) {
		t.Fatal("did not expect unrelated error to match")
	}
	if sameAnalysisCacheRollbackTarget(nil, nil) {
		t.Fatal("nil rollback targets should not match")
	}

	reservation := newAnalysisCacheQuarantineReservation(".lopper-cache-rollback-missing-0", filepath.Join(".lopper-cache-rollback-missing-0", "missing"), "token")
	if info := openedAnalysisCacheQuarantineReservationInfo(root, analysisCacheQuarantineReservation{}); info != nil {
		t.Fatalf("expected empty reservation name to have no info, got %#v", info)
	}
	if err := removeAnalysisCacheQuarantineReservationDirectory(root, reservation); err != nil {
		t.Fatalf("remove reservation directory without identity: %v", err)
	}
}

type readCountingAnalysisCacheRoot struct {
	safeio.Root
	name      string
	totalRead int
}

func (r *readCountingAnalysisCacheRoot) Open(name string) (safeio.File, error) {
	file, err := r.Root.Open(name)
	if err != nil || name != r.name {
		return file, err
	}
	return &readCountingAnalysisCacheFile{File: file, root: r}, nil
}

type readCountingAnalysisCacheFile struct {
	safeio.File
	root *readCountingAnalysisCacheRoot
}

func (f *readCountingAnalysisCacheFile) Read(p []byte) (int, error) {
	n, err := f.File.Read(p)
	f.root.totalRead += n
	return n, err
}

func TestAnalysisCacheQuarantineOwnerTokenMatchesBoundsReadsAndRejectsOversizedOwnerFiles(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	reservationName := ".lopper-cache-rollback-keys-0"
	ownerName := filepath.Join(reservationName, analysisCacheQuarantineOwnerFile)
	token := "owned-token"
	if err := os.Mkdir(filepath.Join(repo, reservationName), 0o700); err != nil {
		t.Fatalf("create reservation: %v", err)
	}
	oversized := token + strings.Repeat("x", 1<<20)
	if err := os.WriteFile(filepath.Join(repo, ownerName), []byte(oversized), 0o600); err != nil {
		t.Fatalf("write oversized owner file: %v", err)
	}

	counting := &readCountingAnalysisCacheRoot{Root: root, name: ownerName}
	if analysisCacheQuarantineOwnerTokenMatches(counting, ownerName, token) {
		t.Fatal("expected oversized owner file to be rejected")
	}
	if maxAllowed := len(token) + 1; counting.totalRead > maxAllowed {
		t.Fatalf("expected owner token read to be bounded to %d bytes, read %d", maxAllowed, counting.totalRead)
	}

	if err := os.WriteFile(filepath.Join(repo, ownerName), []byte(token), 0o600); err != nil {
		t.Fatalf("write valid owner file: %v", err)
	}
	if !analysisCacheQuarantineOwnerTokenMatches(root, ownerName, token) {
		t.Fatal("expected exact token match to succeed")
	}
}

func TestAnalysisCacheQuarantineDestinationExistsBranches(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	quarantineName := filepath.Join(".lopper-cache-rollback-keys-0", cacheKeysDirName)
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	if analysisCacheQuarantineDestinationExists(root, cacheKeysDirName, quarantineName, childInfo) {
		t.Fatal("expected missing quarantine destination to report absent")
	}
	if err := os.MkdirAll(filepath.Join(repo, filepath.Dir(quarantineName)), 0o700); err != nil {
		t.Fatalf("create reservation: %v", err)
	}
	if err := os.Mkdir(filepath.Join(repo, quarantineName), 0o750); err != nil {
		t.Fatalf("create quarantine destination: %v", err)
	}
	if err := os.Rename(childPath, filepath.Join(repo, "moved-child")); err != nil {
		t.Fatalf("move current child aside: %v", err)
	}
	if !analysisCacheQuarantineDestinationExists(root, cacheKeysDirName, quarantineName, childInfo) {
		t.Fatal("expected occupied quarantine with missing current child to report destination exists")
	}
	replacementPath, replacementInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	if !analysisCacheQuarantineDestinationExists(root, cacheKeysDirName, quarantineName, replacementInfo) {
		t.Fatal("expected occupied quarantine with same current child to report destination exists")
	}
	if analysisCacheQuarantineDestinationExists(root, cacheKeysDirName, quarantineName, childInfo) {
		t.Fatal("expected changed current child to report different destination race")
	}
	assertAnalysisCacheSameFile(t, replacementPath, replacementInfo)
}

func TestRollbackCreatedAnalysisCacheChildPreservesRemoveFailureAlongsideCloseError(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	child, err := safeio.OpenRoot(childPath)
	if err != nil {
		t.Fatalf("open child: %v", err)
	}
	closeErr := errors.New("close child failed")
	removeErr := errors.New("remove child failed")

	err = rollbackCreatedAnalysisCacheChild(
		&openReservationRemoveChildErrorAnalysisCacheRoot{
			Root:            root,
			reservationName: ".lopper-cache-rollback-keys-0",
			childName:       cacheKeysDirName,
			err:             removeErr,
		},
		cacheKeysDirName,
		&closeErrorAnalysisCacheRoot{Root: child, err: closeErr},
		childInfo,
		true,
	)
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected close error to be preserved, got %v", err)
	}
	if !errors.Is(err, removeErr) {
		t.Fatalf("expected remove error to be preserved, got %v", err)
	}
	assertAnalysisCachePathAbsent(t, childPath)
	quarantinePath := filepath.Join(repo, ".lopper-cache-rollback-keys-0", cacheKeysDirName)
	if info, statErr := os.Stat(quarantinePath); statErr != nil || !info.IsDir() {
		t.Fatalf("expected child to remain quarantined after failed rollback, info=%#v err=%v", info, statErr)
	}
}
