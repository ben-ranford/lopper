package jvm

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/safeio"
)

var (
	afterJVMDetectRootSignals = func(string) error { return nil }
	openJVMDetectionRootHook  = safeio.OpenRootNoFollow
)

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (detection language.Detection, err error) {
	if err := shared.WalkContextErr(ctx, nil); err != nil {
		return language.Detection{}, err
	}
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
	err = walkJVMDetectionWithinRoot(ctx, repoPath, root, roots, &detection, budget)
	_, limited := shared.RootedWalkBudgetWarning("jvm detection", budget, err)
	if err != nil && !limited && !shared.IsPureSentinelError(err, fs.SkipAll) {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

const (
	defaultJVMMaxTraversalEntries   = 4096
	defaultJVMMaxConfinedCandidates = 1024
)

func defaultJVMDetectionBudget() shared.RootedWalkBudget {
	return shared.RootedWalkBudget{
		MaxTraversalEntries: defaultJVMMaxTraversalEntries,
		MaxFiles:            defaultJVMMaxConfinedCandidates,
		CountCandidate:      isJVMDetectionCandidate,
	}
}

type jvmRootSignalReader interface {
	Lstat(name string) (fs.FileInfo, error)
	Close() error
}

func walkJVMDetectionWithinRoot(ctx context.Context, repoPath string, root safeio.Root, roots map[string]struct{}, detection *language.Detection, budget shared.RootedWalkBudget) error {
	if err := shared.WalkContextErr(ctx, nil); err != nil {
		return err
	}
	return shared.WalkRepoFilesWithinRootPinned(ctx, repoPath, root, budget, shouldSkipDir, func(file shared.RootedWalkFile) error {
		if file.Entry.Type()&os.ModeSymlink == 0 && isJVMDetectionCandidate(file.Path, file.Entry) {
			updateJVMDetection(repoPath, file.Path, file.Entry, roots, detection)
		}
		return nil
	})
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

func updateJVMDetection(repoPath, path string, entry fs.DirEntry, roots map[string]struct{}, detection *language.Detection) {
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
		if root := sourceLayoutModuleRootWithinRepo(repoPath, path); root != "" {
			roots[root] = struct{}{}
		}
	}
}

func sourceLayoutModuleRootWithinRepo(repoPath, path string) string {
	root := sourceLayoutModuleRoot(path)
	if root == "" || !shared.IsPathWithin(repoPath, root) {
		return ""
	}
	return root
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
