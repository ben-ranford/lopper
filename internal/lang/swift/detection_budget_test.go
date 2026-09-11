package swift

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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
	if _, _, _, err := discoverSwiftSourceCandidatesWithinLimit(context.Background(), root, ".", maxRootCarthageSourceTraversalEntries); err == nil {
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

func TestSwiftRootCarthageProbeAllowsRequestedRootAliases(t *testing.T) {
	for _, test := range []struct {
		name     string
		ancestor bool
	}{
		{name: "root symlink"},
		{name: "ancestor symlink", ancestor: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolved, requested := swiftRequestedRootAlias(t, test.ancestor)
			assertSwiftRootCarthageAliasMetadata(t, resolved, requested)
			assertSwiftRootCarthageAliasSource(t, resolved, requested)
		})
	}
}

func assertSwiftRootCarthageAliasMetadata(t *testing.T, resolved, requested string) {
	t.Helper()
	testutil.MustWriteFile(t, filepath.Join(resolved, carthageManifestName), "github \"owner/repo\"\n")
	detection, err := NewAdapter().DetectWithConfidence(context.Background(), requested)
	if err != nil {
		t.Fatalf("detect metadata-only alias: %v", err)
	}
	if detection.Matched {
		t.Fatalf("expected metadata-only alias to remain uncorroborated, got %#v", detection)
	}
}

func assertSwiftRootCarthageAliasSource(t *testing.T, resolved, requested string) {
	t.Helper()
	testutil.MustWriteFile(t, filepath.Join(resolved, "Sources", swiftMainFileName), "import Foundation\n")
	detection, err := NewAdapter().DetectWithConfidence(context.Background(), requested)
	if err != nil {
		t.Fatalf("detect Swift source through alias: %v", err)
	}
	if !detection.Matched || !slices.Contains(detection.Roots, requested) {
		t.Fatalf("expected requested alias root to be retained, got %#v", detection)
	}
}

func TestSwiftRootCarthageProbeRejectsSymlinkBelowRequestedRoot(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, carthageManifestName), "github \"owner/repo\"\n")
	outside := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(outside, swiftMainFileName), "import Foundation\n")
	if err := os.Symlink(outside, filepath.Join(repo, "Sources")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect symlinked child: %v", err)
	}
	if detection.Matched {
		t.Fatalf("expected symlinked child source to be ignored, got %#v", detection)
	}
}

func swiftRequestedRootAlias(t *testing.T, ancestor bool) (string, string) {
	t.Helper()
	resolved := t.TempDir()
	linkTarget := resolved
	if ancestor {
		resolved = filepath.Join(resolved, "repo")
		if err := os.Mkdir(resolved, 0o750); err != nil {
			t.Fatalf("mkdir resolved repo: %v", err)
		}
		linkTarget = filepath.Dir(resolved)
	}
	alias := filepath.Join(t.TempDir(), "checkout")
	if err := os.Symlink(linkTarget, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if ancestor {
		alias = filepath.Join(alias, "repo")
	}
	return resolved, alias
}

func TestSwiftDetectionRequiresRegularCarthageAndSwiftEntries(t *testing.T) {
	detectBroadSignals := func(repo string) (language.Detection, error) {
		detection := language.Detection{}
		err := walkSwiftDetection(context.Background(), repo, &detection, map[string]struct{}{}, rootCarthagePreflight{})
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

func TestSwiftNestedCarthageProbeRejectsReplacedCandidateSymlink(t *testing.T) {
	repo := t.TempDir()
	candidate := filepath.Join(repo, "Packages", "Library")
	if err := os.MkdirAll(filepath.Dir(candidate), 0o750); err != nil {
		t.Fatalf("make candidate parent: %v", err)
	}
	outside := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(outside, swiftMainFileName), "import Foundation\n")
	if err := os.Symlink(outside, candidate); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	detection := language.Detection{}
	roots := map[string]struct{}{}
	err := applyCarthageDetectionRoots(context.Background(), repo, &detection, roots,
		map[string]int{candidate: 10}, map[string]struct{}{})
	if err != nil {
		t.Fatalf("probe replaced nested candidate: %v", err)
	}
	if detection.Matched || rootsContain(roots, candidate) {
		t.Fatalf("expected replaced candidate symlink to be ignored, got detection=%#v roots=%#v", detection, roots)
	}
}

func TestSwiftNestedCarthageProbeRejectsUntrustedRoots(t *testing.T) {
	repo := t.TempDir()
	for _, candidate := range []string{repo, filepath.Dir(repo)} {
		if relative, ok := nestedCarthageRootRelativePath(repo, candidate); ok {
			t.Fatalf("untrusted candidate %q was accepted as %q", candidate, relative)
		}
	}

	missingRepo := filepath.Join(repo, "missing")
	err := applyCarthageDetectionRoots(context.Background(), missingRepo, &language.Detection{}, map[string]struct{}{},
		map[string]int{filepath.Join(missingRepo, "Package"): 10}, map[string]struct{}{})
	if err == nil {
		t.Fatal("expected a missing trusted root to reject nested probing")
	}
}

func TestSwiftRootCarthageProbeSortsChildrenAcrossReadBatches(t *testing.T) {
	repo := t.TempDir()
	candidate := filepath.Join(repo, "parent")
	if err := os.Mkdir(candidate, 0o750); err != nil {
		t.Fatalf("make candidate: %v", err)
	}
	for index := 0; index < rootCarthageSourceReadBatchSize+1; index++ {
		name := "child" + strconv.Itoa(rootCarthageSourceReadBatchSize-index)
		if err := os.Mkdir(filepath.Join(candidate, name), 0o750); err != nil {
			t.Fatalf("make child %q: %v", name, err)
		}
	}
	root, err := safeio.OpenRootNoFollow(repo)
	if err != nil {
		t.Fatalf("open repo root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close repo root: %v", closeErr)
		}
	})

	queue := make([]rootCarthageSourceDirectory, 0, rootCarthageSourceReadBatchSize+1)
	_, entries, err := walkCarthageSwiftSourceDirectory(context.Background(), root, rootCarthageSourceDirectory{path: "parent", depth: 1}, &queue, rootCarthageSourceReadBatchSize+1)
	if err != nil || entries != rootCarthageSourceReadBatchSize+1 {
		t.Fatalf("walk batched children: entries=%d err=%v", entries, err)
	}
	if !slices.IsSortedFunc(queue, func(left, right rootCarthageSourceDirectory) int {
		return strings.Compare(left.path, right.path)
	}) {
		t.Fatalf("children spanning batches are not ordered: %#v", queue)
	}
}

func TestSwiftCarthageProbeReportsActualEntries(t *testing.T) {
	for _, test := range []struct {
		files       int
		budget      int
		wantEntries int
	}{
		{files: 4, budget: 2, wantEntries: 2},
		{files: 4, budget: maxNestedCarthageSourceTraversalEntries, wantEntries: 4},
		{files: 1025, budget: maxRootCarthageSourceTraversalEntries, wantEntries: 1025},
	} {
		t.Run(strconv.Itoa(test.files)+" files", func(t *testing.T) {
			repo := t.TempDir()
			writeSwiftProbeFiles(t, repo, test.files, false)

			found, entries, err := probeSwiftSourceWithinRoot(context.Background(), repo, test.budget)
			if err != nil || found || entries != test.wantEntries {
				t.Fatalf("probe budget %d = found=%v entries=%d err=%v, want found=false entries=%d", test.budget, found, entries, err, test.wantEntries)
			}
		})
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

func TestSwiftWalkRetainsUncorroboratedRootCarthagePreflight(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "Sources", swiftMainFileName), "import Foundation\n")
	detection := language.Detection{}
	roots := map[string]struct{}{}
	err := walkSwiftDetection(context.Background(), repo, &detection, roots, rootCarthagePreflight{confidence: 60})
	if err != nil {
		t.Fatalf("walk retained root preflight: %v", err)
	}
	if !detection.Matched || detection.Confidence < 60 || !rootsContain(roots, repo) {
		t.Fatalf("expected source corroboration to retain root preflight, got detection=%#v roots=%#v", detection, roots)
	}
}

func TestSwiftCaseVariantCarthageMetadataDoesNotInventRootConfidence(t *testing.T) {
	repo := t.TempDir()
	caseVariant := strings.ToLower(carthageManifestName)
	testutil.MustWriteFile(t, filepath.Join(repo, caseVariant), "github \"owner/repo\"\n")
	entries, err := os.ReadDir(repo)
	if err != nil || len(entries) != 1 {
		t.Fatalf("read case-variant metadata: entries=%#v err=%v", entries, err)
	}
	if confidence := carthageDetectionConfidence(entries[0]); confidence != 10 {
		t.Fatalf("case-variant metadata confidence = %d, want ordinary 10", confidence)
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
