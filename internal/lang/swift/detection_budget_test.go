package swift

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestSwiftRootCarthageProbeHonorsCancellation(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "Sources", swiftMainFileName), "import Foundation\n")

	if _, err := hasSwiftSourceNearRoot(testutil.CanceledContext(), repo); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected already-canceled root probe to return context.Canceled, got %v", err)
	}

	ctx := newSwiftCancellationAfterContext(3)
	if _, err := hasSwiftSourceNearRoot(ctx, repo); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation during root metadata traversal, got %v", err)
	}

	root, err := safeio.OpenRootNoFollow(repo)
	if err != nil {
		t.Fatalf("open test root: %v", err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("close test root: %v", err)
		}
	})
	ctx = newSwiftCancellationAfterContext(2)
	if _, _, err := findSwiftSourceWithinRootDirectory(ctx, root, "Sources", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation while reading source candidates, got %v", err)
	}
}

func TestSwiftRootCarthageProbeFindsRootSourceAndRejectsInvalidCandidate(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "App.SWIFT"), "import Foundation\n")

	found, err := hasSwiftSourceNearRoot(context.Background(), repo)
	if err != nil || !found {
		t.Fatalf("expected regular root Swift source to corroborate metadata, found=%v err=%v", found, err)
	}

	root, err := safeio.OpenRootNoFollow(repo)
	if err != nil {
		t.Fatalf("open test root: %v", err)
	}
	if _, _, err := findSwiftSourceWithinRootDirectory(context.Background(), root, "missing", 1); err == nil {
		t.Fatal("expected invalid source candidate directory to fail")
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close test root: %v", err)
	}
	if _, _, _, err := discoverRootSwiftSourceCandidates(context.Background(), root); err == nil {
		t.Fatal("expected closed root to reject candidate discovery")
	}
}

func TestSwiftRootCarthageProbeRequiresRegularNonSymlinkSource(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "notes.txt"), "not a Swift source\n")
	if err := os.Mkdir(filepath.Join(repo, "Sources"), 0o750); err != nil {
		t.Fatalf("mkdir source directory: %v", err)
	}

	outside := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(outside, swiftMainFileName), "import Foundation\n")
	if err := os.Symlink(filepath.Join(outside, swiftMainFileName), filepath.Join(repo, "Sources", swiftMainFileName)); err != nil {
		t.Fatalf("symlink Swift source: %v", err)
	}

	found, err := hasSwiftSourceNearRoot(context.Background(), repo)
	if err != nil {
		t.Fatalf("probe symlinked source: %v", err)
	}
	if found {
		t.Fatal("expected symlinked Swift source to be ignored as Carthage corroboration")
	}
}

func TestSwiftRootCarthageConfidenceCountsOnlyRegularMetadata(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, carthageManifestName), 0o750); err != nil {
		t.Fatalf("mkdir Cartfile: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(repo, carthageResolvedName), "github \"owner/repo\" \"1.0.0\"\n")

	confidence, err := rootCarthageDetectionConfidence(repo)
	if err != nil {
		t.Fatalf("read root Carthage metadata: %v", err)
	}
	if confidence != 25 {
		t.Fatalf("expected only regular Cartfile.resolved metadata to count, got %d", confidence)
	}

	if _, err := hasSwiftSourceNearRoot(context.Background(), filepath.Join(repo, "missing")); err == nil {
		t.Fatal("expected source probe to reject a missing root")
	}
	if _, err := rootCarthageDetectionConfidence("\x00"); err == nil {
		t.Fatal("expected invalid metadata root to fail")
	}
}

func TestSwiftDetectPropagatesCanceledRootCarthageProbe(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, carthageManifestName), "github \"owner/repo\"\n")

	if _, err := NewAdapter().DetectWithConfidence(testutil.CanceledContext(), repo); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled root Carthage probe to abort detection, got %v", err)
	}
}

func TestSwiftCarthageInputGuardsRejectIncompleteMetadata(t *testing.T) {
	if _, ok := parseCarthageLine("github", false); ok {
		t.Fatal("expected incomplete Carthage declaration to be ignored")
	}
	if _, ok := parseCarthageLine(`github ""`, false); ok {
		t.Fatal("expected Carthage declaration without an identity to be ignored")
	}
	if isLikelyCarthageVersion("1.invalid") {
		t.Fatal("expected non-numeric Carthage version segment to be rejected")
	}

}

type swiftCancellationAfterContext struct {
	calls    int
	cancelAt int
	done     chan struct{}
	canceled bool
}

func newSwiftCancellationAfterContext(cancelAt int) *swiftCancellationAfterContext {
	return &swiftCancellationAfterContext{cancelAt: cancelAt, done: make(chan struct{})}
}

func (*swiftCancellationAfterContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (c *swiftCancellationAfterContext) Done() <-chan struct{} {
	return c.done
}

func (c *swiftCancellationAfterContext) Err() error {
	if c.canceled {
		return context.Canceled
	}
	c.calls++
	if c.calls >= c.cancelAt {
		close(c.done)
		c.canceled = true
		return context.Canceled
	}
	return nil
}

func (*swiftCancellationAfterContext) Value(any) any {
	return nil
}
