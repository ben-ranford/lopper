//go:build unix

package analysis

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
)

type statOverrideAnalysisCacheFileInfo struct {
	fs.FileInfo
	stat *syscall.Stat_t
}

func (i *statOverrideAnalysisCacheFileInfo) Sys() any {
	return i.stat
}

type ownerChangedAnalysisCacheRoot struct {
	safeio.Root
	name string
}

func (r *ownerChangedAnalysisCacheRoot) Lstat(name string) (fs.FileInfo, error) {
	info, err := r.Root.Lstat(name)
	if err != nil || name != r.name {
		return info, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return info, nil
	}
	overridden := *stat
	if overridden.Uid == 0 {
		overridden.Uid = 1
	} else {
		overridden.Uid--
	}
	return &statOverrideAnalysisCacheFileInfo{FileInfo: info, stat: &overridden}, nil
}

func TestRollbackCreatedAnalysisCacheChildSkipsOwnershipChangedChild(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	child, err := safeio.OpenRoot(childPath)
	if err != nil {
		t.Fatalf("open child: %v", err)
	}

	if err := rollbackCreatedAnalysisCacheChild(
		&ownerChangedAnalysisCacheRoot{Root: root, name: cacheKeysDirName},
		cacheKeysDirName,
		child,
		childInfo,
		true,
	); err != nil {
		t.Fatalf("rollback ownership-changed child: %v", err)
	}
	if info, err := os.Stat(childPath); err != nil || !info.IsDir() {
		t.Fatalf("expected ownership-changed child to remain, info=%#v err=%v", info, err)
	}
	if _, err := os.Stat(filepath.Join(repo, cacheKeysDirName)); err != nil {
		t.Fatalf("expected ownership-changed child path to remain accessible: %v", err)
	}
}
