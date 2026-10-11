package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"strings"
	"time"

	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMaterialisationPreservesBytesAndRejectsCollision(t *testing.T) {
	root := providerTestRoot(t)
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "python.exe"), []byte("binary\x00bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	captured, err := inventory(context.Background(), source, false)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "private")
	copied, err := materialize(context.Background(), captured, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyInventory(context.Background(), copied); err != nil {
		t.Fatal(err)
	}
	if _, err := materialize(context.Background(), captured, target); err == nil {
		t.Fatal("existing destination overwritten")
	}
	if err := os.WriteFile(filepath.Join(source, "python.exe"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := materialize(context.Background(), captured, filepath.Join(root, "changed")); err == nil {
		t.Fatal("changed captured source accepted")
	}
}

func TestCopyKeepsExclusiveOutputAndCancelledPartialOutput(t *testing.T) {
	root := providerTestRoot(t)
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	digest, err := hashRegular(context.Background(), source, 6)
	if err != nil {
		t.Fatal(err)
	}
	row := record{Bytes: 6, Hash: digest}
	if err := os.WriteFile(destination, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyRecorded(context.Background(), source, destination, row); !errors.Is(err, os.ErrExist) {
		t.Fatalf("exclusive destination not retained: %v", err)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatal("existing output overwritten")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	partial := filepath.Join(root, "partial")
	if err := copyRecorded(ctx, source, partial, row); !errors.Is(err, context.Canceled) {
		t.Fatalf("copy cancellation lost: %v", err)
	}
	info, err := os.Stat(partial)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatal("cancelled copy wrote data")
	}
	if err := copyRecorded(context.Background(), filepath.Join(root, "missing"), filepath.Join(root, "absent"), row); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing copy source error lost: %v", err)
	}
}

func TestMaterialisationRechecksRemovedEmptySource(t *testing.T) {
	root := providerTestRoot(t)
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	captured, err := inventory(context.Background(), source, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "destination")
	if _, err := materialize(context.Background(), captured, destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed source did not fail recheck: %v", err)
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatalf("failed materialisation deleted owned output: %v", err)
	}
}

func TestMaterialisationRechecksDirectoryIdentity(t *testing.T) {
	root := providerTestRoot(t)
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	captured, err := inventory(context.Background(), source, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source, filepath.Join(root, "old-source")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := materialize(context.Background(), captured, filepath.Join(root, "destination")); err == nil || !strings.Contains(err.Error(), "source inventory changed") {
		t.Fatalf("replaced source accepted: %v", err)
	}
}

func TestMaterialisedInventoryComparison(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{name: "equal copied bytes", files: map[string]string{"python.exe": "abc"}},
		{name: "count before content", files: map[string]string{"python.exe": "changed", "extra": "x"}, want: "materialised inventory count differs"},
		{name: "changed size", files: map[string]string{"python.exe": "abcd"}, want: "materialised inventory differs"},
		{name: "changed digest", files: map[string]string{"python.exe": "xyz"}, want: "materialised inventory differs"},
		{name: "renamed path", files: map[string]string{"renamed.exe": "abc"}, want: "materialised inventory differs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := comparisonLayout(t, map[string]string{"python.exe": "abc"})
			copied := comparisonLayout(t, tc.files)
			if source.Root == copied.Root || source.Records[0].Target == copied.Records[0].Target {
				t.Fatal("comparison fixtures do not have independent directory identities")
			}
			err := compareMaterialisedInventory(source, copied)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || err.Error() != tc.want {
				t.Fatalf("comparison error = %v, want %q", err, tc.want)
			}
		})
	}
}

func comparisonLayout(t *testing.T, files map[string]string) receipt {
	t.Helper()
	root := providerTestRoot(t)
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	captured, err := inventory(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	return captured
}

func TestMaterialisedInventoryComparisonKeepsOrder(t *testing.T) {
	source := comparisonLayout(t, map[string]string{"a": "same", "b": "same"})
	copied := comparisonLayout(t, map[string]string{"a": "same", "b": "same"})
	copied.Records[1], copied.Records[2] = copied.Records[2], copied.Records[1]
	if err := compareMaterialisedInventory(source, copied); err == nil || err.Error() != "materialised inventory differs" {
		t.Fatalf("reordered captured records accepted: %v", err)
	}
}

// Missing-root receipts are non-admitted private-helper inputs: inventory
// records the root, and parseReceipt rejects a receipt without records.
func TestMaterialisationPropagatesDestinationInventoryCancellation(t *testing.T) {
	source, destination, before := emptyMaterialisationSource(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ctx.Err() != context.Canceled {
		t.Fatalf("precancelled context = %v, want Canceled", ctx.Err())
	}
	got, err := materialize(ctx, receipt{Root: source}, destination)
	// Value equality requires the exact sentinel pointer, as err == Canceled
	// would; errors.Is independently checks the cancellation contract.
	if reflect.ValueOf(err) != reflect.ValueOf(context.Canceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("destination inventory cancellation = %v, want exact Canceled", err)
	}
	if !reflect.DeepEqual(got, receipt{}) {
		t.Fatalf("cancelled destination inventory published receipt: %+v", got)
	}
	assertRetainedEmptyMaterialisation(t, source, destination, before)
}

func TestMaterialisationPropagatesMissingRootCountMismatch(t *testing.T) {
	source, destination, before := emptyMaterialisationSource(t)
	got, err := materialize(context.Background(), receipt{Root: source}, destination)
	if err == nil || err.Error() != "materialised inventory count differs" {
		t.Fatalf("missing-root count propagation = %v, want materialised inventory count differs", err)
	}
	if !reflect.DeepEqual(got, receipt{}) {
		t.Fatalf("count mismatch published receipt: %+v", got)
	}
	assertRetainedEmptyMaterialisation(t, source, destination, before)
}

func TestMaterialisationPreservesCapturedEmptyInventory(t *testing.T) {
	source, destination, before := emptyMaterialisationSource(t)
	captured, err := inventory(context.Background(), source, false)
	if err != nil {
		t.Fatal(err)
	}
	if captured.Root != source || len(captured.Records) != 1 || captured.Records[0].Kind != "directory" || captured.Records[0].Path != "." {
		t.Fatalf("captured empty source lacks its admitted root row: %+v", captured)
	}
	got, err := materialize(context.Background(), captured, destination)
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != destination || len(got.Records) != 1 || got.Records[0].Target == captured.Records[0].Target {
		t.Fatalf("empty materialisation did not retain independent destination: %+v", got)
	}
	if err := compareMaterialisedInventory(captured, got); err != nil {
		t.Fatal(err)
	}
	if err := verifyInventory(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	assertRetainedEmptyMaterialisation(t, source, destination, before)
}

func emptyMaterialisationSource(t *testing.T) (source, destination string, before os.FileInfo) {
	t.Helper()
	root := providerTestRoot(t)
	source, destination = filepath.Join(root, "source"), filepath.Join(root, "destination")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := canonicalDirectory(source); err != nil {
		t.Fatal(err)
	}
	var err error
	before, err = os.Lstat(source)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(source)
	if err != nil || len(entries) != 0 {
		t.Fatalf("source fixture is not empty: %v, %v", entries, err)
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination fixture is not absent: %v", err)
	}
	return source, destination, before
}

func assertRetainedEmptyMaterialisation(t *testing.T, source, destination string, before os.FileInfo) {
	t.Helper()
	// Observe the source's own metadata; creating the destination may change
	// the common parent mtime. Neither failure path revalidates the source.
	after, err := os.Lstat(source)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("source directory identity, full mode or mtime changed")
	}
	for _, path := range []string{source, destination} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() {
			t.Fatalf("real directory not retained at %q: %v", path, err)
		}
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) != 0 {
			t.Fatalf("retained directory not empty at %q: %v, %v", path, entries, err)
		}
	}
}

func TestRecordedCopyCompletionRetainsOwnersAndErrorPriority(t *testing.T) {
	for _, change := range []string{"unchanged", "input-closed", "size", "mtime", "output-closed", "changed-and-output-closed"} {
		t.Run(change, func(t *testing.T) {
			input, output, before := recordedCompletionFixture(t)
			mutateRecordedCompletion(t, input, output, before, change)
			err := finishRecordedCopy(input, output, before)
			assertRecordedCompletionError(t, err, input, output, change)
			data, readErr := os.ReadFile(output.Name())
			if readErr != nil || string(data) != "recorded bytes" {
				t.Fatalf("copied bytes changed: %q, %v", data, readErr)
			}
			if change != "input-closed" {
				assertCompletionOwner(t, input)
			}
			if change != "output-closed" && change != "changed-and-output-closed" {
				assertCompletionOwner(t, output)
			}
		})
	}
}

func recordedCompletionFixture(t *testing.T) (*os.File, *os.File, os.FileInfo) {
	t.Helper()
	root := providerTestRoot(t)
	data := []byte("recorded bytes")
	path := filepath.Join(root, "source")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	retainCompletionOwner(t, input)
	before, err := input.Stat()
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.OpenFile(filepath.Join(root, "output"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	retainCompletionOwner(t, output)
	hasher := sha256.New()
	if err := streamRecorded(context.Background(), input, io.MultiWriter(output, hasher), uint64(len(data))); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(data)
	if !bytes.Equal(hasher.Sum(nil), want[:]) {
		t.Fatal("streamed digest differs from independent input digest")
	}
	return input, output, before
}

func mutateRecordedCompletion(t *testing.T, input, output *os.File, before os.FileInfo, change string) {
	t.Helper()
	var err error
	switch change {
	case "input-closed":
		err = input.Close()
	case "size", "changed-and-output-closed":
		err = os.WriteFile(input.Name(), []byte("different source size"), 0600)
		if err == nil {
			err = os.Chtimes(input.Name(), before.ModTime(), before.ModTime())
		}
	case "mtime":
		stamp := before.ModTime().Add(-time.Hour)
		err = os.Chtimes(input.Name(), stamp, stamp)
	}
	if err != nil {
		t.Fatal(err)
	}
	if change == "size" || change == "changed-and-output-closed" || change == "mtime" {
		after, err := input.Stat()
		if err != nil {
			t.Fatal(err)
		}
		sizeChanged := after.Size() != before.Size()
		mtimeChanged := !after.ModTime().Equal(before.ModTime())
		wantSizeChange := change != "mtime"
		if sizeChanged != wantSizeChange || mtimeChanged == wantSizeChange || !os.SameFile(before, after) {
			t.Fatalf("copy source mutation not isolated: size changed=%t, mtime changed=%t", sizeChanged, mtimeChanged)
		}
	}
	if change == "output-closed" || change == "changed-and-output-closed" {
		if err := output.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func assertRecordedCompletionError(t *testing.T, err error, input, output *os.File, change string) {
	t.Helper()
	switch change {
	case "unchanged":
		if err != nil {
			t.Fatal(err)
		}
	case "input-closed", "output-closed":
		path, op := input.Name(), "stat"
		if change == "output-closed" {
			path, op = output.Name(), "sync"
		}
		var pathErr *os.PathError
		if !errors.Is(err, os.ErrClosed) || !errors.As(err, &pathErr) || pathErr.Path != path || pathErr.Op != op {
			t.Fatalf("closed owner error lost: %v", err)
		}
	default:
		if err == nil || err.Error() != "copy source identity changed" || errors.Is(err, os.ErrClosed) {
			t.Fatalf("source custody did not precede output Sync: %v", err)
		}
	}
}

func assertCompletionOwner(t *testing.T, file *os.File) {
	t.Helper()
	if _, err := file.Stat(); err != nil {
		t.Fatalf("borrowed owner was closed: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close retained owner: %v", err)
	}
}

func retainCompletionOwner(t *testing.T, file *os.File) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := file.Stat(); errors.Is(err, os.ErrClosed) {
			return // The control or checked success path already closed this owner.
		}
		if err := file.Close(); err != nil {
			t.Errorf("cleanup completion owner: %v", err)
		}
	})
}
