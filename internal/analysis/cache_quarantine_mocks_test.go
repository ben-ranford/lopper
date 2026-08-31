package analysis

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
)

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

// onceCloseErrorOnOpenRootAnalysisCacheRoot fails the Close of exactly one
// OpenRoot(name) result -- the first -- and returns the real root
// thereafter, so a test can inject a close failure at one specific point in
// a multi-step flow (e.g. the quarantine rename+verify's own reservation
// handle) without also breaking a later, independent reopen of the same
// name (e.g. cleanup's own openOwnedAnalysisCacheQuarantineReservation).
type onceCloseErrorOnOpenRootAnalysisCacheRoot struct {
	safeio.Root
	name    string
	err     error
	applied bool
}

func (r *onceCloseErrorOnOpenRootAnalysisCacheRoot) OpenRoot(name string) (safeio.Root, error) {
	next, err := r.Root.OpenRoot(name)
	if err != nil || name != r.name || r.applied {
		return next, err
	}
	r.applied = true
	return &closeErrorAnalysisCacheRoot{Root: next, err: r.err}, nil
}

func (r *onceCloseErrorOnOpenRootAnalysisCacheRoot) RenameNoReplace(oldName, newName string) error {
	return safeio.RenameNoReplace(r.Root, oldName, newName)
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

type secondLstatErrorAnalysisCacheRoot struct {
	safeio.Root
	name  string
	err   error
	calls int
}

func (r *secondLstatErrorAnalysisCacheRoot) Lstat(name string) (fs.FileInfo, error) {
	if name == r.name {
		r.calls++
		if r.calls >= 2 {
			return nil, r.err
		}
	}
	return r.Root.Lstat(name)
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

func openQuarantinedChildReservationRoot(t *testing.T, repo, reservationName, quarantineName string) safeio.Root {
	t.Helper()
	if err := os.Mkdir(filepath.Join(repo, quarantineName), 0o750); err != nil {
		t.Fatalf("create quarantined child: %v", err)
	}
	reservationRoot, err := safeio.OpenRoot(filepath.Join(repo, reservationName))
	if err != nil {
		t.Fatalf("open reservation root: %v", err)
	}
	return reservationRoot
}
