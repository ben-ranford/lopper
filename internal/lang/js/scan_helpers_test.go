package js

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
	sitter "github.com/smacker/go-tree-sitter"
)

const testJSSourceReadMaxBytes = 8 << 20

func TestScanRepoParseErrorWarning(t *testing.T) {
	repo := t.TempDir()
	broken := "import { map from 'lodash'\n"
	if err := os.WriteFile(filepath.Join(repo, "broken.js"), []byte(broken), 0o600); err != nil {
		t.Fatalf("write broken js: %v", err)
	}
	result, err := ScanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected scanned file even with parse errors")
	}
	if len(result.Warnings) == 0 || !strings.Contains(result.Warnings[0], "parse errors") {
		t.Fatalf("expected parse error warning, got %#v", result.Warnings)
	}
}

func TestScanHelpers(t *testing.T) {
	var files []string
	for i := 0; i < 7; i++ {
		appendParseErrorFile(&files, "f"+string(rune('a'+i))+".js")
	}
	if len(files) != 5 {
		t.Fatalf("expected parse error file list to cap at 5, got %d", len(files))
	}

	if !isSupportedFile("x.ts") || isSupportedFile("x.css") {
		t.Fatalf("unexpected supported file detection")
	}

	parser := newSourceParser()
	if _, err := parser.languageForPath("x.ts"); err != nil {
		t.Fatalf("expected ts language, got error: %v", err)
	}
	if _, err := parser.languageForPath("x.unknown"); err == nil {
		t.Fatalf("expected unsupported extension error")
	}
}

func TestScanRepoAndEntryBranches(t *testing.T) {
	if _, err := ScanRepo(context.Background(), ""); err == nil {
		t.Fatalf("expected empty repo path error")
	}

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "node_modules"), 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "node_modules", "skip.js"), []byte("export const x = 1"), 0o600); err != nil {
		t.Fatalf("write skipped file: %v", err)
	}
	result, err := ScanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	if len(result.Files) != 0 {
		t.Fatalf("expected no scanned files due to directory skipping, got %#v", result.Files)
	}
	if len(result.Warnings) == 0 || !strings.Contains(strings.Join(result.Warnings, "\n"), "no JS/TS files found") {
		t.Fatalf("expected no-files warning, got %#v", result.Warnings)
	}
}

func TestReadAndParseFileBranches(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "index.js")
	if err := os.WriteFile(path, []byte("export const v = 1\n"), 0o600); err != nil {
		t.Fatalf("write index.js: %v", err)
	}
	parser := newSourceParser()

	content, tree, rel, err := readAndParseFile(context.Background(), parser, "", path)
	if err != nil {
		t.Fatalf("read and parse file: %v", err)
	}
	if len(content) == 0 || tree == nil || rel == "" {
		t.Fatalf("unexpected readAndParseFile result content=%d tree=%v rel=%q", len(content), tree, rel)
	}

	if _, _, _, err := readAndParseFile(context.Background(), parser, repo, filepath.Join(repo, "missing.js")); err == nil {
		t.Fatalf("expected read error for missing file")
	}

	cssPath := filepath.Join(repo, "index.css")
	if err := os.WriteFile(cssPath, []byte("body{}"), 0o600); err != nil {
		t.Fatalf("write index.css: %v", err)
	}
	if _, _, _, err := readAndParseFile(context.Background(), parser, repo, cssPath); err == nil {
		t.Fatalf("expected parser language error for unsupported extension")
	}
}

func TestReadAndParseFileRejectsOversizedInput(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "large.js")
	if err := os.WriteFile(path, bytesOfLength(testJSSourceReadMaxBytes+1), 0o600); err != nil {
		t.Fatalf("write large.js: %v", err)
	}

	_, _, _, err := readAndParseFile(context.Background(), newSourceParser(), repo, path)
	if !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected oversized file to fail with ErrFileTooLarge, got %v", err)
	}
}

func TestScanRepoWarnsWhenBoundedReadSkipsOversizedFile(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "large.js")
	if err := os.WriteFile(path, bytesOfLength(testJSSourceReadMaxBytes+1), 0o600); err != nil {
		t.Fatalf("write large.js: %v", err)
	}

	result, err := ScanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan repo with oversized file: %v", err)
	}
	if len(result.Files) != 0 {
		t.Fatalf("expected oversized file to be skipped, got %#v", result.Files)
	}
	warnings := strings.Join(result.Warnings, "\n")
	if !strings.Contains(warnings, "skipped 1 JS/TS file(s) above") {
		t.Fatalf("expected oversized-file warning, got %#v", result.Warnings)
	}
}

func TestReadAndParseFileRejectsEscapingSourceSymlink(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	outsidePath := filepath.Join(outside, "outside.js")
	outsideContent := "import escape from \"outside-dependency\"\nexport const leaked = escape\n"
	if err := os.WriteFile(outsidePath, []byte(outsideContent), 0o600); err != nil {
		t.Fatalf("write outside.js: %v", err)
	}

	linkPath := filepath.Join(repo, "escape.js")
	if err := os.Symlink(outsidePath, linkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, _, _, err := readAndParseFile(context.Background(), newSourceParser(), repo, linkPath); err == nil {
		t.Fatalf("expected escaping source symlink read to fail")
	}

	result, err := ScanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan repo with escaping symlink: %v", err)
	}
	if len(result.Files) != 0 {
		t.Fatalf("expected escaping source symlink to be skipped, got %#v", result.Files)
	}
	if containsModuleImport(result, "outside-dependency") {
		t.Fatalf("expected outside symlink content not to be scanned")
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "skipped 1 non-regular JS/TS file(s)") {
		t.Fatalf("expected non-regular skip warning, got %#v", result.Warnings)
	}
}

func TestReadAndParseFileAllowsInRepoSourceSymlinkToRegularFile(t *testing.T) {
	repo := t.TempDir()
	targetPath := filepath.Join(repo, "src", "real.js")
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	source := "export const inside = 1\n"
	if err := os.WriteFile(targetPath, []byte(source), 0o600); err != nil {
		t.Fatalf("write real.js: %v", err)
	}

	linkPath := filepath.Join(repo, "linked.js")
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	content, tree, relPath, err := readAndParseFile(context.Background(), newSourceParser(), repo, linkPath)
	if err != nil {
		t.Fatalf("read in-repo source symlink: %v", err)
	}
	if string(content) != source {
		t.Fatalf("expected symlinked source content, got %q", string(content))
	}
	if tree == nil {
		t.Fatal("expected parse tree for in-repo source symlink")
	}
	if relPath != "linked.js" {
		t.Fatalf("expected symlink path to stay relative to scanned repo entry, got %q", relPath)
	}

	result, err := ScanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan repo with in-repo symlink: %v", err)
	}
	if len(result.Files) != 2 {
		t.Fatalf("expected both real file and symlinked file to be scanned, got %#v", result.Files)
	}
	if strings.Contains(strings.Join(result.Warnings, "\n"), "non-regular") {
		t.Fatalf("did not expect non-regular skip warning for in-repo source symlink, got %#v", result.Warnings)
	}
}

func TestReadAndParseFilePreservesMixedNonRegularAndCloseFailure(t *testing.T) {
	repo := t.TempDir()
	targetPath := filepath.Join(repo, "real.js")
	if err := os.WriteFile(targetPath, []byte("export const inside = 1\n"), 0o600); err != nil {
		t.Fatalf("write real.js: %v", err)
	}
	linkPath := filepath.Join(repo, "linked.js")
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	closeErr := errors.New("source root close failed")

	originalReadRepoSourceUnderLimit := readRepoSourceUnderLimit
	readRepoSourceUnderLimit = func(string, string, int64) ([]byte, error) {
		return nil, errors.Join(safeio.ErrNonRegularFile, closeErr)
	}
	t.Cleanup(func() {
		readRepoSourceUnderLimit = originalReadRepoSourceUnderLimit
	})

	_, _, _, err := readAndParseFile(context.Background(), newSourceParser(), repo, linkPath)
	if !errors.Is(err, safeio.ErrNonRegularFile) || !errors.Is(err, closeErr) {
		t.Fatalf("expected mixed non-regular and close failure to surface, got %v", err)
	}
}

func TestScanRepoSkipsEscapingSourceSymlinkDeterministically(t *testing.T) {
	repo := t.TempDir()
	targetPath := filepath.Join(t.TempDir(), "external.js")
	if err := os.WriteFile(targetPath, []byte("export const outside = true\n"), 0o600); err != nil {
		t.Fatalf("write external.js: %v", err)
	}
	linkPath := filepath.Join(repo, "linked.js")
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	result, err := ScanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan repo with escaping symlink: %v", err)
	}
	if len(result.Files) != 0 {
		t.Fatalf("expected escaping symlink to be skipped, got %#v", result.Files)
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "skipped 1 non-regular JS/TS file(s)") {
		t.Fatalf("expected escaping symlink skip warning, got %#v", result.Warnings)
	}
}

func TestReadRepoSourceThroughInRootSymlinkRejectsParentSwapBeforeFinalRootedRead(t *testing.T) {
	repo := t.TempDir()
	sourceDir := filepath.Join(repo, "src")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	targetName := "real.js"
	targetPath := filepath.Join(sourceDir, targetName)
	if err := os.WriteFile(targetPath, []byte("export const inside = true\n"), 0o600); err != nil {
		t.Fatalf("write in-repo target: %v", err)
	}
	linkPath := filepath.Join(repo, "linked.js")
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	outsideDir := t.TempDir()
	outsideSource := "import leaked from \"outside-dependency\"\n"
	if err := os.WriteFile(filepath.Join(outsideDir, targetName), []byte(outsideSource), 0o600); err != nil {
		t.Fatalf("write outside target: %v", err)
	}

	swapped := false
	originalOpenRepoSourceRoot := openRepoSourceRoot
	openRepoSourceRoot = func(path string) (safeio.Root, string, error) {
		root, validatedPath, err := originalOpenRepoSourceRoot(path)
		if err != nil {
			return nil, "", err
		}
		return &swapOnLstatRoot{
			Root:      root,
			targetRel: filepath.Join("src", targetName),
			swap: func() error {
				swapped = true
				if err := os.Rename(sourceDir, sourceDir+"-original"); err != nil {
					return err
				}
				return os.Symlink(outsideDir, sourceDir)
			},
		}, validatedPath, nil
	}
	t.Cleanup(func() {
		openRepoSourceRoot = originalOpenRepoSourceRoot
	})

	content, err := readRepoSourceThroughInRootSymlink(repo, linkPath)
	if !swapped {
		t.Fatal("expected parent swap immediately before the rooted final read")
	}
	if err == nil {
		t.Fatalf("expected escaping parent swap to be rejected, read %q", string(content))
	}
	if strings.Contains(string(content), "outside-dependency") {
		t.Fatalf("outside source content was read: %q", string(content))
	}
}

func TestReadRepoSourceThroughInRootSymlinkRejectsInvalidInputsAndNonRegularTargets(t *testing.T) {
	repo := t.TempDir()

	if _, err := readRepoSourceThroughInRootSymlink("", filepath.Join(repo, "linked.js")); !errors.Is(err, safeio.ErrNonRegularFile) {
		t.Fatalf("expected empty repo to be rejected as non-regular fallback, got %v", err)
	}
	if _, err := readRepoSourceThroughInRootSymlink(repo, filepath.Join(repo, "missing.js")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected missing symlink path to surface not-exist, got %v", err)
	}

	brokenLink := filepath.Join(repo, "broken.js")
	if err := os.Symlink(filepath.Join(repo, "missing-target.js"), brokenLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := readRepoSourceThroughInRootSymlink(repo, brokenLink); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected broken symlink target to surface not-exist, got %v", err)
	}

	plainPath := filepath.Join(repo, "plain.js")
	if err := os.WriteFile(plainPath, []byte("export const plain = true\n"), 0o600); err != nil {
		t.Fatalf("write plain.js: %v", err)
	}
	if _, err := readRepoSourceThroughInRootSymlink(repo, plainPath); !errors.Is(err, safeio.ErrNonRegularFile) {
		t.Fatalf("expected non-symlink path to be rejected, got %v", err)
	}

	targetDir := filepath.Join(repo, "target-dir")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir target dir: %v", err)
	}
	dirLink := filepath.Join(repo, "dir-link.js")
	if err := os.Symlink(targetDir, dirLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := readRepoSourceThroughInRootSymlink(repo, dirLink); !errors.Is(err, safeio.ErrNonRegularFile) {
		t.Fatalf("expected symlinked directory target to be rejected, got %v", err)
	}
}

func TestReadRepoSourceThroughInRootSymlinkPreservesRootOpenAndCloseFailures(t *testing.T) {
	t.Run("open", func(t *testing.T) {
		openErr := errors.New("open repo source root")
		originalOpenRepoSourceRoot := openRepoSourceRoot
		openRepoSourceRoot = func(string) (safeio.Root, string, error) {
			return nil, "", openErr
		}
		t.Cleanup(func() {
			openRepoSourceRoot = originalOpenRepoSourceRoot
		})

		if _, err := readRepoSourceThroughInRootSymlink(t.TempDir(), "linked.js"); !errors.Is(err, openErr) {
			t.Fatalf("expected root open failure to surface, got %v", err)
		}
	})

	t.Run("close", func(t *testing.T) {
		repo := t.TempDir()
		source := "export const inside = true\n"
		targetPath := filepath.Join(repo, "real.js")
		if err := os.WriteFile(targetPath, []byte(source), 0o600); err != nil {
			t.Fatalf("write in-repo target: %v", err)
		}
		linkPath := filepath.Join(repo, "linked.js")
		if err := os.Symlink(targetPath, linkPath); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		closeErr := errors.New("close repo source root")

		originalOpenRepoSourceRoot := openRepoSourceRoot
		openRepoSourceRoot = func(path string) (safeio.Root, string, error) {
			root, validatedPath, err := originalOpenRepoSourceRoot(path)
			if err != nil {
				return nil, "", err
			}
			return &closingLicenseRoot{Root: root, closeErr: closeErr}, validatedPath, nil
		}
		t.Cleanup(func() {
			openRepoSourceRoot = originalOpenRepoSourceRoot
		})

		content, err := readRepoSourceThroughInRootSymlink(repo, linkPath)
		if !errors.Is(err, closeErr) {
			t.Fatalf("expected root close failure to surface, got %v", err)
		}
		if string(content) != source {
			t.Fatalf("expected successful bounded read before close failure, got %q", string(content))
		}
	})
}

type swapOnLstatRoot struct {
	safeio.Root
	targetRel string
	swap      func() error
}

func (r *swapOnLstatRoot) Lstat(name string) (fs.FileInfo, error) {
	if name == r.targetRel && r.swap != nil {
		swap := r.swap
		r.swap = nil
		if err := swap(); err != nil {
			return nil, err
		}
	}
	return r.Root.Lstat(name)
}

func TestScanRepoEntrySkipsNonRegularSourceWhenDirEntryTypeIsUnknown(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	outsidePath := filepath.Join(outside, "outside.js")
	if err := os.WriteFile(outsidePath, []byte("export const leaked = true\n"), 0o600); err != nil {
		t.Fatalf("write outside.js: %v", err)
	}

	linkPath := filepath.Join(repo, "escape.js")
	if err := os.Symlink(outsidePath, linkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	entry, err := dirEntryAtPath(linkPath)
	if err != nil {
		t.Fatalf("dir entry for symlink: %v", err)
	}

	state := scanRepoState{
		parser:   newSourceParser(),
		repoPath: repo,
		result:   &ScanResult{},
	}
	if err := scanRepoEntry(context.Background(), &state, linkPath, &zeroTypeDirEntry{DirEntry: entry}); err != nil {
		t.Fatalf("scanRepoEntry returned error for unknown-type non-regular source: %v", err)
	}
	if state.skippedNonRegularFiles != 1 {
		t.Fatalf("expected skippedNonRegularFiles=1, got %d", state.skippedNonRegularFiles)
	}
	if state.skippedLargeFiles != 0 {
		t.Fatalf("expected no oversized-file skips, got %d", state.skippedLargeFiles)
	}
	if len(state.result.Files) != 0 {
		t.Fatalf("expected unknown-type non-regular source to be skipped, got %#v", state.result.Files)
	}
}

func TestScanRepoEntrySkipsSymlinkFromDirEntryMetadata(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	outsidePath := filepath.Join(outside, "outside.js")
	if err := os.WriteFile(outsidePath, []byte("export const leaked = true\n"), 0o600); err != nil {
		t.Fatalf("write outside.js: %v", err)
	}

	linkPath := filepath.Join(repo, "escape.js")
	if err := os.Symlink(outsidePath, linkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	entry, err := dirEntryAtPath(linkPath)
	if err != nil {
		t.Fatalf("dir entry for symlink: %v", err)
	}

	state := scanRepoState{
		parser:   newSourceParser(),
		repoPath: repo,
		result:   &ScanResult{},
	}
	if err := scanRepoEntry(context.Background(), &state, linkPath, entry); err != nil {
		t.Fatalf("scanRepoEntry returned error for symlink metadata branch: %v", err)
	}
	if state.skippedNonRegularFiles != 1 {
		t.Fatalf("expected skippedNonRegularFiles=1, got %d", state.skippedNonRegularFiles)
	}
}

func TestScanRepoEntrySkipsUnsupportedFile(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "styles.css")
	entry := mustDirEntryForFile(t, path, "body {}\n")

	state := scanRepoState{
		parser:   newSourceParser(),
		repoPath: repo,
		result:   &ScanResult{},
	}
	if err := scanRepoEntry(context.Background(), &state, path, entry); err != nil {
		t.Fatalf("scanRepoEntry returned error for unsupported file: %v", err)
	}
	if state.skippedNonRegularFiles != 0 || state.skippedLargeFiles != 0 {
		t.Fatalf("expected unsupported file not to affect skip counters, got nonRegular=%d large=%d", state.skippedNonRegularFiles, state.skippedLargeFiles)
	}
	if len(state.result.Files) != 0 {
		t.Fatalf("expected unsupported file to be ignored, got %#v", state.result.Files)
	}
}

func TestScanRepoEntrySkipsPureJoinedNonRegularError(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "app.js")
	entry := mustDirEntryForFile(t, path, "export const value = 1\n")

	state := scanRepoState{
		parser:   newSourceParser(),
		repoPath: repo,
		readAndParseFile: func(context.Context, *sourceParser, string, string) ([]byte, *sitter.Tree, string, error) {
			return nil, nil, "", errors.Join(safeio.ErrNonRegularFile)
		},
		result: &ScanResult{},
	}
	if err := scanRepoEntry(context.Background(), &state, path, entry); err != nil {
		t.Fatalf("scanRepoEntry returned error for pure joined non-regular source: %v", err)
	}
	if state.skippedNonRegularFiles != 1 {
		t.Fatalf("expected skippedNonRegularFiles=1, got %d", state.skippedNonRegularFiles)
	}
	if len(state.result.Files) != 0 {
		t.Fatalf("expected pure joined non-regular source to be skipped, got %#v", state.result.Files)
	}
}

func TestScanRepoEntryReturnsJoinedNonRegularAndCloseError(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "app.js")
	entry := mustDirEntryForFile(t, path, "export const value = 1\n")
	closeErr := errors.New("root close failed")

	state := scanRepoState{
		parser:   newSourceParser(),
		repoPath: repo,
		readAndParseFile: func(context.Context, *sourceParser, string, string) ([]byte, *sitter.Tree, string, error) {
			return nil, nil, "", errors.Join(safeio.ErrNonRegularFile, closeErr)
		},
		result: &ScanResult{},
	}
	err := scanRepoEntry(context.Background(), &state, path, entry)
	if !errors.Is(err, safeio.ErrNonRegularFile) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined non-regular and close error to surface, got %v", err)
	}
	if state.skippedNonRegularFiles != 0 {
		t.Fatalf("expected joined non-regular and close error not to increment skipped count, got %d", state.skippedNonRegularFiles)
	}
}

func TestIsPureNonRegularReadError(t *testing.T) {
	t.Parallel()

	otherErr := errors.New("close failed")
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "plain non-regular", err: safeio.ErrNonRegularFile, want: true},
		{name: "wrapped non-regular", err: &fs.PathError{Op: "open", Path: "app.js", Err: safeio.ErrNonRegularFile}, want: true},
		{name: "joined pure non-regular", err: errors.Join(safeio.ErrNonRegularFile, &fs.PathError{Op: "open", Path: "app.js", Err: safeio.ErrNonRegularFile}), want: true},
		{name: "joined mixed", err: errors.Join(safeio.ErrNonRegularFile, otherErr), want: false},
		{name: "wrapped joined mixed", err: &fs.PathError{Op: "open", Path: "app.js", Err: errors.Join(safeio.ErrNonRegularFile, otherErr)}, want: false},
		{name: "unwrap nil error", err: &nilUnwrapError{}, want: false},
		{name: "other", err: otherErr, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPureNonRegularReadError(tt.err); got != tt.want {
				t.Fatalf("isPureNonRegularReadError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func bytesOfLength(n int64) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = 'a'
	}
	return data
}

func TestScanRepoEntrySkipsDirectoriesAndContextCancel(t *testing.T) {
	repo := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ScanRepo(ctx, repo); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled error, got %v", err)
	}

	state := scanRepoState{
		parser:   newSourceParser(),
		repoPath: repo,
		result:   &ScanResult{},
	}
	if err := os.MkdirAll(filepath.Join(repo, "dist"), 0o755); err != nil {
		t.Fatalf("mkdir dist: %v", err)
	}
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("readdir repo: %v", err)
	}
	var dirEntry fs.DirEntry
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() == "dist" {
			dirEntry = entry
			break
		}
	}
	if dirEntry == nil {
		t.Fatalf("expected dist entry")
	}
	if err := scanRepoEntry(context.Background(), &state, filepath.Join(repo, "dist"), dirEntry); !errors.Is(err, fs.SkipDir) {
		t.Fatalf("expected skip dir result, got %v", err)
	}

	if err := os.MkdirAll(filepath.Join(repo, ".next"), 0o755); err != nil {
		t.Fatalf("mkdir .next: %v", err)
	}
	nextEntry, err := os.Stat(filepath.Join(repo, ".next"))
	if err != nil {
		t.Fatalf("stat .next: %v", err)
	}
	if err := scanRepoEntry(context.Background(), &state, filepath.Join(repo, ".next"), fs.FileInfoToDirEntry(nextEntry)); !errors.Is(err, fs.SkipDir) {
		t.Fatalf("expected .next skip dir result, got %v", err)
	}
}

type zeroTypeDirEntry struct {
	fs.DirEntry
}

func (*zeroTypeDirEntry) Type() fs.FileMode {
	return 0
}

func dirEntryAtPath(path string) (fs.DirEntry, error) {
	dirEntries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	for _, entry := range dirEntries {
		if entry.Name() == filepath.Base(path) {
			return entry, nil
		}
	}
	return nil, fs.ErrNotExist
}

func mustDirEntryForFile(t *testing.T, path string, content string) fs.DirEntry {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write file %s: %v", path, err)
	}
	entry, err := dirEntryAtPath(path)
	if err != nil {
		t.Fatalf("dir entry for %s: %v", path, err)
	}
	return entry
}

type nilUnwrapError struct{}

func (*nilUnwrapError) Error() string {
	return "nil unwrap"
}

func (*nilUnwrapError) Unwrap() error {
	return nil
}
