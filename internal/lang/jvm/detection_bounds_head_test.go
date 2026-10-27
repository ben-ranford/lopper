package jvm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestJVMDetectAndWalkBranches(t *testing.T) {
	adapter := NewAdapter()

	t.Run("confidence cap", func(t *testing.T) {
		repo := t.TempDir()
		for path, content := range map[string]string{
			"pom.xml":          "<project/>",
			buildGradleName:    "",
			"build.gradle.kts": "",
		} {
			testutil.MustWriteFile(t, filepath.Join(repo, path), content)
		}
		detection, err := adapter.DetectWithConfidence(context.Background(), repo)
		if err != nil {
			t.Fatalf("detect with confidence: %v", err)
		}
		if !detection.Matched || detection.Confidence != 95 {
			t.Fatalf("expected matched detection capped at 95, got %#v", detection)
		}
	})

	t.Run("max file walk budget", testJVMMaxTraversalWalkBudget)
	t.Run("escaping symlinks do not consume file budget", func(t *testing.T) {
		testJVMEscapingSymlinkFloodDoesNotConsumeCandidateBudget(t, adapter)
	})
	t.Run("oversized directory fails closed", testJVMOversizedDirectoryFailsClosed)
	t.Run("exact traversal budget completes", testJVMExactTraversalBudgetCompletes)
	t.Run("directory enumeration errors propagate", testJVMDetectionDirectoryErrors)
	t.Run("traversal budget stops rejected-entry flood", testJVMTraversalBudgetStopsRejectedEntryFlood)
	t.Run("confined candidate budget still stops ordinary file flood", testJVMConfinedCandidateBudgetStopsOrdinaryFileFlood)
	t.Run("broken and escaping entries count toward traversal only", testJVMBrokenAndEscapingEntriesCountTowardTraversalOnly)
}

func testJVMMaxTraversalWalkBudget(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "Main.java"), "class Main {}")
	walker := newJVMDetectionTestWalker(repo, map[string]struct{}{}, &language.Detection{}, shared.RootedWalkBudget{MaxTraversalEntries: 1, MaxFiles: 1})
	if err := walker.walk(context.Background()); !jvmDetectionTraversalLimited(err) {
		t.Fatalf("expected traversal-limit error, got %v", err)
	}
}

func testJVMOversizedDirectoryFailsClosed(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, "z-seed"), 0o755); err != nil {
		t.Fatalf("mkdir seed directory: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(repo, "a-Main.java"), "class Main {}\n")
	rawEntries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("read flood entries: %v", err)
	}
	markerEntry := rawEntries[0]
	fillEntry := rawEntries[1]

	budget := defaultJVMDetectionBudget()
	wantChildren := budget.MaxTraversalEntries - 1
	directory := &jvmDetectionTestDirectory{
		fillEntry:     fillEntry,
		repeatEntries: wantChildren,
		overflowEntry: markerEntry,
	}
	roots := map[string]struct{}{}
	detect := &language.Detection{}
	walker := newJVMDetectionTestWalker(repo, roots, detect, budget)
	openCalls := 0
	walker.openRoot = func(string) (safeio.Root, error) {
		return newJVMDetectionTestRoot(t, repo), nil
	}
	walker.openDirectory = func(safeio.Root, string) (safeio.ReadDirFile, error) {
		openCalls++
		return directory, nil
	}

	err = walker.walk(context.Background())
	assertJVMOversizedDirectoryOutcome(t, jvmOversizedDirectoryOutcome{
		repo:        repo,
		err:         err,
		markerEntry: markerEntry,
		fillEntry:   fillEntry,
		directory:   directory,
		budget:      budget,
		detect:      detect,
		openCalls:   openCalls,
	})
	assertJVMOversizedDirectoryReads(t, directory, wantChildren)
}

type jvmOversizedDirectoryOutcome struct {
	repo        string
	err         error
	markerEntry fs.DirEntry
	fillEntry   fs.DirEntry
	directory   *jvmDetectionTestDirectory
	budget      shared.RootedWalkBudget
	detect      *language.Detection
	openCalls   int
}

func assertJVMOversizedDirectoryOutcome(t *testing.T, outcome jvmOversizedDirectoryOutcome) {
	t.Helper()

	wantChildren := outcome.budget.MaxTraversalEntries - 1
	if !jvmDetectionTraversalLimited(outcome.err) {
		t.Fatalf("expected explicit traversal-limit error, got %v", outcome.err)
	}
	if !strings.Contains(outcome.err.Error(), outcome.repo) {
		t.Fatalf("expected traversal-limit error to contain directory path %q, got %v", outcome.repo, outcome.err)
	}
	if outcome.markerEntry.Name() >= outcome.fillEntry.Name() || !outcome.directory.overflowReturned {
		t.Fatalf("expected lexically early JVM marker to appear only in overflow probe, marker=%q fill=%q", outcome.markerEntry.Name(), outcome.fillEntry.Name())
	}
	if outcome.directory.entriesReturned != wantChildren+1 {
		t.Fatalf("expected %d bounded children plus one overflow probe, got %d", wantChildren, outcome.directory.entriesReturned)
	}
	if outcome.detect.Matched {
		t.Fatalf("expected overflow marker not to produce a partial detection, got %#v", outcome.detect)
	}
	if outcome.openCalls != 1 || outcome.directory.closeCalls != 1 {
		t.Fatalf("expected one directory open/close, got opens=%d closes=%d", outcome.openCalls, outcome.directory.closeCalls)
	}
}

func assertJVMOversizedDirectoryReads(t *testing.T, directory *jvmDetectionTestDirectory, wantChildren int) {
	t.Helper()

	wantBudgetReads := (wantChildren + 128 - 1) / 128
	if len(directory.readSizes) != wantBudgetReads+1 {
		t.Fatalf("expected %d bounded reads plus one overflow probe, got %d", wantBudgetReads, len(directory.readSizes))
	}
	for index, size := range directory.readSizes[:wantBudgetReads] {
		if size <= 0 || size > 128 {
			t.Fatalf("read %d requested invalid batch size %d", index, size)
		}
	}
	if got := directory.readSizes[wantBudgetReads-1]; got != wantChildren%128 {
		t.Fatalf("expected final bounded read size %d, got %d", wantChildren%128, got)
	}
	if got := directory.readSizes[wantBudgetReads]; got != 1 {
		t.Fatalf("expected one-entry overflow probe, got %d", got)
	}
}

func testJVMExactTraversalBudgetCompletes(t *testing.T) {
	repo := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(repo, name), 0o755); err != nil {
			t.Fatalf("mkdir exact-budget directory %s: %v", name, err)
		}
	}

	budget := shared.RootedWalkBudget{MaxTraversalEntries: 3, MaxFiles: 2}
	walker := newJVMDetectionTestWalker(repo, map[string]struct{}{}, &language.Detection{}, budget)
	if err := walker.walk(context.Background()); err != nil {
		t.Fatalf("expected complete tree at exact traversal budget to succeed, got %v", err)
	}
}

func testJVMDetectionDirectoryErrors(t *testing.T) {
	t.Run("root open", testJVMDetectionRootOpenError)
	t.Run("open", testJVMDetectionDirectoryOpenError)
	t.Run("read and close", testJVMDetectionDirectoryReadAndCloseErrors)
	t.Run("no progress", testJVMDetectionDirectoryNoProgress)
	t.Run("oversized batch", testJVMDetectionDirectoryOversizedBatch)
	t.Run("limit probe read error", testJVMDetectionLimitProbeReadError)
	t.Run("limit probe entry and read error", testJVMDetectionLimitProbeEntryAndReadError)
	t.Run("limit probe no progress", testJVMDetectionLimitProbeNoProgress)
}

func testJVMDetectionRootOpenError(t *testing.T) {
	openErr := errors.New("open root")
	walker := newJVMDetectionTestWalker(t.TempDir(), map[string]struct{}{}, &language.Detection{}, defaultJVMDetectionBudget())
	walker.openRoot = func(string) (safeio.Root, error) {
		return nil, openErr
	}
	if err := walker.walk(context.Background()); !errors.Is(err, openErr) {
		t.Fatalf("expected root open error, got %v", err)
	}
}

func testJVMDetectionDirectoryOpenError(t *testing.T) {
	openErr := errors.New("open directory")
	repo := t.TempDir()
	walker := newJVMDetectionTestWalker(repo, map[string]struct{}{}, &language.Detection{}, defaultJVMDetectionBudget())
	walker.openRoot = func(string) (safeio.Root, error) {
		return newJVMDetectionTestRoot(t, repo), nil
	}
	walker.openDirectory = func(safeio.Root, string) (safeio.ReadDirFile, error) {
		return nil, openErr
	}
	if err := walker.walk(context.Background()); !errors.Is(err, openErr) {
		t.Fatalf("expected directory open error, got %v", err)
	}
}

func testJVMDetectionDirectoryReadAndCloseErrors(t *testing.T) {
	readErr := errors.New("read directory")
	closeErr := errors.New("close directory")
	directory := &jvmDetectionTestDirectory{readErr: readErr, closeErr: closeErr}
	repo := t.TempDir()
	walker := newJVMDetectionTestWalker(repo, map[string]struct{}{}, &language.Detection{}, defaultJVMDetectionBudget())
	walker.openRoot = func(string) (safeio.Root, error) {
		return newJVMDetectionTestRoot(t, repo), nil
	}
	walker.openDirectory = func(safeio.Root, string) (safeio.ReadDirFile, error) {
		return directory, nil
	}
	err := walker.walk(context.Background())
	if !errors.Is(err, readErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined read and close errors, got %v", err)
	}
	if directory.closeCalls != 1 {
		t.Fatalf("expected failed reader to close once, got %d", directory.closeCalls)
	}
}

func testJVMDetectionDirectoryNoProgress(t *testing.T) {
	directory := &jvmDetectionTestDirectory{noProgress: true}
	repo := t.TempDir()
	walker := newJVMDetectionTestWalker(repo, map[string]struct{}{}, &language.Detection{}, defaultJVMDetectionBudget())
	walker.openRoot = func(string) (safeio.Root, error) {
		return newJVMDetectionTestRoot(t, repo), nil
	}
	walker.openDirectory = func(safeio.Root, string) (safeio.ReadDirFile, error) {
		return directory, nil
	}
	if err := walker.walk(context.Background()); !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("expected no-progress error, got %v", err)
	}
	if directory.closeCalls != 1 {
		t.Fatalf("expected no-progress reader to close once, got %d", directory.closeCalls)
	}
}

func testJVMDetectionDirectoryOversizedBatch(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, "seed"), 0o755); err != nil {
		t.Fatalf("mkdir oversized batch seed: %v", err)
	}
	seedEntries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("read oversized batch seed: %v", err)
	}
	readErr := errors.New("read oversized detection batch")
	directory := &jvmDetectionTestDirectory{
		fillEntry:          seedEntries[0],
		extraEntries:       1,
		readErrWithEntries: errors.Join(io.EOF, readErr),
	}
	walker := newJVMDetectionTestWalker(repo, map[string]struct{}{}, &language.Detection{}, defaultJVMDetectionBudget())
	walker.openRoot = func(string) (safeio.Root, error) {
		return newJVMDetectionTestRoot(t, repo), nil
	}
	walker.openDirectory = func(safeio.Root, string) (safeio.ReadDirFile, error) {
		return directory, nil
	}
	if err := walker.walk(context.Background()); !jvmDetectionTraversalLimited(err) || !errors.Is(err, io.EOF) || !errors.Is(err, readErr) {
		t.Fatalf("expected joined oversized-batch traversal-limit, EOF, and read error, got %v", err)
	}
	if directory.closeCalls != 1 {
		t.Fatalf("expected oversized batch reader to close once, got %d", directory.closeCalls)
	}
}

func testJVMDetectionLimitProbeReadError(t *testing.T) {
	readErr := errors.New("probe directory")
	directory := &jvmDetectionTestDirectory{readErr: readErr}
	budget := shared.RootedWalkBudget{MaxTraversalEntries: 1, MaxFiles: 1}
	repo := t.TempDir()
	walker := newJVMDetectionTestWalker(repo, map[string]struct{}{}, &language.Detection{}, budget)
	walker.openRoot = func(string) (safeio.Root, error) {
		return newJVMDetectionTestRoot(t, repo), nil
	}
	walker.openDirectory = func(safeio.Root, string) (safeio.ReadDirFile, error) {
		return directory, nil
	}
	if err := walker.walk(context.Background()); !errors.Is(err, readErr) {
		t.Fatalf("expected limit probe read error, got %v", err)
	}
	if directory.closeCalls != 1 {
		t.Fatalf("expected failed limit probe to close once, got %d", directory.closeCalls)
	}
}

func testJVMDetectionLimitProbeEntryAndReadError(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "entry"), []byte("entry"), 0o600); err != nil {
		t.Fatalf("write probe entry: %v", err)
	}
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("read probe entry: %v", err)
	}
	readErr := errors.New("read overflowing detection probe")
	directory := &jvmDetectionTestDirectory{
		fillEntry:          entries[0],
		overflowEntry:      entries[0],
		readErrWithEntries: readErr,
	}
	budget := shared.RootedWalkBudget{MaxTraversalEntries: 1, MaxFiles: 1}
	walker := newJVMDetectionTestWalker(repo, map[string]struct{}{}, &language.Detection{}, budget)
	walker.openRoot = func(string) (safeio.Root, error) {
		return newJVMDetectionTestRoot(t, repo), nil
	}
	walker.openDirectory = func(safeio.Root, string) (safeio.ReadDirFile, error) {
		return directory, nil
	}

	err = walker.walk(context.Background())
	if !jvmDetectionTraversalLimited(err) || !errors.Is(err, readErr) {
		t.Fatalf("expected joined probe traversal-limit and read error, got %v", err)
	}
	if directory.closeCalls != 1 {
		t.Fatalf("expected overflowing probe directory to close once, got %d", directory.closeCalls)
	}
}

func testJVMDetectionLimitProbeNoProgress(t *testing.T) {
	directory := &jvmDetectionTestDirectory{noProgress: true}
	budget := shared.RootedWalkBudget{MaxTraversalEntries: 1, MaxFiles: 1}
	repo := t.TempDir()
	walker := newJVMDetectionTestWalker(repo, map[string]struct{}{}, &language.Detection{}, budget)
	walker.openRoot = func(string) (safeio.Root, error) {
		return newJVMDetectionTestRoot(t, repo), nil
	}
	walker.openDirectory = func(safeio.Root, string) (safeio.ReadDirFile, error) {
		return directory, nil
	}
	if err := walker.walk(context.Background()); !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("expected limit probe no-progress error, got %v", err)
	}
	if directory.closeCalls != 1 {
		t.Fatalf("expected no-progress limit probe to close once, got %d", directory.closeCalls)
	}
}

type jvmDetectionTestDirectory struct {
	safeio.File
	fillEntry          fs.DirEntry
	readErr            error
	readErrWithEntries error
	closeErr           error
	noProgress         bool
	extraEntries       int
	repeatEntries      int
	overflowEntry      fs.DirEntry
	overflowReturned   bool
	readSizes          []int
	entriesReturned    int
	closeCalls         int
}

func (d *jvmDetectionTestDirectory) Stat() (fs.FileInfo, error) {
	if d.fillEntry != nil {
		return d.fillEntry.Info()
	}
	if d.overflowEntry != nil {
		return d.overflowEntry.Info()
	}
	return nil, errors.New("unexpected stat")
}

func (d *jvmDetectionTestDirectory) ReadDir(count int) ([]fs.DirEntry, error) {
	d.readSizes = append(d.readSizes, count)
	if d.readErr != nil {
		return nil, d.readErr
	}
	if d.noProgress {
		return nil, nil
	}
	if d.fillEntry == nil {
		return nil, io.EOF
	}

	if d.repeatEntries > 0 {
		entryCount := min(count, d.repeatEntries)
		entries := make([]fs.DirEntry, entryCount)
		for index := range entries {
			entries[index] = d.fillEntry
		}
		d.repeatEntries -= entryCount
		d.entriesReturned += len(entries)
		return entries, d.readErrWithEntries
	}
	if d.overflowEntry != nil && !d.overflowReturned {
		d.overflowReturned = true
		d.entriesReturned++
		return []fs.DirEntry{d.overflowEntry}, d.readErrWithEntries
	}

	entries := make([]fs.DirEntry, count+d.extraEntries)
	for index := range entries {
		entries[index] = d.fillEntry
	}
	d.entriesReturned += len(entries)
	return entries, d.readErrWithEntries
}

func (d *jvmDetectionTestDirectory) Close() error {
	d.closeCalls++
	return d.closeErr
}

type jvmDetectionTestRoot struct {
	safeio.Root
	info fs.FileInfo
}

func (*jvmDetectionTestRoot) Open(string) (safeio.File, error) {
	return nil, errors.New("unexpected root open")
}

func (*jvmDetectionTestRoot) OpenRoot(string) (safeio.Root, error) {
	return nil, errors.New("unexpected child root open")
}

func (r *jvmDetectionTestRoot) Lstat(name string) (fs.FileInfo, error) {
	if name == "." && r.info != nil {
		return r.info, nil
	}
	return nil, errors.New("unexpected root lstat")
}

func (*jvmDetectionTestRoot) Close() error {
	return nil
}

func newJVMDetectionTestRoot(t testing.TB, path string) *jvmDetectionTestRoot {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat detection test root %s: %v", path, err)
	}
	return &jvmDetectionTestRoot{info: info}
}

func testJVMEscapingSymlinkFloodDoesNotConsumeCandidateBudget(t *testing.T, adapter *Adapter) {
	repo := t.TempDir()
	outsideSource := filepath.Join(t.TempDir(), "Outside.java")
	testutil.MustWriteFile(t, outsideSource, "class Outside {}\n")
	for index := 0; index < 1024; index++ {
		linkPath := filepath.Join(repo, fmt.Sprintf("a-%04d.java", index))
		if err := os.Symlink(outsideSource, linkPath); err != nil {
			t.Skipf("symlink not supported: %v", err)
		}
	}
	testutil.MustWriteFile(t, filepath.Join(repo, "z-module", "src", "main", "java", "Main.java"), "class Main {}\n")

	detection, err := adapter.DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence after escaping symlink flood: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected legitimate later JVM file to remain detectable, got %#v", detection)
	}
}

func testJVMTraversalBudgetStopsRejectedEntryFlood(t *testing.T) {
	repo := t.TempDir()
	outside := filepath.Join(t.TempDir(), "Outside.java")
	testutil.MustWriteFile(t, outside, "class Outside {}")
	for i := 0; i < 4; i++ {
		if err := os.Symlink(outside, filepath.Join(repo, fmt.Sprintf("escape-%04d.java", i))); err != nil {
			t.Skipf("symlink not supported: %v", err)
		}
	}
	testutil.MustWriteFile(t, filepath.Join(repo, "z-module", "src", "main", "java", "Main.java"), "class Main {}")
	detect := &language.Detection{}
	walker := newJVMDetectionTestWalker(repo, map[string]struct{}{}, detect, shared.RootedWalkBudget{MaxTraversalEntries: 2, MaxFiles: 1024})
	if err := walker.walk(context.Background()); !jvmDetectionTraversalLimited(err) {
		t.Fatalf("expected rejected-entry traversal limit, got %v", err)
	}
	if detect.Matched {
		t.Fatalf("expected traversal stop before legitimate file update, got %#v", detect)
	}
}

func testJVMConfinedCandidateBudgetStopsOrdinaryFileFlood(t *testing.T) {
	repo := t.TempDir()
	for index := 0; index < 4; index++ {
		testutil.MustWriteFile(t, filepath.Join(repo, fmt.Sprintf("Main%04d.java", index)), "class Main {}\n")
	}

	budget := shared.RootedWalkBudget{MaxTraversalEntries: 8, MaxFiles: 2}
	roots := map[string]struct{}{}
	detect := &language.Detection{}
	walker := newJVMDetectionTestWalker(repo, roots, detect, budget)
	if err := walker.walk(context.Background()); !jvmDetectionCandidateLimited(err) {
		t.Fatalf("expected confined candidate budget flood to stop the walker, got %v", err)
	}
	if !detect.Matched {
		t.Fatalf("expected ordinary confined files to update detection before the stop, got %#v", detect)
	}
}

func testJVMBrokenAndEscapingEntriesCountTowardTraversalOnly(t *testing.T) {
	repo := t.TempDir()
	outsideSource := filepath.Join(t.TempDir(), "Outside.java")
	testutil.MustWriteFile(t, outsideSource, "class Outside {}\n")

	escapingLink := filepath.Join(repo, "Escaping.java")
	if err := os.Symlink(outsideSource, escapingLink); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	brokenLink := filepath.Join(repo, "Broken.java")
	if err := os.Symlink(filepath.Join(repo, "missing", "Broken.java"), brokenLink); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	budget := shared.RootedWalkBudget{MaxTraversalEntries: 3, MaxFiles: 1}
	detect := &language.Detection{}
	walker := newJVMDetectionTestWalker(repo, map[string]struct{}{}, detect, budget)
	if err := walker.walk(context.Background()); err != nil {
		t.Fatalf("rejected links exhausted candidate budget: %v", err)
	}

	if detect.Matched {
		t.Fatalf("expected rejected links to keep detection unmatched, got %#v", detect)
	}
}
