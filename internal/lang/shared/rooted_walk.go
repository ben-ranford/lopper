package shared

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

type rootedReadDirFile interface {
	safeio.ReadDirFile
}

var (
	errRootedWalkTraversalLimit = errors.New("rooted walk traversal limit exceeded")
	errRootedWalkFileLimit      = errors.New("rooted walk file limit exceeded")
	errRootedWalkWorkLimit      = errors.New("rooted walk work limit exceeded")
)

const rootedWalkReadBatchSize = 128

type RootedWalkBudget struct {
	MaxTraversalEntries int
	MaxFiles            int
	MaxWorkItems        int
	CountCandidate      func(path string, entry fs.DirEntry) bool
}

// RootedWalkFile identifies a non-directory entry and its pinned parent.
// Parent is valid only for the duration of the visit callback.
type RootedWalkFile struct {
	Parent safeio.Root
	Leaf   string
	Path   string
	Entry  fs.DirEntry
}

type RootedWalkVisit func(file RootedWalkFile) error

type rootedWalkDirectorySkip func(path, name string) bool

// WalkRepoFilesWithinRoot traverses repoPath using an already-open confined
// root and emits synthetic absolute paths rooted at repoPath.
func WalkRepoFilesWithinRoot(ctx context.Context, repoPath string, root safeio.Root, budget RootedWalkBudget, skipDir func(string) bool, visit func(path string, entry fs.DirEntry) error) error {
	return WalkRepoFilesWithinRootPinned(ctx, repoPath, root, budget, skipDir, func(file RootedWalkFile) error {
		return visit(file.Path, file.Entry)
	})
}

// WalkRepoFilesWithinRootPinned traverses repoPath and exposes each entry
// through its callback-scoped pinned parent root and leaf name.
func WalkRepoFilesWithinRootPinned(ctx context.Context, repoPath string, root safeio.Root, budget RootedWalkBudget, skipDir func(string) bool, visit RootedWalkVisit) error {
	return walkRepoFilesWithinRootPinnedWithPathSkip(ctx, repoPath, root, budget, rootedWalkDirectoryNameSkip(skipDir), visit)
}

func walkRepoFilesWithinRootPinnedWithPathSkip(ctx context.Context, repoPath string, root safeio.Root, budget RootedWalkBudget, skipDir rootedWalkDirectorySkip, visit RootedWalkVisit) error {
	if skipDir == nil {
		skipDir = rootedWalkDirectoryNameSkip(nil)
	}

	info, err := root.Lstat(".")
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fs.ErrInvalid
	}

	walker := rootedRepoWalker{
		repoPath: filepath.Clean(repoPath),
		budget:   budget,
		skipDir:  skipDir,
		visit:    visit,
	}
	err = walker.walk(ctx, root, ".", walker.repoPath, fs.FileInfoToDirEntry(info))
	if err != nil {
		return err
	}
	return nil
}

func rootedWalkDirectoryNameSkip(skipDir func(string) bool) rootedWalkDirectorySkip {
	if skipDir == nil {
		skipDir = ShouldSkipCommonDir
	}
	return func(_, name string) bool {
		return skipDir(name)
	}
}

type rootedRepoWalker struct {
	repoPath string
	budget   RootedWalkBudget
	skipDir  rootedWalkDirectorySkip
	visit    RootedWalkVisit
	state    rootedWalkBudgetState
}

type rootedWalkBudgetState struct {
	traversalEntriesSeen   int
	traversalEntriesQueued int
	filesSeen              int
	workItemsSeen          int
}

func (w *rootedRepoWalker) walk(ctx context.Context, parentRoot safeio.Root, relPath, path string, entry fs.DirEntry) (err error) {
	if err := walkContextErr(ctx); err != nil {
		return err
	}
	if err := w.countTraversalEntry(path); err != nil {
		return err
	}
	if entry.IsDir() {
		return w.walkDirectory(ctx, parentRoot, relPath, path, entry)
	}
	return w.walkFile(parentRoot, path, entry)
}

func (w *rootedRepoWalker) walkDirectory(ctx context.Context, parentRoot safeio.Root, relPath, path string, entry fs.DirEntry) (err error) {
	if relPath != "." && w.skipDir(path, entry.Name()) {
		return nil
	}
	if relPath == "." {
		return w.walkDirectoryChildren(ctx, parentRoot, relPath, path)
	}
	currentRoot, err := openPinnedChildRoot(parentRoot, entry.Name(), relPath)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, currentRoot.Close())
	}()
	return w.walkDirectoryChildren(ctx, currentRoot, relPath, path)
}

func (w *rootedRepoWalker) walkDirectoryChildren(ctx context.Context, root safeio.Root, relPath, path string) error {
	entries, err := w.readDir(ctx, root, path)
	if err != nil {
		return err
	}
	for _, child := range entries {
		if !w.dequeueTraversalEntry() {
			return fs.ErrInvalid
		}
		childRelPath := filepath.Join(relPath, child.Name())
		childPath := filepath.Join(path, child.Name())
		if err := w.walk(ctx, root, childRelPath, childPath, child); err != nil {
			return err
		}
	}
	return nil
}

func (w *rootedRepoWalker) walkFile(parentRoot safeio.Root, path string, entry fs.DirEntry) error {
	if w.shouldCountCandidate(path, entry) {
		if err := w.countFile(path); err != nil {
			return err
		}
		if err := w.countWorkItem(path); err != nil {
			return err
		}
	}
	return w.visit(RootedWalkFile{
		Parent: parentRoot,
		Leaf:   entry.Name(),
		Path:   path,
		Entry:  entry,
	})
}

func (w *rootedRepoWalker) readDir(ctx context.Context, root safeio.Root, path string) (entries []fs.DirEntry, err error) {
	if err := walkContextErr(ctx); err != nil {
		return nil, err
	}
	file, err := safeio.OpenPinnedDirectory(root, ".")
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()
	defer func() {
		if err == nil {
			sortRootedWalkEntries(entries)
		}
	}()

	for {
		if err := walkContextErr(ctx); err != nil {
			return nil, err
		}
		readSize := w.traversalReadSize()
		if readSize == 0 {
			return entries, w.probeDirectoryLimit(ctx, path, file)
		}
		batch, done, readErr := w.readDirBatch(path, file, readSize)
		if readErr != nil {
			return nil, readErr
		}
		entries = append(entries, batch...)
		if done {
			return entries, nil
		}
	}
}

func (w *rootedRepoWalker) readDirBatch(path string, file rootedReadDirFile, readSize int) ([]fs.DirEntry, bool, error) {
	batch, err := file.ReadDir(readSize)
	if len(batch) > readSize || !w.queueTraversalEntries(len(batch)) {
		return nil, false, rootedWalkReadLimitError(path, w.budget.MaxTraversalEntries, err)
	}
	if IsPureSentinelError(err, io.EOF) {
		return batch, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	if len(batch) == 0 {
		return nil, false, io.ErrNoProgress
	}
	return batch, false, nil
}

func rootedWalkReadLimitError(path string, limit int, readErr error) error {
	limitErr := newRootedWalkTraversalLimitError(path, limit)
	if readErr != nil && !IsPureSentinelError(readErr, io.EOF) {
		return errors.Join(limitErr, readErr)
	}
	return limitErr
}

func sortRootedWalkEntries(entries []fs.DirEntry) {
	slices.SortFunc(entries, func(left, right fs.DirEntry) int {
		return strings.Compare(left.Name(), right.Name())
	})
}

func walkContextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func (w *rootedRepoWalker) totalTraversalEntries() int {
	return w.state.traversalEntriesSeen + w.state.traversalEntriesQueued
}

func (w *rootedRepoWalker) countTraversalEntry(path string) error {
	if w.budget.MaxTraversalEntries > 0 && w.totalTraversalEntries() >= w.budget.MaxTraversalEntries {
		return newRootedWalkTraversalLimitError(path, w.budget.MaxTraversalEntries)
	}
	w.state.traversalEntriesSeen++
	return nil
}

func (w *rootedRepoWalker) traversalReadSize() int {
	if w.budget.MaxTraversalEntries <= 0 {
		return rootedWalkReadBatchSize
	}
	remaining := w.budget.MaxTraversalEntries - w.totalTraversalEntries()
	return min(rootedWalkReadBatchSize, max(remaining, 0))
}

func (w *rootedRepoWalker) queueTraversalEntries(count int) bool {
	if count < 0 {
		return false
	}
	if w.budget.MaxTraversalEntries > 0 && w.totalTraversalEntries()+count > w.budget.MaxTraversalEntries {
		return false
	}
	w.state.traversalEntriesQueued += count
	return true
}

func (w *rootedRepoWalker) dequeueTraversalEntry() bool {
	if w.state.traversalEntriesQueued == 0 {
		return false
	}
	w.state.traversalEntriesQueued--
	return true
}

func (w *rootedRepoWalker) countFile(path string) error {
	w.state.filesSeen++
	if w.budget.MaxFiles > 0 && w.state.filesSeen > w.budget.MaxFiles {
		return newRootedWalkFileLimitError(path, w.budget.MaxFiles)
	}
	return nil
}

func (w *rootedRepoWalker) countWorkItem(path string) error {
	w.state.workItemsSeen++
	if w.budget.MaxWorkItems > 0 && w.state.workItemsSeen > w.budget.MaxWorkItems {
		return newRootedWalkWorkLimitError(path, w.budget.MaxWorkItems)
	}
	return nil
}

func (w *rootedRepoWalker) shouldCountCandidate(path string, entry fs.DirEntry) bool {
	if entry.Type()&fs.ModeSymlink != 0 {
		return false
	}
	if w.budget.CountCandidate == nil {
		return true
	}
	return w.budget.CountCandidate(path, entry)
}

func (w *rootedRepoWalker) probeDirectoryLimit(ctx context.Context, path string, dir rootedReadDirFile) error {
	if err := walkContextErr(ctx); err != nil {
		return err
	}
	entries, err := dir.ReadDir(1)
	if len(entries) > 0 {
		limitErr := newRootedWalkTraversalLimitError(path, w.budget.MaxTraversalEntries)
		if err != nil && !IsPureSentinelError(err, io.EOF) {
			return errors.Join(limitErr, err)
		}
		return limitErr
	}
	if IsPureSentinelError(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return io.ErrNoProgress
}

func openPinnedChildRoot(root safeio.Root, name, path string) (safeio.Root, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("root contains symlink: %s", path)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("root is not a directory: %s", path)
	}

	child, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	openedInfo, err := child.Lstat(".")
	if err != nil {
		return nil, errors.Join(err, child.Close())
	}
	if !os.SameFile(info, openedInfo) {
		return nil, errors.Join(fmt.Errorf("root changed while opening: %s", path), child.Close())
	}
	return child, nil
}

func newRootedWalkTraversalLimitError(path string, limit int) error {
	return fmt.Errorf("%w at %q (maximum entries: %d)", errRootedWalkTraversalLimit, path, limit)
}

func newRootedWalkFileLimitError(path string, limit int) error {
	return fmt.Errorf("%w at %q (maximum files: %d)", errRootedWalkFileLimit, path, limit)
}

func newRootedWalkWorkLimitError(path string, limit int) error {
	return fmt.Errorf("%w at %q (maximum work items: %d)", errRootedWalkWorkLimit, path, limit)
}

func RootedWalkBudgetWarning(scope string, budget RootedWalkBudget, err error) (string, bool) {
	switch {
	case IsPureSentinelError(err, errRootedWalkTraversalLimit):
		return rootedWalkBudgetWarning(scope, "traversal entries", budget.MaxTraversalEntries), true
	case IsPureSentinelError(err, errRootedWalkFileLimit):
		return rootedWalkBudgetWarning(scope, "candidate files", budget.MaxFiles), true
	case IsPureSentinelError(err, errRootedWalkWorkLimit):
		return rootedWalkBudgetWarning(scope, "candidate work items", budget.MaxWorkItems), true
	default:
		return "", false
	}
}

func rootedWalkBudgetWarning(scope, unit string, limit int) string {
	scope = strings.TrimSpace(scope)
	if limit > 0 {
		return fmt.Sprintf("%s reached the rooted walk limit of %d %s; results may be partial", scope, limit, unit)
	}
	return fmt.Sprintf("%s reached a rooted walk limit; results may be partial", scope)
}
