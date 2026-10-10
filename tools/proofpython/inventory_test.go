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
	"reflect"
	"strings"
	"testing"
)

func providerTestRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestInventoryDetectsSubstitutionAndExtraFiles(t *testing.T) {
	root := providerTestRoot(t)
	path := filepath.Join(root, "python.exe")
	if err := os.WriteFile(path, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	original, err := inventory(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyInventory(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyInventory(context.Background(), original); err == nil {
		t.Fatal("same-size substitution accepted")
	}
	if err := os.WriteFile(path, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "extra.pth"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyInventory(context.Background(), original); err == nil {
		t.Fatal("unrecorded file accepted")
	}
}

func TestInventoryRejectsCancellationAndWrongSize(t *testing.T) {
	root := providerTestRoot(t)
	path := filepath.Join(root, "python.exe")
	if err := os.WriteFile(path, []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := inventory(ctx, root, false); err == nil {
		t.Fatal("cancelled inventory accepted")
	}
	for _, size := range []int64{-1, 0, 2, 4, maxFileBytes + 1} {
		if _, err := hashRegular(context.Background(), path, size); err == nil {
			t.Fatalf("wrong size %d accepted", size)
		}
	}
	if _, err := hashRegular(context.Background(), path, 3); err != nil {
		t.Fatal(err)
	}
}

func TestIndependentFileDigestRejectsChangedBytes(t *testing.T) {
	root := providerTestRoot(t)
	path := filepath.Join(root, "verifier")
	if err := os.WriteFile(path, []byte("trusted"), 0600); err != nil {
		t.Fatal(err)
	}
	expected, err := hashRegular(context.Background(), path, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err := authenticateFile(path, expected); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := authenticateFile(path, expected); err == nil {
		t.Fatal("substituted verifier accepted")
	}
}

func TestByteIdenticalReplacementChangesCapturedFileIdentity(t *testing.T) {
	root := providerTestRoot(t)
	original := filepath.Join(root, "python.exe")
	replacement := filepath.Join(root, "replacement.exe")
	for _, path := range []string{original, replacement} {
		if err := os.WriteFile(path, []byte("same"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	digest, identity, err := inspectRegular(context.Background(), original, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, original); err != nil {
		t.Fatal(err)
	}
	changedDigest, changedIdentity, err := inspectRegular(context.Background(), original, 4)
	if err != nil {
		t.Fatal(err)
	}
	if digest != changedDigest || identity == changedIdentity {
		t.Fatalf("replacement identity not distinguished: %s %s", identity, changedIdentity)
	}
}

func TestInventoryRejectsAdmissionBeforeMutation(t *testing.T) {
	root := providerTestRoot(t)
	out := receipt{Root: root}
	budget := inventoryBudget{metadata: maxMetadataBytes}
	if err := appendInventory(&out, &budget, record{Kind: "file", Path: "x"}); err == nil || len(out.Records) != 0 || budget.metadata != maxMetadataBytes {
		t.Fatalf("metadata admission mutated inventory: %v", err)
	}
	for _, relative := range []string{"../escape", "missing"} {
		if err := collectDirectory(context.Background(), &out, &inventoryBudget{}, relative, false); err == nil {
			t.Fatalf("invalid directory accepted: %q", relative)
		}
	}
	if err := collectDirectory(context.Background(), &out, &inventoryBudget{directories: maxDirectories}, ".", false); err == nil {
		t.Fatal("directory capacity accepted")
	}
	if err := collectDirectory(context.Background(), &out, &inventoryBudget{metadata: maxMetadataBytes}, ".", false); err == nil {
		t.Fatal("directory metadata capacity accepted")
	}
}

func TestInventoryClosedHandlesAndMissingSourcesFail(t *testing.T) {
	root := providerTestRoot(t)
	dir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := dir.Close(); err != nil {
		t.Fatal(err)
	}
	out := receipt{Root: root}
	if err := collectEntries(context.Background(), dir, &out, &inventoryBudget{}, ".", false); err == nil {
		t.Fatal("closed directory accepted")
	}
	if _, err := fileIdentity(dir); err == nil {
		t.Fatal("closed identity accepted")
	}
	if err := verifyOpenedPath(dir, root); err == nil {
		t.Fatal("closed handle accepted")
	}
	if _, err := inventory(context.Background(), filepath.Join(root, "missing"), false); err == nil {
		t.Fatal("missing inventory accepted")
	}
	if err := verifyInventory(context.Background(), receipt{Root: filepath.Join(root, "missing")}); err == nil {
		t.Fatal("missing runtime verified")
	}
	if err := collectFile(context.Background(), &out, &inventoryBudget{}, "missing", false); err == nil {
		t.Fatal("missing file collected")
	}
	if err := collectFile(context.Background(), &out, &inventoryBudget{files: maxFiles}, "missing", false); err == nil {
		t.Fatal("file capacity accepted")
	}
}

type providerFailWriter struct{ err error }

func (w *providerFailWriter) Write([]byte) (int, error) { return 0, w.err }

func TestRecordedStreamingPreservesReadWriteAndCancellationFailures(t *testing.T) {
	failure := errors.New("owned output failure")
	if err := streamRecorded(context.Background(), strings.NewReader("abc"), &providerFailWriter{failure}, 3); !errors.Is(err, failure) {
		t.Fatalf("write error lost: %v", err)
	}
	if err := streamRecorded(context.Background(), strings.NewReader("ab"), io.Discard, 3); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncation error lost: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := streamRecorded(ctx, strings.NewReader(""), io.Discard, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("empty cancellation lost: %v", err)
	}
}

func TestMaterializationRejectsInvalidIdentityAndCapacity(t *testing.T) {
	root := providerTestRoot(t)
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	digest, err := hashRegular(context.Background(), source, 3)
	if err != nil {
		t.Fatal(err)
	}
	for name, row := range map[string]record{
		"hash": {Bytes: 3, Hash: "bad"}, "oversize": {Bytes: maxFileBytes + 1, Hash: digest}, "size": {Bytes: 2, Hash: digest}, "changed": {Bytes: 3, Hash: strings.Repeat("0", 64)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := copyRecorded(context.Background(), source, filepath.Join(root, name), row); err == nil {
				t.Fatal("invalid copy accepted")
			}
		})
	}
	for name, budget := range map[string]inventoryBudget{"entries": {entries: maxEntries}, "bytes": {bytes: maxTotalBytes}} {
		t.Run(name, func(t *testing.T) {
			if err := materializeRow(context.Background(), root, root, &budget, record{Kind: "file", Path: "source", Bytes: 3, Hash: digest}); err == nil {
				t.Fatal("materialization capacity accepted")
			}
		})
	}
	if err := materializeRow(context.Background(), root, root, &inventoryBudget{}, record{Kind: "file", Path: "../escape"}); err == nil {
		t.Fatal("escaping copy accepted")
	}
	if _, err := materialize(context.Background(), receipt{Root: root}, filepath.Join(root, "missing", "target")); err == nil {
		t.Fatal("missing destination parent accepted")
	}
}

func TestOpenedIdentityRejectsDifferentAndMissingPath(t *testing.T) {
	root := providerTestRoot(t)
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("same"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	file, err := os.Open(first)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	for _, path := range []string{second, filepath.Join(root, "absent")} {
		if err := verifyOpenedPath(file, path); err == nil {
			t.Fatalf("wrong opened identity accepted: %s", path)
		}
	}
	for _, path := range []string{"relative", root + string(os.PathSeparator) + "..", first + "\x00"} {
		if err := canonicalRegular(path); err == nil {
			t.Fatalf("invalid canonical path accepted: %q", path)
		}
	}
	if err := canonicalRegular(root); err == nil {
		t.Fatal("directory accepted as regular file")
	}
}

func TestNestedInventoryCopiesCompleteLayout(t *testing.T) {
	root := providerTestRoot(t)
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, "Lib", "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "Lib", "nested", "module.py"), []byte("module"), 0600); err != nil {
		t.Fatal(err)
	}
	captured, err := inventory(context.Background(), source, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(captured.Records) != 4 {
		t.Fatalf("incomplete nested inventory: %+v", captured.Records)
	}
	copied, err := materialize(context.Background(), captured, filepath.Join(root, "copy"))
	if err != nil {
		t.Fatal(err)
	}
	if len(copied.Records) != 4 || copied.Records[3].Path != "Lib/nested/module.py" {
		t.Fatalf("nested file lost: %+v", copied.Records)
	}
}

func TestEntryAdmissionRejectsCollisionMetadataAndCancellation(t *testing.T) {
	root := providerTestRoot(t)
	if err := os.WriteFile(filepath.Join(root, "module.py"), []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for name, fixture := range map[string]struct {
		budget inventoryBudget
		seen   map[string]bool
	}{
		"capacity":  {inventoryBudget{entries: maxEntries}, map[string]bool{}},
		"metadata":  {inventoryBudget{metadata: maxMetadataBytes}, map[string]bool{}},
		"collision": {inventoryBudget{}, map[string]bool{"module.py": true}},
	} {
		t.Run(name, func(t *testing.T) {
			out := receipt{Root: root}
			if err := collectEntry(context.Background(), &out, &fixture.budget, ".", false, entries[0], fixture.seen); err == nil || len(out.Records) != 0 {
				t.Fatalf("invalid entry admitted: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := receipt{Root: root}
	if err := collectEntry(ctx, &out, &inventoryBudget{}, ".", false, entries[0], map[string]bool{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("entry cancellation lost: %v", err)
	}
	if err := collectFile(ctx, &out, &inventoryBudget{}, "module.py", false); !errors.Is(err, context.Canceled) {
		t.Fatalf("file cancellation lost: %v", err)
	}
	if err := materializeRow(ctx, root, root, &inventoryBudget{}, record{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("copy cancellation lost: %v", err)
	}
	if err := collectFile(context.Background(), &out, &inventoryBudget{bytes: maxTotalBytes}, "module.py", false); err == nil {
		t.Fatal("aggregate file capacity accepted")
	}
}

type providerCancelReader struct {
	reader io.Reader
	cancel context.CancelFunc
	reads  int
}

func (r *providerCancelReader) Read(data []byte) (int, error) {
	r.reads++
	n, err := r.reader.Read(data)
	r.cancel()
	return n, err
}

func TestRecordedStreamCancelsBetweenRealBlocksAndRejectsGrowth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := &providerCancelReader{reader: strings.NewReader(strings.Repeat("x", ioBlockBytes+1)), cancel: cancel}
	var output strings.Builder
	if err := streamRecorded(ctx, reader, &output, ioBlockBytes+1); !errors.Is(err, context.Canceled) {
		t.Fatalf("midstream cancellation lost: %v", err)
	}
	if reader.reads != 1 || output.Len() != ioBlockBytes {
		t.Fatalf("cancelled read advanced: reads=%d bytes=%d", reader.reads, output.Len())
	}
	if err := streamRecorded(context.Background(), strings.NewReader("abcd"), io.Discard, 3); err == nil || !strings.Contains(err.Error(), "grew") {
		t.Fatalf("extra byte accepted: %v", err)
	}
}

func TestInspectorAndAuthenticatorKeepPrecancelledFailure(t *testing.T) {
	root := providerTestRoot(t)
	path := filepath.Join(root, "python.exe")
	if err := os.WriteFile(path, []byte("actual bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	digest, err := hashRegular(context.Background(), path, 12)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := inspectRegular(ctx, path, 12); !errors.Is(err, context.Canceled) {
		t.Fatalf("inspector cancellation lost: %v", err)
	}
	if err := authenticateFileContext(ctx, path, digest); !errors.Is(err, context.Canceled) {
		t.Fatalf("authentication cancellation lost: %v", err)
	}
	if _, _, err := inspectRegular(context.Background(), filepath.Join(root, "missing"), 0); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing inspector input error lost: %v", err)
	}
	for _, value := range []string{string([]byte{0xff}), "bad\x00path", strings.Repeat("x", maxStringBytes+1)} {
		if err := validateRelative(value); err == nil || !strings.Contains(err.Error(), "invalid or oversized") {
			t.Fatalf("invalid path bytes accepted: %v", err)
		}
	}
}

func TestRegularInspectionWrapperPublishesExactDigestAndIdentity(t *testing.T) {
	path := filepath.Join(providerTestRoot(t), "python.exe")
	data := []byte("real wrapper bytes")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	})
	expectedIdentity, err := fileIdentity(file)
	if err != nil {
		t.Fatal(err)
	}
	expectedDigest := fmt.Sprintf("%x", sha256.Sum256(data))
	digest, identity, err := inspectRegular(context.Background(), path, int64(len(data)))
	if err != nil || digest != expectedDigest || identity != expectedIdentity {
		t.Fatalf("wrapper evidence differs: digest=%q identity=%q err=%v", digest, identity, err)
	}
	hash, err := hashRegular(context.Background(), path, int64(len(data)))
	if err != nil || hash != expectedDigest {
		t.Fatalf("hash wrapper differs: %q %v", hash, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(data) {
		t.Fatalf("wrapper changed bytes: %q %v", got, err)
	}
}

// This fixture owns a real file; completion controls borrow its already-streamed state.
func regularInspectionFixture(t *testing.T) (*os.File, os.FileInfo, string, hash.Hash) {
	t.Helper()
	path := filepath.Join(providerTestRoot(t), "python.exe")
	if err := os.WriteFile(path, []byte("borrowed inspection bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := canonicalRegular(path); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := file.Stat(); errors.Is(err, os.ErrClosed) {
			return // The external-close control already released its owned handle.
		}
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	})
	before, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	hasher := sha256.New()
	if err := streamRecorded(context.Background(), file, hasher, uint64(before.Size())); err != nil {
		t.Fatal(err)
	}
	return file, before, path, hasher
}

func requireUsableInspectionFile(t *testing.T, file *os.File) {
	t.Helper()
	if _, err := file.Stat(); err != nil {
		t.Fatalf("borrowed file no longer usable: %v", err)
	}
	var first [1]byte
	if n, err := file.ReadAt(first[:], 0); err != nil || n != 1 || first[0] != 'b' {
		t.Fatalf("borrowed file read failed: %q %v", first, err)
	}
}

func requireInspectionPathError(t *testing.T, digest, identity string, err, observed error) {
	t.Helper()
	var got, want *os.PathError
	if digest != "" || identity != "" || !errors.As(err, &got) || !errors.As(observed, &want) || !reflect.DeepEqual(got, want) || !errors.Is(err, want.Err) {
		t.Fatalf("completion native error lost: digest=%q identity=%q got=%v want=%v", digest, identity, err, observed)
	}
}

func TestFinishRegularInspectionBorrowsStreamedFile(t *testing.T) {
	file, before, path, hasher := regularInspectionFixture(t)
	expectedIdentity, err := fileIdentity(file)
	if err != nil {
		t.Fatal(err)
	}
	digest, identity, err := finishRegularInspection(file, before, path, before.Size(), hasher)
	expectedDigest := fmt.Sprintf("%x", sha256.Sum256([]byte("borrowed inspection bytes")))
	if err != nil || digest != expectedDigest || identity != expectedIdentity {
		t.Fatalf("completion evidence differs: digest=%q identity=%q err=%v", digest, identity, err)
	}
	requireUsableInspectionFile(t, file)
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "borrowed inspection bytes" {
		t.Fatalf("completion changed bytes: %q %v", got, err)
	}
}

func TestFinishRegularInspectionPreservesExternalStatError(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing=%t", missing), func(t *testing.T) {
			checkExternalInspectionStatError(t, missing)
		})
	}
}

func checkExternalInspectionStatError(t *testing.T, missing bool) {
	t.Helper()
	file, before, path, hasher := regularInspectionFixture(t)
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if missing {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("absent-path witness unavailable: %v", err)
		}
	}
	_, observed := file.Stat()
	if !errors.Is(observed, os.ErrClosed) {
		t.Fatalf("closed-FD witness unavailable: %v", observed)
	}
	digest, identity, err := finishRegularInspection(file, before, path, before.Size(), hasher)
	requireInspectionPathError(t, digest, identity, err, observed)
	if !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Stat must dominate absent path: %v", err)
	}
}

func TestRecordedStreamingUnsignedChunkBoundaries(t *testing.T) {
	for _, size := range []int{0, 1, ioBlockBytes, ioBlockBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			data := strings.Repeat("x", size)
			var output strings.Builder
			if err := streamRecorded(context.Background(), strings.NewReader(data), &output, uint64(size)); err != nil {
				t.Fatal(err)
			}
			if output.String() != data {
				t.Fatal("bounded stream changed bytes")
			}
			if err := streamRecorded(context.Background(), strings.NewReader(data+"x"), io.Discard, uint64(size)); err == nil {
				t.Fatal("growth beyond unsigned chunk boundary accepted")
			}
		})
	}
}

func TestConfinedFilesRetainCallerOwnership(t *testing.T) {
	root := providerTestRoot(t)
	path := filepath.Join(root, "owned")
	output, err := openConfined(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write([]byte("bytes")); err != nil {
		t.Fatal(err)
	}
	if err := output.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	file, collisionErr := openConfined(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	var pathErr *os.PathError
	if !errors.Is(collisionErr, os.ErrExist) || file != nil || !errors.As(collisionErr, &pathErr) {
		t.Fatalf("existing destination error lost: %v %v", file, collisionErr)
	}
	if pathErr.Op != "open" || pathErr.Path != path || !errors.Is(pathErr.Err, os.ErrExist) {
		t.Fatalf("confined acquisition changed original error metadata: %+v", pathErr)
	}
	input, err := openConfined(path, os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	var data [5]byte
	if _, err := input.ReadAt(data[:], 0); err != nil || string(data[:]) != "bytes" {
		t.Fatalf("returned handle unusable: %q %v", data, err)
	}
	if _, err := fileIdentity(input); err != nil {
		t.Fatal(err)
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	assertConfinedDirectoryOwnership(t, root)
}

func TestConfinedOpenRejectsInvalidAcquisition(t *testing.T) {
	root := providerTestRoot(t)
	for name, value := range map[string]struct {
		path string
		flag int
	}{
		"relative":       {"relative", os.O_RDONLY},
		"invalid bytes":  {root + "\x00", os.O_RDONLY},
		"missing parent": {filepath.Join(root, "absent", "child"), os.O_CREATE | os.O_EXCL | os.O_WRONLY},
		"missing leaf":   {filepath.Join(root, "absent"), os.O_RDONLY},
		"truncate":       {filepath.Join(root, "absent"), os.O_TRUNC | os.O_WRONLY},
		"unclean":        {root + string(os.PathSeparator) + ".", os.O_RDONLY},
	} {
		t.Run(name, func(t *testing.T) {
			if file, err := openConfined(value.path, value.flag, 0600); err == nil || file != nil {
				t.Fatalf("invalid acquisition accepted: %v %v", file, err)
			}
		})
	}
	if file, err := openConfined(filepath.Join(root, "invalid-mode"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.ModeDir); err == nil || file != nil {
		t.Fatalf("invalid create mode accepted: %v %v", file, err)
	}
}

func TestConfinedParentRejectsDifferentOrClosedRoot(t *testing.T) {
	path := providerTestRoot(t)
	other := providerTestRoot(t)
	expected, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(other)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyConfinedParent(root, path, expected); err == nil {
		t.Fatal("different parent accepted")
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if err := verifyConfinedParent(root, path, expected); err == nil {
		t.Fatal("closed parent accepted")
	}
	if err := verifyConfinedParent(root, filepath.Join(path, "missing"), expected); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing parent error lost: %v", err)
	}
}

func TestConfinedVolumeRootRemainsCallerOwned(t *testing.T) {
	path := filepath.VolumeName(providerTestRoot(t)) + string(os.PathSeparator)
	file, err := openConfined(path, os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		t.Fatalf("volume root handle unavailable: %v %v", info, err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenedRegularAdmissionRetainsBorrowedHandle(t *testing.T) {
	for _, mode := range []string{"success", "wrong-size", "closed", "missing", "replaced"} {
		t.Run(mode, func(t *testing.T) {
			file, path, size := openedAdmissionFixture(t, mode)
			info, err := admitOpenedRegular(file, path, size)
			assertOpenedAdmission(t, file, mode, info, err)
		})
	}
}

func openedAdmissionFixture(t *testing.T, mode string) (*os.File, string, int64) {
	t.Helper()
	root := providerTestRoot(t)
	path := filepath.Join(root, "file")
	if err := os.WriteFile(path, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode == "closed" {
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Cleanup(func() {
			if err := file.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	changeAdmissionNamespace(t, mode, root, path)
	size := int64(4)
	if mode == "wrong-size" {
		size++
	}
	return file, path, size
}

func assertOpenedAdmission(t *testing.T, file *os.File, mode string, info os.FileInfo, err error) {
	t.Helper()
	if mode == "success" {
		current, statErr := file.Stat()
		if err != nil || statErr != nil || !os.SameFile(info, current) {
			t.Fatalf("admitted identity changed: %v %v", err, statErr)
		}
	} else if err == nil || info != nil {
		t.Fatalf("invalid borrowed file admitted: %v %v", info, err)
	}
	if mode == "closed" {
		if !errors.Is(err, os.ErrClosed) {
			t.Fatalf("closed handle error lost: %v", err)
		}
		return
	}
	if mode == "missing" && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing path error lost: %v", err)
	}
	if mode == "wrong-size" && err.Error() != "file changed before read" {
		t.Fatalf("size admission error changed: %v", err)
	}
	var data [4]byte
	if _, err := file.ReadAt(data[:], 0); err != nil || string(data[:]) != "data" {
		t.Fatalf("borrowed owner became unusable: %q %v", data, err)
	}
}

func assertConfinedDirectoryOwnership(t *testing.T, root string) {
	t.Helper()
	directory, err := openConfined(root, os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := directory.ReadDir(-1)
	if err != nil || len(entries) != 1 || entries[0].Name() != "owned" {
		t.Fatalf("returned directory unusable: %v %v", entries, err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
}

func changeAdmissionNamespace(t *testing.T, mode, root, path string) {
	t.Helper()
	if mode == "missing" || mode == "replaced" {
		if err := os.Rename(path, filepath.Join(root, "moved")); err != nil {
			t.Fatal(err)
		}
	}
	if mode == "replaced" {
		if err := os.WriteFile(path, []byte("else"), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
