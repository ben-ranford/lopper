package shared

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/language"
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

func WalkRepoFiles(
	ctx context.Context,
	repoPath string,
	maxFiles int,
	skipDir func(string) bool,
	visit func(path string, entry fs.DirEntry) error,
) error {
	if skipDir == nil {
		skipDir = ShouldSkipCommonDir
	}

	walker := repoWalker{
		ctx:      ctx,
		maxFiles: maxFiles,
		skipDir:  skipDir,
		visit:    visit,
	}
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		return walker.handle(path, entry, walkErr)
	})
	if err != nil && err != fs.SkipAll {
		return err
	}
	return nil
}

type repoWalker struct {
	ctx      context.Context
	maxFiles int
	skipDir  func(string) bool
	visit    func(path string, entry fs.DirEntry) error
	visited  int
}

func (w *repoWalker) handle(path string, entry fs.DirEntry, walkErr error) error {
	if walkErr != nil {
		return walkErr
	}
	if w.ctx != nil && w.ctx.Err() != nil {
		return w.ctx.Err()
	}
	if entry.IsDir() {
		if w.skipDir(entry.Name()) {
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
	rel, err := filepath.Rel(absRoot, absCandidate)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != "..")
}
