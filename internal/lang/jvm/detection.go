package jvm

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

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/safeio"
)

var (
	errJVMDetectionTraversalLimit = errors.New("jvm detection traversal limit exceeded")
	afterJVMDetectRootSignals     = func(string) error { return nil }
	openJVMDetectionRootHook      = openJVMDetectionRoot
)

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (detection language.Detection, err error) {
	_ = ctx
	repoPath = shared.DefaultRepoPath(repoPath)

	detection = language.Detection{}
	roots := make(map[string]struct{})
	root, err := openJVMDetectionRootHook(repoPath)
	if err != nil {
		return language.Detection{}, err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()

	if err := applyJVMRootSignalsWithinRoot(repoPath, root, &detection, roots); err != nil {
		return language.Detection{}, err
	}
	if err := afterJVMDetectRootSignals(repoPath); err != nil {
		return language.Detection{}, err
	}

	budget := defaultJVMDetectionBudget()
	walker := newJVMDetectionWalker(repoPath, roots, &detection, budget)
	err = walker.walkPinned(root)
	if err != nil && !shared.IsPureSentinelError(err, fs.SkipAll, errJVMDetectionTraversalLimit) {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

type jvmDetectionBudget struct {
	maxTraversalEntries    int
	maxConfinedCandidates  int
	traversalEntriesSeen   int
	traversalEntriesQueued int
	confinedCandidatesSeen int
}

const (
	// The traversal limit counts the root plus every queued or visited entry.
	// Four times the candidate limit leaves headroom for rejected entries.
	defaultJVMMaxTraversalEntries   = 4096
	defaultJVMMaxConfinedCandidates = 1024
	// ReadDir allocations stay fixed-size except for the final remaining budget.
	jvmDetectionReadBatchSize = 128
)

func defaultJVMDetectionBudget() *jvmDetectionBudget {
	return &jvmDetectionBudget{
		maxTraversalEntries:   defaultJVMMaxTraversalEntries,
		maxConfinedCandidates: defaultJVMMaxConfinedCandidates,
	}
}

func (b *jvmDetectionBudget) countTraversalEntry() error {
	if b.traversalBudgetExhausted() {
		return errJVMDetectionTraversalLimit
	}
	b.traversalEntriesSeen++
	return nil
}

func (b *jvmDetectionBudget) traversalReadSize() int {
	if b.maxTraversalEntries <= 0 {
		return jvmDetectionReadBatchSize
	}
	remaining := b.maxTraversalEntries - b.totalTraversalEntries()
	return min(jvmDetectionReadBatchSize, max(remaining, 0))
}

func (b *jvmDetectionBudget) queueTraversalEntries(count int) bool {
	if count < 0 || (b.maxTraversalEntries > 0 && b.totalTraversalEntries()+count > b.maxTraversalEntries) {
		return false
	}
	b.traversalEntriesQueued += count
	return true
}

func (b *jvmDetectionBudget) dequeueTraversalEntry() bool {
	if b.traversalEntriesQueued == 0 {
		return false
	}
	b.traversalEntriesQueued--
	return true
}

func (b *jvmDetectionBudget) traversalBudgetExhausted() bool {
	return b.maxTraversalEntries > 0 && b.totalTraversalEntries() >= b.maxTraversalEntries
}

func (b *jvmDetectionBudget) totalTraversalEntries() int {
	return b.traversalEntriesSeen + b.traversalEntriesQueued
}

func (b *jvmDetectionBudget) countConfinedCandidate() error {
	b.confinedCandidatesSeen++
	if b.maxConfinedCandidates > 0 && b.confinedCandidatesSeen > b.maxConfinedCandidates {
		return fs.SkipAll
	}
	return nil
}

type jvmDetectionDirectory interface {
	ReadDir(count int) ([]fs.DirEntry, error)
	Stat() (fs.FileInfo, error)
	Close() error
}

type jvmDetectionRoot interface {
	Open(name string) (jvmDetectionDirectory, error)
	OpenRoot(name string) (jvmDetectionRoot, error)
	Lstat(name string) (fs.FileInfo, error)
	Close() error
}

type jvmRootSignalReader interface {
	Lstat(name string) (fs.FileInfo, error)
	Close() error
}

type jvmDetectionWalker struct {
	repoPath      string
	roots         map[string]struct{}
	detection     *language.Detection
	budget        *jvmDetectionBudget
	openRoot      func(string) (jvmDetectionRoot, error)
	openDirectory func(jvmDetectionRoot, string) (jvmDetectionDirectory, error)
}

func newJVMDetectionWalker(repoPath string, roots map[string]struct{}, detection *language.Detection, budget *jvmDetectionBudget) *jvmDetectionWalker {
	return &jvmDetectionWalker{
		repoPath:      repoPath,
		roots:         roots,
		detection:     detection,
		budget:        budget,
		openRoot:      openJVMDetectionRoot,
		openDirectory: openJVMDetectionDirectory,
	}
}

type osJVMDetectionRoot struct {
	root safeio.Root
}

func openJVMDetectionRoot(path string) (jvmDetectionRoot, error) {
	root, err := safeio.OpenRootNoFollow(path)
	if err != nil {
		return nil, err
	}
	return &osJVMDetectionRoot{root: root}, nil
}

func (r *osJVMDetectionRoot) Open(name string) (jvmDetectionDirectory, error) {
	file, err := safeio.OpenPinnedDirectory(r.root, name)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (r *osJVMDetectionRoot) OpenRoot(name string) (jvmDetectionRoot, error) {
	root, err := r.root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return &osJVMDetectionRoot{root: root}, nil
}

func (r *osJVMDetectionRoot) Lstat(name string) (fs.FileInfo, error) {
	return r.root.Lstat(name)
}

func (r *osJVMDetectionRoot) Close() error {
	return r.root.Close()
}

func openJVMDetectionDirectory(root jvmDetectionRoot, path string) (jvmDetectionDirectory, error) {
	return root.Open(path)
}

func (w *jvmDetectionWalker) walk() (returnErr error) {
	root, err := w.openRoot(w.repoPath)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, root.Close())
	}()

	return w.walkPinned(root)
}

func (w *jvmDetectionWalker) walkPinned(root jvmDetectionRoot) error {
	info, err := root.Lstat(".")
	if err != nil {
		return err
	}
	return w.walkEntry(root, w.repoPath, fs.FileInfoToDirEntry(info))
}

func (w *jvmDetectionWalker) walkEntry(root jvmDetectionRoot, path string, entry fs.DirEntry) error {
	err := walkJVMDetectionEntry(w.repoPath, path, entry, w.roots, w.detection, w.budget)
	if errors.Is(err, filepath.SkipDir) {
		return nil
	}
	if err != nil {
		return err
	}
	if !entry.IsDir() {
		return nil
	}
	return w.walkDirectory(root, path, ".")
}

func (w *jvmDetectionWalker) walkDirectory(root jvmDetectionRoot, path, relativePath string) error {
	entries, err := w.readDirectory(root, path)
	if err != nil {
		return err
	}
	for _, child := range entries {
		if !w.budget.dequeueTraversalEntry() {
			return fs.ErrInvalid
		}
		childPath := filepath.Join(path, child.Name())
		err := walkJVMDetectionEntry(w.repoPath, childPath, child, w.roots, w.detection, w.budget)
		if errors.Is(err, filepath.SkipDir) {
			continue
		}
		if err != nil {
			return err
		}
		if !child.IsDir() {
			continue
		}

		childRelativePath := filepath.Join(relativePath, child.Name())
		childRoot, err := openJVMDetectionChildRoot(root, child.Name(), childRelativePath)
		if err != nil {
			return err
		}
		walkErr := w.walkDirectory(childRoot, childPath, childRelativePath)
		if err := errors.Join(walkErr, childRoot.Close()); err != nil {
			return err
		}
	}
	return nil
}

func (w *jvmDetectionWalker) readDirectory(root jvmDetectionRoot, path string) ([]fs.DirEntry, error) {
	directory, err := w.openDirectory(root, ".")
	if err != nil {
		return nil, err
	}

	entries, readErr := w.readDirectoryEntries(path, directory)
	closeErr := directory.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	// Only complete directories are sorted and processed.
	slices.SortFunc(entries, func(left, right fs.DirEntry) int {
		return strings.Compare(left.Name(), right.Name())
	})
	return entries, nil
}

func openJVMDetectionChildRoot(root jvmDetectionRoot, name, path string) (jvmDetectionRoot, error) {
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
		return nil, errors.Join(fmt.Errorf("path changed while opening: %s", path), child.Close())
	}
	return child, nil
}

func (w *jvmDetectionWalker) readDirectoryEntries(path string, directory jvmDetectionDirectory) ([]fs.DirEntry, error) {
	var entries []fs.DirEntry
	for {
		readSize := w.budget.traversalReadSize()
		if readSize == 0 {
			return entries, w.probeDirectoryLimit(path, directory)
		}
		batch, done, err := w.readDirectoryBatch(path, directory, readSize)
		if err != nil {
			return nil, err
		}
		entries = append(entries, batch...)
		if done {
			return entries, nil
		}
	}
}

func (w *jvmDetectionWalker) readDirectoryBatch(path string, directory jvmDetectionDirectory, readSize int) ([]fs.DirEntry, bool, error) {
	batch, err := directory.ReadDir(readSize)
	if len(batch) > readSize || !w.budget.queueTraversalEntries(len(batch)) {
		return nil, false, jvmDetectionReadLimitError(path, w.budget.maxTraversalEntries, err)
	}
	if shared.IsPureSentinelError(err, io.EOF) {
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

func jvmDetectionReadLimitError(path string, limit int, readErr error) error {
	limitErr := newJVMDetectionTraversalLimitError(path, limit)
	if readErr != nil && !shared.IsPureSentinelError(readErr, io.EOF) {
		return errors.Join(limitErr, readErr)
	}
	return limitErr
}

func (w *jvmDetectionWalker) probeDirectoryLimit(path string, directory jvmDetectionDirectory) error {
	entries, err := directory.ReadDir(1)
	if len(entries) > 0 {
		limitErr := newJVMDetectionTraversalLimitError(path, w.budget.maxTraversalEntries)
		if err != nil && !shared.IsPureSentinelError(err, io.EOF) {
			return errors.Join(limitErr, err)
		}
		return limitErr
	}
	if shared.IsPureSentinelError(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return io.ErrNoProgress
}

func newJVMDetectionTraversalLimitError(path string, limit int) error {
	return fmt.Errorf("%w at %q (maximum entries: %d)", errJVMDetectionTraversalLimit, path, limit)
}

func walkJVMDetectionEntry(repoPath, path string, entry fs.DirEntry, roots map[string]struct{}, detection *language.Detection, budget *jvmDetectionBudget) error {
	if err := budget.countTraversalEntry(); err != nil {
		return newJVMDetectionTraversalLimitError(path, budget.maxTraversalEntries)
	}
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}
	if entry.Type()&os.ModeSymlink != 0 {
		return nil
	}
	if !isJVMDetectionCandidate(path, entry) {
		return nil
	}
	if err := budget.countConfinedCandidate(); err != nil {
		return err
	}
	updateJVMDetection(path, entry, roots, detection)
	return nil
}

func isJVMDetectionCandidate(path string, entry fs.DirEntry) bool {
	name := strings.ToLower(entry.Name())
	if name == pomXMLName || name == buildGradleName || name == buildGradleKTSName {
		return true
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".java", ".kt", ".kts":
		return true
	default:
		return false
	}
}

func applyJVMRootSignals(repoPath string, detection *language.Detection, roots map[string]struct{}) (err error) {
	root, err := safeio.OpenRootNoFollow(repoPath)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()

	return applyJVMRootSignalsWithinRoot(repoPath, root, detection, roots)
}

func applyJVMRootSignalsWithinRoot(repoPath string, root jvmRootSignalReader, detection *language.Detection, roots map[string]struct{}) error {
	for _, signal := range jvmRootSignals {
		info, signalErr := root.Lstat(signal.Name)
		if signalErr == nil {
			if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if detection != nil {
				detection.Matched = true
				detection.Confidence += signal.Confidence
			}
			if roots != nil {
				roots[repoPath] = struct{}{}
			}
			continue
		}
		if !os.IsNotExist(signalErr) {
			return signalErr
		}
	}
	return nil
}

var jvmRootSignals = []shared.RootSignal{
	{Name: pomXMLName, Confidence: 55},
	{Name: buildGradleName, Confidence: 45},
	{Name: buildGradleKTSName, Confidence: 45},
}

func updateJVMDetection(path string, entry fs.DirEntry, roots map[string]struct{}, detection *language.Detection) {
	switch strings.ToLower(entry.Name()) {
	case pomXMLName, buildGradleName, buildGradleKTSName:
		detection.Matched = true
		detection.Confidence += 10
		roots[filepath.Dir(path)] = struct{}{}
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".java", ".kt", ".kts":
		detection.Matched = true
		detection.Confidence += 2
		if root := sourceLayoutModuleRoot(path); root != "" {
			roots[root] = struct{}{}
		}
	}
}

func sourceLayoutModuleRoot(path string) string {
	normalized := filepath.ToSlash(filepath.Clean(path))
	if normalized == "" {
		return ""
	}

	segments := strings.Split(normalized, "/")
	lastSrcIndex := -1
	for index := 0; index+2 < len(segments); index++ {
		if segments[index] != "src" {
			continue
		}
		switch segments[index+2] {
		case "java", "kotlin":
			lastSrcIndex = index
		}
	}
	if lastSrcIndex < 1 {
		return ""
	}

	root := strings.Join(segments[:lastSrcIndex], "/")
	if root == "" {
		return ""
	}
	return filepath.FromSlash(root)
}
