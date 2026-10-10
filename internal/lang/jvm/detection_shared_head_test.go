package jvm

import (
	"context"
	"errors"
	"io/fs"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/safeio"
)

// This fixture instruments directory reads while invoking the production shared
// walker boundary. It contains no traversal or budget implementation.
type jvmDetectionTestWalker struct {
	repoPath      string
	roots         map[string]struct{}
	detection     *language.Detection
	budget        shared.RootedWalkBudget
	openRoot      func(string) (safeio.Root, error)
	openDirectory func(safeio.Root, string) (safeio.ReadDirFile, error)
}

func newJVMDetectionTestWalker(repo string, roots map[string]struct{}, detection *language.Detection, budget shared.RootedWalkBudget) *jvmDetectionTestWalker {
	budget.CountCandidate = isJVMDetectionCandidate
	return &jvmDetectionTestWalker{repoPath: repo, roots: roots, detection: detection, budget: budget, openRoot: safeio.OpenRootNoFollow}
}

func (w *jvmDetectionTestWalker) walk(ctx context.Context) (err error) {
	root, err := w.openRoot(w.repoPath)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	return w.walkPinned(ctx, root)
}

func (w *jvmDetectionTestWalker) walkPinned(ctx context.Context, root safeio.Root) error {
	if w.openDirectory != nil {
		root = &jvmDetectionReadFixtureRoot{Root: root, openDirectory: w.openDirectory}
	}
	return walkJVMDetectionWithinRoot(ctx, w.repoPath, root, w.roots, w.detection, w.budget)
}

type jvmDetectionReadFixtureRoot struct {
	safeio.Root
	openDirectory func(safeio.Root, string) (safeio.ReadDirFile, error)
}

func (r *jvmDetectionReadFixtureRoot) Open(name string) (safeio.File, error) {
	file, err := r.openDirectory(r.Root, name)
	if err != nil {
		return nil, err
	}
	info, err := r.Lstat(name)
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return &jvmDetectionReadFixtureFile{ReadDirFile: file, info: info}, nil
}

type jvmDetectionReadFixtureFile struct {
	safeio.ReadDirFile
	info fs.FileInfo
}

func (f *jvmDetectionReadFixtureFile) Stat() (fs.FileInfo, error) { return f.info, nil }

func jvmDetectionTraversalLimited(err error) bool {
	return err != nil && strings.Contains(err.Error(), "rooted walk traversal limit exceeded")
}

func jvmDetectionCandidateLimited(err error) bool {
	return err != nil && strings.Contains(err.Error(), "rooted walk file limit exceeded")
}
