package analysis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/runtime"
	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	cacheDirName          = ".lopper-cache"
	cacheKeysDirName      = "keys"
	cacheObjectsDirName   = "objects"
	cacheTestGoModName    = "go.mod"
	cacheTestGoModContent = "module demo\n"
	cacheMissingFileName  = "missing.txt"
)

type analysisCacheLookupCase struct {
	name         string
	setup        func(*testing.T)
	wantReason   string
	wantHit      bool
	wantRepoPath string
}

type openRootErrorAnalysisCacheRoot struct {
	safeio.Root
	err error
}

func (r *openRootErrorAnalysisCacheRoot) OpenRoot(string) (safeio.Root, error) {
	return nil, r.err
}

func (r *openRootErrorAnalysisCacheRoot) RenameNoReplace(oldName, newName string) error {
	return safeio.RenameNoReplace(r.Root, oldName, newName)
}

type postCreateLstatErrorAnalysisCacheRoot struct {
	safeio.Root
	name    string
	err     error
	created bool
}

func (r *postCreateLstatErrorAnalysisCacheRoot) Mkdir(name string, perm os.FileMode) error {
	if err := r.Root.Mkdir(name, perm); err != nil {
		return err
	}
	if name == r.name {
		r.created = true
	}
	return nil
}

func (r *postCreateLstatErrorAnalysisCacheRoot) Lstat(name string) (fs.FileInfo, error) {
	if r.created && name == r.name {
		return nil, r.err
	}
	return r.Root.Lstat(name)
}

func (r *postCreateLstatErrorAnalysisCacheRoot) RenameNoReplace(oldName, newName string) error {
	return safeio.RenameNoReplace(r.Root, oldName, newName)
}

type secondPostCreateLstatErrorAnalysisCacheRoot struct {
	safeio.Root
	name             string
	err              error
	created          bool
	postCreateLstats int
}

func (r *secondPostCreateLstatErrorAnalysisCacheRoot) Mkdir(name string, perm os.FileMode) error {
	if err := r.Root.Mkdir(name, perm); err != nil {
		return err
	}
	if name == r.name {
		r.created = true
	}
	return nil
}

func (r *secondPostCreateLstatErrorAnalysisCacheRoot) Lstat(name string) (fs.FileInfo, error) {
	if r.created && name == r.name {
		r.postCreateLstats++
		if r.postCreateLstats == 2 {
			return nil, r.err
		}
	}
	return r.Root.Lstat(name)
}

func (r *secondPostCreateLstatErrorAnalysisCacheRoot) RenameNoReplace(oldName, newName string) error {
	return safeio.RenameNoReplace(r.Root, oldName, newName)
}

type retargetedPostCreateAnalysisCacheRoot struct {
	safeio.Root
	name       string
	retargeted bool
}

func (r *retargetedPostCreateAnalysisCacheRoot) Mkdir(name string, perm os.FileMode) error {
	if err := r.Root.Mkdir(name, perm); err != nil {
		return err
	}
	if name == r.name {
		r.retargeted = true
	}
	return nil
}

func (r *retargetedPostCreateAnalysisCacheRoot) Lstat(name string) (fs.FileInfo, error) {
	if r.retargeted && name == r.name {
		return nil, os.ErrNotExist
	}
	return r.Root.Lstat(name)
}

func (r *retargetedPostCreateAnalysisCacheRoot) RenameNoReplace(oldName, newName string) error {
	return safeio.RenameNoReplace(r.Root, oldName, newName)
}

type writeErrorAnalysisCacheRoot struct {
	safeio.Root
	name string
	err  error
}

func (r *writeErrorAnalysisCacheRoot) OpenFile(name string, flag int, perm os.FileMode) (safeio.File, error) {
	file, err := r.Root.OpenFile(name, flag, perm)
	if err != nil || name != r.name {
		return file, err
	}
	return &writeErrorAnalysisCacheFile{File: file, err: r.err}, nil
}

func (r *writeErrorAnalysisCacheRoot) RenameNoReplace(oldName, newName string) error {
	return safeio.RenameNoReplace(r.Root, oldName, newName)
}

type writeErrorAnalysisCacheFile struct {
	safeio.File
	err error
}

func (f *writeErrorAnalysisCacheFile) Write([]byte) (int, error) {
	return 0, f.err
}

type mkdirErrorAnalysisCacheRoot struct {
	safeio.Root
	name string
	err  error
}

func (r *mkdirErrorAnalysisCacheRoot) Mkdir(name string, perm os.FileMode) error {
	if name == r.name {
		return r.err
	}
	return r.Root.Mkdir(name, perm)
}

type removeErrorAnalysisCacheRoot struct {
	safeio.Root
	name string
	err  error
}

func (r *removeErrorAnalysisCacheRoot) Remove(name string) error {
	if name == r.name || strings.HasPrefix(name, ".lopper-cache-rollback-") {
		return r.err
	}
	return r.Root.Remove(name)
}

func (r *removeErrorAnalysisCacheRoot) RenameNoReplace(oldName, newName string) error {
	return safeio.RenameNoReplace(r.Root, oldName, newName)
}

type exactRemoveErrorAnalysisCacheRoot struct {
	safeio.Root
	name string
	err  error
}

func (r *exactRemoveErrorAnalysisCacheRoot) Remove(name string) error {
	if name == r.name {
		return r.err
	}
	return r.Root.Remove(name)
}

func (r *exactRemoveErrorAnalysisCacheRoot) RenameNoReplace(oldName, newName string) error {
	return safeio.RenameNoReplace(r.Root, oldName, newName)
}

type removeQuarantineCreatesReplacementAnalysisCacheRoot struct {
	safeio.Root
	t               *testing.T
	repo            string
	reservationName string
	name            string
	err             error
	replacementInfo fs.FileInfo
}

func (r *removeQuarantineCreatesReplacementAnalysisCacheRoot) Remove(name string) error {
	return r.Root.Remove(name)
}

func (r *removeQuarantineCreatesReplacementAnalysisCacheRoot) OpenRoot(name string) (safeio.Root, error) {
	root, err := r.Root.OpenRoot(name)
	if err != nil || name != r.reservationName {
		return root, err
	}
	return &removeQuarantineChildCreatesReplacementAnalysisCacheRoot{
		Root: root,
		t:    r.t,
		repo: r.repo,
		name: r.name,
		err:  r.err,
		info: &r.replacementInfo,
	}, nil
}

func (r *removeQuarantineCreatesReplacementAnalysisCacheRoot) RenameNoReplace(oldName, newName string) error {
	return safeio.RenameNoReplace(r.Root, oldName, newName)
}

type removeQuarantineChildCreatesReplacementAnalysisCacheRoot struct {
	safeio.Root
	t    *testing.T
	repo string
	name string
	err  error
	info *fs.FileInfo
}

func (r *removeQuarantineChildCreatesReplacementAnalysisCacheRoot) Remove(name string) error {
	if name == r.name {
		replacementPath := filepath.Join(r.repo, r.name)
		if err := os.Mkdir(replacementPath, 0o750); err != nil {
			r.t.Fatalf("create replacement before failed quarantine remove: %v", err)
		}
		info, err := os.Lstat(replacementPath)
		if err != nil {
			r.t.Fatalf("stat replacement before failed quarantine remove: %v", err)
		}
		*r.info = info
		return r.err
	}
	return r.Root.Remove(name)
}

type openReservationRemoveChildErrorAnalysisCacheRoot struct {
	safeio.Root
	reservationName string
	childName       string
	err             error
}

func (r *openReservationRemoveChildErrorAnalysisCacheRoot) OpenRoot(name string) (safeio.Root, error) {
	root, err := r.Root.OpenRoot(name)
	if err != nil || name != r.reservationName {
		return root, err
	}
	return &removeErrorAnalysisCacheRoot{Root: root, name: r.childName, err: r.err}, nil
}

func (r *openReservationRemoveChildErrorAnalysisCacheRoot) RenameNoReplace(oldName, newName string) error {
	return safeio.RenameNoReplace(r.Root, oldName, newName)
}

type removeQuarantineSwapsReservationAnalysisCacheRoot struct {
	safeio.Root
	t               *testing.T
	repo            string
	quarantineName  string
	reservationName string
	replacementInfo fs.FileInfo
	swapped         bool
}

func (r *removeQuarantineSwapsReservationAnalysisCacheRoot) Remove(name string) error {
	if name == r.reservationName {
		r.t.Fatalf("unsafe path-only reservation removal attempted for %s", name)
	}
	err := r.Root.Remove(name)
	return err
}

func (r *removeQuarantineSwapsReservationAnalysisCacheRoot) RenameNoReplace(oldName, newName string) error {
	return safeio.RenameNoReplace(r.Root, oldName, newName)
}

func (r *removeQuarantineSwapsReservationAnalysisCacheRoot) OpenRoot(name string) (safeio.Root, error) {
	if name == r.reservationName && !r.swapped {
		r.swapped = true
		reservationPath := filepath.Join(r.repo, r.reservationName)
		replacementPath := filepath.Join(r.repo, "replacement-reservation")
		if err := os.Rename(reservationPath, replacementPath); err != nil {
			r.t.Fatalf("move owned reservation aside: %v", err)
		}
		if err := os.Mkdir(reservationPath, 0o750); err != nil {
			r.t.Fatalf("create replacement reservation: %v", err)
		}
		info, statErr := os.Lstat(reservationPath)
		if statErr != nil {
			r.t.Fatalf("stat replacement reservation: %v", statErr)
		}
		r.replacementInfo = info
	}
	return r.Root.OpenRoot(name)
}

type renameErrorAnalysisCacheRoot struct {
	safeio.Root
	name string
	err  error
}

func (r *renameErrorAnalysisCacheRoot) Rename(oldName, newName string) error {
	if oldName == r.name {
		return r.err
	}
	return r.Root.Rename(oldName, newName)
}

func (r *renameErrorAnalysisCacheRoot) RenameNoReplace(oldName, newName string) error {
	if oldName == r.name {
		return r.err
	}
	return safeio.RenameNoReplace(r.Root, oldName, newName)
}

type renameErrExistOnceAnalysisCacheRoot struct {
	safeio.Root
	name string
	seen bool
}

func (r *renameErrExistOnceAnalysisCacheRoot) Rename(oldName, newName string) error {
	if oldName == r.name && strings.HasSuffix(filepath.Dir(newName), "-0") && !r.seen {
		r.seen = true
		return os.ErrExist
	}
	return r.Root.Rename(oldName, newName)
}

func (r *renameErrExistOnceAnalysisCacheRoot) RenameNoReplace(oldName, newName string) error {
	if oldName == r.name && strings.HasSuffix(filepath.Dir(newName), "-0") && !r.seen {
		r.seen = true
		return os.ErrExist
	}
	return safeio.RenameNoReplace(r.Root, oldName, newName)
}

type renameErrExistAnalysisCacheRoot struct {
	safeio.Root
	name string
}

func (r *renameErrExistAnalysisCacheRoot) Rename(oldName, newName string) error {
	if oldName == r.name && strings.HasPrefix(newName, ".lopper-cache-rollback-") {
		return os.ErrExist
	}
	return r.Root.Rename(oldName, newName)
}

func (r *renameErrExistAnalysisCacheRoot) RenameNoReplace(oldName, newName string) error {
	if oldName == r.name && strings.HasPrefix(newName, ".lopper-cache-rollback-") {
		return os.ErrExist
	}
	return safeio.RenameNoReplace(r.Root, oldName, newName)
}

type closeErrorAnalysisCacheRoot struct {
	safeio.Root
	err error
}

func (r *closeErrorAnalysisCacheRoot) Close() error {
	return errors.Join(r.err, r.Root.Close())
}

type lstatErrorAnalysisCacheRoot struct {
	safeio.Root
	name string
	err  error
}

func (r *lstatErrorAnalysisCacheRoot) Lstat(name string) (fs.FileInfo, error) {
	if name == r.name {
		return nil, r.err
	}
	return r.Root.Lstat(name)
}

func (r *lstatErrorAnalysisCacheRoot) RenameNoReplace(oldName, newName string) error {
	return safeio.RenameNoReplace(r.Root, oldName, newName)
}

type reservationLstatSwapAfterOpenAnalysisCacheRoot struct {
	safeio.Root
	t               *testing.T
	repo            string
	name            string
	err             error
	opened          bool
	replacementInfo fs.FileInfo
}

func (r *reservationLstatSwapAfterOpenAnalysisCacheRoot) Lstat(name string) (fs.FileInfo, error) {
	if name == r.name {
		return nil, r.err
	}
	return r.Root.Lstat(name)
}

func (r *reservationLstatSwapAfterOpenAnalysisCacheRoot) OpenRoot(name string) (safeio.Root, error) {
	opened, err := r.Root.OpenRoot(name)
	if err != nil || name != r.name || r.opened {
		return opened, err
	}
	r.opened = true
	reservationPath := filepath.Join(r.repo, r.name)
	movedPath := filepath.Join(r.repo, "moved-reservation")
	if err := os.Rename(reservationPath, movedPath); err != nil {
		r.t.Fatalf("move owned reservation after opening: %v", err)
	}
	if err := os.Mkdir(reservationPath, 0o750); err != nil {
		r.t.Fatalf("create replacement reservation after opening: %v", err)
	}
	info, statErr := os.Lstat(reservationPath)
	if statErr != nil {
		r.t.Fatalf("stat replacement reservation: %v", statErr)
	}
	r.replacementInfo = info
	return opened, nil
}

func (r *reservationLstatSwapAfterOpenAnalysisCacheRoot) RenameNoReplace(oldName, newName string) error {
	return safeio.RenameNoReplace(r.Root, oldName, newName)
}

type lstatSwapAnalysisCacheRoot struct {
	t *testing.T
	safeio.Root
	name             string
	childPath        string
	renamedChildPath string
	replacementInfo  fs.FileInfo
	swapped          bool
}

func (r *lstatSwapAnalysisCacheRoot) Lstat(name string) (fs.FileInfo, error) {
	info, err := r.Root.Lstat(name)
	if err != nil || name != r.name || r.swapped {
		return info, err
	}
	r.swapped = true
	if err := os.Rename(r.childPath, r.renamedChildPath); err != nil {
		r.t.Fatalf("rename rollback target during lstat: %v", err)
	}
	if err := os.Mkdir(r.childPath, 0o750); err != nil {
		r.t.Fatalf("replace rollback target during lstat: %v", err)
	}
	replacementInfo, statErr := os.Lstat(r.childPath)
	if statErr != nil {
		r.t.Fatalf("stat replacement during lstat: %v", statErr)
	}
	r.replacementInfo = replacementInfo
	return info, nil
}

func (r *lstatSwapAnalysisCacheRoot) RenameNoReplace(oldName, newName string) error {
	return safeio.RenameNoReplace(r.Root, oldName, newName)
}

type quarantineDestinationRaceAnalysisCacheRoot struct {
	safeio.Root
	t               *testing.T
	repo            string
	name            string
	replacementInfo fs.FileInfo
}

func (r *quarantineDestinationRaceAnalysisCacheRoot) Mkdir(name string, perm os.FileMode) error {
	if err := r.Root.Mkdir(name, perm); err != nil {
		return err
	}
	if strings.HasPrefix(name, ".lopper-cache-rollback-") {
		replacementPath := filepath.Join(r.repo, name, r.name)
		if err := os.Mkdir(replacementPath, 0o750); err != nil {
			r.t.Fatalf("seed quarantine destination race: %v", err)
		}
		if r.replacementInfo == nil {
			info, err := os.Lstat(replacementPath)
			if err != nil {
				r.t.Fatalf("stat quarantine destination race: %v", err)
			}
			r.replacementInfo = info
		}
	}
	return nil
}

type renameNoReplaceRaceAnalysisCacheRoot struct {
	safeio.Root
	t               *testing.T
	repo            string
	oldName         string
	newName         string
	replacementInfo fs.FileInfo
}

func (r *renameNoReplaceRaceAnalysisCacheRoot) Rename(oldName, newName string) error {
	r.createReplacement(oldName, newName)
	return r.Root.Rename(oldName, newName)
}

func (r *renameNoReplaceRaceAnalysisCacheRoot) RenameNoReplace(oldName, newName string) error {
	r.createReplacement(oldName, newName)
	return safeio.RenameNoReplace(r.Root, oldName, newName)
}

func (r *renameNoReplaceRaceAnalysisCacheRoot) createReplacement(oldName, newName string) {
	if oldName != r.oldName || newName != r.newName || r.replacementInfo != nil {
		return
	}
	replacementPath := filepath.Join(r.repo, newName)
	if err := os.Mkdir(replacementPath, 0o750); err != nil {
		r.t.Fatalf("create rename no-replace race destination: %v", err)
	}
	info, err := os.Lstat(replacementPath)
	if err != nil {
		r.t.Fatalf("stat rename no-replace race destination: %v", err)
	}
	r.replacementInfo = info
}

type openNamedRootAnalysisCacheRoot struct {
	safeio.Root
	name string
	root safeio.Root
}

func (r *openNamedRootAnalysisCacheRoot) OpenRoot(name string) (safeio.Root, error) {
	if name == r.name {
		return r.root, nil
	}
	return r.Root.OpenRoot(name)
}

func (r *openNamedRootAnalysisCacheRoot) RenameNoReplace(oldName, newName string) error {
	return safeio.RenameNoReplace(r.Root, oldName, newName)
}

type openNamedRootErrorAnalysisCacheRoot struct {
	safeio.Root
	name string
	err  error
}

func (r *openNamedRootErrorAnalysisCacheRoot) OpenRoot(name string) (safeio.Root, error) {
	if name == r.name {
		return nil, r.err
	}
	return r.Root.OpenRoot(name)
}

func (r *openNamedRootErrorAnalysisCacheRoot) RenameNoReplace(oldName, newName string) error {
	return safeio.RenameNoReplace(r.Root, oldName, newName)
}

type analysisCacheSwapFixture struct {
	repo            string
	renamedRepoPath string
	cachePath       string
}

type analysisCacheWriteRootFixture struct {
	repo      string
	cachePath string
	cache     *analysisCache
}

func newAnalysisCacheSwapFixture(t *testing.T) analysisCacheSwapFixture {
	t.Helper()
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	if err := os.Mkdir(repo, 0o750); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	return analysisCacheSwapFixture{
		repo:            repo,
		renamedRepoPath: filepath.Join(parent, "repo-renamed"),
		cachePath:       filepath.Join(repo, "missing", cacheDirName),
	}
}

func (f *analysisCacheSwapFixture) swapRepo(t *testing.T, description string) {
	t.Helper()
	if err := os.Rename(f.repo, f.renamedRepoPath); err != nil {
		t.Fatalf("rename repo %s: %v", description, err)
	}
	if err := os.Mkdir(f.repo, 0o750); err != nil {
		t.Fatalf("replace repo %s: %v", description, err)
	}
}

func (f *analysisCacheSwapFixture) assertMissingPartsAbsent(t *testing.T) {
	t.Helper()
	assertAnalysisCachePathAbsent(t, filepath.Join(f.repo, "missing"))
	assertAnalysisCachePathAbsent(t, filepath.Join(f.renamedRepoPath, "missing"))
}

func newAnalysisCacheWriteRootFixture(t *testing.T) analysisCacheWriteRootFixture {
	t.Helper()
	repo := t.TempDir()
	cachePath := filepath.Join(repo, cacheDirName)
	mustMkdirCacheLayout(t, cachePath)
	rootInfo, err := os.Lstat(cachePath)
	if err != nil {
		t.Fatalf("stat cache root: %v", err)
	}
	return analysisCacheWriteRootFixture{
		repo:      repo,
		cachePath: cachePath,
		cache:     &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: cachePath}, rootIdentity: rootInfo},
	}
}

func hookMkdirAnalysisCacheDir(t *testing.T, hook func(root safeio.Root, name string, perm os.FileMode, mkdir func(safeio.Root, string, os.FileMode) (fs.FileInfo, error)) (fs.FileInfo, error)) {
	t.Helper()
	original := mkdirAnalysisCacheDir
	mkdirAnalysisCacheDir = func(root safeio.Root, name string, perm os.FileMode) (fs.FileInfo, error) {
		return hook(root, name, perm, original)
	}
	t.Cleanup(func() {
		mkdirAnalysisCacheDir = original
	})
}

func hookCloseAnalysisCacheRoot(t *testing.T, hook func(closeRoot func(safeio.Root) error, root safeio.Root) error) {
	t.Helper()
	original := closeAnalysisCacheRoot
	closeAnalysisCacheRoot = func(root safeio.Root) error {
		return hook(original, root)
	}
	t.Cleanup(func() {
		closeAnalysisCacheRoot = original
	})
}

func hookValidateAnalysisCacheRoot(t *testing.T, hook func(validate func(string, fs.FileInfo) error, path string, expected fs.FileInfo) error) {
	t.Helper()
	original := validateAnalysisCacheRoot
	validateAnalysisCacheRoot = func(path string, expected fs.FileInfo) error {
		return hook(original, path, expected)
	}
	t.Cleanup(func() {
		validateAnalysisCacheRoot = original
	})
}

func hookValidateAnalysisCacheRootCall(t *testing.T, cachePath string, hook func(call int) (bool, error)) {
	t.Helper()
	validateCalls := 0
	hookValidateAnalysisCacheRoot(t, func(validate func(string, fs.FileInfo) error, path string, expected fs.FileInfo) error {
		if path == cachePath {
			validateCalls++
			handled, err := hook(validateCalls)
			if handled || err != nil {
				return err
			}
		}
		return validate(path, expected)
	})
}

func hookOpenAnalysisCacheAncestor(t *testing.T, hook func(open func(string) (safeio.Root, string, []string, error), name string) (safeio.Root, string, []string, error)) {
	t.Helper()
	original := openAnalysisCacheAncestor
	openAnalysisCacheAncestor = func(name string) (safeio.Root, string, []string, error) {
		return hook(original, name)
	}
	t.Cleanup(func() {
		openAnalysisCacheAncestor = original
	})
}

func openAnalysisCacheTestRoot(t *testing.T, path string) safeio.Root {
	t.Helper()
	root, err := safeio.OpenRoot(path)
	if err != nil {
		t.Fatalf("open root %s: %v", path, err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Fatalf("close root %s: %v", path, err)
		}
	})
	return root
}

func createAnalysisCacheChild(t *testing.T, repo, name string) (string, fs.FileInfo) {
	t.Helper()
	childPath := filepath.Join(repo, name)
	if err := os.Mkdir(childPath, 0o750); err != nil {
		t.Fatalf("create child: %v", err)
	}
	childInfo, err := os.Lstat(childPath)
	if err != nil {
		t.Fatalf("stat child: %v", err)
	}
	return childPath, childInfo
}

func createAnalysisCacheQuarantineReservationWithOwner(t *testing.T, repo string, root safeio.Root, reservationName, quarantineName, ownerToken string) analysisCacheQuarantineReservation {
	t.Helper()
	reservation := newAnalysisCacheQuarantineReservation(reservationName, quarantineName, ownerToken)
	if err := os.Mkdir(filepath.Join(repo, reservationName), 0o700); err != nil {
		t.Fatalf("create reservation: %v", err)
	}
	info, err := os.Lstat(filepath.Join(repo, reservationName))
	if err != nil {
		t.Fatalf("stat reservation: %v", err)
	}
	reservation.info = info
	if err := writeAnalysisCacheQuarantineOwner(root, reservation); err != nil {
		t.Fatalf("write owner token: %v", err)
	}
	return reservation
}

func TestRemoveCreatedAnalysisCacheRootsFallsBackToLiveParentWhenRollbackParentMissing(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	_, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)

	opened := []openedAnalysisCacheRoot{{
		parent:         root,
		rollbackParent: nil,
		name:           cacheKeysDirName,
		info:           childInfo,
		created:        true,
	}}

	if err := removeCreatedAnalysisCacheRoots(opened, true); err != nil {
		t.Fatalf("remove created cache roots with missing rollback parent: %v", err)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, cacheKeysDirName))
}

func TestAnalysisCacheWarningLifecycleAndSnapshot(t *testing.T) {
	cache := &analysisCache{
		metadata: report.CacheMetadata{Invalidations: []report.CacheInvalidation{{Key: "k", Reason: "reason"}}},
		warnings: []string{},
	}

	cache.warn("")
	cache.warn("cache warning")
	warnings := cache.takeWarnings()
	if len(warnings) != 1 || warnings[0] != "cache warning" {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}
	if len(cache.takeWarnings()) != 0 {
		t.Fatalf("expected warnings to be drained")
	}

	snapshot := cache.metadataSnapshot()
	if snapshot == nil || len(snapshot.Invalidations) != 1 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	cache.metadata.Invalidations[0].Reason = "mutated"
	if snapshot.Invalidations[0].Reason == "mutated" {
		t.Fatalf("expected snapshot invalidations to be copied")
	}

	var nilCache *analysisCache
	if nilCache.metadataSnapshot() != nil {
		t.Fatalf("expected nil snapshot for nil cache")
	}
}

func TestCachePathAndRelevantFileBoundaryBranches(t *testing.T) {
	var nilCache *analysisCache
	cachePath := filepath.Join(t.TempDir(), cacheDirName)
	if got := nilCache.stableCacheRoot(cachePath); got != cachePath {
		t.Fatalf("expected nil cache to preserve cache root, got %q", got)
	}

	repo := t.TempDir()
	outside := t.TempDir()
	symlink := filepath.Join(repo, "linked-cache")
	if err := os.Symlink(outside, symlink); err != nil {
		t.Fatalf("create cache symlink: %v", err)
	}
	if !cachePathEscapesRepo(symlink, repo) {
		t.Fatal("expected symlinked cache path to be rejected")
	}
	missingRootPath := filepath.Join(repo, "missing", cacheDirName)
	missingRootInfo, err := prepareWritableAnalysisCacheRoot(missingRootPath)
	if err != nil {
		t.Fatalf("expected missing cache root to be created capability-bound: %v", err)
	}
	if missingRootInfo == nil {
		t.Fatal("expected identity for newly created missing cache root")
	}
	if info, statErr := os.Stat(missingRootPath); statErr != nil || !info.IsDir() {
		t.Fatalf("expected missing cache root to exist on disk, stat err=%v", statErr)
	}
	cache := &analysisCache{options: resolvedCacheOptions{Path: repo}}
	writeRoot, err := cache.openWriteRoot()
	if err != nil {
		t.Fatalf("open cache write root: %v", err)
	}
	if err := writeRoot.Close(); err != nil {
		t.Fatalf("close cache write root: %v", err)
	}

	exclusions := cacheExcludedPathSet(cacheAnalysisExclusions{files: []string{"", filepath.Join(repo, "trace.ndjson")}})
	if len(exclusions) != 1 {
		t.Fatalf("expected only non-empty cache exclusion, got %#v", exclusions)
	}
	if !shouldSkipCacheDir(cacheDirName) || isCacheRelevantFile("README.txt") {
		t.Fatal("expected cache directory and unsupported file handling")
	}
	if _, err := collectPHPShortOpenTagTraversalEntries(filepath.Join(repo, "missing-root"), cacheAnalysisExclusions{}); err == nil {
		t.Fatal("expected missing short-open-tag traversal root to fail")
	}
	if err := os.MkdirAll(filepath.Join(repo, "objects", "broken.json"), 0o750); err != nil {
		t.Fatalf("create malformed cache object path: %v", err)
	}
	if _, reason, err := readCachedPayload(repo, "broken"); err != nil || reason != "object-read-error" {
		t.Fatalf("expected malformed cache object read to invalidate, reason=%q err=%v", reason, err)
	}
}

func TestNewAnalysisCacheUnavailablePathAddsWarning(t *testing.T) {
	repo := t.TempDir()
	blockingPath := filepath.Join(repo, "not-a-dir")
	if err := os.WriteFile(blockingPath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: blockingPath}}, repo)
	if cache.cacheable {
		t.Fatalf("expected cache to be non-cacheable when path is invalid")
	}
	if len(cache.takeWarnings()) == 0 {
		t.Fatalf("expected warning when cache directory init fails")
	}
}

func TestNewAnalysisCacheCreatesMissingRootWithinPinnedAncestor(t *testing.T) {
	repo := t.TempDir()
	cachePath := filepath.Join(repo, "missing", cacheDirName)

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: cachePath}}, repo)
	if !cache.cacheable {
		t.Fatalf("expected missing cache root to be created, warnings=%#v", cache.takeWarnings())
	}
	for _, path := range []string{
		cachePath,
		filepath.Join(cachePath, cacheKeysDirName),
		filepath.Join(cachePath, cacheObjectsDirName),
	} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("expected cache initialization to create directory %s, info=%#v err=%v", path, info, err)
		}
	}
}

func TestNewAnalysisCacheReadOnlyMissingRootDoesNotCreate(t *testing.T) {
	repo := t.TempDir()
	cachePath := filepath.Join(repo, "missing", cacheDirName)

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: cachePath, ReadOnly: true}}, repo)
	if cache.cacheable {
		t.Fatal("expected missing read-only cache root to be unavailable")
	}
	warnings := cache.takeWarnings()
	if len(warnings) == 0 {
		t.Fatal("expected missing read-only cache root warning")
	}
	for _, path := range []string{
		filepath.Join(repo, "missing"),
		cachePath,
		filepath.Join(cachePath, cacheKeysDirName),
		filepath.Join(cachePath, cacheObjectsDirName),
	} {
		assertAnalysisCachePathAbsent(t, path)
	}
}

func TestNewAnalysisCacheReadOnlyExistingRootUsesPinnedIdentity(t *testing.T) {
	repo := t.TempDir()
	cachePath := filepath.Join(repo, cacheDirName)
	mustMkdirCacheLayout(t, cachePath)

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: cachePath, ReadOnly: true}}, repo)
	if !cache.cacheable {
		t.Fatalf("expected existing read-only cache root to be usable, warnings=%#v", cache.takeWarnings())
	}
	if cache.rootIdentity == nil {
		t.Fatal("expected read-only cache root identity to be pinned")
	}
}

func TestPrepareAnalysisCacheRootReturnsOpenAncestorErrors(t *testing.T) {
	openErr := errors.New("open ancestor failed")
	hookOpenAnalysisCacheAncestor(t, func(func(string) (safeio.Root, string, []string, error), string) (safeio.Root, string, []string, error) {
		return nil, "", nil, openErr
	})

	if _, err := prepareReadableAnalysisCacheRoot(filepath.Join(t.TempDir(), cacheDirName)); !errors.Is(err, openErr) {
		t.Fatalf("readable root error = %v, want %v", err, openErr)
	}
	if _, err := prepareWritableAnalysisCacheRoot(filepath.Join(t.TempDir(), cacheDirName)); !errors.Is(err, openErr) {
		t.Fatalf("writable root error = %v, want %v", err, openErr)
	}
}

func TestPrepareAnalysisCacheRootReadOnlyPreservesValidationFailure(t *testing.T) {
	repo := t.TempDir()
	cachePath := filepath.Join(repo, cacheDirName)
	mustMkdirCacheLayout(t, cachePath)
	validationErr := errors.New("read-only validation failed")

	hookValidateAnalysisCacheRoot(t, func(validate func(string, fs.FileInfo) error, path string, expected fs.FileInfo) error {
		if path == cachePath {
			return validationErr
		}
		return validate(path, expected)
	})

	if _, err := prepareAnalysisCacheRoot(resolvedCacheOptions{Enabled: true, Path: cachePath, ReadOnly: true}); !errors.Is(err, validationErr) {
		t.Fatalf("prepare read-only cache root error = %v, want %v", err, validationErr)
	}
}

func TestVerifyPinnedAnalysisCacheDirectoryPropagatesLstatError(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	lstatErr := errors.New("root lstat failed")

	if _, err := verifyPinnedAnalysisCacheDirectory(&lstatErrorAnalysisCacheRoot{Root: root, name: ".", err: lstatErr}, repo); !errors.Is(err, lstatErr) {
		t.Fatalf("verify pinned directory error = %v, want %v", err, lstatErr)
	}
}

func TestAnalysisCacheStableCacheRootNilReceiver(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "repo", cacheDirName)
	var cache *analysisCache
	if got := cache.stableCacheRoot(rootPath); got != rootPath {
		t.Fatalf("nil cache stable root = %q, want %q", got, rootPath)
	}
}

func TestNewAnalysisCacheRejectsAncestorSwapBeforeMissingRootCreate(t *testing.T) {
	fixture := newAnalysisCacheSwapFixture(t)
	swapped := false
	createdMissingPart := false
	observedAncestorPath := ""
	var observedMissingParts []string
	hookOpenAnalysisCacheAncestor(t, func(open func(string) (safeio.Root, string, []string, error), name string) (safeio.Root, string, []string, error) {
		root, currentPath, missingParts, err := open(name)
		if err == nil && name == fixture.cachePath && !swapped {
			observedAncestorPath = currentPath
			observedMissingParts = append([]string(nil), missingParts...)
			swapped = true
			fixture.swapRepo(t, "during cache root init")
		}
		return root, currentPath, missingParts, err
	})
	hookMkdirAnalysisCacheDir(t, func(root safeio.Root, name string, perm os.FileMode, mkdir func(safeio.Root, string, os.FileMode) (fs.FileInfo, error)) (fs.FileInfo, error) {
		createdMissingPart = true
		return mkdir(root, name, perm)
	})

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: fixture.cachePath}}, fixture.repo)
	if cache.cacheable {
		t.Fatal("expected retargeted cache ancestor to be unavailable")
	}
	if !swapped {
		t.Fatal("expected test hook to swap repo before missing cache root creation")
	}
	if filepath.Base(observedAncestorPath) != filepath.Base(fixture.repo) {
		t.Fatalf("expected deepest opened ancestor to be repo, got %q", observedAncestorPath)
	}
	if !slices.Equal(observedMissingParts, []string{"missing", cacheDirName}) {
		t.Fatalf("expected missing cache components after ancestor open, got %#v", observedMissingParts)
	}
	if createdMissingPart {
		t.Fatal("expected cache root initialization to fail before creating missing path components")
	}
	if warnings := cache.takeWarnings(); len(warnings) == 0 {
		t.Fatal("expected cache initialization warning")
	}
	fixture.assertMissingPartsAbsent(t)
}

func TestNewAnalysisCacheRollsBackAllCreatedMissingPartsWhenLaterAncestorSwapFails(t *testing.T) {
	fixture := newAnalysisCacheSwapFixture(t)
	swapped := false
	hookMkdirAnalysisCacheDir(t, func(root safeio.Root, name string, perm os.FileMode, mkdir func(safeio.Root, string, os.FileMode) (fs.FileInfo, error)) (fs.FileInfo, error) {
		info, err := mkdir(root, name, perm)
		if err == nil && name == cacheDirName && !swapped {
			swapped = true
			fixture.swapRepo(t, "after nested cache root create")
		}
		return info, err
	})

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: fixture.cachePath}}, fixture.repo)
	if cache.cacheable {
		t.Fatal("expected retargeted cache ancestor to be unavailable")
	}
	if !swapped {
		t.Fatal("expected test hook to swap repo after nested cache root creation")
	}
	if warnings := cache.takeWarnings(); len(warnings) == 0 {
		t.Fatal("expected cache initialization warning")
	}
	fixture.assertMissingPartsAbsent(t)
}

func TestNewAnalysisCacheRollsBackMissingRootWhenAncestorSwapsAfterCreate(t *testing.T) {
	fixture := newAnalysisCacheSwapFixture(t)
	swapped := false
	hookMkdirAnalysisCacheDir(t, func(root safeio.Root, name string, perm os.FileMode, mkdir func(safeio.Root, string, os.FileMode) (fs.FileInfo, error)) (fs.FileInfo, error) {
		info, err := mkdir(root, name, perm)
		if err == nil && name == "missing" && !swapped {
			swapped = true
			fixture.swapRepo(t, "after missing cache root create")
		}
		return info, err
	})

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: fixture.cachePath}}, fixture.repo)
	if cache.cacheable {
		t.Fatal("expected retargeted cache ancestor to be unavailable")
	}
	if !swapped {
		t.Fatal("expected test hook to swap repo after missing cache root creation")
	}
	if warnings := cache.takeWarnings(); len(warnings) == 0 || !strings.Contains(warnings[0], "directory identity changed") {
		t.Fatalf("expected directory identity warning, got %#v", warnings)
	}
	fixture.assertMissingPartsAbsent(t)
}

func TestNewAnalysisCacheRollsBackAllCreatedPartsWhenCleanupCloseFails(t *testing.T) {
	fixture := newAnalysisCacheSwapFixture(t)
	closeErr := errors.New("close ancestor failed")
	var ancestor safeio.Root
	ancestorCloseFailed := false
	hookOpenAnalysisCacheAncestor(t, func(open func(string) (safeio.Root, string, []string, error), name string) (safeio.Root, string, []string, error) {
		root, currentPath, missingParts, err := open(name)
		if err == nil && name == fixture.cachePath {
			ancestor = root
		}
		return root, currentPath, missingParts, err
	})
	hookCloseAnalysisCacheRoot(t, func(closeRoot func(safeio.Root) error, root safeio.Root) error {
		if root == ancestor {
			ancestorCloseFailed = true
			return closeErr
		}
		return closeRoot(root)
	})
	t.Cleanup(func() {
		if ancestorCloseFailed {
			if err := ancestor.Close(); err != nil {
				t.Fatalf("close test ancestor: %v", err)
			}
		}
	})

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: fixture.cachePath}}, fixture.repo)
	if cache.cacheable {
		t.Fatal("expected close failure to make the cache unavailable")
	}
	if !ancestorCloseFailed {
		t.Fatal("expected cache initialization to close the opened ancestor")
	}
	if warnings := cache.takeWarnings(); len(warnings) == 0 || !strings.Contains(warnings[0], closeErr.Error()) {
		t.Fatalf("expected close failure warning, got %#v", warnings)
	}
	fixture.assertMissingPartsAbsent(t)
}

func TestNewAnalysisCacheRollsBackCreatedPartsWhenFinalPathValidationFails(t *testing.T) {
	fixture := newAnalysisCacheSwapFixture(t)
	validationErr := errors.New("final cache path validation failed")
	validated := false
	hookValidateAnalysisCacheRoot(t, func(validate func(string, fs.FileInfo) error, path string, expected fs.FileInfo) error {
		if path == fixture.cachePath && !validated {
			validated = true
			fixture.swapRepo(t, "before final cache root validation")
			return validationErr
		}
		return validate(path, expected)
	})

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: fixture.cachePath}}, fixture.repo)
	if cache.cacheable {
		t.Fatal("expected final validation failure to make the cache unavailable")
	}
	if !validated {
		t.Fatal("expected writable cache initialization to validate the final cache path")
	}
	if warnings := cache.takeWarnings(); len(warnings) == 0 || !strings.Contains(warnings[0], validationErr.Error()) {
		t.Fatalf("expected final validation warning, got %#v", warnings)
	}
	fixture.assertMissingPartsAbsent(t)
}

func TestNewAnalysisCacheRejectsExistingRootSwapAfterOpen(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	cachePath := filepath.Join(repo, cacheDirName)
	mustMkdirCacheLayout(t, cachePath)
	renamedCachePath := filepath.Join(parent, "cache-renamed")
	swapped := false
	hookOpenAnalysisCacheAncestor(t, func(open func(string) (safeio.Root, string, []string, error), name string) (safeio.Root, string, []string, error) {
		root, currentPath, missingParts, err := open(name)
		if err == nil && name == cachePath && !swapped {
			swapped = true
			if err := os.Rename(cachePath, renamedCachePath); err != nil {
				t.Fatalf("rename cache root after open: %v", err)
			}
			if err := os.Mkdir(cachePath, 0o750); err != nil {
				t.Fatalf("replace cache root after open: %v", err)
			}
		}
		return root, currentPath, missingParts, err
	})

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: cachePath}}, repo)
	if cache.cacheable {
		t.Fatal("expected swapped existing cache root to be unavailable")
	}
	if !swapped {
		t.Fatal("expected test hook to swap cache root after open")
	}
	if warnings := cache.takeWarnings(); len(warnings) == 0 || !strings.Contains(warnings[0], "directory identity changed") {
		t.Fatalf("expected directory identity warning, got %#v", warnings)
	}
}

func TestOpenOrCreatePinnedAnalysisCacheChildPreservesOpenRootError(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	openErr := errors.New("open child denied")

	_, err := openOrCreatePinnedAnalysisCacheChild(&openRootErrorAnalysisCacheRoot{Root: root, err: openErr}, repo, "keys")
	if !errors.Is(err, openErr) {
		t.Fatalf("expected OpenRoot error to be preserved, got %v", err)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, "keys"))
}

func TestOpenOrCreatePinnedAnalysisCacheChildRollsBackWhenPostCreateLstatFails(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	lstatErr := errors.New("child lstat failed after create")

	_, err := openOrCreatePinnedAnalysisCacheChild(&postCreateLstatErrorAnalysisCacheRoot{
		Root: root,
		name: cacheKeysDirName,
		err:  lstatErr,
	}, repo, cacheKeysDirName)
	if !errors.Is(err, lstatErr) {
		t.Fatalf("expected post-create lstat error to be preserved, got %v", err)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, cacheKeysDirName))
}

func TestOpenOrCreatePinnedAnalysisCacheChildRollsBackWhenSecondPostCreateLstatFails(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	lstatErr := errors.New("child lstat failed after create helper returned")

	_, err := openOrCreatePinnedAnalysisCacheChild(&secondPostCreateLstatErrorAnalysisCacheRoot{
		Root: root,
		name: cacheKeysDirName,
		err:  lstatErr,
	}, repo, cacheKeysDirName)
	if !errors.Is(err, lstatErr) {
		t.Fatalf("expected second post-create lstat error to be preserved, got %v", err)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, cacheKeysDirName))
}

func TestOpenOrCreatePinnedAnalysisCacheChildUsesPreCreateRollbackParentAfterRetarget(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	wrappedRoot := &retargetedPostCreateAnalysisCacheRoot{Root: root, name: cacheKeysDirName}

	_, err := openOrCreatePinnedAnalysisCacheChild(wrappedRoot, repo, cacheKeysDirName)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected retargeted post-create lookup error, got %v", err)
	}
	if !wrappedRoot.retargeted {
		t.Fatal("expected test root to retarget after creating cache child")
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, cacheKeysDirName))
}

func TestMkdirAnalysisCacheDirValidatesOpenedChildIdentity(t *testing.T) {
	t.Run("mkdir error is preserved", testMkdirAnalysisCacheDirPreservesMkdirError)
	t.Run("opened child lstat error is preserved", testMkdirAnalysisCacheDirPreservesOpenedLstatError)
	t.Run("opened child identity mismatch is rejected", testMkdirAnalysisCacheDirRejectsOpenedIdentityMismatch)
}

func testMkdirAnalysisCacheDirPreservesMkdirError(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	mkdirErr := errors.New("mkdir denied")

	if _, err := mkdirAnalysisCacheDir(&mkdirErrorAnalysisCacheRoot{Root: root, name: cacheKeysDirName, err: mkdirErr}, cacheKeysDirName, 0o750); !errors.Is(err, mkdirErr) {
		t.Fatalf("mkdir cache dir error = %v, want %v", err, mkdirErr)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, cacheKeysDirName))
}

func testMkdirAnalysisCacheDirPreservesOpenedLstatError(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath := filepath.Join(repo, cacheKeysDirName)
	childRoot, err := safeio.OpenRoot(repo)
	if err != nil {
		t.Fatalf("open alternate child root: %v", err)
	}
	lstatErr := errors.New("opened child lstat failed")

	_, err = mkdirAnalysisCacheDir(
		&openNamedRootAnalysisCacheRoot{
			Root: root,
			name: cacheKeysDirName,
			root: &lstatErrorAnalysisCacheRoot{Root: childRoot, name: ".", err: lstatErr},
		},
		cacheKeysDirName,
		0o750,
	)
	if !errors.Is(err, lstatErr) {
		t.Fatalf("mkdir cache dir opened child lstat error = %v, want %v", err, lstatErr)
	}
	assertAnalysisCacheDirExists(t, childPath)
}

func testMkdirAnalysisCacheDirRejectsOpenedIdentityMismatch(t *testing.T) {
	repo := t.TempDir()
	alternate := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	alternateRoot, err := safeio.OpenRoot(alternate)
	if err != nil {
		t.Fatalf("open alternate child root: %v", err)
	}

	_, err = mkdirAnalysisCacheDir(
		&openNamedRootAnalysisCacheRoot{Root: root, name: cacheKeysDirName, root: alternateRoot},
		cacheKeysDirName,
		0o750,
	)
	if err == nil || !strings.Contains(err.Error(), "directory changed after creation") {
		t.Fatalf("expected opened child identity mismatch, got %v", err)
	}
	assertAnalysisCacheDirExists(t, filepath.Join(repo, cacheKeysDirName))
}

func TestOpenOrCreatePinnedAnalysisCacheChildDoesNotRemovePostMkdirReplacement(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath := filepath.Join(repo, cacheKeysDirName)
	renamedChildPath := filepath.Join(repo, "created-child")
	hookMkdirAnalysisCacheDir(t, func(root safeio.Root, name string, perm os.FileMode, mkdir func(safeio.Root, string, os.FileMode) (fs.FileInfo, error)) (fs.FileInfo, error) {
		info, err := mkdir(root, name, perm)
		if err != nil {
			return info, err
		}
		if err := os.Rename(childPath, renamedChildPath); err != nil {
			t.Fatalf("rename created child: %v", err)
		}
		if err := os.Mkdir(childPath, 0o750); err != nil {
			t.Fatalf("replace created child: %v", err)
		}
		return info, nil
	})

	_, err := openOrCreatePinnedAnalysisCacheChild(root, repo, cacheKeysDirName)
	if err == nil || !strings.Contains(err.Error(), "directory changed after creation") {
		t.Fatalf("expected replacement to fail closed, got %v", err)
	}
	for _, path := range []string{childPath, renamedChildPath} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("expected %q to remain intact, info=%#v err=%v", path, info, err)
		}
	}
}

func TestOpenOrCreatePinnedAnalysisCacheChildReturnsLstatAfterConcurrentCreateError(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	hookMkdirAnalysisCacheDir(t, func(safeio.Root, string, os.FileMode, func(safeio.Root, string, os.FileMode) (fs.FileInfo, error)) (fs.FileInfo, error) {
		return nil, fs.ErrExist
	})

	_, err := openOrCreatePinnedAnalysisCacheChild(root, repo, cacheKeysDirName)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("child lstat after fs.ErrExist = %v, want not-exist", err)
	}
}

func TestOpenOrCreatePinnedAnalysisCacheChildHandlesConcurrentCreate(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	hookMkdirAnalysisCacheDir(t, func(root safeio.Root, name string, perm os.FileMode, mkdir func(safeio.Root, string, os.FileMode) (fs.FileInfo, error)) (fs.FileInfo, error) {
		info, err := mkdir(root, name, perm)
		if err != nil {
			return nil, err
		}
		return info, fs.ErrExist
	})

	child, err := openOrCreatePinnedAnalysisCacheChild(root, repo, cacheKeysDirName)
	if err != nil {
		t.Fatalf("open concurrently created child: %v", err)
	}
	if child.created {
		t.Fatal("expected fs.ErrExist path to preserve non-created rollback state")
	}
	if err := child.root.Close(); err != nil {
		t.Fatalf("close child: %v", err)
	}
}

func TestOpenOrCreatePinnedAnalysisCacheChildFailureEdges(t *testing.T) {
	t.Run("existing child open error leaves directory intact", testOpenOrCreatePinnedAnalysisCacheChildExistingOpenError)
	t.Run("opened existing child lstat error is preserved", testOpenOrCreatePinnedAnalysisCacheChildExistingLstatError)
	t.Run("opened existing child identity mismatch fails closed", testOpenOrCreatePinnedAnalysisCacheChildExistingIdentityMismatch)
	t.Run("rollback parent open failure removes created child", testOpenOrCreatePinnedAnalysisCacheChildRollbackParentOpenFailure)
	t.Run("mkdir failure rolls back only the attempted child", testOpenOrCreatePinnedAnalysisCacheChildMkdirFailure)
}

func testOpenOrCreatePinnedAnalysisCacheChildExistingOpenError(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, _ := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	openErr := errors.New("open existing child failed")

	_, err := openOrCreatePinnedAnalysisCacheChild(
		&openNamedRootErrorAnalysisCacheRoot{Root: root, name: cacheKeysDirName, err: openErr},
		repo,
		cacheKeysDirName,
	)
	if !errors.Is(err, openErr) {
		t.Fatalf("existing child open error = %v, want %v", err, openErr)
	}
	assertAnalysisCacheDirExists(t, childPath)
}

func testOpenOrCreatePinnedAnalysisCacheChildExistingLstatError(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, _ := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	childRoot, err := safeio.OpenRoot(childPath)
	if err != nil {
		t.Fatalf("open child root: %v", err)
	}
	lstatErr := errors.New("opened child dot lstat failed")

	_, err = openOrCreatePinnedAnalysisCacheChild(
		&openNamedRootAnalysisCacheRoot{
			Root: root,
			name: cacheKeysDirName,
			root: &lstatErrorAnalysisCacheRoot{Root: childRoot, name: ".", err: lstatErr},
		},
		repo,
		cacheKeysDirName,
	)
	if !errors.Is(err, lstatErr) {
		t.Fatalf("opened existing child lstat error = %v, want %v", err, lstatErr)
	}
}

func testOpenOrCreatePinnedAnalysisCacheChildExistingIdentityMismatch(t *testing.T) {
	repo := t.TempDir()
	alternate := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, _ := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	alternateRoot, err := safeio.OpenRoot(alternate)
	if err != nil {
		t.Fatalf("open alternate root: %v", err)
	}

	_, err = openOrCreatePinnedAnalysisCacheChild(
		&openNamedRootAnalysisCacheRoot{Root: root, name: cacheKeysDirName, root: alternateRoot},
		repo,
		cacheKeysDirName,
	)
	if err == nil || !strings.Contains(err.Error(), "directory changed while opening") {
		t.Fatalf("expected opened child identity mismatch, got %v", err)
	}
	assertAnalysisCacheDirExists(t, childPath)
}

func testOpenOrCreatePinnedAnalysisCacheChildRollbackParentOpenFailure(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	openErr := errors.New("rollback parent open failed")

	_, err := openOrCreatePinnedAnalysisCacheChild(
		&openNamedRootErrorAnalysisCacheRoot{Root: root, name: ".", err: openErr},
		repo,
		cacheKeysDirName,
	)
	if !errors.Is(err, openErr) {
		t.Fatalf("rollback parent open error = %v, want %v", err, openErr)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, cacheKeysDirName))
}

func testOpenOrCreatePinnedAnalysisCacheChildMkdirFailure(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	mkdirErr := errors.New("mkdir cache child failed")
	hookMkdirAnalysisCacheDir(t, func(safeio.Root, string, os.FileMode, func(safeio.Root, string, os.FileMode) (fs.FileInfo, error)) (fs.FileInfo, error) {
		return nil, mkdirErr
	})

	_, err := openOrCreatePinnedAnalysisCacheChild(root, repo, cacheKeysDirName)
	if !errors.Is(err, mkdirErr) {
		t.Fatalf("mkdir failure error = %v, want %v", err, mkdirErr)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, cacheKeysDirName))
}

func TestOpenedAnalysisCacheChildInfoBranches(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		repo := t.TempDir()
		root := openAnalysisCacheTestRoot(t, repo)
		_, wantInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)

		got, err := openedAnalysisCacheChildInfo(root, cacheKeysDirName)
		if err != nil {
			t.Fatalf("opened child info: %v", err)
		}
		if !os.SameFile(got, wantInfo) {
			t.Fatal("expected opened child info to preserve the created directory identity")
		}
	})

	t.Run("open error", func(t *testing.T) {
		repo := t.TempDir()
		root := openAnalysisCacheTestRoot(t, repo)
		openErr := errors.New("open child denied")

		if _, err := openedAnalysisCacheChildInfo(&openNamedRootErrorAnalysisCacheRoot{Root: root, name: cacheKeysDirName, err: openErr}, cacheKeysDirName); !errors.Is(err, openErr) {
			t.Fatalf("opened child info open error = %v, want %v", err, openErr)
		}
	})

	t.Run("lstat error joins close", func(t *testing.T) {
		repo := t.TempDir()
		root := openAnalysisCacheTestRoot(t, repo)
		childPath, _ := createAnalysisCacheChild(t, repo, cacheKeysDirName)
		childRoot, err := safeio.OpenRoot(childPath)
		if err != nil {
			t.Fatalf("open child root: %v", err)
		}
		lstatErr := errors.New("child dot lstat failed")
		closeErr := errors.New("close child failed")

		_, err = openedAnalysisCacheChildInfo(&openNamedRootAnalysisCacheRoot{
			Root: root,
			name: cacheKeysDirName,
			root: &closeErrorAnalysisCacheRoot{
				Root: &lstatErrorAnalysisCacheRoot{Root: childRoot, name: ".", err: lstatErr},
				err:  closeErr,
			},
		}, cacheKeysDirName)
		if !errors.Is(err, lstatErr) {
			t.Fatalf("expected lstat error to be preserved, got %v", err)
		}
		if !errors.Is(err, closeErr) {
			t.Fatalf("expected close error to be preserved, got %v", err)
		}
	})
}

func TestValidateOpenedAnalysisCacheChildRejectsRetargetedPath(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	childRoot, err := safeio.OpenRoot(childPath)
	if err != nil {
		t.Fatalf("open child root: %v", err)
	}
	retargetedPath := filepath.Join(repo, "missing-child")

	rollback := analysisCacheChildRollback{
		root:    root,
		name:    cacheKeysDirName,
		child:   childRoot,
		info:    childInfo,
		created: false,
	}
	err = validateOpenedAnalysisCacheChild(root, repo, retargetedPath, rollback)
	if err == nil || !strings.Contains(err.Error(), "missing-child") {
		t.Fatalf("expected retargeted child path validation failure, got %v", err)
	}
}

func TestAnalysisCacheChildHelperBranches(t *testing.T) {
	t.Run("rollback parent existing child", testRollbackParentExistingChild)
	t.Run("rollback parent missing child", testRollbackParentMissingChild)
	t.Run("rollback parent lstat error", testRollbackParentLstatError)
	t.Run("load existing child info", testLoadExistingChildInfo)
	t.Run("load child lstat error", testLoadChildLstatError)
	t.Run("validate opened child success", testValidateOpenedChildSuccess)
	t.Run("validate opened child parent identity failure", testValidateOpenedChildParentIdentityFailure)
}

func testRollbackParentExistingChild(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	createAnalysisCacheChild(t, repo, cacheKeysDirName)

	parent, err := rollbackParentForMissingAnalysisCacheChild(root, cacheKeysDirName)
	if err != nil {
		t.Fatalf("rollback parent for existing child: %v", err)
	}
	if parent != nil {
		t.Fatal("expected existing child to avoid opening a rollback parent")
	}
}

func testRollbackParentMissingChild(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)

	parent, err := rollbackParentForMissingAnalysisCacheChild(root, cacheKeysDirName)
	if err != nil {
		t.Fatalf("rollback parent for missing child: %v", err)
	}
	if parent == nil {
		t.Fatal("expected missing child to open a rollback parent")
	}
	if err := parent.Close(); err != nil {
		t.Fatalf("close rollback parent: %v", err)
	}
}

func testRollbackParentLstatError(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	lstatErr := errors.New("child lstat denied")

	parent, err := rollbackParentForMissingAnalysisCacheChild(&lstatErrorAnalysisCacheRoot{Root: root, name: cacheKeysDirName, err: lstatErr}, cacheKeysDirName)
	if !errors.Is(err, lstatErr) {
		t.Fatalf("rollback parent lstat error = %v, want %v", err, lstatErr)
	}
	if parent != nil {
		t.Fatal("expected lstat error to avoid opening a rollback parent")
	}
}

func testLoadExistingChildInfo(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, wantInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)

	gotInfo, created, err := loadOrCreateAnalysisCacheChildInfo(root, nil, childPath, cacheKeysDirName)
	if err != nil {
		t.Fatalf("load existing child info: %v", err)
	}
	if created {
		t.Fatal("expected existing child to report created=false")
	}
	if !os.SameFile(gotInfo, wantInfo) {
		t.Fatal("expected loaded child info to match the existing child")
	}
}

func testLoadChildLstatError(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	lstatErr := errors.New("load child lstat denied")

	_, created, err := loadOrCreateAnalysisCacheChildInfo(&lstatErrorAnalysisCacheRoot{Root: root, name: cacheKeysDirName, err: lstatErr}, nil, filepath.Join(repo, cacheKeysDirName), cacheKeysDirName)
	if !errors.Is(err, lstatErr) {
		t.Fatalf("load child lstat error = %v, want %v", err, lstatErr)
	}
	if created {
		t.Fatal("expected lstat error to report created=false")
	}
}

func testValidateOpenedChildSuccess(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	childRoot, err := safeio.OpenRoot(childPath)
	if err != nil {
		t.Fatalf("open child root: %v", err)
	}
	defer func() {
		if err := childRoot.Close(); err != nil {
			t.Fatalf("close child root: %v", err)
		}
	}()

	rollback := analysisCacheChildRollback{root: root, name: cacheKeysDirName, child: childRoot, info: childInfo, created: false}
	if err := validateOpenedAnalysisCacheChild(root, repo, childPath, rollback); err != nil {
		t.Fatalf("validate opened child success: %v", err)
	}
}

func testValidateOpenedChildParentIdentityFailure(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	childRoot, err := safeio.OpenRoot(childPath)
	if err != nil {
		t.Fatalf("open child root: %v", err)
	}

	rollback := analysisCacheChildRollback{root: root, name: cacheKeysDirName, child: childRoot, info: childInfo, created: false}
	err = validateOpenedAnalysisCacheChild(root, filepath.Join(repo, "missing-parent"), childPath, rollback)
	if err == nil || !strings.Contains(err.Error(), "missing-parent") {
		t.Fatalf("expected parent identity validation failure, got %v", err)
	}
}

func TestRollbackCreatedAnalysisCacheChildNoopsForUncreatedChild(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)

	if err := rollbackCreatedAnalysisCacheChild(root, cacheKeysDirName, nil, childInfo, false); err != nil {
		t.Fatalf("rollback uncreated child: %v", err)
	}
	if info, err := os.Stat(childPath); err != nil || !info.IsDir() {
		t.Fatalf("expected uncreated child to remain, info=%#v err=%v", info, err)
	}
}

func TestRollbackCreatedAnalysisCacheChildSkipsMissingOrReplacedChild(t *testing.T) {
	t.Run("missing", testRollbackCreatedAnalysisCacheChildSkipsMissingChild)
	t.Run("replaced", testRollbackCreatedAnalysisCacheChildSkipsReplacedChild)
}

func testRollbackCreatedAnalysisCacheChildSkipsMissingChild(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	child, err := safeio.OpenRoot(childPath)
	if err != nil {
		t.Fatalf("open child: %v", err)
	}
	if err := os.Remove(childPath); err != nil {
		t.Fatalf("remove child before rollback: %v", err)
	}

	if err := rollbackCreatedAnalysisCacheChild(root, cacheKeysDirName, child, childInfo, true); err != nil {
		t.Fatalf("rollback missing child: %v", err)
	}
	assertAnalysisCachePathAbsent(t, childPath)
}

func testRollbackCreatedAnalysisCacheChildSkipsReplacedChild(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	child, err := safeio.OpenRoot(childPath)
	if err != nil {
		t.Fatalf("open child: %v", err)
	}
	if err := os.Rename(childPath, filepath.Join(repo, "renamed-child")); err != nil {
		t.Fatalf("rename child before rollback: %v", err)
	}
	if err := os.Mkdir(childPath, 0o750); err != nil {
		t.Fatalf("replace child before rollback: %v", err)
	}

	if err := rollbackCreatedAnalysisCacheChild(root, cacheKeysDirName, child, childInfo, true); err != nil {
		t.Fatalf("rollback replaced child: %v", err)
	}
	assertAnalysisCacheDirExists(t, childPath)
}

func TestRollbackCreatedAnalysisCacheChildSkipsReplacementSwappedAfterVerification(t *testing.T) {
	repo := t.TempDir()
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	renamedChildPath := filepath.Join(repo, "renamed-child")

	root := openAnalysisCacheTestRoot(t, repo)
	if err := rollbackCreatedAnalysisCacheChild(
		&lstatSwapAnalysisCacheRoot{
			t:                t,
			Root:             root,
			name:             cacheKeysDirName,
			childPath:        childPath,
			renamedChildPath: renamedChildPath,
		},
		cacheKeysDirName,
		nil,
		childInfo,
		true,
	); err != nil {
		t.Fatalf("rollback swapped child after verification: %v", err)
	}
	for _, path := range []string{childPath, renamedChildPath} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("expected %q to remain after rollback race, info=%#v err=%v", path, info, err)
		}
	}
}

func TestRollbackCreatedAnalysisCacheChildPreservesCurrentLstatFailure(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	lstatErr := errors.New("rollback current lstat failed")

	err := rollbackCreatedAnalysisCacheChild(&lstatErrorAnalysisCacheRoot{Root: root, name: cacheKeysDirName, err: lstatErr}, cacheKeysDirName, nil, childInfo, true)
	if !errors.Is(err, lstatErr) {
		t.Fatalf("expected current lstat error to be preserved, got %v", err)
	}
	assertAnalysisCacheDirExists(t, childPath)
}

func TestRollbackCreatedAnalysisCacheChildAtPathErrorBranches(t *testing.T) {
	t.Run("missing child returns nil", func(t *testing.T) {
		repo := t.TempDir()
		root := openAnalysisCacheTestRoot(t, repo)
		childPath := filepath.Join(repo, cacheKeysDirName)

		if err := rollbackCreatedAnalysisCacheChildAtPath(root, childPath, cacheKeysDirName, nil, true); err != nil {
			t.Fatalf("rollback missing child at path: %v", err)
		}
	})

	t.Run("remove failure is preserved", func(t *testing.T) {
		repo := t.TempDir()
		root := openAnalysisCacheTestRoot(t, repo)
		childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
		removeErr := errors.New("remove child failed")

		err := rollbackCreatedAnalysisCacheChildAtPath(
			&openReservationRemoveChildErrorAnalysisCacheRoot{
				Root:            root,
				reservationName: ".lopper-cache-rollback-keys-0",
				childName:       cacheKeysDirName,
				err:             removeErr,
			},
			childPath,
			cacheKeysDirName,
			childInfo,
			true,
		)
		if !errors.Is(err, removeErr) {
			t.Fatalf("expected remove error to be preserved, got %v", err)
		}
		assertAnalysisCachePathAbsent(t, childPath)
		quarantinePath := filepath.Join(repo, ".lopper-cache-rollback-keys-0", cacheKeysDirName)
		if info, statErr := os.Stat(quarantinePath); statErr != nil || !info.IsDir() {
			t.Fatalf("expected child to remain quarantined after failed rollback, info=%#v err=%v", info, statErr)
		}
	})
}

func TestConditionallyRemoveAnalysisCacheChildBranches(t *testing.T) {
	t.Run("nil child info noops", testConditionallyRemoveAnalysisCacheChildNilInfo)
	t.Run("missing current child noops", testConditionallyRemoveAnalysisCacheChildMissingCurrent)
	t.Run("post-verification replacement is restored and reported", testConditionallyRemoveAnalysisCacheChildRestoresReplacementRace)
	t.Run("pre-quarantine destination race preserves both entries", testConditionallyRemoveAnalysisCacheChildPreservesPreRenameQuarantineRace)
	t.Run("lstat and rename errors are joined", testConditionallyRemoveAnalysisCacheChildJoinsLstatAndRenameErrors)
	t.Run("failed quarantine removal does not overwrite replacement", testConditionallyRemoveAnalysisCacheChildRemoveFailurePreservesReplacement)
	t.Run("replacement reservation is not removed after quarantine cleanup", testConditionallyRemoveAnalysisCacheChildPreservesSwappedReservation)
	t.Run("successful rollback removes reservation directories", testConditionallyRemoveAnalysisCacheChildCleansReservations)
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
	assertAnalysisCachePathAbsent(t, childPath)
	assertAnalysisCacheSameFile(t, filepath.Join(repo, reservationName), wrappedRoot.replacementInfo)
	assertAnalysisCacheDirExists(t, filepath.Join(repo, "replacement-reservation", cacheKeysDirName))
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

	err = restoreMovedAnalysisCacheReplacement(
		wrappedRoot,
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
		if err := restoreMovedAnalysisCacheReplacement(root, analysisCacheQuarantineReservation{}, cacheKeysDirName, nil); err != nil {
			t.Fatalf("restore nil moved info: %v", err)
		}
	})

	t.Run("occupied target fails", func(t *testing.T) {
		repo := t.TempDir()
		root := openAnalysisCacheTestRoot(t, repo)
		_, movedInfo := createAnalysisCacheChild(t, repo, "moved-child")
		createAnalysisCacheChild(t, repo, cacheKeysDirName)

		err := restoreMovedAnalysisCacheReplacement(root, analysisCacheQuarantineReservation{}, cacheKeysDirName, movedInfo)
		if err == nil || !strings.Contains(err.Error(), "restore target occupied") {
			t.Fatalf("expected occupied restore target error, got %v", err)
		}
	})

	t.Run("target lstat failure is preserved", func(t *testing.T) {
		repo := t.TempDir()
		root := openAnalysisCacheTestRoot(t, repo)
		_, movedInfo := createAnalysisCacheChild(t, repo, "moved-child")
		lstatErr := errors.New("restore target lstat failed")

		err := restoreMovedAnalysisCacheReplacement(&lstatErrorAnalysisCacheRoot{Root: root, name: cacheKeysDirName, err: lstatErr}, analysisCacheQuarantineReservation{}, cacheKeysDirName, movedInfo)
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

		err := restoreMovedAnalysisCacheReplacement(root, newAnalysisCacheQuarantineReservation(reservationName, quarantineName, "token"), cacheKeysDirName, differentInfo)
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

	if err := restoreMovedAnalysisCacheReplacement(root, reservation, cacheKeysDirName, movedInfo); err != nil {
		t.Fatalf("restore moved replacement: %v", err)
	}

	assertAnalysisCacheSameFile(t, filepath.Join(repo, cacheKeysDirName), movedInfo)
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, reservation.ownerName))
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
	if err := os.Mkdir(filepath.Join(repo, quarantineName), 0o750); err != nil {
		t.Fatalf("create quarantined child: %v", err)
	}
	reservationRoot, err := safeio.OpenRoot(filepath.Join(repo, reservationName))
	if err != nil {
		t.Fatalf("open reservation root: %v", err)
	}
	removeErr := errors.New("remove owner failed")
	closeErr := errors.New("close reservation failed")

	err = removeAnalysisCacheQuarantine(
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
	if err := os.Mkdir(filepath.Join(repo, quarantineName), 0o750); err != nil {
		t.Fatalf("create quarantined child: %v", err)
	}
	reservationRoot, err := safeio.OpenRoot(filepath.Join(repo, reservationName))
	if err != nil {
		t.Fatalf("open reservation root: %v", err)
	}
	closeErr := errors.New("close reservation failed")

	err = removeAnalysisCacheQuarantine(
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

func TestNewAnalysisCacheObjectsDirInitFailureAddsWarning(t *testing.T) {
	repo := t.TempDir()
	cachePath := filepath.Join(repo, cacheDirName)
	keysDir := filepath.Join(cachePath, cacheKeysDirName)
	objectsPath := filepath.Join(cachePath, cacheObjectsDirName)
	if err := os.MkdirAll(keysDir, 0o750); err != nil {
		t.Fatalf("mkdir keys dir: %v", err)
	}
	if err := os.WriteFile(objectsPath, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatalf("write blocking objects file: %v", err)
	}

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: cachePath}}, repo)
	if cache.cacheable {
		t.Fatalf("expected cache to be non-cacheable when objects dir init fails")
	}
	if len(cache.takeWarnings()) == 0 {
		t.Fatalf("expected warning when objects dir init fails")
	}
}

func TestNewAnalysisCacheRejectsSymlinkedDefaultPathOutsideRepo(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	assertSymlinkedDefaultCachePathRejected(t, repo, outside, "symlinked")
}

func TestNewAnalysisCacheRejectsBrokenSymlinkedDefaultPathOutsideRepo(t *testing.T) {
	repo := t.TempDir()
	outside := filepath.Join(t.TempDir(), "missing-target")
	assertSymlinkedDefaultCachePathRejected(t, repo, outside, "broken symlinked")
}

func TestNewAnalysisCacheRejectsRootReplacementAfterValidation(t *testing.T) {
	repo := t.TempDir()
	cachePath := filepath.Join(repo, cacheDirName)
	outside := t.TempDir()
	if err := os.Symlink(outside, cachePath); err != nil {
		t.Skipf("replace cache root with symlink: %v", err)
	}

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: cachePath}}, repo)
	if cache.cacheable {
		t.Fatal("expected replaced cache root to fail closed")
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(outside, cacheKeysDirName))
	assertAnalysisCachePathAbsent(t, filepath.Join(outside, cacheObjectsDirName))
}

func TestNewAnalysisCacheRejectsSymlinkedCacheChild(t *testing.T) {
	repo := t.TempDir()
	cachePath := filepath.Join(repo, cacheDirName)
	outside := t.TempDir()
	if err := os.Mkdir(cachePath, 0o750); err != nil {
		t.Fatalf("create cache root: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(cachePath, cacheKeysDirName)); err != nil {
		t.Skipf("create cache child symlink: %v", err)
	}

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: cachePath}}, repo)
	if cache.cacheable {
		t.Fatal("expected symlinked cache child to fail closed")
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(outside, "key.json"))
}

func TestNewAnalysisCacheRejectsSymlinkedAncestorAndCleansTraversal(t *testing.T) {
	t.Run("symlinked ancestor", func(t *testing.T) {
		repo := t.TempDir()
		outside := t.TempDir()
		ancestor := filepath.Join(repo, "ancestor")
		if err := os.Symlink(outside, ancestor); err != nil {
			t.Skipf("symlink not supported: %v", err)
		}
		cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: filepath.Join(ancestor, "cache")}}, repo)
		if cache.cacheable {
			t.Fatal("expected symlinked cache ancestry to fail closed")
		}
		assertAnalysisCachePathAbsent(t, filepath.Join(outside, "cache"))
	})

	t.Run("traversal is cleaned before initialization", func(t *testing.T) {
		repo := t.TempDir()
		cachePath := filepath.Join(repo, "nested", "..", cacheDirName)
		mustMkdirCacheLayout(t, filepath.Join(repo, cacheDirName))
		cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: cachePath}}, repo)
		if !cache.cacheable {
			t.Fatalf("expected cleaned cache path to remain usable, warnings=%#v", cache.takeWarnings())
		}
		if _, err := os.Stat(filepath.Join(repo, cacheDirName, cacheKeysDirName)); err != nil {
			t.Fatalf("expected keys under cleaned cache root: %v", err)
		}
	})
}

func TestAnalysisCacheStoreRejectsRootReplacementBeforeMutation(t *testing.T) {
	repo := t.TempDir()
	cachePath := filepath.Join(repo, cacheDirName)
	mustMkdirCacheLayout(t, cachePath)
	outside := t.TempDir()
	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: cachePath}}, repo)
	if !cache.cacheable {
		t.Fatalf("expected cacheable setup, warnings=%#v", cache.takeWarnings())
	}
	if err := os.Rename(cachePath, filepath.Join(repo, "cache-holding")); err != nil {
		t.Fatalf("move cache root: %v", err)
	}
	if err := os.Symlink(outside, cachePath); err != nil {
		t.Skipf("replace cache root with symlink: %v", err)
	}

	err := cache.store(cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}, report.Report{RepoPath: repo})
	if err == nil {
		t.Fatal("expected root replacement before cache mutation to fail")
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(outside, cacheObjectsDirName))
	assertAnalysisCachePathAbsent(t, filepath.Join(outside, cacheKeysDirName))
}

func TestAnalysisCacheStorePreservesExistingObject(t *testing.T) {
	repo := t.TempDir()
	cachePath := filepath.Join(repo, cacheDirName)
	mustMkdirCacheLayout(t, cachePath)
	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: cachePath}}, repo)
	if !cache.cacheable {
		t.Fatalf("expected cacheable setup, warnings=%#v", cache.takeWarnings())
	}
	reportData := report.Report{RepoPath: repo}
	serializedPayload, err := json.Marshal(newCachedPayload(reportData))
	if err != nil {
		t.Fatalf("marshal cache payload: %v", err)
	}
	objectPath := filepath.Join(cachePath, cacheObjectsDirName, sha256Hex(serializedPayload)+".json")
	if err := os.WriteFile(objectPath, []byte("existing complete object"), 0o640); err != nil {
		t.Fatalf("seed cache object: %v", err)
	}

	if err := cache.store(cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}, reportData); err != nil {
		t.Fatalf("store cache report: %v", err)
	}
	if got, err := os.ReadFile(objectPath); err != nil {
		t.Fatalf("read preserved cache object: %v", err)
	} else if string(got) != "existing complete object" {
		t.Fatalf("existing cache object = %q, want preserved content", got)
	}
}

func TestAnalysisCacheOpenWriteRootFailureBranches(t *testing.T) {
	t.Run("open canonical write root failure after validation", testAnalysisCacheOpenWriteRootCanonicalOpenFailure)
	t.Run("opened canonical root must match pinned identity", testAnalysisCacheOpenWriteRootIdentityMismatch)
	t.Run("second validation after opening write root fails closed", testAnalysisCacheOpenWriteRootSecondValidationFailure)
}

func testAnalysisCacheOpenWriteRootCanonicalOpenFailure(t *testing.T) {
	fixture := newAnalysisCacheWriteRootFixture(t)
	renamedPath := filepath.Join(fixture.repo, "cache-renamed")

	hookValidateAnalysisCacheRootCall(t, fixture.cachePath, func(call int) (bool, error) {
		if call != 1 {
			return false, nil
		}
		if err := os.Rename(fixture.cachePath, renamedPath); err != nil {
			t.Fatalf("rename cache root before canonical open: %v", err)
		}
		return true, nil
	})

	if _, err := fixture.cache.openWriteRoot(); err == nil || !strings.Contains(err.Error(), "open canonical root") {
		t.Fatalf("open write root error = %v, want canonical open failure", err)
	}
}

func testAnalysisCacheOpenWriteRootIdentityMismatch(t *testing.T) {
	fixture := newAnalysisCacheWriteRootFixture(t)
	renamedPath := filepath.Join(fixture.repo, "cache-renamed")

	hookValidateAnalysisCacheRootCall(t, fixture.cachePath, func(call int) (bool, error) {
		if call != 1 {
			return false, nil
		}
		if err := os.Rename(fixture.cachePath, renamedPath); err != nil {
			t.Fatalf("rename cache root before canonical open: %v", err)
		}
		mustMkdirCacheLayout(t, fixture.cachePath)
		return true, nil
	})

	if _, err := fixture.cache.openWriteRoot(); err == nil || !strings.Contains(err.Error(), "pinned root identity changed") {
		t.Fatalf("open write root identity error = %v, want identity mismatch", err)
	}
}

func testAnalysisCacheOpenWriteRootSecondValidationFailure(t *testing.T) {
	fixture := newAnalysisCacheWriteRootFixture(t)
	validationErr := errors.New("post-open validation failed")

	hookValidateAnalysisCacheRootCall(t, fixture.cachePath, func(call int) (bool, error) {
		if call == 2 {
			return true, validationErr
		}
		return false, nil
	})

	if _, err := fixture.cache.openWriteRoot(); !errors.Is(err, validationErr) {
		t.Fatalf("open write root error = %v, want %v", err, validationErr)
	}
}

func TestCachePathEscapesRepoHandlesResolvedAndMissingPaths(t *testing.T) {
	repo := t.TempDir()
	inside := filepath.Join(repo, cacheDirName)
	if err := os.Mkdir(inside, 0o750); err != nil {
		t.Fatalf("create inside cache path: %v", err)
	}
	if cachePathEscapesRepo(inside, repo) {
		t.Fatal("expected cache path inside repository to be accepted")
	}
	if !cachePathEscapesRepo(t.TempDir(), repo) {
		t.Fatal("expected cache path outside repository to be rejected")
	}
	if !cachePathEscapesRepo(inside, filepath.Join(repo, "missing-repository")) {
		t.Fatal("expected cache path to be rejected when its repository root is missing")
	}
}

func TestResolveCacheOptionsUsesResolvedPath(t *testing.T) {
	resolvedPath := filepath.Join(t.TempDir(), "resolved-cache")
	options := resolveCacheOptions(&CacheOptions{Enabled: true, Path: "requested-cache", ResolvedPath: resolvedPath}, "repository")
	if options.Path != resolvedPath {
		t.Fatalf("cache path = %q, want resolved path %q", options.Path, resolvedPath)
	}
}

func TestAnalysisCacheDigestSerializationErrors(t *testing.T) {
	dir := t.TempDir()
	existingPath := filepath.Join(dir, "cache-input.go")
	if err := os.WriteFile(existingPath, []byte("cache input"), 0o600); err != nil {
		t.Fatalf("write cache input: %v", err)
	}
	if _, err := hashJSON(make(chan struct{})); err == nil {
		t.Fatal("expected unsupported digest value to fail JSON serialization")
	}
	if err := writeFileDigest(&cacheFailAfterWriter{failOn: 1}, existingPath); err == nil {
		t.Fatal("expected digest write failure")
	}
	if err := writeFileDigestOrMissing(&cacheFailAfterWriter{failOn: 1}, filepath.Join(t.TempDir(), cacheMissingFileName)); err == nil {
		t.Fatal("expected missing digest marker write failure")
	}
	if _, err := hashFile(filepath.Join(dir, cacheMissingFileName)); err == nil {
		t.Fatal("expected missing file hash to fail")
	}
	if _, err := hashFileDigest(dir); err == nil {
		t.Fatal("expected directory digest to fail")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read cache input directory: %v", err)
	}
	files := make([]cacheRelevantFile, 0)
	if err := collectRelevantFile("\x00", existingPath, entries[0], nil, &files); err == nil {
		t.Fatal("expected invalid cache root to reject relevant file")
	}
}

func TestAnalysisCacheLookupRejectsMissingDefaultCacheRoot(t *testing.T) {
	cache := &analysisCache{
		options:         resolvedCacheOptions{Enabled: true, Path: filepath.Join(t.TempDir(), cacheDirName)},
		cacheable:       true,
		rejectReadHits:  true,
		metadata:        report.CacheMetadata{},
		inputDigestMemo: make(map[cacheInputDigestMemoKey]string),
	}

	if _, hit, err := cache.lookup(cacheEntryDescriptor{KeyDigest: "key", KeyLabel: "adapter:root"}); err == nil || hit {
		t.Fatalf("lookup with a missing no-follow cache root = hit=%v err=%v, want error", hit, err)
	}
}

func TestAnalysisCacheLookupRejectsUsageIncompleteIndexOutsideReport(t *testing.T) {
	cache, entry := cacheWithPayloadForLookupTest(t, cachedPayload{UsageIncompleteDependencies: []int{0}}, "invalid-usage-incomplete-index")

	assertLookupMissWithReason(t, cache, entry, cacheObjectCorruptReason)
}

func TestCachePointerExistsRejectsNonDirectoryKeysPath(t *testing.T) {
	cachePath := t.TempDir()
	if err := os.WriteFile(filepath.Join(cachePath, cacheKeysDirName), []byte("not-a-directory"), 0o600); err != nil {
		t.Fatalf("write keys path: %v", err)
	}

	if _, err := cachePointerExists(cachePath, "key"); err == nil {
		t.Fatal("expected non-directory keys path to fail cache pointer lookup")
	}
}

func assertAnalysisCachePathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %q to remain absent, stat err=%v", path, err)
	}
}

func assertAnalysisCacheDirExists(t *testing.T, path string) {
	t.Helper()
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("expected %q to be a directory, info=%#v err=%v", path, info, err)
	}
}

func assertAnalysisCacheSameFile(t *testing.T, path string, want fs.FileInfo) {
	t.Helper()
	got, err := os.Lstat(path)
	if err != nil || !os.SameFile(got, want) {
		t.Fatalf("expected %q to keep identity, got=%#v want=%#v err=%v", path, got, want, err)
	}
}

func assertAnalysisCacheQuarantineSuffix(t *testing.T, quarantineName, suffix string) {
	t.Helper()
	if !strings.HasSuffix(filepath.Dir(quarantineName), suffix) {
		t.Fatalf("expected quarantine suffix %s, got %q", suffix, quarantineName)
	}
}

func assertSymlinkedDefaultCachePathRejected(t *testing.T, repo, outside, description string) {
	t.Helper()

	symlinkPath := filepath.Join(repo, cacheDirName)
	if err := os.Symlink(outside, symlinkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	cache := newAnalysisCache(Request{}, repo)
	if cache.cacheable {
		t.Fatalf("expected %s default cache path to be rejected", description)
	}
	warnings := cache.takeWarnings()
	if len(warnings) == 0 || !strings.Contains(warnings[0], "cache path escapes repository root") {
		t.Fatalf("expected %s cache path warning, got %#v", description, warnings)
	}
	if _, err := os.Stat(filepath.Join(outside, cacheKeysDirName)); !os.IsNotExist(err) {
		t.Fatalf("expected no keys dir to be created outside repo, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, cacheObjectsDirName)); !os.IsNotExist(err) {
		t.Fatalf("expected no objects dir to be created outside repo, stat err=%v", err)
	}
}

func TestLockOrConfigFileRecognizesGradleVersionCatalogs(t *testing.T) {
	if !lockOrConfigFile("libs.versions.toml") {
		t.Fatalf("expected Gradle version catalogs to participate in cache invalidation")
	}
	if lockOrConfigFile("README.md") {
		t.Fatalf("did not expect README.md to be treated as a cache-relevant config file")
	}
}

func TestAnalysisCacheCollectsPHPShortOpenTagConfigs(t *testing.T) {
	repo := t.TempDir()
	for _, filename := range []string{"php.ini", ".user.ini", ".htaccess"} {
		mustWriteFile(t, filepath.Join(repo, filename), []byte("short_open_tag = On\n"))
	}

	cache := &analysisCache{}
	records, err := cache.collectRelevantFiles(repo)
	if err != nil {
		t.Fatalf("collect relevant files: %v", err)
	}
	collected := make(map[string]struct{}, len(records))
	for _, record := range records {
		collected[record.relativePath] = struct{}{}
	}
	for _, filename := range []string{"php.ini", ".user.ini", ".htaccess"} {
		if _, ok := collected[filename]; !ok {
			t.Fatalf("expected %s to participate in cache invalidation, got %#v", filename, collected)
		}
	}
}

func TestAnalysisCachePHPShortOpenTagConfigChangesInvalidateInputDigest(t *testing.T) {
	for _, filename := range []string{"php.ini", ".user.ini", ".htaccess"} {
		t.Run(filename, func(t *testing.T) {
			repo := t.TempDir()
			configPath := filepath.Join(repo, filename)
			mustWriteFile(t, configPath, []byte("short_open_tag = Off\n"))

			cache := &analysisCache{}
			before, err := cache.computeInputDigest(repo, "")
			if err != nil {
				t.Fatalf("compute digest before config update: %v", err)
			}
			mustWriteFile(t, configPath, []byte("short_open_tag = On\n"))
			after, err := cache.computeInputDigest(repo, "")
			if err != nil {
				t.Fatalf("compute digest after config update: %v", err)
			}
			if before == after {
				t.Fatalf("expected %s update to invalidate the input digest", filename)
			}
		})
	}
}

func TestAnalysisCachePHPShortOpenTagTraversalCutoffInvalidatesInputDigest(t *testing.T) {
	repo := t.TempDir()
	mustWriteFile(t, filepath.Join(repo, "z-config", "php.ini"), []byte("short_open_tag = On\n"))

	cache := &analysisCache{}
	before, err := cache.computeInputDigest(repo, "")
	if err != nil {
		t.Fatalf("compute digest before traversal change: %v", err)
	}
	for i := 0; i < shared.PHPShortOpenTagConfigWalkEntryLimit; i++ {
		if err := os.Mkdir(filepath.Join(repo, fmt.Sprintf("a-%04d", i)), 0o750); err != nil {
			t.Fatalf("create traversal entry %d: %v", i, err)
		}
	}
	after, err := cache.computeInputDigest(repo, "")
	if err != nil {
		t.Fatalf("compute digest after traversal change: %v", err)
	}
	if before == after {
		t.Fatal("expected PHP short_open_tag traversal cutoff to invalidate the input digest")
	}
}

func TestAnalysisCacheExplicitRuntimeTraceExcludesOnlyTraceArtifacts(t *testing.T) {
	repo := t.TempDir()
	tracePath := filepath.Join("tests", "trace.ndjson")
	sourcePath := filepath.Join(repo, "tests", "source.php")
	mustWriteFile(t, sourcePath, []byte("<?php echo 'before';\n"))

	cache := &analysisCache{}
	exclusions := cache.cacheAnalysisExclusions(repo, Request{RuntimeTracePath: tracePath})
	before, err := cache.computeInputDigestWithExclusions(repo, "", exclusions)
	if err != nil {
		t.Fatalf("compute digest before trace artifacts: %v", err)
	}
	mustWriteFile(t, filepath.Join(repo, tracePath), []byte("{\"module\":\"example\"}\n"))
	mustWriteFile(t, runtime.TraceStatePath(filepath.Join(repo, tracePath)), []byte("{\"schema\":\"v2\"}\n"))
	afterTrace, err := cache.computeInputDigestWithExclusions(repo, "", exclusions)
	if err != nil {
		t.Fatalf("compute digest after trace artifacts: %v", err)
	}
	if before != afterTrace {
		t.Fatal("expected generated runtime trace artifacts not to invalidate the static input digest")
	}
	mustWriteFile(t, sourcePath, []byte("<?php echo 'after';\n"))
	afterSource, err := cache.computeInputDigestWithExclusions(repo, "", exclusions)
	if err != nil {
		t.Fatalf("compute digest after source change: %v", err)
	}
	if afterTrace == afterSource {
		t.Fatal("expected source beside an explicit runtime trace to invalidate the input digest")
	}
}

func TestAnalysisCacheRuntimeTraceResolvesRelativeToRepoRootForNestedCandidateRoots(t *testing.T) {
	repo := t.TempDir()
	nestedRoot := filepath.Join(repo, "pkg")
	tracePath := filepath.Join("pkg", "tests", "trace.ndjson")
	sourcePath := filepath.Join(nestedRoot, "tests", "source.php")
	mustWriteFile(t, sourcePath, []byte("<?php echo 'before';\n"))

	cache := &analysisCache{}
	req := Request{RuntimeTracePath: tracePath}

	exclusions := cache.cacheAnalysisExclusions(nestedRoot, req, repo)
	before, err := cache.computeInputDigestWithExclusions(nestedRoot, "", exclusions)
	if err != nil {
		t.Fatalf("compute digest before trace artifacts: %v", err)
	}
	mustWriteFile(t, filepath.Join(repo, tracePath), []byte("{\"module\":\"example\"}\n"))
	after, err := cache.computeInputDigestWithExclusions(nestedRoot, "", exclusions)
	if err != nil {
		t.Fatalf("compute digest after trace artifacts: %v", err)
	}
	if before != after {
		t.Fatal("expected a relative runtime trace path to resolve against the repository root, excluding trace artifacts from a nested candidate root's digest")
	}
}

func TestAnalysisCacheRuntimeTraceExclusionRemapsIntoScopedWorkspace(t *testing.T) {
	trueRepo := t.TempDir()
	scopedRoot := t.TempDir()
	tracePath := filepath.Join("tests", "trace.ndjson")
	sourcePath := filepath.Join(scopedRoot, "tests", "source.php")
	mustWriteFile(t, sourcePath, []byte("<?php echo 'before';\n"))

	cache := &analysisCache{stableRepoPath: trueRepo, analysisRepoPath: scopedRoot}
	req := Request{RuntimeTracePath: tracePath}

	exclusions := cache.cacheAnalysisExclusions(scopedRoot, req, trueRepo)
	before, err := cache.computeInputDigestWithExclusions(scopedRoot, "", exclusions)
	if err != nil {
		t.Fatalf("compute digest before trace artifacts: %v", err)
	}
	mustWriteFile(t, filepath.Join(scopedRoot, tracePath), []byte("{\"module\":\"example\"}\n"))
	after, err := cache.computeInputDigestWithExclusions(scopedRoot, "", exclusions)
	if err != nil {
		t.Fatalf("compute digest after trace artifacts: %v", err)
	}
	if before != after {
		t.Fatal("expected a runtime trace exclusion resolved against the true repo root to be remapped into a scoped workspace copy's candidate root")
	}
}

func TestHashFileOrMissingAndWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	missingPath := filepath.Join(dir, cacheMissingFileName)
	digest, err := hashFileOrMissing(missingPath)
	if err != nil {
		t.Fatalf("hash missing file: %v", err)
	}
	if digest != "missing" {
		t.Fatalf("expected missing digest marker, got %q", digest)
	}

	targetPath := filepath.Join(dir, "nested", "file.txt")
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o750); err != nil {
		t.Fatalf("mkdir target parent: %v", err)
	}
	if err := os.WriteFile(targetPath, []byte("before"), 0o644); err != nil {
		t.Fatalf("seed target file: %v", err)
	}
	if err := os.Chmod(targetPath, 0o644); err != nil {
		t.Fatalf("chmod target file: %v", err)
	}
	if err := writeFileAtomic(targetPath, []byte("hello")); err != nil {
		t.Fatalf("write file atomic: %v", err)
	}
	digest, err = hashFileOrMissing(targetPath)
	if err != nil {
		t.Fatalf("hash existing file: %v", err)
	}
	if digest == "" || digest == "missing" {
		t.Fatalf("expected real digest for existing file, got %q", digest)
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("stat target file: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("expected existing file mode 0644 to be preserved, got %#o", info.Mode().Perm())
	}
}

func TestWriteFileDigestAndMissingMarker(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "tracked.txt")
	if err := os.WriteFile(targetPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}

	var existing bytes.Buffer
	if err := writeFileDigest(&existing, targetPath); err != nil {
		t.Fatalf("write existing digest: %v", err)
	}
	if len(strings.TrimSpace(existing.String())) != 64 {
		t.Fatalf("expected SHA-256 hex digest, got %q", existing.String())
	}

	var missing bytes.Buffer
	if err := writeFileDigestOrMissing(&missing, filepath.Join(dir, cacheMissingFileName)); err != nil {
		t.Fatalf("write missing digest marker: %v", err)
	}
	if missing.String() != "missing" {
		t.Fatalf("expected missing marker, got %q", missing.String())
	}
}

func TestHashFileOrMissingReturnsErrorForUnreadablePath(t *testing.T) {
	dir := t.TempDir()
	if _, err := hashFileOrMissing(dir); err == nil {
		t.Fatalf("expected hashFileOrMissing to fail for directory path")
	}
}

func TestAnalysisCacheLookupBranches(t *testing.T) {
	cacheDir := t.TempDir()
	mustMkdirCacheLayout(t, cacheDir)

	cache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: cacheDir}, cacheable: true}
	entry := cacheEntryDescriptor{KeyLabel: "js-ts:/repo", KeyDigest: "key", InputDigest: "input-current"}
	pointerPath := filepath.Join(cacheDir, cacheKeysDirName, entry.KeyDigest+".json")

	cases := []analysisCacheLookupCase{
		{
			name: "pointer-corrupt",
			setup: func(t *testing.T) {
				mustWriteFile(t, pointerPath, []byte("{bad-json"))
			},
			wantReason: "pointer-corrupt",
		},
		{
			name: "input-changed",
			setup: func(t *testing.T) {
				mustWritePointer(t, pointerPath, cachePointer{InputDigest: "input-old", ObjectDigest: "obj"})
			},
			wantReason: "input-changed",
		},
		{
			name: "object-missing",
			setup: func(t *testing.T) {
				mustWritePointer(t, pointerPath, cachePointer{InputDigest: entry.InputDigest, ObjectDigest: "missing-object"})
			},
			wantReason: "object-missing",
		},
		{
			name: "object-corrupt",
			setup: func(t *testing.T) {
				mustWritePointer(t, pointerPath, cachePointer{InputDigest: entry.InputDigest, ObjectDigest: "obj-corrupt"})
				mustWriteFile(t, filepath.Join(cacheDir, cacheObjectsDirName, "obj-corrupt.json"), []byte("{not-json"))
			},
			wantReason: "object-corrupt",
		},
		{
			name: "hit",
			setup: func(t *testing.T) {
				mustWriteCachedObject(t, cacheDir, "obj-hit", report.Report{RepoPath: "repo"})
				mustWritePointer(t, pointerPath, cachePointer{InputDigest: entry.InputDigest, ObjectDigest: "obj-hit"})
			},
			wantHit:      true,
			wantRepoPath: "repo",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cache.metadata.Invalidations = nil
			tc.setup(t)
			got, hit, err := cache.lookup(entry)
			assertLookupCaseOutcome(t, cache.metadata.Invalidations, tc, got, hit, err)
		})
	}
}

func assertLookupCaseOutcome(t *testing.T, invalidations []report.CacheInvalidation, tc analysisCacheLookupCase, got report.Report, hit bool, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("lookup error: %v", err)
	}
	if hit != tc.wantHit {
		t.Fatalf("unexpected hit state: got %v want %v", hit, tc.wantHit)
	}
	if tc.wantRepoPath != "" && got.RepoPath != tc.wantRepoPath {
		t.Fatalf("unexpected cached report: %#v", got)
	}
	if tc.wantReason == "" {
		return
	}
	if len(invalidations) == 0 || invalidations[len(invalidations)-1].Reason != tc.wantReason {
		t.Fatalf("expected invalidation reason %q, got %#v", tc.wantReason, invalidations)
	}
}

func TestAnalysisCacheStoreAndFileCollectionBranches(t *testing.T) {
	repo := t.TempDir()
	cacheDir := filepath.Join(repo, cacheDirName)
	mustMkdirCacheLayout(t, cacheDir)

	entry := cacheEntryDescriptor{KeyDigest: "readonly-key", InputDigest: "readonly-input"}
	readOnlyCache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: cacheDir, ReadOnly: true}, cacheable: true}
	if err := readOnlyCache.store(entry, report.Report{RepoPath: "repo"}); err != nil {
		t.Fatalf("readonly store should no-op, got %v", err)
	}
	if readOnlyCache.metadata.Writes != 0 {
		t.Fatalf("expected no writes in readonly mode, got %d", readOnlyCache.metadata.Writes)
	}

	writableCache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: cacheDir}, cacheable: true}
	if err := writableCache.store(entry, report.Report{RepoPath: "repo"}); err != nil {
		t.Fatalf("writable store: %v", err)
	}
	if writableCache.metadata.Writes != 1 {
		t.Fatalf("expected write count 1, got %d", writableCache.metadata.Writes)
	}

	ignoredDir := filepath.Join(repo, ".git")
	if err := os.MkdirAll(ignoredDir, 0o750); err != nil {
		t.Fatalf("mkdir ignored dir: %v", err)
	}
	mustWriteFile(t, filepath.Join(ignoredDir, "config"), []byte("x"))
	mustWriteFile(t, filepath.Join(repo, cacheTestGoModName), []byte(cacheTestGoModContent))

	records, err := writableCache.collectRelevantFiles(repo)
	if err != nil {
		t.Fatalf("collect relevant files: %v", err)
	}
	if len(records) == 0 {
		t.Fatalf("expected at least one relevant file record")
	}
}

func TestAnalysisCacheStoreReplacesExistingFilesPreservingMode(t *testing.T) {
	cacheDir := t.TempDir()
	mustMkdirCacheLayout(t, cacheDir)

	entry := cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}
	rep := report.Report{RepoPath: "repo"}
	payload := cachedPayload{Report: rep}
	serializedPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal cached payload: %v", err)
	}
	objectDigest := sha256Hex(serializedPayload)
	objectPath := filepath.Join(cacheDir, cacheObjectsDirName, objectDigest+".json")
	pointerPath := filepath.Join(cacheDir, cacheKeysDirName, entry.KeyDigest+".json")

	mustWriteFile(t, objectPath, []byte("old-object"))
	mustWriteFile(t, pointerPath, []byte("old-pointer"))
	if err := os.Chmod(objectPath, 0o644); err != nil {
		t.Fatalf("chmod object path: %v", err)
	}
	if err := os.Chmod(pointerPath, 0o640); err != nil {
		t.Fatalf("chmod pointer path: %v", err)
	}

	cache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: cacheDir}, cacheable: true}
	if err := cache.store(entry, rep); err != nil {
		t.Fatalf("store cache entry: %v", err)
	}

	var pointer cachePointer
	pointerData, err := os.ReadFile(pointerPath)
	if err != nil {
		t.Fatalf("read pointer file: %v", err)
	}
	if err := json.Unmarshal(pointerData, &pointer); err != nil {
		t.Fatalf("unmarshal pointer file: %v", err)
	}
	if pointer.ObjectDigest != objectDigest {
		t.Fatalf("unexpected object digest: got %q want %q", pointer.ObjectDigest, objectDigest)
	}
	objectInfo, err := os.Stat(objectPath)
	if err != nil {
		t.Fatalf("stat object path: %v", err)
	}
	if objectInfo.Mode().Perm() != 0o644 {
		t.Fatalf("expected object mode 0644 to be preserved, got %#o", objectInfo.Mode().Perm())
	}
	pointerInfo, err := os.Stat(pointerPath)
	if err != nil {
		t.Fatalf("stat pointer path: %v", err)
	}
	if pointerInfo.Mode().Perm() != 0o640 {
		t.Fatalf("expected pointer mode 0640 to be preserved, got %#o", pointerInfo.Mode().Perm())
	}
}

func TestAnalysisCacheHelperErrorBranches(t *testing.T) {
	t.Run("prepare entry and hash json error", testAnalysisCachePrepareEntryAndHashJSONError)
	t.Run("write atomic and hash file errors", testAnalysisCacheWriteAtomicAndHashFileErrors)
	t.Run("prepare and load cache warnings", testAnalysisCachePrepareAndLoadWarnings)
	t.Run("store cached report warning branch", testAnalysisCacheStoreCachedReportWarningBranch)
	t.Run("new cache disabled", testAnalysisCacheNewCacheDisabled)
}

func testAnalysisCachePrepareEntryAndHashJSONError(t *testing.T) {
	repo := t.TempDir()
	root := mustCreateRootWithGoMod(t, repo, "pkg")
	configPath := filepath.Join(repo, ".lopper.yml")
	mustWriteFile(t, configPath, []byte("thresholds: {}\n"))

	cache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: filepath.Join(repo, cacheDirName)}, cacheable: true}
	entry, err := cache.prepareEntry(Request{Dependency: "lodash", TopN: 1, RuntimeProfile: "node-import", ConfigPath: configPath, LowConfidenceWarningPercent: intPtr(30), MinUsagePercentForRecommendations: intPtr(40), RemovalCandidateWeights: &report.RemovalCandidateWeights{Usage: 0.5, Impact: 0.3, Confidence: 0.2}}, "js-ts", root)
	if err != nil {
		t.Fatalf("prepare entry: %v", err)
	}
	if entry.KeyDigest == "" || entry.InputDigest == "" {
		t.Fatalf("expected non-empty cache entry digests: %#v", entry)
	}
	if _, err := hashJSON(map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatalf("expected hashJSON to fail for unsupported value")
	}
}

func testAnalysisCacheWriteAtomicAndHashFileErrors(t *testing.T) {
	dir := t.TempDir()
	targetDir := filepath.Join(dir, "target")
	if err := os.MkdirAll(targetDir, 0o750); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if writeFileAtomic(targetDir, []byte("x")) == nil {
		t.Fatalf("expected writeFileAtomic to fail when target is directory")
	}
	if _, err := hashFile(targetDir); err == nil {
		t.Fatalf("expected hashFile to fail for directory")
	}
}

func testAnalysisCachePrepareAndLoadWarnings(t *testing.T) {
	repo := t.TempDir()
	cacheDir := filepath.Join(repo, cacheDirName)
	mustMkdirCacheLayout(t, cacheDir)
	cache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: cacheDir}, cacheable: true}

	_, _, hit := prepareAndLoadCachedReport(Request{RepoPath: repo, Dependency: "dep"}, cache, "js-ts", filepath.Join(repo, "missing-root"))
	if hit {
		t.Fatalf("did not expect cache hit when prepare entry fails")
	}
	if len(cache.takeWarnings()) == 0 {
		t.Fatalf("expected warning when prepare entry fails")
	}

	root := mustCreateRootWithGoMod(t, repo, "root")
	entry, err := cache.prepareEntry(Request{RepoPath: repo, Dependency: "dep"}, "js-ts", root)
	if err != nil {
		t.Fatalf("prepare entry: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cacheDir, cacheKeysDirName, entry.KeyDigest+".json"), 0o750); err != nil {
		t.Fatalf("mkdir pointer as dir: %v", err)
	}
	_, _, hit = prepareAndLoadCachedReport(Request{RepoPath: repo, Dependency: "dep"}, cache, "js-ts", root)
	if hit {
		t.Fatalf("did not expect cache hit on lookup error")
	}
	if warnings := strings.Join(cache.takeWarnings(), "\n"); !strings.Contains(warnings, "lookup failed") {
		t.Fatalf("expected lookup warning, got %q", warnings)
	}
}

func testAnalysisCacheStoreCachedReportWarningBranch(t *testing.T) {
	repo := t.TempDir()
	cache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: filepath.Join(repo, "cache-as-file")}, cacheable: true}
	mustWriteFile(t, cache.options.Path, []byte("x"))
	storeCachedReport(cache, "js-ts", repo, cacheEntryDescriptor{KeyDigest: "k", InputDigest: "i"}, report.Report{})
	if len(cache.takeWarnings()) == 0 {
		t.Fatalf("expected cache store warning on invalid path")
	}
	storeCachedReport(cache, "js-ts", repo, cacheEntryDescriptor{}, report.Report{})
	if len(cache.takeWarnings()) != 0 {
		t.Fatalf("expected no warning for empty key digest")
	}
}

func testAnalysisCacheNewCacheDisabled(t *testing.T) {
	repo := t.TempDir()
	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: false}}, repo)
	if cache.cacheable {
		t.Fatalf("expected disabled cache to be non-cacheable")
	}
	if cache.metadata.Enabled {
		t.Fatalf("expected metadata to mark cache disabled")
	}
}

func TestCollectRelevantFileWalkError(t *testing.T) {
	files := make([]cacheRelevantFile, 0)
	root := t.TempDir()
	if collectRelevantFile(root, filepath.Join(root, "missing"), nil, errors.New("walk failure"), &files) == nil {
		t.Fatalf("expected collectRelevantFile to return walk error")
	}
}

func TestCacheServiceBranchWithNoRootSeen(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('x')\n")
	adapter := &countingAdapter{id: "cachelang"}
	reg := language.NewRegistry()
	if err := reg.Register(adapter); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	svc := &Service{Registry: reg}
	if _, err := svc.Analyse(context.Background(), Request{
		RepoPath: repo,
		Language: "cachelang",
		TopN:     1,
		Cache:    &CacheOptions{Enabled: true, Path: filepath.Join(repo, "cache")},
	}); err != nil {
		t.Fatalf("analyse with cache branch: %v", err)
	}
}

func mustMkdirCacheLayout(t *testing.T, cacheDir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(cacheDir, cacheKeysDirName), 0o750); err != nil {
		t.Fatalf("mkdir keys: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cacheDir, cacheObjectsDirName), 0o750); err != nil {
		t.Fatalf("mkdir objects: %v", err)
	}
}

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustWritePointer(t *testing.T, pointerPath string, pointer cachePointer) {
	t.Helper()
	payload, err := json.Marshal(pointer)
	if err != nil {
		t.Fatalf("marshal pointer: %v", err)
	}
	mustWriteFile(t, pointerPath, payload)
}

func mustWriteCachedObject(t *testing.T, cacheDir string, objectDigest string, data report.Report) {
	t.Helper()
	payload, err := json.Marshal(cachedPayload{Report: data})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	mustWriteFile(t, filepath.Join(cacheDir, cacheObjectsDirName, objectDigest+".json"), payload)
}

func mustCreateRootWithGoMod(t *testing.T, repo, dirName string) string {
	t.Helper()
	root := filepath.Join(repo, dirName)
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	mustWriteFile(t, filepath.Join(root, cacheTestGoModName), []byte(cacheTestGoModContent))
	return root
}

func intPtr(value int) *int { return &value }
