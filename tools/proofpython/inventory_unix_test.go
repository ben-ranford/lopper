//go:build unix

package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"reflect"
	"strings"
	"time"

	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSourceAliasMaterialisesOnlyExactInterpreterTarget(t *testing.T) {
	root := providerTestRoot(t)
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	interpreter := filepath.Join(source, "python.exe")
	alias := filepath.Join(source, "python3.exe")
	if err := os.WriteFile(interpreter, []byte("interpreter"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(interpreter, alias); err != nil {
		t.Fatal(err)
	}
	captured, err := inventory(context.Background(), source, true)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "private")
	if _, err := materialize(context.Background(), captured, destination); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(destination, "python3.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatal("source alias remained a link")
	}
	if _, err := inventory(context.Background(), source, false); err == nil {
		t.Fatal("private runtime admitted symlink")
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside"), alias); err != nil {
		t.Fatal(err)
	}
	if _, err := inventory(context.Background(), source, true); err == nil {
		t.Fatal("escaped source alias accepted")
	}
}

func TestInventoryRejectsFIFOWithoutOpeningIt(t *testing.T) {
	root := providerTestRoot(t)
	if err := syscall.Mkfifo(filepath.Join(root, "python.exe"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := inventory(context.Background(), root, false); err == nil {
		t.Fatal("FIFO admitted as runtime file")
	}
}

func TestInventoryRejectsSymlinkAncestorWithoutReadingTarget(t *testing.T) {
	root := providerTestRoot(t)
	target := filepath.Join(root, "target")
	alias := filepath.Join(root, "alias")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := inventory(context.Background(), alias, false); err == nil || !strings.Contains(err.Error(), "reparse") {
		t.Fatalf("symlink ancestor admitted: %v", err)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("rejected inventory modified target")
	}
}

func TestRealEntryRejectsInvalidComponentBeforeRecording(t *testing.T) {
	root := providerTestRoot(t)
	if err := os.WriteFile(filepath.Join(root, "module."), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	out := receipt{Root: root}
	if err := collectEntry(context.Background(), &out, &inventoryBudget{}, ".", false, entries[0], map[string]bool{}); err == nil || !strings.Contains(err.Error(), "invalid inventory component") {
		t.Fatalf("invalid actual entry accepted: %v", err)
	}
	if len(out.Records) != 0 {
		t.Fatal("invalid entry recorded")
	}
}

func TestAllowedSourceAliasRequiresExistingInterpreter(t *testing.T) {
	root := providerTestRoot(t)
	if err := os.Symlink(filepath.Join(root, "python.exe"), filepath.Join(root, "python3.exe")); err != nil {
		t.Fatal(err)
	}
	if _, err := inventory(context.Background(), root, true); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing allowed link target accepted: %v", err)
	}
}

func TestCachePresenceDoesNotReplaceInventoryLinkAdmission(t *testing.T) {
	root := providerTestRoot(t)
	runtime := filepath.Join(root, "Python", "3.13.16", "x64")
	if err := os.MkdirAll(runtime, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtime+".complete", nil, 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "interpreter")
	if err := os.WriteFile(target, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(runtime, "python.exe")); err != nil {
		t.Fatal(err)
	}
	selected, err := cachedPython(root, root)
	if err != nil || selected != runtime {
		t.Fatalf("cache Stat presence contract changed: %q %v", selected, err)
	}
	if _, err := inventory(context.Background(), selected, true); err == nil || !strings.Contains(err.Error(), "unexpected runtime symlink") {
		t.Fatalf("inventory admitted interpreter link: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fixture" {
		t.Fatal("rejected inventory changed target")
	}
	entries, err := os.ReadDir(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Type()&os.ModeSymlink == 0 {
		t.Fatal("cache inspection provisioned or replaced files")
	}
}

func TestSourceAliasRetainsReadlinkError(t *testing.T) {
	root := providerTestRoot(t)
	path := filepath.Join(root, "python3.exe")
	if err := os.WriteFile(path, []byte("regular interpreter"), 0600); err != nil {
		t.Fatal(err)
	}
	target, err := sourceAlias(path, root, "python3.exe", true)
	var pathErr *os.PathError
	if target != "" || !errors.As(err, &pathErr) || pathErr.Op != "readlink" || pathErr.Path != path {
		t.Fatalf("Readlink error identity lost: target=%q err=%v", target, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "regular interpreter" {
		t.Fatalf("regular file changed: %q", data)
	}
}

func TestMaterialisedInventoryComparisonAllowsCopiedModes(t *testing.T) {
	source := comparisonLayout(t, nil)
	copied := comparisonLayout(t, nil)
	if err := os.Chmod(copied.Root, os.FileMode(source.Records[0].Mode)^0044); err != nil {
		t.Fatal(err)
	}
	copied, err := inventory(context.Background(), copied.Root, false)
	if err != nil {
		t.Fatal(err)
	}
	if source.Records[0].Mode == copied.Records[0].Mode || source.Records[0].Target == copied.Records[0].Target {
		t.Fatal("fixture did not capture different copied modes and identities")
	}
	if err := compareMaterialisedInventory(source, copied); err != nil {
		t.Fatalf("copied directory mode/identity affected content comparison: %v", err)
	}
}

func requireDeniedOpen(t *testing.T, path string, err error) {
	t.Helper()
	var pathErr *os.PathError
	if !errors.Is(err, os.ErrPermission) || !errors.As(err, &pathErr) || pathErr.Op != "open" || pathErr.Path != path {
		t.Fatalf("expected permission-denied open of %q, got %v", path, err)
	}
}

func denyProviderReads(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, info.Mode().Perm()); err != nil {
			t.Error(err)
		}
	})
	if err := os.Chmod(path, info.Mode().Perm()&^0444); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err == nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Error(closeErr)
		}
		t.Fatal("host admitted restricted native open; permission control unavailable")
	}
	requireDeniedOpen(t, path, err)
}

func unreadableProviderFile(t *testing.T) (string, string, record) {
	t.Helper()
	root := providerTestRoot(t)
	path := filepath.Join(root, "python.exe")
	data := make([]byte, 90)
	copy(data, "MZ")
	binary.LittleEndian.PutUint32(data[60:], 64)
	copy(data[64:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(data[68:], 0x8664)
	binary.LittleEndian.PutUint16(data[88:], 0x20b)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	captured, err := inventory(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireAMD64File(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, data) {
			t.Errorf("source bytes changed: %v", err)
		}
		if err := verifyInventory(context.Background(), captured); err != nil {
			t.Error(err)
		}
	})
	denyProviderReads(t, path)
	if len(captured.Records) != 2 || captured.Records[1].Path != "python.exe" {
		t.Fatal("unexpected captured provider layout")
	}
	return root, path, captured.Records[1]
}

func TestInventoryPropagatesDeniedFileOpen(t *testing.T) {
	root, path, row := unreadableProviderFile(t)
	digest, identity, err := inspectRegular(context.Background(), path, int64(row.Bytes))
	requireDeniedOpen(t, path, err)
	if digest != "" || identity != "" {
		t.Fatal("denied inspection returned partial evidence")
	}
	got, err := inventory(context.Background(), root, false)
	requireDeniedOpen(t, path, err)
	if !reflect.DeepEqual(got, receipt{}) {
		t.Fatal("denied inventory returned partial evidence")
	}
}

func TestCopyDeniedSourceKeepsDestinations(t *testing.T) {
	_, path, row := unreadableProviderFile(t)
	root := providerTestRoot(t)
	missing := filepath.Join(root, "missing")
	requireDeniedOpen(t, path, copyRecorded(context.Background(), path, missing, row))
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("denied source created destination: %v", err)
	}
	existing := filepath.Join(root, "existing")
	if err := os.WriteFile(existing, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	requireDeniedOpen(t, path, copyRecorded(context.Background(), path, existing, row))
	got, err := os.ReadFile(existing)
	if err != nil || string(got) != "keep" {
		t.Fatalf("existing destination changed: %q %v", got, err)
	}
}

func TestPEPropagatesDeniedFileOpen(t *testing.T) {
	_, path, _ := unreadableProviderFile(t)
	requireDeniedOpen(t, path, requireAMD64File(path))
}

func TestInventoryPropagatesDeniedDirectoryOpen(t *testing.T) {
	root := providerTestRoot(t)
	before, err := inventory(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := verifyInventory(context.Background(), before); err != nil {
			t.Error(err)
		}
	})
	denyProviderReads(t, root)
	got, err := inventory(context.Background(), root, false)
	requireDeniedOpen(t, root, err)
	if !reflect.DeepEqual(got, receipt{}) {
		t.Fatal("denied directory returned partial inventory")
	}
}

func TestFinishRegularInspectionPreservesUnlinkedLstatError(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed=%t", changed), func(t *testing.T) {
			checkUnlinkedInspectionLstatError(t, changed)
		})
	}
}

func checkUnlinkedInspectionLstatError(t *testing.T, changed bool) {
	t.Helper()
	file, before, path, hasher := regularInspectionFixture(t)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if changed {
		if err := file.Truncate(before.Size() + 1); err != nil {
			t.Fatal(err)
		}
	}
	after, err := file.Stat()
	if err != nil || (changed && after.Size() == before.Size()) {
		t.Fatalf("live unlinked-FD witness unavailable: %v", err)
	}
	_, observed := os.Lstat(path)
	if !errors.Is(observed, os.ErrNotExist) {
		t.Fatalf("real unlink witness unavailable: %v", observed)
	}
	digest, identity, err := finishRegularInspection(file, before, path, before.Size(), hasher)
	requireInspectionPathError(t, digest, identity, err, observed)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Lstat must dominate metadata mismatch: %v", err)
	}
	requireUsableInspectionFile(t, file)
}

func TestFinishRegularInspectionRejectsByteIdenticalReplacement(t *testing.T) {
	file, before, path, hasher := regularInspectionFixture(t)
	replacement := filepath.Join(filepath.Dir(path), "replacement.exe")
	if err := os.WriteFile(replacement, []byte("borrowed inspection bytes"), before.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	other, err := os.Open(replacement)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := other.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	after, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	current, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || os.SameFile(after, current) || current.Size() != before.Size() || current.Mode() != before.Mode() || !current.ModTime().Equal(before.ModTime()) {
		t.Fatal("byte-identical replacement did not isolate path identity mismatch")
	}
	requireInspectionChanged(t, file, before, path, hasher)
	requireInspectionFileBytes(t, file, before.Size())
	requireInspectionFileBytes(t, other, before.Size())
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "borrowed inspection bytes" {
		t.Fatalf("replacement path bytes changed: %q %v", got, err)
	}
}

func TestFinishRegularInspectionRejectsObservedSizeChange(t *testing.T) {
	file, before, path, hasher := regularInspectionFixture(t)
	if err := file.Truncate(before.Size() + 1); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() == before.Size() || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) || !os.SameFile(before, after) {
		t.Fatal("size mutation did not isolate an effective observed change")
	}
	requireInspectionChanged(t, file, before, path, hasher)
}

func TestFinishRegularInspectionRejectsObservedModeChange(t *testing.T) {
	file, before, path, hasher := regularInspectionFixture(t)
	if err := file.Chmod(before.Mode().Perm() ^ 0040); err != nil {
		t.Fatal(err)
	}
	after, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode() == before.Mode() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || !os.SameFile(before, after) {
		t.Fatal("mode mutation did not isolate an effective full-mode change")
	}
	requireInspectionChanged(t, file, before, path, hasher)
}

func TestFinishRegularInspectionRejectsObservedMtimeChange(t *testing.T) {
	file, before, path, hasher := regularInspectionFixture(t)
	if err := os.Chtimes(path, before.ModTime(), before.ModTime().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	after, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() || after.Mode() != before.Mode() || !os.SameFile(before, after) {
		t.Fatal("mtime mutation did not isolate an effective observed change")
	}
	requireInspectionChanged(t, file, before, path, hasher)
}

func requireInspectionChanged(t *testing.T, file *os.File, before os.FileInfo, path string, hasher hash.Hash) {
	t.Helper()
	digest, identity, err := finishRegularInspection(file, before, path, before.Size(), hasher)
	if digest != "" || identity != "" || err == nil || err.Error() != "runtime file changed during read" {
		t.Fatalf("changed completion accepted: digest=%q identity=%q err=%v", digest, identity, err)
	}
	requireUsableInspectionFile(t, file)
}

func requireInspectionFileBytes(t *testing.T, file *os.File, size int64) {
	t.Helper()
	got := make([]byte, size)
	if _, err := file.ReadAt(got, 0); err != nil || string(got) != "borrowed inspection bytes" {
		t.Fatalf("held replacement bytes changed: %q %v", got, err)
	}
}

func TestDirectoryCompletionPreservesCollectionAndCustodyErrors(t *testing.T) {
	for _, change := range []string{"unchanged", "missing", "mode", "mtime"} {
		for _, failedCollection := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/collection-error-%t", change, failedCollection), func(t *testing.T) {
				dir, full, before, collectErr := directoryCompletionFixture(t)
				if !failedCollection {
					collectErr = nil
				}
				mutateCompletionDirectory(t, full, before, change)
				err := finishDirectoryCollection(before, full, collectErr)
				assertDirectoryCompletionError(t, err, collectErr, full, change)
				if _, err := dir.Stat(); err != nil {
					t.Fatalf("borrowed directory no longer usable: %v", err)
				}
			})
		}
	}
}

func directoryCompletionFixture(t *testing.T) (*os.File, string, os.FileInfo, error) {
	t.Helper()
	full := filepath.Join(providerTestRoot(t), "held")
	if err := os.Mkdir(full, 0700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(full, "module.")
	if err := os.WriteFile(child, []byte("invalid name"), 0600); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(full)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := dir.Close(); err != nil {
			t.Errorf("close directory owner: %v", err)
		}
	})
	collectErr := collectEntries(context.Background(), dir, &receipt{Root: full}, &inventoryBudget{}, ".", false)
	if collectErr == nil {
		t.Fatal("invalid child accepted")
	}
	if err := os.Remove(child); err != nil {
		t.Fatal(err)
	}
	before, err := dir.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return dir, full, before, collectErr
}

func mutateCompletionDirectory(t *testing.T, full string, before os.FileInfo, change string) {
	t.Helper()
	var err error
	switch change {
	case "missing":
		err = os.Remove(full)
	case "mode":
		err = os.Chmod(full, 0500)
	case "mtime":
		stamp := before.ModTime().Add(-time.Hour)
		err = os.Chtimes(full, stamp, stamp)
	}
	if err != nil {
		t.Fatal(err)
	}
	current, err := os.Lstat(full)
	if change == "missing" {
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing directory witness: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if change == "mode" && before.Mode() == current.Mode() || change == "mtime" && before.ModTime().Equal(current.ModTime()) {
		t.Fatal("directory mutation did not take effect")
	}
}

type Unwrapper interface {
	Unwrap() []error
}

func assertDirectoryCompletionError(t *testing.T, err, collectErr error, full, change string) {
	t.Helper()
	if change == "unchanged" {
		if !reflect.ValueOf(err).Equal(reflect.ValueOf(collectErr)) {
			t.Fatalf("collection error identity changed: %v", err)
		}
		return
	}
	joined, ok := err.(Unwrapper)
	if !ok {
		t.Fatalf("completion did not join errors: %v", err)
	}
	members := joined.Unwrap()
	if collectErr != nil {
		if len(members) != 2 || !reflect.ValueOf(members[0]).Equal(reflect.ValueOf(collectErr)) || !errors.Is(err, collectErr) {
			t.Fatalf("collection error lost or reordered: %v", err)
		}
		members = members[1:]
	}
	if len(members) != 1 {
		t.Fatalf("unexpected custody errors: %v", members)
	}
	if change == "missing" {
		var pathErr *os.PathError
		if !errors.As(members[0], &pathErr) || pathErr.Op != "lstat" || pathErr.Path != full || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("typed Lstat error lost: %v", err)
		}
	} else if members[0].Error() != "directory changed during inventory" {
		t.Fatalf("wrong custody error: %v", err)
	}
}

func TestConfinedOpenRejectsSymlinkLeafAndAncestor(t *testing.T) {
	root := providerTestRoot(t)
	target := providerTestRoot(t)
	file := filepath.Join(target, "python.exe")
	if err := os.WriteFile(file, []byte("keep"), 0700); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(root, "leaf")
	ancestor := filepath.Join(root, "ancestor")
	if err := os.Symlink(file, leaf); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, ancestor); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{leaf, filepath.Join(ancestor, "python.exe")} {
		if opened, err := openConfined(path, os.O_RDONLY, 0); err == nil || opened != nil {
			t.Fatalf("symlink read accepted: %v %v", opened, err)
		}
		if cmd, err := pythonCommand(context.Background(), path, nil, nil, nil, nil, nil); err == nil || cmd != nil {
			t.Fatalf("symlink executable accepted: %v %v", cmd, err)
		}
		if opened, err := openConfined(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600); err == nil || opened != nil {
			t.Fatalf("symlink create accepted: %v %v", opened, err)
		}
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "keep" {
		t.Fatalf("symlink rejection changed target: %q %v", data, err)
	}
}

func TestPythonCommandRejectsNonRegularFIFO(t *testing.T) {
	path := filepath.Join(providerTestRoot(t), "python.exe")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if cmd, err := pythonCommand(context.Background(), path, nil, nil, nil, nil, nil); err == nil || cmd != nil {
		t.Fatalf("FIFO executable accepted: %v %v", cmd, err)
	}
	if file, err := openConfined(path, os.O_RDONLY, 0); err == nil || file != nil {
		t.Fatalf("FIFO read accepted: %v %v", file, err)
	}
}

func TestConfinedOpenPreservesDeniedParentAcquisition(t *testing.T) {
	parent := providerTestRoot(t)
	path := filepath.Join(parent, "child")
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(parent, 0700); err != nil {
			t.Error(err)
		}
	})
	if err := os.Chmod(parent, 0100); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(parent)
	if err == nil {
		if closeErr := root.Close(); closeErr != nil {
			t.Error(closeErr)
		}
		t.Fatal("host admitted restricted parent open; permission control unavailable")
	}
	requireDeniedOpen(t, parent, err)
	file, err := openConfined(path, os.O_RDONLY, 0)
	if file != nil {
		t.Fatal("denied parent returned an owned file")
	}
	requireDeniedOpen(t, parent, err)
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "keep" {
		t.Fatalf("failed parent acquisition changed child: %q %v", data, err)
	}
}

func TestConfinedCompletionTransfersOrClosesOwnedFile(t *testing.T) {
	for _, mode := range []string{"success", "acquisition-error", "closed-file", "nil-file"} {
		t.Run(mode, func(t *testing.T) {
			root, file, acquisitionErr := confinedCompletionFixture(t, mode)
			returned, completionErr := finishConfinedOpen(root, file, acquisitionErr)
			if _, err := root.Stat("."); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("temporary root ownership retained: %v", err)
			}
			assertConfinedCompletionOwnership(t, mode, file, returned, completionErr)
			if mode != "success" {
				assertConfinedCompletionErrors(t, mode, acquisitionErr, completionErr)
			}
		})
	}
}

func confinedCompletionFixture(t *testing.T, mode string) (*os.Root, *os.File, error) {
	t.Helper()
	path := providerTestRoot(t)
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	var file *os.File
	if mode != "nil-file" {
		file, err = os.Create(filepath.Join(path, "owned"))
		if err != nil {
			t.Fatal(err)
		}
	}
	if mode == "closed-file" {
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	var acquisitionErr error
	if mode != "success" {
		_, acquisitionErr = os.Lstat(filepath.Join(path, "missing"))
		if !errors.Is(acquisitionErr, os.ErrNotExist) {
			t.Fatalf("fixture did not create real failed acquisition: %v", acquisitionErr)
		}
	}
	return root, file, acquisitionErr
}

func assertConfinedCompletionOwnership(t *testing.T, mode string, file, returned *os.File, completionErr error) {
	t.Helper()
	if mode == "success" {
		if completionErr != nil || returned != file {
			t.Fatalf("successful owner transfer failed: %v %v", returned, completionErr)
		}
		if _, err := returned.Write([]byte("owned")); err != nil {
			t.Fatal(err)
		}
		if err := returned.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}
	if returned != nil {
		t.Fatalf("failed acquisition retained file: %v", returned)
	}
	if file != nil {
		if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("failed acquisition leaked file owner: %v", err)
		}
	}
}

func assertConfinedCompletionErrors(t *testing.T, mode string, acquisitionErr, completionErr error) {
	t.Helper()
	if !errors.Is(completionErr, acquisitionErr) {
		t.Fatalf("failed acquisition error lost: %v", completionErr)
	}
	joined, ok := completionErr.(Unwrapper)
	if !ok {
		t.Fatalf("completion aggregate missing: %T", completionErr)
	}
	members := joined.Unwrap()
	if mode == "nil-file" {
		if len(members) != 1 || !errors.Is(members[0], acquisitionErr) {
			t.Fatalf("nil-file acquisition error order changed: %v", members)
		}
		return
	}
	wantMembers := 1
	if mode == "closed-file" {
		wantMembers = 2
	}
	if len(members) != wantMembers {
		t.Fatalf("unexpected completion errors: %v", members)
	}
	first, ok := members[0].(Unwrapper)
	if !ok || len(first.Unwrap()) != 1 || !errors.Is(first.Unwrap()[0], acquisitionErr) {
		t.Fatalf("acquisition error no longer precedes close: %v", members)
	}
	if mode == "closed-file" && !errors.Is(members[1], os.ErrClosed) {
		t.Fatalf("actual file Close error lost: %v", members)
	}
}
