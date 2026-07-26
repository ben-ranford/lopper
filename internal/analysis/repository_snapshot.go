package analysis

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	maxRepositorySnapshotFiles = 20000
	maxRepositorySnapshotBytes = 128 << 20
)

type repositorySnapshotBudget struct {
	files int
	bytes int64
}

type repositorySnapshotResult struct {
	path        string
	diagnostics repositorySnapshotDiagnostics
}

type repositorySnapshotDiagnostics struct {
	unsafeSymlinks []repositorySnapshotUnsafeSymlink
}

type repositorySnapshotUnsafeSymlink struct {
	relativePath string
	reason       repositorySnapshotUnsafeSymlinkReason
}

type repositorySnapshotUnsafeSymlinkReason int

const (
	repositorySnapshotUnsafeSymlinkUntrusted repositorySnapshotUnsafeSymlinkReason = iota
	repositorySnapshotUnsafeSymlinkEscapesRoot
)

func snapshotRepositoryRoot(ctx context.Context, source safeio.Root, skipDir string) (_ repositorySnapshotResult, returnErr error) {
	targetPath, err := os.MkdirTemp("", "lopper-repository-*")
	if err != nil {
		return repositorySnapshotResult{}, fmt.Errorf("create repository execution snapshot: %w", err)
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, os.RemoveAll(targetPath))
		}
	}()

	target, err := safeio.OpenRoot(targetPath)
	if err != nil {
		return repositorySnapshotResult{}, fmt.Errorf("open repository execution snapshot: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, target.Close())
	}()
	result := repositorySnapshotResult{path: targetPath}
	if err := copyRepositoryDirectory(ctx, source, target, ".", filepath.Clean(skipDir), &repositorySnapshotBudget{}, &result.diagnostics); err != nil {
		return repositorySnapshotResult{}, fmt.Errorf("snapshot repository: %w", err)
	}
	return result, nil
}

func copyRepositoryDirectory(ctx context.Context, source, target safeio.Root, relativeDir, skipDir string, budget *repositorySnapshotBudget, diagnostics *repositorySnapshotDiagnostics) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := safeio.ReadDirWithinRoot(source, relativeDir)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := copyRepositoryEntry(ctx, source, target, relativeDir, skipDir, entry, budget, diagnostics); err != nil {
			return err
		}
	}
	return nil
}

func copyRepositoryEntry(ctx context.Context, source, target safeio.Root, relativeDir, skipDir string, entry fs.DirEntry, budget *repositorySnapshotBudget, diagnostics *repositorySnapshotDiagnostics) error {
	relativePath := repositorySnapshotEntryPath(relativeDir, entry.Name())
	if shouldSkipRepositorySnapshotPath(relativePath, skipDir) {
		return nil
	}
	info, err := source.Lstat(relativePath)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return copyRepositorySymlink(source, target, relativePath, diagnostics)
	case info.IsDir():
		return copyRepositorySubdirectory(ctx, source, target, relativePath, skipDir, info, budget, diagnostics)
	case info.Mode().IsRegular():
		return copyRepositorySnapshotFile(ctx, source, target, relativePath, info, budget)
	default:
		return nil
	}
}

func repositorySnapshotEntryPath(relativeDir, entryName string) string {
	if relativeDir == "." {
		return entryName
	}
	return filepath.Join(relativeDir, entryName)
}

func copyRepositorySubdirectory(ctx context.Context, source, target safeio.Root, relativePath, skipDir string, info fs.FileInfo, budget *repositorySnapshotBudget, diagnostics *repositorySnapshotDiagnostics) error {
	if err := target.Mkdir(relativePath, info.Mode().Perm()|0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if err := copyRepositoryDirectory(ctx, source, target, relativePath, skipDir, budget, diagnostics); err != nil {
		return err
	}
	return target.Chmod(relativePath, info.Mode().Perm())
}

func copyRepositorySnapshotFile(ctx context.Context, source, target safeio.Root, relativePath string, info fs.FileInfo, budget *repositorySnapshotBudget) error {
	if err := budget.noteFile(info.Size()); err != nil {
		return err
	}
	return copyRepositoryRegularFile(ctx, source, target, relativePath, info.Mode().Perm())
}

func shouldSkipRepositorySnapshotPath(relativePath, skipDir string) bool {
	cleanPath := filepath.Clean(relativePath)
	if cleanPath == ".git" || cleanPath == defaultAnalysisCacheDirName {
		return true
	}
	return skipDir != "" && skipDir != "." && cleanPath == skipDir
}

func copyRepositorySymlink(source, target safeio.Root, relativePath string, diagnostics *repositorySnapshotDiagnostics) error {
	linkTarget, err := safeio.ReadlinkWithinRoot(source, relativePath)
	if err != nil {
		return err
	}
	if filepath.IsAbs(linkTarget) {
		diagnostics.recordUnsafeSymlink(relativePath, repositorySnapshotUnsafeSymlinkUntrusted)
		return nil
	}
	resolvedRelative := filepath.Clean(filepath.Join(filepath.Dir(relativePath), linkTarget))
	if resolvedRelative == ".." || strings.HasPrefix(resolvedRelative, ".."+string(filepath.Separator)) {
		diagnostics.recordUnsafeSymlink(relativePath, repositorySnapshotUnsafeSymlinkEscapesRoot)
		return nil
	}
	return safeio.SymlinkWithinRoot(target, linkTarget, relativePath)
}

func copyRepositoryRegularFile(ctx context.Context, source, target safeio.Root, relativePath string, perm os.FileMode) (returnErr error) {
	sourceFile, err := source.Open(relativePath)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, sourceFile.Close())
	}()

	targetFile, err := target.OpenFile(relativePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, targetFile.Close())
	}()
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := sourceFile.Read(buffer)
		if n > 0 {
			if _, err := targetFile.Write(buffer[:n]); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func (b *repositorySnapshotBudget) noteFile(size int64) error {
	if b == nil {
		return nil
	}
	b.files++
	if b.files > maxRepositorySnapshotFiles {
		return fmt.Errorf("repository snapshot exceeds file limit (%d)", maxRepositorySnapshotFiles)
	}
	b.bytes += size
	if b.bytes > maxRepositorySnapshotBytes {
		return fmt.Errorf("repository snapshot exceeds byte limit (%d)", maxRepositorySnapshotBytes)
	}
	return nil
}

func (d *repositorySnapshotDiagnostics) recordUnsafeSymlink(relativePath string, reason repositorySnapshotUnsafeSymlinkReason) {
	if d == nil {
		return
	}
	d.unsafeSymlinks = append(d.unsafeSymlinks, repositorySnapshotUnsafeSymlink{
		relativePath: relativePath,
		reason:       reason,
	})
}

func (d *repositorySnapshotDiagnostics) warnings() []string {
	if d == nil || len(d.unsafeSymlinks) == 0 {
		return nil
	}
	warnings := make([]string, 0, len(d.unsafeSymlinks)+1)
	skippedJVMSourceSymlinks := 0
	for _, item := range d.unsafeSymlinks {
		if repositorySnapshotJVMSourcePath(item.relativePath) {
			skippedJVMSourceSymlinks++
			warnings = append(warnings, fmt.Sprintf("skipped JVM source symlink %s: %s", item.relativePath, item.warningDescription()))
		}
		if repositorySnapshotJVMBuildPath(item.relativePath) {
			warnings = append(warnings, fmt.Sprintf("unable to read %s: %s", filepath.ToSlash(item.relativePath), item.buildWarningError()))
		}
	}
	if skippedJVMSourceSymlinks > 0 {
		warnings = append(warnings, fmt.Sprintf("skipped %d unreadable or untrusted JVM source symlink(s)", skippedJVMSourceSymlinks))
	}
	return warnings
}

func repositorySnapshotJVMSourcePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".java", ".kt", ".kts":
		return true
	default:
		return false
	}
}

func repositorySnapshotJVMBuildPath(path string) bool {
	switch strings.ToLower(filepath.Base(path)) {
	case "build.gradle", "build.gradle.kts", "pom.xml":
		return true
	default:
		return false
	}
}

func (s *repositorySnapshotUnsafeSymlink) warningDescription() string {
	switch s.reason {
	case repositorySnapshotUnsafeSymlinkEscapesRoot:
		return "target escapes repo root"
	default:
		return "target is an untrusted symlink"
	}
}

func (s *repositorySnapshotUnsafeSymlink) buildWarningError() string {
	switch s.reason {
	case repositorySnapshotUnsafeSymlinkEscapesRoot:
		return safeio.ErrPathEscapesRoot.Error()
	default:
		return safeio.ErrTargetPathSymlink.Error()
	}
}
