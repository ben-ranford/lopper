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
	entries, err := readRootDirEntries(root)
	if err != nil {
		if shouldContinueRootWalk(relDir, err, onError) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if state.stopped {
			return nil
		}
		relPath := entry.Name()
		if relDir != "" {
			relPath = filepath.Join(relDir, relPath)
		}

		info, err := root.Lstat(entry.Name())
		if err != nil {
			if shouldContinueRootWalk(relPath, err, onError) {
				continue
			}
			return err
		}
		skipDir, stop, err := visit(relPath, info)
		if err != nil {
			return err
		}
		if stop {
			state.stopped = true
			return nil
		}
		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || skipDir {
			continue
		}

		childRoot, err := openRootChildNoFollow(root, entry.Name(), relPath)
		if err != nil {
			if shouldContinueRootWalk(relPath, err, onError) {
				continue
			}
			return err
		}
		if err := walkChildRootNoFollow(childRoot, relPath, visit, state, onError); err != nil {
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

func readRootDirEntries(root safeio.Root) (entries []fs.DirEntry, err error) {
	file, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

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
