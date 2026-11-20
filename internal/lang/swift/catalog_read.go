package swift

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

var (
	errInputNotRegular     = errors.New("not a regular file")
	errPodLockEntryLimit   = errors.New("pod lock entry limit exceeded")
	errPodDeclarationLimit = errors.New("pod declaration limit exceeded")
	errResolvedPinLimit    = errors.New("resolved pin limit exceeded")
)

func readCatalogFile(repoPath, fileName string, maxBytes int64) ([]byte, error) {
	data, err := readBoundedCatalogFile(repoPath, filepath.Join(repoPath, fileName), maxBytes)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return data, err
	}
	return nil, classifyCatalogReadError(fileName, maxBytes, err)
}

func classifyCatalogReadError(fileName string, maxBytes int64, err error) error {
	if errors.Is(err, safeio.ErrFileTooLarge) {
		return fmt.Errorf("read %s: exceeds %d byte limit: %w", fileName, maxBytes, err)
	}
	return fmt.Errorf("read %s: %w", fileName, err)
}

func readBoundedCatalogFile(repoPath, targetPath string, maxBytes int64) ([]byte, error) {
	rel, err := filepath.Rel(filepath.Clean(repoPath), filepath.Clean(targetPath))
	if err != nil || !singleCatalogName(rel) {
		return safeio.ReadFileUnderLimit(repoPath, targetPath, maxBytes)
	}
	info, err := lstatCatalogName(repoPath, rel)
	if err != nil {
		return nil, err
	}
	if isBlockingSpecial(info.Mode()) {
		return nil, errInputNotRegular
	}
	return safeio.ReadFileUnderLimit(repoPath, targetPath, maxBytes)
}

func singleCatalogName(rel string) bool {
	return rel != "" && rel != "." && rel != ".." && !strings.ContainsAny(rel, `/\`)
}

func lstatCatalogName(repoPath, name string) (info fs.FileInfo, err error) {
	root, err := safeio.OpenRootNoFollow(repoPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()
	return root.Lstat(name)
}

func isBlockingSpecial(mode fs.FileMode) bool {
	return !mode.IsRegular() && !mode.IsDir() && mode&os.ModeSymlink == 0
}
