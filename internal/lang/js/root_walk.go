package js

import (
	"errors"
	"io/fs"
	"path/filepath"
	"sort"

	"github.com/ben-ranford/lopper/internal/safeio"
)

type rootWalkFunc func(relPath string, info fs.FileInfo) (skipDir bool, stop bool, err error)

func walkRootNoFollow(root safeio.Root, visit rootWalkFunc) error {
	return walkRootNoFollowFrom(root, "", visit)
}

func walkRootNoFollowFrom(root safeio.Root, relDir string, visit rootWalkFunc) error {
	entries, err := readRootDirEntries(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		relPath := entry.Name()
		if relDir != "" {
			relPath = filepath.Join(relDir, relPath)
		}

		info, err := root.Lstat(entry.Name())
		if err != nil {
			return err
		}
		skipDir, stop, err := visit(relPath, info)
		if err != nil || stop {
			return err
		}
		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || skipDir {
			continue
		}

		childRoot, err := openRootChildNoFollow(root, entry.Name(), relPath)
		if err != nil {
			return err
		}
		if err := walkChildRootNoFollow(childRoot, relPath, visit); err != nil {
			return err
		}
	}
	return nil
}

func walkChildRootNoFollow(root safeio.Root, relDir string, visit rootWalkFunc) (err error) {
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	return walkRootNoFollowFrom(root, relDir, visit)
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
