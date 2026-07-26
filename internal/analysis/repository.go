package analysis

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ben-ranford/lopper/internal/safeio"
)

// RepositoryAuthorization is an opaque repository identity established at the
// path-validation boundary.
type RepositoryAuthorization struct {
	self  *RepositoryAuthorization
	state *repositoryAuthState
	nonce uint64
}

// RepositoryView retains a no-follow repository handle and a stable execution
// snapshot derived from that handle.
type RepositoryView struct {
	state         *repositoryAuthState
	root          safeio.Root
	executionRoot safeio.Root
	executionPath string
	warnings      []string
	gitMetadata   RepositoryGitMetadata
	closeOnce     sync.Once
	closeErr      error
}

var repositoryViewHandleOpenedFn = func() error { return nil }
var repositoryViewCloseFn = func() error { return nil }
var repositoryAuthorizationNonce uint64

// SetRepositoryViewHandleOpenedHookForTest replaces the retained-handle hook
// used by repository-opening tests and returns a restore function.
func SetRepositoryViewHandleOpenedHookForTest(hook func() error) func() {
	previous := repositoryViewHandleOpenedFn
	if hook == nil {
		hook = func() error { return nil }
	}
	repositoryViewHandleOpenedFn = hook
	return func() {
		repositoryViewHandleOpenedFn = previous
	}
}

// SetRepositoryViewCloseHookForTest replaces the retained-view close hook used
// by ownership and error-propagation tests and returns a restore function.
func SetRepositoryViewCloseHookForTest(hook func() error) func() {
	previous := repositoryViewCloseFn
	if hook == nil {
		hook = func() error { return nil }
	}
	repositoryViewCloseFn = hook
	return func() {
		repositoryViewCloseFn = previous
	}
}

// ResolveTrustedRepository establishes an immutable canonical repository
// identity. Filesystem canonicalization happens only in this function.
func ResolveTrustedRepository(repoPath string) (*RepositoryAuthorization, error) {
	paths, err := resolveTrustedRepoPaths(repoPath)
	if err != nil {
		return nil, err
	}
	return newRepositoryAuthorization(&repositoryAuthState{
		paths: paths,
		nonce: atomic.AddUint64(&repositoryAuthorizationNonce, 1),
	}), nil
}

// ResolveAuthorizedRepository reuses an existing repository authorization or a
// trusted cache-bound repository identity when available before falling back to
// a fresh authorization for repoPath.
func ResolveAuthorizedRepository(repoPath string, repository *RepositoryAuthorization, cache *CacheOptions) (*RepositoryAuthorization, error) {
	if repository != nil {
		if err := useTrustedRepository(repoPath, repository); err != nil {
			return nil, err
		}
		return repository, nil
	}
	if cache != nil && cache.hasTrustedPin() {
		repository = newRepositoryAuthorization(cache.trustedPin.repositoryState)
		if err := useTrustedRepository(repoPath, repository); err != nil {
			return nil, err
		}
		return repository, nil
	}
	return ResolveTrustedRepository(repoPath)
}

// TrustedRepositoryPath returns the normalized path supplied at authorization.
func TrustedRepositoryPath(repository *RepositoryAuthorization) string {
	if repository == nil {
		return ""
	}
	if state := repository.authorizationState(); state != nil {
		return state.paths.requestedPath
	}
	return ""
}

func useTrustedRepository(repoPath string, repository *RepositoryAuthorization) error {
	state := repository.authorizationState()
	if state == nil || state.paths.canonicalInfo == nil {
		return errors.New("trusted repository authorization is required")
	}
	normalizedRepoPath, err := normalizeTrustedRepoPath(repoPath)
	if err != nil {
		return err
	}
	if !repository.matchesPath(normalizedRepoPath) {
		return errors.New("trusted repository authorization does not match repoPath")
	}
	return nil
}

func (r *RepositoryAuthorization) matchesPath(repoPath string) bool {
	state := r.authorizationState()
	if state == nil {
		return false
	}
	cleanRepoPath := filepath.Clean(repoPath)
	if cleanRepoPath == state.paths.requestedPath || cleanRepoPath == state.paths.canonicalPath {
		return true
	}
	info, err := os.Lstat(cleanRepoPath)
	if err != nil || !info.IsDir() {
		return false
	}
	return os.SameFile(state.paths.canonicalInfo, info)
}

// OpenTrustedRepository verifies a no-follow root handle against the authorized
// identity and snapshots repository contents through that retained handle.
func OpenTrustedRepository(ctx context.Context, repository *RepositoryAuthorization, repoPath string, cache *CacheOptions) (_ *RepositoryView, returnErr error) {
	return openTrustedRepository(ctx, repository, repoPath, cache, false)
}

// OpenTrustedRepositoryWithGitMetadata additionally seals Git-sensitive state
// into the retained view before the completed-view test hook can run.
func OpenTrustedRepositoryWithGitMetadata(ctx context.Context, repository *RepositoryAuthorization, repoPath string, cache *CacheOptions) (_ *RepositoryView, returnErr error) {
	return openTrustedRepository(ctx, repository, repoPath, cache, true)
}

func openTrustedRepository(ctx context.Context, repository *RepositoryAuthorization, repoPath string, cache *CacheOptions, captureGitMetadata bool) (_ *RepositoryView, returnErr error) {
	if err := useTrustedRepository(repoPath, repository); err != nil {
		return nil, err
	}
	state := repository.authorizationState()
	if cache != nil && cache.hasTrustedPin() && cache.trustedPin.repositoryState != state {
		return nil, errors.New("trusted cache pin does not match repository authorization")
	}

	root, err := safeio.OpenRootNoFollow(state.paths.canonicalPath)
	if err != nil {
		return nil, fmt.Errorf("open trusted repository: %w", err)
	}
	var executionPath string
	var executionRoot safeio.Root
	defer func() {
		returnErr = cleanupFailedRepositoryOpen(returnErr, root, executionRoot, executionPath)
	}()

	openedInfo, err := inspectTrustedRepositoryRoot(root, state)
	if err != nil {
		return nil, err
	}

	var gitMetadata RepositoryGitMetadata
	if captureGitMetadata {
		gitMetadata = captureRepositoryGitMetadata(ctx, state.paths.canonicalPath)
	}

	var snapshotWarnings []string
	executionPath, executionRoot, snapshotWarnings, err = openRepositoryExecutionSnapshot(ctx, root, cache)
	if err != nil {
		return nil, err
	}
	if err := verifyRepositoryAfterSnapshot(ctx, state, openedInfo, captureGitMetadata, gitMetadata); err != nil {
		return nil, err
	}

	view := &RepositoryView{
		state:         state,
		root:          root,
		executionRoot: executionRoot,
		executionPath: executionPath,
		warnings:      snapshotWarnings,
		gitMetadata:   gitMetadata,
	}
	if err := repositoryViewHandleOpenedFn(); err != nil {
		return nil, err
	}
	return view, nil
}

func cleanupFailedRepositoryOpen(openErr error, root, executionRoot safeio.Root, executionPath string) error {
	if openErr == nil {
		return nil
	}
	if executionRoot != nil {
		openErr = errors.Join(openErr, executionRoot.Close())
	}
	if executionPath != "" {
		openErr = errors.Join(openErr, os.RemoveAll(executionPath))
	}
	return errors.Join(openErr, root.Close())
}

func inspectTrustedRepositoryRoot(root safeio.Root, state *repositoryAuthState) (fs.FileInfo, error) {
	openedInfo, err := root.Lstat(".")
	if err != nil {
		return nil, fmt.Errorf("inspect trusted repository handle: %w", err)
	}
	if !openedInfo.IsDir() || !os.SameFile(state.paths.canonicalInfo, openedInfo) {
		return nil, errors.New("trusted cache repository changed after validation")
	}
	return openedInfo, nil
}

func openRepositoryExecutionSnapshot(ctx context.Context, root safeio.Root, cache *CacheOptions) (string, safeio.Root, []string, error) {
	snapshot, err := snapshotRepositoryRoot(ctx, root, repositorySnapshotSkip(cache))
	if err != nil {
		return "", nil, nil, err
	}
	executionPath := snapshot.path
	executionRoot, err := safeio.OpenRoot(executionPath)
	if err != nil {
		return executionPath, nil, nil, fmt.Errorf("open repository execution snapshot: %w", err)
	}
	return executionPath, executionRoot, snapshot.diagnostics.warnings(), nil
}

func (r *RepositoryView) snapshotWarnings() []string {
	if r == nil || len(r.warnings) == 0 {
		return nil
	}
	return append([]string(nil), r.warnings...)
}

func verifyRepositoryAfterSnapshot(ctx context.Context, state *repositoryAuthState, openedInfo fs.FileInfo, captureGitMetadata bool, expectedMetadata RepositoryGitMetadata) error {
	if captureGitMetadata {
		verifiedMetadata := captureRepositoryGitMetadata(ctx, state.paths.canonicalPath)
		if !expectedMetadata.equal(verifiedMetadata) {
			return errors.New("trusted repository Git state changed while opening repository view")
		}
	}
	currentInfo, err := os.Lstat(state.paths.canonicalPath)
	if err != nil {
		return errors.New("trusted repository changed while opening repository view")
	}
	if !currentInfo.IsDir() || !os.SameFile(openedInfo, currentInfo) {
		return errors.New("trusted repository changed while opening repository view")
	}
	return nil
}

func repositorySnapshotSkip(cache *CacheOptions) string {
	if !InRepoCacheOptions(cache) {
		return ""
	}
	return filepath.Clean(cache.trustedPin.repoRelativePath)
}

// ExecutionPath returns the stable snapshot used for path-based adapters and
// subprocesses.
func (r *RepositoryView) ExecutionPath() string {
	if r == nil {
		return ""
	}
	return r.executionPath
}

// GitMetadata returns a defensive copy of Git-sensitive state sealed while the
// retained repository view was opened.
func (r *RepositoryView) GitMetadata() RepositoryGitMetadata {
	if r == nil {
		return RepositoryGitMetadata{}
	}
	return r.gitMetadata.clone()
}

func (r *RepositoryView) canonicalPath() string {
	if r == nil || r.state == nil {
		return ""
	}
	return r.state.paths.canonicalPath
}

func (r *RepositoryView) matches(repository *RepositoryAuthorization) bool {
	return r != nil && repository != nil && r.state != nil && r.state == repository.authorizationState()
}

// ValidateRepositoryView verifies that a retained view belongs to the supplied
// repository authorization without reopening either repository pathname.
func ValidateRepositoryView(repository *RepositoryAuthorization, view *RepositoryView) error {
	if !view.matches(repository) {
		return errors.New("trusted repository view does not match repository authorization")
	}
	return nil
}

var errUnsafeRepositoryRelativePath = errors.New("repository path must stay within the repository root")

// SnapshotPath remaps a repository-relative or authorized absolute path into
// the stable execution snapshot. External absolute paths are left unchanged.
func (r *RepositoryView) SnapshotPath(path string) (string, error) {
	if r == nil || strings.TrimSpace(path) == "" {
		return path, nil
	}
	cleanPath := filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(cleanPath) {
		if !isSafeRepositoryRelativePath(cleanPath, true) {
			return "", fmt.Errorf("%w: %s", errUnsafeRepositoryRelativePath, path)
		}
		return filepath.Join(r.executionPath, cleanPath), nil
	}
	for _, root := range r.repositoryRoots() {
		if !pathWithinDir(root, cleanPath) {
			continue
		}
		relativePath, err := filepath.Rel(root, cleanPath)
		if err == nil {
			return filepath.Join(r.executionPath, relativePath), nil
		}
	}
	return cleanPath, nil
}

// RepositoryRelativePath returns a safe repository-relative representation for
// paths expressed relative to, or absolutely beneath, either authorized path.
func (r *RepositoryView) RepositoryRelativePath(path string) (string, bool) {
	if r == nil || r.state == nil || strings.TrimSpace(path) == "" {
		return "", false
	}
	cleanPath := filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(cleanPath) {
		return cleanPath, isSafeRepositoryRelativePath(cleanPath, true)
	}
	for _, root := range r.repositoryRoots() {
		if !pathWithinDir(root, cleanPath) {
			continue
		}
		relativePath, err := filepath.Rel(root, cleanPath)
		if err == nil && isSafeRepositoryRelativePath(relativePath, true) {
			return filepath.Clean(relativePath), true
		}
	}
	return "", false
}

func (r *RepositoryView) repositoryRoots() []string {
	if r == nil || r.state == nil {
		return nil
	}
	return []string{
		r.state.paths.requestedPath,
		r.state.paths.canonicalPath,
		r.executionPath,
	}
}

// DisplayPath returns the authorized requested-path representation of a
// repository-relative path.
func (r *RepositoryView) DisplayPath(relativePath string) string {
	if r == nil || r.state == nil {
		return ""
	}
	return filepath.Join(r.state.paths.requestedPath, filepath.Clean(relativePath))
}

// StablePath maps a repository path, including an execution-snapshot path,
// back to the authorized requested-path identity. External paths are unchanged.
func (r *RepositoryView) StablePath(path string) string {
	if r == nil || strings.TrimSpace(path) == "" {
		return strings.TrimSpace(path)
	}
	if relativePath, ok := r.RepositoryRelativePath(path); ok {
		return r.DisplayPath(relativePath)
	}
	return filepath.Clean(strings.TrimSpace(path))
}

// OpenWriteRoot opens a repository-relative write root from the retained
// repository handle.
func (r *RepositoryView) OpenWriteRoot(relativePath string, create bool) (*safeio.WriteRoot, error) {
	if r == nil || r.root == nil {
		return nil, errors.New("trusted repository view is required")
	}
	return safeio.OpenRelativeWriteRoot(r.root, relativePath, create, 0o750)
}

// ReadFile reads a repository-relative file through the retained handle.
func (r *RepositoryView) ReadFile(relativePath string) ([]byte, error) {
	if r == nil || r.root == nil {
		return nil, errors.New("trusted repository view is required")
	}
	return safeio.ReadFileWithinRoot(r.root, relativePath)
}

// ReadExecutionFile reads a repository-relative file from the retained
// execution snapshot.
func (r *RepositoryView) ReadExecutionFile(relativePath string) ([]byte, error) {
	if r == nil || r.executionRoot == nil {
		return nil, errors.New("trusted repository execution view is required")
	}
	return safeio.ReadFileWithinRoot(r.executionRoot, relativePath)
}

// Lstat inspects a repository-relative path through the retained handle.
func (r *RepositoryView) Lstat(relativePath string) (fs.FileInfo, error) {
	if r == nil || r.root == nil {
		return nil, errors.New("trusted repository view is required")
	}
	if !isSafeRepositoryRelativePath(relativePath, true) {
		return nil, fmt.Errorf("repository path must be relative: %s", relativePath)
	}
	return r.root.Lstat(filepath.Clean(relativePath))
}

// WriteFile atomically writes a repository-relative file through the retained
// handle, creating missing parents.
func (r *RepositoryView) WriteFile(relativePath string, data []byte, perm os.FileMode) (returnErr error) {
	root, err := r.OpenWriteRoot(".", false)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, root.Close())
	}()
	return root.WriteFileReplacingParents(relativePath, data, perm, 0o750)
}

// EnsureDir creates a repository-relative directory through the retained
// handle.
func (r *RepositoryView) EnsureDir(relativePath string, perm os.FileMode) (returnErr error) {
	root, err := r.OpenWriteRoot(".", false)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, root.Close())
	}()
	return root.EnsureDir(relativePath, perm)
}

func isSafeRepositoryRelativePath(path string, allowRoot bool) bool {
	if filepath.IsAbs(path) {
		return false
	}
	cleanPath := filepath.Clean(path)
	if cleanPath == "." {
		return allowRoot
	}
	return cleanPath != ".." && !strings.HasPrefix(cleanPath, ".."+string(filepath.Separator))
}

// Close removes the execution snapshot and releases the retained handle.
func (r *RepositoryView) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		var cleanupErr error
		if r.executionRoot != nil {
			cleanupErr = r.executionRoot.Close()
		}
		if r.executionPath != "" {
			cleanupErr = errors.Join(cleanupErr, os.RemoveAll(r.executionPath))
		}
		if r.root != nil {
			cleanupErr = errors.Join(cleanupErr, r.root.Close())
		}
		cleanupErr = errors.Join(cleanupErr, repositoryViewCloseFn())
		r.closeErr = cleanupErr
	})
	return r.closeErr
}

func newRepositoryAuthorization(state *repositoryAuthState) *RepositoryAuthorization {
	if state == nil {
		return nil
	}
	authorization := &RepositoryAuthorization{
		state: state,
		nonce: state.nonce,
	}
	authorization.self = authorization
	return authorization
}

func (r *RepositoryAuthorization) authorizationState() *repositoryAuthState {
	if r == nil || r.self != r || r.state == nil || r.state.nonce == 0 || r.state.nonce != r.nonce {
		return nil
	}
	return r.state
}
