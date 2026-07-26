//go:build !windows

package safeio

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type durableMkdirSyncFixture struct {
	t           *testing.T
	infos       map[string]fs.FileInfo
	created     map[string]bool
	mkdirErrors map[string]error
	syncErrors  map[string]error
	events      []string
}

type strictAtomicFailureFixture struct {
	t                *testing.T
	rootAbs          string
	info             fs.FileInfo
	renameErr        error
	targetData       []byte
	targetTouched    bool
	candidatePath    string
	renamedCandidate string
	removedPaths     []string
}

type nestedAtomicReplacementFixture struct {
	t          *testing.T
	parentInfo fs.FileInfo
	targetInfo fs.FileInfo
	candidate  string
	events     []string
}

type exactPermissionReplacementTestCase struct {
	name  string
	write func(*WriteRoot, string, []byte, os.FileMode) error
}

type nestedParentSwapFixture struct {
	rootDir          string
	originalParent   string
	relocatedParent  string
	redirectedParent string
	redirectedTarget string
	parentSwapped    bool
}

func exactPermissionReplacementTestCases() []exactPermissionReplacementTestCase {
	return []exactPermissionReplacementTestCase{
		{name: "general", write: (*WriteRoot).WriteFileReplacingWithExactPermissions},
		{name: "strict", write: (*WriteRoot).WriteFileReplacingAtomicallyWithExactPermissions},
	}
}

func newNestedParentSwapFixture(t *testing.T) *nestedParentSwapFixture {
	t.Helper()
	rootDir := t.TempDir()
	fixture := &nestedParentSwapFixture{
		rootDir:          rootDir,
		originalParent:   filepath.Join(rootDir, "sub"),
		relocatedParent:  filepath.Join(rootDir, "sub-relocated"),
		redirectedParent: filepath.Join(rootDir, "redirected"),
	}
	fixture.redirectedTarget = filepath.Join(fixture.redirectedParent, "key")
	if err := os.Mkdir(fixture.originalParent, 0o755); err != nil {
		t.Fatalf("create original parent: %v", err)
	}
	if err := os.Mkdir(fixture.redirectedParent, 0o755); err != nil {
		t.Fatalf("create redirected parent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.originalParent, "key"), []byte("original-before"), 0o600); err != nil {
		t.Fatalf("seed original target: %v", err)
	}
	if err := os.WriteFile(fixture.redirectedTarget, []byte("redirected-before"), 0o600); err != nil {
		t.Fatalf("seed redirected target: %v", err)
	}
	return fixture
}

func (f *nestedParentSwapFixture) openRoot(name string) (Root, error) {
	root, err := (&osFileSystem{}).OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return &fakeRoot{
		Root: root,
		openRoot: func(child string) (Root, error) {
			return f.openAndSwapParent(root, child)
		},
	}, nil
}

func (f *nestedParentSwapFixture) openAndSwapParent(root Root, child string) (Root, error) {
	parent, err := root.OpenRoot(child)
	if err != nil {
		return nil, err
	}
	if err := os.Rename(f.originalParent, f.relocatedParent); err != nil {
		return nil, closeRootWithError(parent, err)
	}
	if err := os.Symlink(filepath.Base(f.redirectedParent), f.originalParent); err != nil {
		return nil, closeRootWithError(parent, err)
	}
	f.parentSwapped = true
	return parent, nil
}

func (f *nestedParentSwapFixture) assertReplacementStayedPinned(t *testing.T) {
	t.Helper()
	if !f.parentSwapped {
		t.Fatal("expected parent path swap seam to run")
	}
	assertFileContent(t, filepath.Join(f.relocatedParent, "key"), "after")
	assertFileContent(t, f.redirectedTarget, "redirected-before")
}

func newDurableMkdirSyncFixture(t *testing.T, components ...string) *durableMkdirSyncFixture {
	t.Helper()
	fixture := &durableMkdirSyncFixture{
		t:           t,
		infos:       make(map[string]fs.FileInfo, len(components)),
		created:     make(map[string]bool, len(components)),
		mkdirErrors: make(map[string]error),
		syncErrors:  make(map[string]error),
	}
	infoPath := t.TempDir()
	logicalPath := ""
	for _, component := range components {
		infoPath = filepath.Join(infoPath, component)
		if err := os.Mkdir(infoPath, 0o755); err != nil {
			t.Fatalf("create identity directory %q: %v", component, err)
		}
		logicalPath = fixtureChildPath(logicalPath, component)
		fixture.infos[logicalPath] = statTestPath(t, infoPath)
	}
	return fixture
}

func fixtureChildPath(parent, child string) string {
	if parent == "" {
		return child
	}
	return filepath.Join(parent, child)
}

func fixtureEventPath(path string) string {
	if path == "" {
		return "."
	}
	return filepath.ToSlash(path)
}

func (f *durableMkdirSyncFixture) rootAt(path string) Root {
	return &fakeRoot{
		lstat:    func(name string) (fs.FileInfo, error) { return f.lstat(path, name) },
		mkdir:    func(name string, _ os.FileMode) error { return f.mkdir(path, name) },
		open:     func(name string) (File, error) { return f.openSyncHandle(path, name) },
		openRoot: func(name string) (Root, error) { return f.openRoot(path, name) },
		close:    func() error { return f.closeRoot(path) },
	}
}

func (f *durableMkdirSyncFixture) lstat(parent, name string) (fs.FileInfo, error) {
	if name == "." {
		f.events = append(f.events, "lstat-opened:"+fixtureEventPath(parent))
		return f.info(parent), nil
	}
	path := fixtureChildPath(parent, name)
	f.events = append(f.events, "lstat:"+fixtureEventPath(path))
	if !f.created[path] {
		return nil, os.ErrNotExist
	}
	return f.info(path), nil
}

func (f *durableMkdirSyncFixture) info(path string) fs.FileInfo {
	info, ok := f.infos[path]
	if !ok {
		f.t.Fatalf("missing identity info for %q", path)
	}
	return info
}

func (f *durableMkdirSyncFixture) mkdir(parent, name string) error {
	path := fixtureChildPath(parent, name)
	f.info(path)
	f.events = append(f.events, "mkdir:"+fixtureEventPath(path))
	if err := f.mkdirErrors[path]; err != nil {
		if errors.Is(err, fs.ErrExist) {
			f.created[path] = true
		}
		return err
	}
	f.created[path] = true
	return nil
}

func (f *durableMkdirSyncFixture) openSyncHandle(path, name string) (File, error) {
	if name != "." {
		f.t.Fatalf("unexpected directory sync path %q", name)
	}
	eventPath := fixtureEventPath(path)
	f.events = append(f.events, "open-sync:"+eventPath)
	return &fakeFile{
		sync: func() error {
			f.events = append(f.events, "sync:"+eventPath)
			return f.syncErrors[path]
		},
		close: func() error {
			f.events = append(f.events, "close-sync:"+eventPath)
			return nil
		},
	}, nil
}

func (f *durableMkdirSyncFixture) openRoot(parent, name string) (Root, error) {
	path := fixtureChildPath(parent, name)
	f.events = append(f.events, "open-root:"+fixtureEventPath(path))
	return f.rootAt(path), nil
}

func (f *durableMkdirSyncFixture) closeRoot(path string) error {
	f.events = append(f.events, "close-root:"+fixtureEventPath(path))
	return nil
}

func newStrictAtomicFailureFixture(t *testing.T) *strictAtomicFailureFixture {
	t.Helper()
	targetPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("before"), 0o644); err != nil {
		t.Fatalf("seed target file: %v", err)
	}
	return &strictAtomicFailureFixture{
		t:            t,
		rootAbs:      filepath.Dir(targetPath),
		info:         statTestPath(t, targetPath),
		renameErr:    errors.New("rename failure"),
		targetData:   []byte("before"),
		removedPaths: make([]string, 0, 1),
	}
}

func (f *strictAtomicFailureFixture) writeRoot() *WriteRoot {
	return &WriteRoot{
		rootAbs: f.rootAbs,
		root: &fakeRoot{
			lstat:    f.lstat,
			openFile: f.openFile,
			rename:   f.rename,
			remove:   f.remove,
		},
	}
}

func (f *strictAtomicFailureFixture) lstat(name string) (os.FileInfo, error) {
	if name == writeTestFileName {
		return f.info, nil
	}
	return nil, os.ErrNotExist
}

func (f *strictAtomicFailureFixture) openFile(name string, _ int, _ os.FileMode) (File, error) {
	if name == writeTestFileName {
		f.targetTouched = true
		return f.liveTargetFile(), nil
	}
	if f.candidatePath != "" {
		f.t.Fatalf("created multiple replacement candidates: %q then %q", f.candidatePath, name)
	}
	f.candidatePath = name
	return &fakeFile{
		write: func(p []byte) (int, error) { return len(p), nil },
		chmod: chmodWithoutError,
		sync:  func() error { return nil },
		close: closeWithoutError,
	}, nil
}

func (f *strictAtomicFailureFixture) liveTargetFile() File {
	return &truncatingFakeFile{
		fakeFile: &fakeFile{
			stat:  func() (os.FileInfo, error) { return f.info, nil },
			write: func(p []byte) (int, error) { f.targetData = append(f.targetData, p...); return len(p), nil },
			close: closeWithoutError,
		},
		truncate: func(int64) error { f.targetData = f.targetData[:0]; return nil },
	}
}

func (f *strictAtomicFailureFixture) rename(oldName, newName string) error {
	f.renamedCandidate = oldName
	if newName != writeTestFileName {
		f.t.Fatalf("unexpected live replacement target %q", newName)
	}
	return f.renameErr
}

func (f *strictAtomicFailureFixture) remove(name string) error {
	f.removedPaths = append(f.removedPaths, name)
	if name == writeTestFileName {
		f.t.Fatal("cleanup attempted to remove the live target")
	}
	return nil
}

func (f *strictAtomicFailureFixture) assertPreserved(t *testing.T) {
	t.Helper()
	if f.targetTouched {
		t.Fatal("expected strict atomic replacement to skip live-file fallback on non-Windows")
	}
	if string(f.targetData) != "before" {
		t.Fatalf("expected old live file to remain unchanged, got %q", string(f.targetData))
	}
	if f.candidatePath == "" {
		t.Fatal("expected replacement candidate path to be captured")
	}
	if f.renamedCandidate != f.candidatePath {
		t.Fatalf("rename used candidate %q, want %q", f.renamedCandidate, f.candidatePath)
	}
	if len(f.removedPaths) != 1 || f.removedPaths[0] != f.candidatePath {
		t.Fatalf("cleanup removed %#v, want only candidate %q", f.removedPaths, f.candidatePath)
	}
}

func newNestedAtomicReplacementFixture(t *testing.T) *nestedAtomicReplacementFixture {
	t.Helper()
	identityRoot := t.TempDir()
	parentPath := filepath.Join(identityRoot, "sub")
	if err := os.Mkdir(parentPath, 0o755); err != nil {
		t.Fatalf("create parent identity: %v", err)
	}
	targetPath := filepath.Join(parentPath, "key")
	if err := os.WriteFile(targetPath, []byte("before"), 0o600); err != nil {
		t.Fatalf("create target identity: %v", err)
	}
	return &nestedAtomicReplacementFixture{
		t:          t,
		parentInfo: statTestPath(t, parentPath),
		targetInfo: statTestPath(t, targetPath),
	}
}

func (f *nestedAtomicReplacementFixture) writeRoot() *WriteRoot {
	parent := f.parentRoot()
	outer := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			switch name {
			case filepath.Join("sub", "key"):
				f.events = append(f.events, "outer-lstat-target")
				return f.targetInfo, nil
			case "sub":
				f.events = append(f.events, "outer-lstat-parent")
				return f.parentInfo, nil
			default:
				f.t.Fatalf("unexpected outer lstat path %q", name)
				return nil, os.ErrNotExist
			}
		},
		openRoot: func(name string) (Root, error) {
			if name != "sub" {
				f.t.Fatalf("unexpected outer parent open %q", name)
			}
			f.events = append(f.events, "outer-open-parent")
			return parent, nil
		},
		open: func(name string) (File, error) {
			f.t.Fatalf("outer root was synced through %q", name)
			return nil, errors.New("unexpected outer sync")
		},
		openFile: func(name string, _ int, _ os.FileMode) (File, error) {
			f.t.Fatalf("outer root created or opened %q", name)
			return nil, errors.New("unexpected outer file operation")
		},
		rename: func(oldName, newName string) error {
			f.t.Fatalf("outer root renamed %q to %q", oldName, newName)
			return errors.New("unexpected outer rename")
		},
		remove: func(name string) error {
			f.t.Fatalf("outer root removed %q", name)
			return errors.New("unexpected outer cleanup")
		},
	}
	return &WriteRoot{root: outer, rootAbs: string(os.PathSeparator)}
}

func (f *nestedAtomicReplacementFixture) parentRoot() Root {
	return &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "." {
				f.t.Fatalf("unexpected pinned-parent lstat path %q", name)
			}
			f.events = append(f.events, "parent-lstat-opened")
			return f.parentInfo, nil
		},
		openFile: func(name string, _ int, _ os.FileMode) (File, error) {
			if filepath.Base(name) != name || !strings.HasPrefix(name, atomicTempPrefix) {
				f.t.Fatalf("candidate was not a parent-relative basename: %q", name)
			}
			f.candidate = name
			f.events = append(f.events, "parent-create-candidate")
			return f.candidateFile(), nil
		},
		rename: func(oldName, newName string) error {
			if oldName != f.candidate || newName != "key" {
				f.t.Fatalf("rename was not parent-relative: %q -> %q", oldName, newName)
			}
			f.events = append(f.events, "parent-rename-candidate")
			return nil
		},
		remove: func(name string) error {
			f.t.Fatalf("successful replacement unexpectedly cleaned up %q", name)
			return errors.New("unexpected cleanup")
		},
		open: func(name string) (File, error) {
			if name != "." {
				f.t.Fatalf("unexpected pinned-parent sync path %q", name)
			}
			f.events = append(f.events, "parent-open-sync")
			return &fakeFile{
				sync: func() error {
					f.events = append(f.events, "parent-sync")
					return nil
				},
				close: func() error {
					f.events = append(f.events, "parent-close-sync")
					return nil
				},
			}, nil
		},
		close: func() error {
			f.events = append(f.events, "parent-close-root")
			return nil
		},
	}
}

func (f *nestedAtomicReplacementFixture) candidateFile() File {
	return &fakeFile{
		write: func(p []byte) (int, error) {
			f.events = append(f.events, "candidate-write")
			return len(p), nil
		},
		chmod: func(perm os.FileMode) error {
			if perm != 0o600 {
				f.t.Fatalf("unexpected candidate mode %#o", perm)
			}
			f.events = append(f.events, "candidate-chmod")
			return nil
		},
		sync: func() error {
			f.events = append(f.events, "candidate-sync")
			return nil
		},
		close: func() error {
			f.events = append(f.events, "candidate-close")
			return nil
		},
	}
}

func TestOpenPinnedReplacementTargetIfNeededSkipsPinnedTargetOnNonWindows(t *testing.T) {
	openCalls := 0
	root := &fakeRoot{
		openFile: func(string, int, os.FileMode) (File, error) {
			openCalls++
			return nil, errors.New("unexpected pinned open")
		},
	}

	file, closeFile, err := openPinnedReplacementTargetIfNeeded(root, writeTestFileName, statTestPath(t, t.TempDir()))
	if err != nil {
		t.Fatalf("expected pinned target open to be skipped, got %v", err)
	}
	if file != nil {
		t.Fatal("expected no pinned target file on non-Windows")
	}
	if err := closeFile(); err != nil {
		t.Fatalf("expected no-op pinned target close, got %v", err)
	}
	if openCalls != 0 {
		t.Fatalf("expected no pinned target open calls, got %d", openCalls)
	}
}

func TestWriteFileReplacingUnderReplacesReadOnlyExistingRegularFileOnNonWindows(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("before"), 0o444); err != nil {
		t.Fatalf("seed target file: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(targetPath, 0o600); err != nil && !os.IsNotExist(err) {
			t.Errorf("restore target file permissions: %v", err)
		}
	})

	probe, probeErr := os.OpenFile(targetPath, os.O_WRONLY, 0)
	if probeErr == nil {
		if err := probe.Close(); err != nil {
			t.Fatalf("close writability probe: %v", err)
		}
		t.Skip("effective privileges bypass read-only file permissions")
	}
	if !os.IsPermission(probeErr) {
		t.Skipf("read-only file semantics are not testable: %v", probeErr)
	}

	if err := WriteFileReplacingUnder(rootDir, targetPath, []byte("after"), 0o600); err != nil {
		t.Fatalf("WriteFileReplacingUnder returned error: %v", err)
	}

	assertFileContent(t, targetPath, "after")
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("stat replaced file: %v", err)
	}
	if info.Mode().Perm() != 0o444 {
		t.Fatalf("expected existing read-only mode 0444 to be preserved, got %#o", info.Mode().Perm())
	}
}

func TestExactPermissionReplacementOverridesExistingModeOnNonWindows(t *testing.T) {
	for _, tt := range exactPermissionReplacementTestCases() {
		t.Run(tt.name, func(t *testing.T) {
			assertExactPermissionReplacement(t, tt.write)
		})
	}
}

func assertExactPermissionReplacement(t *testing.T, write func(*WriteRoot, string, []byte, os.FileMode) error) {
	t.Helper()
	canonicalRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve canonical root: %v", err)
	}
	targetPath := filepath.Join(canonicalRoot, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("before"), 0o644); err != nil {
		t.Fatalf("seed target file: %v", err)
	}

	root := openTestWriteRoot(t, canonicalRoot, OpenCanonicalWriteRoot)
	if err := write(root, writeTestFileName, []byte("after"), 0o600); err != nil {
		t.Fatalf("exact-permission replacement returned error: %v", err)
	}

	assertFileContent(t, targetPath, "after")
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("stat replaced file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected exact replacement mode 0600, got %#o", info.Mode().Perm())
	}
}

func TestWriteFileReplacingAtomicallyWithExactPermissionsPreservesOldFileOnRenameFailureNonWindows(t *testing.T) {
	fixture := newStrictAtomicFailureFixture(t)
	err := fixture.writeRoot().WriteFileReplacingAtomicallyWithExactPermissions(writeTestFileName, []byte("after"), 0o600)
	if !errors.Is(err, fixture.renameErr) {
		t.Fatalf("expected rename error, got %v", err)
	}
	fixture.assertPreserved(t)
}

func TestNestedAtomicReplacementPinsAndSyncsTargetParentOnNonWindows(t *testing.T) {
	for _, tt := range exactPermissionReplacementTestCases() {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newNestedAtomicReplacementFixture(t)
			err := tt.write(fixture.writeRoot(), filepath.Join("sub", "key"), []byte("after"), 0o600)
			if err != nil {
				t.Fatalf("nested replacement returned error: %v", err)
			}
			wantEvents := []string{
				"outer-lstat-target",
				"outer-lstat-parent",
				"outer-open-parent",
				"parent-lstat-opened",
				"parent-create-candidate",
				"candidate-write",
				"candidate-chmod",
				"candidate-sync",
				"candidate-close",
				"parent-rename-candidate",
				"parent-open-sync",
				"parent-sync",
				"parent-close-sync",
				"parent-close-root",
			}
			if !slices.Equal(fixture.events, wantEvents) {
				t.Fatalf("unexpected nested replacement events: got %#v want %#v", fixture.events, wantEvents)
			}
		})
	}
}

func TestNestedAtomicReplacementStaysOnPinnedParentAfterPathSwapOnNonWindows(t *testing.T) {
	for _, tt := range exactPermissionReplacementTestCases() {
		t.Run(tt.name, func(t *testing.T) {
			assertNestedAtomicReplacementStaysOnPinnedParentAfterPathSwap(t, tt.write)
		})
	}
}

func assertNestedAtomicReplacementStaysOnPinnedParentAfterPathSwap(t *testing.T, write func(*WriteRoot, string, []byte, os.FileMode) error) {
	t.Helper()
	fixture := newNestedParentSwapFixture(t)
	withFileSystem(t, &fakeFileSystem{openRoot: fixture.openRoot})
	root := openTestWriteRoot(t, fixture.rootDir, OpenWriteRoot)
	if err := write(root, filepath.Join("sub", "key"), []byte("after"), 0o600); err != nil {
		t.Fatalf("nested replacement after parent swap: %v", err)
	}
	fixture.assertReplacementStayedPinned(t)
}

func TestWriteRootMkdirAllDurableSyncsEveryNewParentOnNonWindows(t *testing.T) {
	fixture := newDurableMkdirSyncFixture(t, "keys", "nested")
	root := &WriteRoot{
		rootAbs: string(os.PathSeparator),
		root:    fixture.rootAt(""),
	}

	if err := root.MkdirAllDurable(filepath.Join("keys", "nested"), 0o750); err != nil {
		t.Fatalf("durable mkdir within root: %v", err)
	}

	wantEvents := []string{
		"lstat:keys",
		"mkdir:keys",
		"lstat:keys",
		"open-sync:.",
		"sync:.",
		"close-sync:.",
		"open-root:keys",
		"lstat-opened:keys",
		"lstat:keys/nested",
		"mkdir:keys/nested",
		"lstat:keys/nested",
		"open-sync:keys",
		"sync:keys",
		"close-sync:keys",
		"open-root:keys/nested",
		"lstat-opened:keys/nested",
		"close-root:keys",
		"close-root:keys/nested",
	}
	if !slices.Equal(fixture.events, wantEvents) {
		t.Fatalf("unexpected durable mkdir events: got %#v want %#v", fixture.events, wantEvents)
	}
}

func TestWriteRootMkdirAllDurableReturnsSyncErrorOnNonWindows(t *testing.T) {
	syncErr := errors.New("sync parent failure")
	fixture := newDurableMkdirSyncFixture(t, "keys")
	fixture.syncErrors[""] = syncErr
	root := &WriteRoot{
		rootAbs: string(os.PathSeparator),
		root:    fixture.rootAt(""),
	}

	err := root.MkdirAllDurable("keys", 0o750)
	if !errors.Is(err, syncErr) {
		t.Fatalf("expected durable mkdir sync error, got %v", err)
	}
	wantEvents := []string{
		"lstat:keys",
		"mkdir:keys",
		"lstat:keys",
		"open-sync:.",
		"sync:.",
		"close-sync:.",
	}
	if !slices.Equal(fixture.events, wantEvents) {
		t.Fatalf("unexpected sync-error events: got %#v want %#v", fixture.events, wantEvents)
	}
}

func TestWriteRootMkdirAllDurableSyncsParentAfterErrExistRaceOnNonWindows(t *testing.T) {
	fixture := newDurableMkdirSyncFixture(t, "keys")
	fixture.mkdirErrors["keys"] = fs.ErrExist
	root := &WriteRoot{
		rootAbs: string(os.PathSeparator),
		root:    fixture.rootAt(""),
	}

	if err := root.MkdirAllDurable("keys", 0o750); err != nil {
		t.Fatalf("durable mkdir after err-exist race: %v", err)
	}

	wantEvents := []string{
		"lstat:keys",
		"mkdir:keys",
		"lstat:keys",
		"open-sync:.",
		"sync:.",
		"close-sync:.",
		"open-root:keys",
		"lstat-opened:keys",
		"close-root:keys",
	}
	if !slices.Equal(fixture.events, wantEvents) {
		t.Fatalf("unexpected err-exist-race events: got %#v want %#v", fixture.events, wantEvents)
	}
}

func TestWriteRootRootRelativeFileOperationsAndSyncOnNonWindows(t *testing.T) {
	rootDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	root := openTestWriteRoot(t, rootDir, OpenCanonicalWriteRoot)
	tempPath, tempFile, err := root.CreateTempFile(0o600)
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := tempFile.Write([]byte("secret")); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	if err := tempFile.Sync(); err != nil {
		t.Fatalf("sync temp file: %v", err)
	}
	if err := tempFile.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}

	if err := root.Link(tempPath, "key"); err != nil {
		t.Fatalf("link key: %v", err)
	}
	if err := root.Rename(tempPath, "installed"); err != nil {
		t.Fatalf("rename temp file: %v", err)
	}
	if err := root.Sync(); err != nil {
		t.Fatalf("sync root: %v", err)
	}
	data, _, err := root.ReadRegularFile("key")
	if err != nil {
		t.Fatalf("read linked key: %v", err)
	}
	if string(data) != "secret" {
		t.Fatalf("unexpected key data: %q", string(data))
	}
	if err := root.Remove("installed"); err != nil {
		t.Fatalf("remove installed path: %v", err)
	}
	if err := root.Remove("key"); err != nil {
		t.Fatalf("remove key path: %v", err)
	}
}

func TestWriteRootRenameNoReplacePreservesExistingDestinationOnNonWindows(t *testing.T) {
	rootDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	sourcePath := filepath.Join(rootDir, "candidate")
	targetPath := filepath.Join(rootDir, "winner")
	if err := os.WriteFile(sourcePath, []byte("candidate"), 0o600); err != nil {
		t.Fatalf("write candidate: %v", err)
	}
	if err := os.WriteFile(targetPath, []byte("winner"), 0o600); err != nil {
		t.Fatalf("write winner: %v", err)
	}
	winnerInfo, err := os.Lstat(targetPath)
	if err != nil {
		t.Fatalf("stat winner before no-replace rename: %v", err)
	}
	root := openTestWriteRoot(t, rootDir, OpenCanonicalWriteRoot)

	err = root.RenameNoReplace("candidate", "winner")
	if errors.Is(err, ErrRenameNoReplaceUnsupported) {
		t.Skip("atomic no-replace rename is not supported by this platform build")
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("expected destination-exists error, got %v", err)
	}
	persistedInfo, err := os.Lstat(targetPath)
	if err != nil {
		t.Fatalf("stat winner after no-replace rename: %v", err)
	}
	if !os.SameFile(winnerInfo, persistedInfo) {
		t.Fatal("expected no-replace rename to preserve destination identity")
	}
	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read winner after no-replace rename: %v", err)
	}
	if string(data) != "winner" {
		t.Fatalf("expected winner bytes to be preserved, got %q", string(data))
	}
	if _, err := os.Lstat(sourcePath); err != nil {
		t.Fatalf("expected candidate to remain after lost no-replace race: %v", err)
	}
}

func TestWriteRootRenameNoReplacePublishesAbsentDestinationOnNonWindows(t *testing.T) {
	rootDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	sourcePath := filepath.Join(rootDir, "candidate")
	if err := os.WriteFile(sourcePath, []byte("candidate"), 0o600); err != nil {
		t.Fatalf("write candidate: %v", err)
	}
	candidateInfo, err := os.Lstat(sourcePath)
	if err != nil {
		t.Fatalf("stat candidate: %v", err)
	}
	root := openTestWriteRoot(t, rootDir, OpenCanonicalWriteRoot)

	err = root.RenameNoReplace("candidate", "winner")
	if errors.Is(err, ErrRenameNoReplaceUnsupported) {
		t.Skip("atomic no-replace rename is not supported by this platform build")
	}
	if err != nil {
		t.Fatalf("publish absent destination with no-replace rename: %v", err)
	}
	if _, err := os.Lstat(sourcePath); !os.IsNotExist(err) {
		t.Fatalf("expected candidate name to be consumed, stat err=%v", err)
	}
	winnerInfo, err := os.Lstat(filepath.Join(rootDir, "winner"))
	if err != nil {
		t.Fatalf("stat no-replace winner: %v", err)
	}
	if !os.SameFile(candidateInfo, winnerInfo) {
		t.Fatal("expected no-replace winner to retain candidate identity")
	}
}

func TestWriteRootDirectoryLockCanBeReleasedAndReacquiredOnNonWindows(t *testing.T) {
	rootDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	root := openTestWriteRoot(t, rootDir, OpenCanonicalWriteRoot)
	lock, err := root.LockDirectory()
	if errors.Is(err, ErrDirectoryLockUnsupported) {
		t.Skip("directory locking is not supported by this platform")
	}
	if err != nil {
		t.Fatalf("lock pinned root directory: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("unlock pinned root directory: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("close released directory lock again: %v", err)
	}
	reacquired, err := root.LockDirectory()
	if err != nil {
		t.Fatalf("reacquire pinned root directory lock: %v", err)
	}
	if err := reacquired.Close(); err != nil {
		t.Fatalf("release reacquired pinned root directory lock: %v", err)
	}
}

func TestWriteRootRenameNoReplaceReturnsHandleCloseErrorAfterPublishOnNonWindows(t *testing.T) {
	rootDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "candidate"), []byte("candidate"), 0o600); err != nil {
		t.Fatalf("write candidate: %v", err)
	}
	directory, err := os.Open(rootDir)
	if err != nil {
		t.Fatalf("open root directory handle: %v", err)
	}
	info, err := directory.Stat()
	if err != nil {
		t.Fatalf("stat root directory handle: %v", err)
	}
	closeErr := errors.New("close failed")
	file := &descriptorTestFile{
		File: directory,
		fd:   directory.Fd(),
		closeFn: func() error {
			return errors.Join(directory.Close(), closeErr)
		},
	}
	root := &WriteRoot{root: &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return info, nil },
		open:  func(string) (File, error) { return file, nil },
	}}

	err = root.RenameNoReplace("candidate", "winner")
	if errors.Is(err, ErrRenameNoReplaceUnsupported) {
		t.Skip("atomic no-replace rename is not supported by this platform build")
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected published rename to retain handle close error, got %v", err)
	}
	data, err := os.ReadFile(filepath.Join(rootDir, "winner"))
	if err != nil {
		t.Fatalf("read published winner after close error: %v", err)
	}
	if string(data) != "candidate" {
		t.Fatalf("unexpected published bytes after close error: %q", string(data))
	}
}

func TestPinnedDirectoryLockCloseJoinsUnlockAndHandleErrorsOnNonWindows(t *testing.T) {
	closeErr := errors.New("close failed")
	const invalidDescriptor = uintptr(^uint32(0) >> 1)
	lock := &pinnedDirectoryLock{
		directory: &fakeFile{close: func() error { return closeErr }},
		fd:        invalidDescriptor,
	}
	err := lock.Close()
	if err == nil || !errors.Is(err, closeErr) {
		t.Fatalf("expected lock close to retain handle error, got %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("expected second lock close to be idempotent, got %v", err)
	}
}

func TestWriteRootSyncReturnsDirectorySyncErrorOnNonWindows(t *testing.T) {
	syncErr := errors.New("directory sync failure")
	root := &WriteRoot{
		root: &fakeRoot{
			open: func(string) (File, error) {
				return &fakeFile{
					sync:  func() error { return syncErr },
					close: closeWithoutError,
				}, nil
			},
		},
	}
	if err := root.Sync(); !errors.Is(err, syncErr) {
		t.Fatalf("expected directory sync error, got %v", err)
	}
}

func TestSyncRootDirectoryErrorsOnNonWindows(t *testing.T) {
	t.Run("open", func(t *testing.T) {
		openErr := errors.New("open directory failure")
		root := &fakeRoot{
			open: func(string) (File, error) {
				return nil, openErr
			},
		}
		if err := syncRootDirectory(root); !errors.Is(err, openErr) {
			t.Fatalf("expected open error, got %v", err)
		}
	})

	t.Run("close", func(t *testing.T) {
		closeErr := errors.New("close directory failure")
		root := &fakeRoot{
			open: func(string) (File, error) {
				return &fakeFile{
					sync:  func() error { return nil },
					close: func() error { return closeErr },
				}, nil
			},
		}
		if err := syncRootDirectory(root); !errors.Is(err, closeErr) {
			t.Fatalf("expected close error, got %v", err)
		}
	})
}

func TestAtomicWriteSessionWriteAndCommitSyncsInDurableOrderOnNonWindows(t *testing.T) {
	events := make([]string, 0, 8)
	root := &fakeRoot{
		rename: func(oldName, newName string) error {
			events = append(events, "rename:"+oldName+"->"+newName)
			return nil
		},
		open: func(string) (File, error) {
			events = append(events, "open-dir")
			return &fakeFile{
				sync: func() error {
					events = append(events, "sync-dir")
					return nil
				},
				close: func() error {
					events = append(events, "close-dir")
					return nil
				},
			}, nil
		},
	}
	session := &atomicWriteSession{
		root:      root,
		targetRel: "final.json",
		tempRel:   "temp.json",
		tempFile: &fakeFile{
			write: func([]byte) (int, error) {
				events = append(events, "write-temp")
				return 4, nil
			},
			chmod: func(os.FileMode) error {
				events = append(events, "chmod-temp")
				return nil
			},
			sync: func() error {
				events = append(events, "sync-temp")
				return nil
			},
			close: func() error {
				events = append(events, "close-temp")
				return nil
			},
		},
	}

	if err := session.writeAndCommit([]byte("data"), 0o600); err != nil {
		t.Fatalf("writeAndCommit returned error: %v", err)
	}
	wantEvents := []string{
		"write-temp",
		"chmod-temp",
		"sync-temp",
		"close-temp",
		"rename:temp.json->final.json",
		"open-dir",
		"sync-dir",
		"close-dir",
	}
	if !slices.Equal(events, wantEvents) {
		t.Fatalf("unexpected durable ordering: got %#v want %#v", events, wantEvents)
	}
	if session.tempRel != "" {
		t.Fatalf("expected committed session temp path to be cleared, got %q", session.tempRel)
	}
}

func TestAtomicWriteSessionReturnsDirectorySyncErrorAfterRenameOnNonWindows(t *testing.T) {
	dirSyncErr := errors.New("directory sync failure")
	renamed := false
	root := &fakeRoot{
		rename: func(string, string) error {
			renamed = true
			return nil
		},
		open: func(string) (File, error) {
			return &fakeFile{
				sync:  func() error { return dirSyncErr },
				close: closeWithoutError,
			}, nil
		},
	}
	session := &atomicWriteSession{
		root:      root,
		targetRel: "final.json",
		tempRel:   "temp.json",
		tempFile: &fakeFile{
			write: func(data []byte) (int, error) { return len(data), nil },
			chmod: chmodWithoutError,
			sync:  func() error { return nil },
			close: closeWithoutError,
		},
	}

	err := session.writeAndCommit([]byte("data"), 0o600)
	if !errors.Is(err, dirSyncErr) {
		t.Fatalf("expected directory sync error, got %v", err)
	}
	if !renamed {
		t.Fatal("expected rename to happen before directory sync failure")
	}
	if session.tempRel != "" {
		t.Fatalf("expected committed temp path cleared before directory sync error, got %q", session.tempRel)
	}
}
