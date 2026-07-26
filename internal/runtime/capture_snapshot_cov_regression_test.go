package runtime

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestStableRuntimeTraceFileSnapshotRejectsChangedFileDuringHashing(t *testing.T) {
	tracePath := writeTraceFixture(t)
	setHashRuntimeTraceMutationHook(t, func() {
		if err := os.WriteFile(tracePath, []byte("{\"module\":\"chalk/index\"}\n"), 0o600); err != nil {
			t.Fatalf("rewrite trace before second snapshot: %v", err)
		}
	})

	if _, err := stableRuntimeTraceFileSnapshot(tracePath); err == nil || !strings.Contains(err.Error(), "changed while hashing") {
		t.Fatalf("expected changed trace to be rejected, got %v", err)
	}
}

func TestStableRuntimeTraceFileSnapshotWithinRootReturnsSnapshot(t *testing.T) {
	traceDir := t.TempDir()
	tracePath := filepath.Join(traceDir, runtimeTraceNDJSON)
	traceData := []byte("{\"module\":\"lodash/map\"}\n")
	if err := os.WriteFile(tracePath, traceData, 0o600); err != nil {
		t.Fatalf("write trace fixture: %v", err)
	}

	root := openRuntimeSnapshotRoot(t, traceDir)
	snapshot, err := stableRuntimeTraceFileSnapshotWithinRoot(root, runtimeTraceNDJSON, tracePath)
	if err != nil {
		t.Fatalf("stableRuntimeTraceFileSnapshotWithinRoot: %v", err)
	}
	if string(snapshot.data) != string(traceData) {
		t.Fatalf("expected snapshot data %q, got %q", traceData, snapshot.data)
	}
}

func TestStableRuntimeTraceFileSnapshotWithinRootSurfacesFirstSnapshotError(t *testing.T) {
	traceDir := t.TempDir()
	root := openRuntimeSnapshotRoot(t, traceDir)
	tracePath := filepath.Join(traceDir, runtimeTraceNDJSON)

	if _, err := stableRuntimeTraceFileSnapshotWithinRoot(root, runtimeTraceNDJSON, tracePath); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("expected first snapshot error to surface, got %v", err)
	}
}

func TestStableRuntimeTraceFileSnapshotWithinRootSurfacesSecondSnapshotError(t *testing.T) {
	traceDir := t.TempDir()
	tracePath := filepath.Join(traceDir, runtimeTraceNDJSON)
	if err := os.WriteFile(tracePath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write trace fixture: %v", err)
	}

	root := openRuntimeSnapshotRoot(t, traceDir)
	setHashRuntimeTraceMutationHook(t, func() {
		if err := os.Remove(tracePath); err != nil {
			t.Fatalf("remove trace before second snapshot: %v", err)
		}
	})

	if _, err := stableRuntimeTraceFileSnapshotWithinRoot(root, runtimeTraceNDJSON, tracePath); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("expected second snapshot error to surface, got %v", err)
	}
}

func TestStableRuntimeTraceFileSnapshotWithinRootRejectsChangedFileDuringHashing(t *testing.T) {
	traceDir := t.TempDir()
	tracePath := filepath.Join(traceDir, runtimeTraceNDJSON)
	if err := os.WriteFile(tracePath, []byte("{\"module\":\"lodash/map\"}\n"), 0o600); err != nil {
		t.Fatalf("write trace fixture: %v", err)
	}

	root := openRuntimeSnapshotRoot(t, traceDir)
	setHashRuntimeTraceMutationHook(t, func() {
		if err := os.WriteFile(tracePath, []byte("{\"module\":\"chalk/index\"}\n"), 0o600); err != nil {
			t.Fatalf("rewrite trace before second snapshot: %v", err)
		}
	})

	if _, err := stableRuntimeTraceFileSnapshotWithinRoot(root, runtimeTraceNDJSON, tracePath); err == nil || !strings.Contains(err.Error(), "changed while hashing") {
		t.Fatalf("expected changed within-root trace to be rejected, got %v", err)
	}
}

func TestSnapshotRuntimeTraceFileWithinRootJoinsFileCloseError(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), runtimeTraceNDJSON)
	infoPath := testutil.WriteTempFile(t, runtimeTraceNDJSON, "{\"module\":\"lodash/map\"}\n")
	info, err := os.Stat(infoPath)
	if err != nil {
		t.Fatalf("stat trace fixture: %v", err)
	}
	closeErr := errors.New("trace close failed")
	root := &trustedExecutableRootStub{
		lstatInfo: map[string]fs.FileInfo{runtimeTraceNDJSON: info},
		files: map[string]safeio.File{
			runtimeTraceNDJSON: &runtimeStageFileStub{
				reader:   bytes.NewReader([]byte("{\"module\":\"lodash/map\"}\n")),
				info:     info,
				closeErr: closeErr,
			},
		},
	}

	if _, err := snapshotRuntimeTraceFileWithinRoot(root, runtimeTraceNDJSON, tracePath); !errors.Is(err, closeErr) {
		t.Fatalf("expected snapshot file close error identity, got %v", err)
	}
}

func TestSnapshotRuntimeTraceFileJoinsRootCloseError(t *testing.T) {
	traceDir := t.TempDir()
	tracePath := filepath.Join(traceDir, runtimeTraceNDJSON)
	if err := os.WriteFile(tracePath, []byte("{\"module\":\"lodash/map\"}\n"), 0o600); err != nil {
		t.Fatalf("write trace fixture: %v", err)
	}
	info, err := os.Stat(tracePath)
	if err != nil {
		t.Fatalf("stat trace fixture: %v", err)
	}
	rootCloseErr := errors.New("trace root close failed")
	withSafeioFileSystemTest(t, &safeioFileSystemStub{
		openRootNoFollow: func(string) (safeio.Root, error) {
			return &trustedExecutableRootStub{
				closeErr:  rootCloseErr,
				lstatInfo: map[string]fs.FileInfo{runtimeTraceNDJSON: info},
				files: map[string]safeio.File{
					runtimeTraceNDJSON: &runtimeStageFileStub{
						reader: bytes.NewReader([]byte("{\"module\":\"lodash/map\"}\n")),
						info:   info,
					},
				},
			}, nil
		},
	})

	if _, err := snapshotRuntimeTraceFile(tracePath); !errors.Is(err, rootCloseErr) {
		t.Fatalf("expected snapshot root close error identity, got %v", err)
	}
}

func TestLoadValidatedRuntimeTraceReturnsNilForWhitespaceTrace(t *testing.T) {
	tracePath := writeTraceFixture(t)
	if err := os.WriteFile(tracePath, []byte(" \n\t"), 0o600); err != nil {
		t.Fatalf("rewrite trace fixture: %v", err)
	}

	snapshot, err := loadValidatedRuntimeTrace(tracePath)
	if err != nil {
		t.Fatalf("loadValidatedRuntimeTrace returned error: %v", err)
	}
	if snapshot != nil {
		t.Fatalf("expected whitespace-only trace to be ignored, got %#v", snapshot)
	}
}

func TestLoadValidatedRuntimeTraceWrapsInvalidTraceData(t *testing.T) {
	tracePath := writeTraceFixture(t)
	if err := os.WriteFile(tracePath, []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("rewrite trace fixture: %v", err)
	}

	snapshot, err := loadValidatedRuntimeTrace(tracePath)
	if snapshot != nil {
		t.Fatalf("expected invalid trace to return no snapshot, got %#v", snapshot)
	}
	if err == nil || !strings.Contains(err.Error(), "validate runtime trace") {
		t.Fatalf("expected invalid trace data to be wrapped, got %v", err)
	}
}

func TestExplicitTraceCaptureStageLoadValidatedRuntimeTraceRejectsNilStage(t *testing.T) {
	var stage *explicitTraceCaptureStage

	snapshot, err := stage.loadValidatedRuntimeTrace()
	if snapshot != nil {
		t.Fatalf("expected nil stage to return no snapshot, got %#v", snapshot)
	}
	if err == nil || !strings.Contains(err.Error(), "explicit trace stage is nil") {
		t.Fatalf("expected nil stage error, got %v", err)
	}
}

func TestExplicitTraceCaptureStageLoadValidatedRuntimeTraceReturnsNilWhenTempFileMissing(t *testing.T) {
	repo := t.TempDir()
	tracePath, _ := writeExplicitTraceFixture(t, repo)
	stage := prepareExplicitStageForCoverage(t, tracePath)
	setHashRuntimeTraceMutationHook(t, func() {
		if err := os.Remove(stage.tempPath); err != nil {
			t.Fatalf("remove staged temp file during snapshot: %v", err)
		}
	})

	snapshot, err := stage.loadValidatedRuntimeTrace()
	if err != nil {
		t.Fatalf("loadValidatedRuntimeTrace returned error: %v", err)
	}
	if snapshot != nil {
		t.Fatalf("expected missing staged trace to return nil snapshot, got %#v", snapshot)
	}
}

func TestExplicitTraceCaptureStageLoadValidatedRuntimeTraceReturnsNilForWhitespaceTrace(t *testing.T) {
	repo := t.TempDir()
	tracePath, _ := writeExplicitTraceFixture(t, repo)
	stage := prepareExplicitStageForCoverage(t, tracePath)
	if err := os.WriteFile(stage.tempPath, []byte(" \n\t"), 0o600); err != nil {
		t.Fatalf("write staged whitespace trace: %v", err)
	}

	snapshot, err := stage.loadValidatedRuntimeTrace()
	if err != nil {
		t.Fatalf("loadValidatedRuntimeTrace returned error: %v", err)
	}
	if snapshot != nil {
		t.Fatalf("expected whitespace staged trace to return nil snapshot, got %#v", snapshot)
	}
}

func TestExplicitTraceCaptureStageRevalidateTempIdentityRejectsMissingTempInfo(t *testing.T) {
	repo := t.TempDir()
	tracePath, _ := writeExplicitTraceFixture(t, repo)
	stage := prepareExplicitStageForCoverage(t, tracePath)
	stage.tempInfo = nil

	if err := stage.revalidateTempIdentity(); err == nil || !strings.Contains(err.Error(), "identity is unavailable") {
		t.Fatalf("expected missing temp identity to be rejected, got %v", err)
	}
}

func TestExplicitTraceCaptureStageRevalidateTempIdentityPropagatesLstatError(t *testing.T) {
	repo := t.TempDir()
	tracePath, _ := writeExplicitTraceFixture(t, repo)
	stage := prepareExplicitStageForCoverage(t, tracePath)
	if err := os.Remove(stage.tempPath); err != nil {
		t.Fatalf("remove staged temp file: %v", err)
	}

	if err := stage.revalidateTempIdentity(); err == nil || !strings.Contains(err.Error(), "stat staged temp file") {
		t.Fatalf("expected staged temp stat error, got %v", err)
	}
}

func TestExplicitTraceCaptureStageRevalidatePathIdentityPropagatesPinnedRootStatError(t *testing.T) {
	stage := &explicitTraceCaptureStage{
		root:     &stubRoot{lstatErr: map[string]error{".": os.ErrPermission}},
		traceDir: t.TempDir(),
	}

	if err := stage.revalidatePathIdentity(); err == nil || !strings.Contains(err.Error(), "stat pinned root") {
		t.Fatalf("expected pinned root stat error, got %v", err)
	}
}

func TestExplicitTraceCaptureStageRevalidatePathIdentityPropagatesCurrentRootOpenError(t *testing.T) {
	traceDir := t.TempDir()
	pinnedInfo, err := os.Stat(traceDir)
	if err != nil {
		t.Fatalf("stat trace dir: %v", err)
	}
	withSafeioFileSystemTest(t, &safeioFileSystemStub{
		openRootNoFollow: func(string) (safeio.Root, error) {
			return nil, os.ErrPermission
		},
	})

	stage := &explicitTraceCaptureStage{
		root:     &stubRoot{lstatInfo: map[string]fs.FileInfo{".": pinnedInfo}},
		traceDir: traceDir,
	}

	if err := stage.revalidatePathIdentity(); err == nil || !strings.Contains(err.Error(), "open current root") {
		t.Fatalf("expected current root open error, got %v", err)
	}
}

func TestExplicitTraceCaptureStageRevalidatePathIdentityPropagatesCurrentRootStatError(t *testing.T) {
	traceDir := t.TempDir()
	pinnedInfo, err := os.Stat(traceDir)
	if err != nil {
		t.Fatalf("stat trace dir: %v", err)
	}
	withSafeioFileSystemTest(t, &safeioFileSystemStub{
		openRootNoFollow: func(string) (safeio.Root, error) {
			return &stubRoot{
				lstatErr: map[string]error{".": os.ErrPermission},
			}, nil
		},
	})

	stage := &explicitTraceCaptureStage{
		root:     &stubRoot{lstatInfo: map[string]fs.FileInfo{".": pinnedInfo}},
		traceDir: traceDir,
	}

	if err := stage.revalidatePathIdentity(); err == nil || !strings.Contains(err.Error(), "stat current root") {
		t.Fatalf("expected current root stat error, got %v", err)
	}
}

func openRuntimeSnapshotRoot(t *testing.T, rootPath string) safeio.Root {
	t.Helper()

	resolvedRootPath, err := runtimeTraceRootPath(rootPath)
	if err != nil {
		t.Fatalf("resolve runtime snapshot root: %v", err)
	}
	root, err := safeio.OpenRootNoFollow(resolvedRootPath)
	if err != nil {
		t.Fatalf("open runtime snapshot root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close runtime snapshot root: %v", closeErr)
		}
	})
	return root
}

func prepareExplicitStageForCoverage(t *testing.T, tracePath string) *explicitTraceCaptureStage {
	t.Helper()

	stage, err := prepareExplicitTraceCaptureStage(capturePlan{
		tracePath:         tracePath,
		tracePathExplicit: true,
	})
	if err != nil {
		t.Fatalf("prepare explicit trace stage: %v", err)
	}
	t.Cleanup(func() {
		if stage != nil {
			if err := stage.cleanup(); err != nil && !os.IsNotExist(err) {
				t.Fatalf("cleanup explicit trace stage: %v", err)
			}
		}
	})
	return stage
}
