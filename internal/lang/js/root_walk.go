package js

import (
	"errors"
	"io/fs"
	"path/filepath"
	"sort"

	"github.com/ben-ranford/lopper/internal/safeio"
)

type rootWalkFunc func(relPath string, info fs.FileInfo) (skipDir bool, stop bool, err error)
type rootWalkErrorFunc func(relPath string, err error) bool

func walkRootNoFollow(root safeio.Root, visit rootWalkFunc) error {
	return walkRootNoFollowFrom(root, "", visit, &rootWalkState{}, nil)
}

func walkRootNoFollowBestEffort(root safeio.Root, visit rootWalkFunc) error {
	return walkRootNoFollowFrom(root, "", visit, &rootWalkState{}, func(string, error) bool {
		return true
	})
}

type rootWalkState struct {
	stopped bool
}

func walkRootNoFollowFrom(root safeio.Root, relDir string, visit rootWalkFunc, state *rootWalkState, onError rootWalkErrorFunc) error {
	if state.stopped {
		return nil
	}
	entries, err := readRootWalkEntries(root, relDir, onError)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := walkRootEntryNoFollow(root, relDir, entry, visit, state, onError); err != nil || state.stopped {
			return err
		}
	}
	return nil
}

func walkChildRootNoFollow(root safeio.Root, relDir string, visit rootWalkFunc, state *rootWalkState, onError rootWalkErrorFunc) (err error) {
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	return walkRootNoFollowFrom(root, relDir, visit, state, onError)
}

func shouldContinueRootWalk(relPath string, err error, onError rootWalkErrorFunc) bool {
	return onError != nil && onError(relPath, err)
}

func readRootWalkEntries(root safeio.Root, relDir string, onError rootWalkErrorFunc) ([]fs.DirEntry, error) {
	entries, err := readRootDirEntries(root)
	if err != nil && relDir != "" && shouldContinueRootWalk(relDir, err, onError) {
		return nil, nil
	}
	return entries, err
}

func walkRootEntryNoFollow(root safeio.Root, relDir string, entry fs.DirEntry, visit rootWalkFunc, state *rootWalkState, onError rootWalkErrorFunc) error {
	if state.stopped {
		return nil
	}
	relPath := rootWalkRelPath(relDir, entry.Name())
	info, err := root.Lstat(entry.Name())
	if err != nil {
		return continueOrReturnRootWalk(relPath, err, onError)
	}
	skipDir, stop, err := visit(relPath, info)
	if err != nil {
		return err
	}
	if stop {
		state.stopped = true
		return nil
	}
	if shouldSkipRootWalkChild(info, skipDir) {
		return nil
	}
	childRoot, err := openRootChildNoFollow(root, entry.Name(), relPath)
	if err != nil {
		return continueOrReturnRootWalk(relPath, err, onError)
	}
	return walkChildRootNoFollow(childRoot, relPath, visit, state, onError)
}

func rootWalkRelPath(relDir, name string) string {
	if relDir == "" {
		return name
	}
	return filepath.Join(relDir, name)
}

func continueOrReturnRootWalk(relPath string, err error, onError rootWalkErrorFunc) error {
	if shouldContinueRootWalk(relPath, err, onError) {
		return nil
	}
	return err
}

func shouldSkipRootWalkChild(info fs.FileInfo, skipDir bool) bool {
	return !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || skipDir
}

func readRootDirEntries(root safeio.Root) (entries []fs.DirEntry, err error) {
	file, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer closeReadCloserPreserveErr(file, &err)

	readDirFile, ok := file.(fs.ReadDirFile)
	if !ok {
		return nil, fs.ErrInvalid
	}
	entries, err = readDirFile.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	return entries, nil
}
