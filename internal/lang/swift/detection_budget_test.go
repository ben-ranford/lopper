package swift

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestSwiftRootCarthageProbeHonorsCancellation(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "Sources", swiftMainFileName), "import Foundation\n")

	if _, _, err := probeSwiftSourceWithinRoot(testutil.CanceledContext(), repo, maxRootCarthageSourceTraversalEntries); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected already-canceled root probe to return context.Canceled, got %v", err)
	}

	ctx := newSwiftCancellationAfterContext(3)
	if _, _, err := probeSwiftSourceWithinRoot(ctx, repo, maxRootCarthageSourceTraversalEntries); !errors.Is(err, context.Canceled) {
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

	found, _, err := probeSwiftSourceWithinRoot(context.Background(), repo, maxRootCarthageSourceTraversalEntries)
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
	if _, _, _, err := discoverRootSwiftSourceCandidatesWithinLimit(context.Background(), root, maxRootCarthageSourceRootEntries); err == nil {
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

	found, _, err := probeSwiftSourceWithinRoot(context.Background(), repo, maxRootCarthageSourceTraversalEntries)
	if err != nil {
		t.Fatalf("probe symlinked source: %v", err)
	}
	if found {
		t.Fatal("expected symlinked Swift source to be ignored as Carthage corroboration")
	}
}

func TestSwiftDetectionRequiresRegularCarthageAndSwiftEntries(t *testing.T) {
	detectBroadSignals := func(repo string) (language.Detection, error) {
		detection := language.Detection{}
		err := walkSwiftDetection(context.Background(), repo, &detection, map[string]struct{}{}, false)
		return detection, err
	}
	for _, test := range []struct {
		name            string
		regularCarthage bool
		regularSwift    bool
		wantMatched     bool
		wantConfidence  int
		detect          func(string) (language.Detection, error)
	}{
		{name: "regular Cartfile with symlink Swift", regularCarthage: true, detect: func(repo string) (language.Detection, error) {
			return NewAdapter().DetectWithConfidence(context.Background(), repo)
		}},
		{name: "regular Swift with symlink Cartfile", regularSwift: true, wantMatched: true, wantConfidence: 2, detect: detectBroadSignals},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := t.TempDir()
			writeSwiftDetectionRegularityFixture(t, repo, test.regularCarthage, test.regularSwift)

			detection, err := test.detect(repo)
			if err != nil {
				t.Fatalf("detect non-regular entries: %v", err)
			}
			if detection.Matched != test.wantMatched || detection.Confidence != test.wantConfidence {
				t.Fatalf("detection = %#v, want matched=%v confidence=%d", detection, test.wantMatched, test.wantConfidence)
			}
		})
	}
}

func writeSwiftDetectionRegularityFixture(t *testing.T, repo string, regularCarthage, regularSwift bool) {
	t.Helper()

	outside := t.TempDir()
	cartfilePath := filepath.Join(repo, carthageManifestName)
	swiftPath := filepath.Join(repo, "main.swift")
	testutil.MustWriteFile(t, filepath.Join(outside, carthageManifestName), "github \"owner/repo\"\n")
	testutil.MustWriteFile(t, filepath.Join(outside, "main.swift"), "import Foundation\n")
	if regularCarthage {
		testutil.MustWriteFile(t, cartfilePath, "github \"owner/repo\"\n")
	} else if err := os.Symlink(filepath.Join(outside, carthageManifestName), cartfilePath); err != nil {
		t.Fatalf("symlink Cartfile: %v", err)
	}
	if regularSwift {
		testutil.MustWriteFile(t, swiftPath, "import Foundation\n")
	} else if err := os.Symlink(filepath.Join(outside, "main.swift"), swiftPath); err != nil {
		t.Fatalf("symlink Swift source: %v", err)
	}
}

func TestSwiftNestedCarthageProbeSharesBudgetFairly(t *testing.T) {
	repo := t.TempDir()
	first := filepath.Join(repo, "a-first")
	second := filepath.Join(repo, "b-second")
	writeSwiftProbeFiles(t, first, maxNestedCarthageSourceTraversalEntries, false)
	writeSwiftProbeFiles(t, second, 0, true)

	detection := language.Detection{}
	roots := map[string]struct{}{}
	err := applyCarthageDetectionRoots(context.Background(), repo, &detection, roots,
		map[string]int{first: 10, second: 10}, map[string]struct{}{})
	if err != nil {
		t.Fatalf("probe nested Carthage roots: %v", err)
	}
	if _, foundFirst := roots[first]; !detection.Matched || !rootsContain(roots, second) || foundFirst {
		t.Fatalf("expected the fairly budgeted second root to be retained, got detection=%#v roots=%#v", detection, roots)
	}
}

func TestSwiftCarthageProbeReportsActualEntries(t *testing.T) {
	repo := t.TempDir()
	writeSwiftProbeFiles(t, repo, 4, false)

	for _, test := range []struct {
		budget      int
		wantEntries int
	}{
		{budget: 2, wantEntries: 2},
		{budget: maxNestedCarthageSourceTraversalEntries, wantEntries: 4},
	} {
		found, entries, err := probeSwiftSourceWithinRoot(context.Background(), repo, test.budget)
		if err != nil || found || entries != test.wantEntries {
			t.Fatalf("probe budget %d = found=%v entries=%d err=%v, want found=false entries=%d", test.budget, found, entries, err, test.wantEntries)
		}
	}
}

func writeSwiftProbeFiles(t *testing.T, root string, filesBeforeSource int, includeSource bool) {
	t.Helper()
	for index := 0; index < filesBeforeSource; index++ {
		testutil.MustWriteFile(t, filepath.Join(root, "file"+strconv.Itoa(index)+".txt"), "ignored\n")
	}
	if includeSource {
		testutil.MustWriteFile(t, filepath.Join(root, "zz-source.swift"), "import Foundation\n")
	}
}

func rootsContain(roots map[string]struct{}, root string) bool {
	_, found := roots[root]
	return found
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

	if _, _, err := probeSwiftSourceWithinRoot(context.Background(), filepath.Join(repo, "missing"), maxRootCarthageSourceTraversalEntries); err == nil {
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
