package shared

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type RootSignal struct {
	Name       string
	Confidence int
}

func ApplyRootSignals(repoPath string, signals []RootSignal, detection *language.Detection, roots map[string]struct{}) error {
	for _, signal := range signals {
		path := filepath.Join(repoPath, signal.Name)
		if _, err := os.Stat(path); err == nil {
			if detection != nil {
				detection.Matched = true
				detection.Confidence += signal.Confidence
			}
			if roots != nil {
				roots[repoPath] = struct{}{}
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func NewReport(rawRepoPath string, now func() time.Time) (string, report.Report, error) {
	repoPath, err := workspace.NormalizeRepoPath(rawRepoPath)
	if err != nil {
		return "", report.Report{}, err
	}

	return repoPath, report.Report{
		GeneratedAt: now(),
		RepoPath:    repoPath,
	}, nil
}

func WalkRepoFiles(ctx context.Context, repoPath string, maxFiles int, skipDir func(string) bool, visit func(path string, entry fs.DirEntry) error) error {
	if skipDir == nil {
		skipDir = ShouldSkipCommonDir
	}

	walker := repoWalker{
		rootPath: filepath.Clean(repoPath),
		maxFiles: maxFiles,
		skipDir:  skipDir,
		visit:    visit,
	}
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		return walker.handle(ctx, path, entry, walkErr)
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return err
	}
	return nil
}

func WalkContextErr(ctx context.Context, walkErr error) error {
	if walkErr != nil {
		return walkErr
	}
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

type repoWalker struct {
	rootPath string
	maxFiles int
	skipDir  func(string) bool
	visit    func(path string, entry fs.DirEntry) error
	visited  int
}

func (w *repoWalker) handle(ctx context.Context, path string, entry fs.DirEntry, walkErr error) error {
	if walkErr != nil {
		return walkErr
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if entry.IsDir() {
		if filepath.Clean(path) != w.rootPath && w.skipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}
	w.visited++
	if w.maxFiles > 0 && w.visited > w.maxFiles {
		return fs.SkipAll
	}
	return w.visit(path, entry)
}

func IsPathWithin(root, candidate string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absCandidate, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	if !pathWithin(absRoot, absCandidate) {
		return false
	}

	resolvedRoot, err := resolvePathWithMissingLeaf(absRoot)
	if err != nil {
		return false
	}
	resolvedCandidate, err := resolvePathWithMissingLeaf(absCandidate)
	if err != nil {
		return false
	}

	return pathWithin(resolvedRoot, resolvedCandidate)
}

func pathWithin(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != "..")
}

func resolvePathWithMissingLeaf(path string) (string, error) {
	cleanPath := filepath.Clean(path)

	_, err := os.Lstat(cleanPath)
	if err == nil {
		return filepath.EvalSymlinks(cleanPath)
	}
	if !os.IsNotExist(err) {
		return "", err
	}

	parent := filepath.Dir(cleanPath)
	if parent == cleanPath {
		return cleanPath, nil
	}

	resolvedParent, err := resolvePathWithMissingLeaf(parent)
	if err != nil {
		return "", err
	}

	return filepath.Join(resolvedParent, filepath.Base(cleanPath)), nil
}
