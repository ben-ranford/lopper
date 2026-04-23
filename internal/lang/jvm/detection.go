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
)

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	_ = ctx
	repoPath = shared.DefaultRepoPath(repoPath)

	detection := language.Detection{}
	roots := make(map[string]struct{})
	if err := applyJVMRootSignals(repoPath, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	const maxFiles = 1024
	visited := 0
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return walkJVMDetectionEntry(path, entry, roots, &detection, &visited, maxFiles)
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func walkJVMDetectionEntry(path string, entry fs.DirEntry, roots map[string]struct{}, detection *language.Detection, visited *int, maxFiles int) error {
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}
	(*visited)++
	if *visited > maxFiles {
		return fs.SkipAll
	}
	updateJVMDetection(path, entry, roots, detection)
	return nil
}

func applyJVMRootSignals(repoPath string, detection *language.Detection, roots map[string]struct{}) error {
	rootSignals := []struct {
		name       string
		confidence int
	}{
		{name: pomXMLName, confidence: 55},
		{name: buildGradleName, confidence: 45},
		{name: buildGradleKTSName, confidence: 45},
	}
	for _, signal := range rootSignals {
		path := filepath.Join(repoPath, signal.name)
		if _, err := os.Stat(path); err == nil {
			detection.Matched = true
			detection.Confidence += signal.confidence
			roots[repoPath] = struct{}{}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
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
