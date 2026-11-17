package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type inventoryBudget struct{ entries, directories, files, bytes, metadata uint64 }

func (b *inventoryBudget) admit(directory bool) error {
	if b.entries >= maxEntries || (directory && b.directories >= maxDirectories) || (!directory && b.files >= maxFiles) {
		return errors.New("inventory entry limit exceeded")
	}
	b.entries++
	if directory {
		b.directories++
	} else {
		b.files++
	}
	return nil
}

func inventory(ctx context.Context, root string, sourceLink bool) (receipt, error) {
	if err := canonicalDirectory(root); err != nil {
		return receipt{}, err
	}
	result := receipt{Version: policyVersion, Root: root}
	budget := &inventoryBudget{metadata: uint64(len(root) + len(policyVersion))}
	if err := collectDirectory(ctx, &result, budget, ".", sourceLink); err != nil {
		return receipt{}, err
	}
	sort.Slice(result.Records, func(i, j int) bool { return result.Records[i].Path < result.Records[j].Path })
	return result, nil
}

func collectDirectory(ctx context.Context, out *receipt, budget *inventoryBudget, relative string, sourceLink bool) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRelative(relative); err != nil {
		return err
	}
	if err := budget.admit(true); err != nil {
		return err
	}
	full := filepath.Join(out.Root, filepath.FromSlash(relative))
	if err := canonicalDirectory(full); err != nil {
		return err
	}
	dir, err := openConfined(full, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, dir.Close()) }()
	info, err := dir.Stat()
	if err != nil {
		return err
	}
	if err := verifyOpenedPath(dir, full); err != nil {
		return err
	}
	identity, err := fileIdentity(dir)
	if err != nil {
		return err
	}
	if err := appendInventory(out, budget, record{Kind: "directory", Path: relative, Mode: uint32(info.Mode().Perm()), Target: identity}); err != nil {
		return err
	}
	collectErr := collectEntries(ctx, dir, out, budget, relative, sourceLink)
	return finishDirectoryCollection(info, full, collectErr)
}

// finishDirectoryCollection checks final custody; the caller retains directory ownership.
func finishDirectoryCollection(info os.FileInfo, full string, collectErr error) error {
	current, statErr := os.Lstat(full)
	if statErr != nil {
		return errors.Join(collectErr, statErr)
	}
	if !os.SameFile(info, current) || info.Mode() != current.Mode() || !info.ModTime().Equal(current.ModTime()) {
		return errors.Join(collectErr, errors.New("directory changed during inventory"))
	}
	return collectErr
}

func collectEntries(ctx context.Context, dir *os.File, out *receipt, budget *inventoryBudget, relative string, sourceLink bool) error {
	seen := map[string]bool{}
	for {
		entries, readErr := dir.ReadDir(128)
		for _, entry := range entries {
			if err := collectEntry(ctx, out, budget, relative, sourceLink, entry, seen); err != nil {
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

func collectEntry(ctx context.Context, out *receipt, budget *inventoryBudget, relative string, sourceLink bool, entry os.DirEntry, seen map[string]bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if budget.entries >= maxEntries {
		return errors.New("inventory entry limit exceeded before entry admission")
	}
	name := entry.Name()
	key := strings.ToLower(name)
	if seen[key] {
		return errors.New("case-colliding inventory entry")
	}
	rel := name
	if relative != "." {
		rel = relative + "/" + name
	}
	if err := validateRelative(rel); err != nil {
		return err
	}
	if err := reserve(&budget.metadata, uint64(len(key)), maxMetadataBytes); err != nil {
		return err
	}
	seen[key] = true
	if entry.IsDir() {
		return collectDirectory(ctx, out, budget, rel, sourceLink)
	}
	return collectFile(ctx, out, budget, rel, sourceLink)
}

func collectFile(ctx context.Context, out *receipt, budget *inventoryBudget, relative string, sourceLink bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := budget.admit(false); err != nil {
		return err
	}
	full := filepath.Join(out.Root, filepath.FromSlash(relative))
	info, err := os.Lstat(full)
	if err != nil {
		return err
	}
	kind, target := "file", ""
	if info.Mode()&os.ModeSymlink != 0 {
		target, err = sourceAlias(full, out.Root, relative, sourceLink)
		if err != nil {
			return err
		}
		full = target
		kind = "alias"
		info, err = os.Lstat(full)
		if err != nil {
			return err
		}
	}
	size := info.Size()
	if !info.Mode().IsRegular() || size < 0 || size > maxFileBytes {
		return errors.New("invalid runtime file type or size")
	}
	if err := reserve(&budget.bytes, uint64(size), maxTotalBytes); err != nil {
		return err
	}
	digest, identity, err := inspectRegular(ctx, full, size)
	if err != nil {
		return err
	}
	if kind != "alias" {
		target = identity
	}
	return appendInventory(out, budget, record{Kind: kind, Path: relative, Hash: digest, Bytes: uint64(size), Mode: uint32(info.Mode().Perm()), Target: target})
}

func hashRegular(ctx context.Context, path string, size int64) (string, error) {
	digest, _, err := inspectRegular(ctx, path, size)
	return digest, err
}

func inspectRegular(ctx context.Context, path string, size int64) (digest, identity string, returnErr error) {
	if size < 0 || size > maxFileBytes {
		return "", "", errors.New("invalid file byte limit")
	}
	if err := canonicalRegular(path); err != nil {
		return "", "", err
	}
	f, err := openConfined(path, os.O_RDONLY, 0)
	if err != nil {
		return "", "", err
	}
	defer func() { returnErr = errors.Join(returnErr, f.Close()) }()
	before, err := admitOpenedRegular(f, path, size)
	if err != nil {
		return "", "", err
	}
	hasher := sha256.New()
	if err := streamRecorded(ctx, f, hasher, uint64(size)); err != nil {
		return "", "", err
	}
	return finishRegularInspection(f, before, path, size, hasher)
}

// finishRegularInspection borrows f and completed digest state; the caller closes f.
func finishRegularInspection(f *os.File, before os.FileInfo, path string, size int64, hasher hash.Hash) (digest, identity string, returnErr error) {
	after, err := f.Stat()
	if err != nil {
		return "", "", err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return "", "", err
	}
	if !os.SameFile(before, after) || !os.SameFile(after, current) || after.Size() != size || after.Mode() != before.Mode() || current.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) {
		return "", "", errors.New("runtime file changed during read")
	}
	identity, err = fileIdentity(f)
	if err != nil {
		return "", "", err
	}
	return fmt.Sprintf("%x", hasher.Sum(nil)), identity, nil
}

func verifyInventory(ctx context.Context, expected receipt) error {
	actual, err := inventory(ctx, expected.Root, false)
	if err != nil {
		return err
	}
	if len(actual.Records) != len(expected.Records) {
		return errors.New("runtime inventory entry count changed")
	}
	for i, row := range actual.Records {
		if row != expected.Records[i] {
			return fmt.Errorf("runtime inventory changed at %q", row.Path)
		}
	}
	return nil
}

func sourceAlias(full, root, relative string, allowed bool) (string, error) {
	if !allowed || relative != "python3.exe" {
		return "", errors.New("unexpected runtime symlink")
	}
	target, err := os.Readlink(full)
	if err != nil {
		return "", err
	}
	if target != filepath.Join(root, "python.exe") {
		return "", errors.New("python3 link target escaped source runtime")
	}
	return target, nil
}

func streamRecorded(ctx context.Context, reader io.Reader, writer io.Writer, size uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	buffer := make([]byte, ioBlockBytes)
	remaining := size
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk := min(uint64(len(buffer)), remaining)
		_, err := io.ReadFull(reader, buffer[:chunk])
		if err != nil {
			return err
		}
		if _, err := writer.Write(buffer[:chunk]); err != nil {
			return err
		}
		remaining -= chunk
	}
	var probe [1]byte
	if n, err := reader.Read(probe[:]); n != 0 || !errors.Is(err, io.EOF) {
		return errors.New("file grew beyond recorded size")
	}
	return nil
}

func appendInventory(out *receipt, budget *inventoryBudget, row record) error {
	size := uint64(len(row.Kind) + len(row.Path) + len(row.Hash) + len(row.Target))
	if err := reserve(&budget.metadata, size, maxMetadataBytes); err != nil {
		return err
	}
	out.Records = append(out.Records, row)
	return nil
}

// openConfined acquires one canonical entry relative to its pinned parent.
// On success the caller owns the returned file independently of the closed root.
func openConfined(path string, flag int, perm os.FileMode) (file *os.File, returnErr error) {
	if err := validateConfinedOpen(path, flag); err != nil {
		return nil, err
	}
	parent := filepath.Dir(path)
	expected, err := os.Lstat(parent)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, err
	}
	defer func() { file, returnErr = finishConfinedOpen(root, file, returnErr) }()
	if err := verifyConfinedParent(root, parent, expected); err != nil {
		return nil, err
	}
	name := filepath.Base(path)
	if path == parent {
		name = "."
	}
	file, returnErr = root.OpenFile(name, flag, perm)
	if returnErr != nil {
		var pathErr *os.PathError
		if errors.As(returnErr, &pathErr) {
			returnErr = &os.PathError{Op: "open", Path: path, Err: pathErr.Err}
		}
		return file, returnErr
	}
	returnErr = errors.Join(verifyOpenedPath(file, path), verifyConfinedParent(root, parent, expected))
	return file, returnErr
}

func validateConfinedOpen(path string, flag int) error {
	if err := validatePathSize(path); err != nil {
		return err
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("path is not canonical absolute")
	}
	if err := canonicalDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if flag == os.O_CREATE|os.O_EXCL|os.O_WRONLY {
		return nil
	}
	if flag != os.O_RDONLY {
		return errors.New("unsupported confined open flags")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return errors.New("invalid confined file type")
	}
	return canonicalPath(path, info.IsDir())
}

func verifyConfinedParent(root *os.Root, path string, expected os.FileInfo) error {
	if err := canonicalDirectory(path); err != nil {
		return err
	}
	opened, err := root.Stat(".")
	if err != nil {
		return err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(expected, opened) || !os.SameFile(opened, current) {
		return errors.New("confined parent identity changed")
	}
	return nil
}

// admitOpenedRegular borrows file and captures its pre-read identity.
func admitOpenedRegular(file *os.File, path string, size int64) (os.FileInfo, error) {
	before, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if before.Size() != size || !before.Mode().IsRegular() {
		return nil, errors.New("file changed before read")
	}
	if err := verifyOpenedPath(file, path); err != nil {
		return nil, err
	}
	return before, nil
}

// finishConfinedOpen closes the temporary root and retains the file only on success.
func finishConfinedOpen(root *os.Root, file *os.File, openErr error) (*os.File, error) {
	openErr = errors.Join(openErr, root.Close())
	if openErr != nil && file != nil {
		openErr = errors.Join(openErr, file.Close())
		file = nil
	}
	return file, openErr
}
