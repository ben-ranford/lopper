package safeio

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type openFileNoFollowByVerificationFailureCase struct {
	name        string
	preInfo     fs.FileInfo
	preErr      error
	openErr     error
	openedInfo  fs.FileInfo
	openedErr   error
	currentInfo fs.FileInfo
	currentErr  error
	closeErr    error
	wantErr     error
	wantMessage string
	wantClosed  bool
}

func TestOpenFileNoFollowByVerificationRejectsMutations(t *testing.T) {
	for _, tc := range openNoFollowMutationCases() {
		t.Run(tc.name, func(t *testing.T) {
			assertOpenFileNoFollowMutationRejected(t, "verification helper", tc, func(t *testing.T, tracePath string) (io.Closer, error) {
				t.Helper()
				root := openTestRoot(t, filepath.Dir(tracePath))
				return openFileNoFollowByVerification(root, filepath.Base(tracePath))
			})
		})
	}
}

func TestOpenFileNoFollowByVerificationFailsClosedForInvalidStates(t *testing.T) {
	rootDir := t.TempDir()
	originalPath := filepath.Join(rootDir, "trace.ndjson")
	otherPath := filepath.Join(rootDir, "other.ndjson")
	if err := os.WriteFile(originalPath, []byte("original\n"), 0o600); err != nil {
		t.Fatalf("write original trace: %v", err)
	}
	if err := os.WriteFile(otherPath, []byte("other\n"), 0o600); err != nil {
		t.Fatalf("write other trace: %v", err)
	}

	originalInfo := statTestPath(t, originalPath)
	otherInfo := statTestPath(t, otherPath)
	directoryInfo := statTestPath(t, rootDir)
	preStatErr := errors.New("pre-open lstat failed")
	openErr := errors.New("open failed")
	openedStatErr := errors.New("opened handle stat failed")
	postStatErr := errors.New("post-open lstat failed")
	closeErr := errors.New("close failed")

	tests := []openFileNoFollowByVerificationFailureCase{
		{
			name:    "pre-open lstat error",
			preErr:  preStatErr,
			wantErr: preStatErr,
		},
		{
			name:       "pre-open symlink",
			preInfo:    &verificationFileInfo{name: "trace.ndjson", mode: os.ModeSymlink},
			wantErr:    ErrNoFollowFinalComponent,
			wantClosed: false,
		},
		{
			name:       "pre-open non-regular",
			preInfo:    directoryInfo,
			wantErr:    ErrNoFollowFinalComponent,
			wantClosed: false,
		},
		{
			name:       "open error",
			preInfo:    originalInfo,
			openErr:    openErr,
			wantErr:    openErr,
			wantClosed: false,
		},
		{
			name:       "opened handle stat error",
			preInfo:    originalInfo,
			openedErr:  openedStatErr,
			closeErr:   closeErr,
			wantErr:    openedStatErr,
			wantClosed: true,
		},
		{
			name:       "opened handle non-regular",
			preInfo:    originalInfo,
			openedInfo: directoryInfo,
			wantErr:    ErrNoFollowFinalComponent,
			wantClosed: true,
		},
		{
			name:       "post-open lstat error",
			preInfo:    originalInfo,
			openedInfo: originalInfo,
			currentErr: postStatErr,
			wantErr:    postStatErr,
			wantClosed: true,
		},
		{
			name:        "post-open non-regular",
			preInfo:     originalInfo,
			openedInfo:  originalInfo,
			currentInfo: directoryInfo,
			wantErr:     ErrNoFollowFinalComponent,
			wantClosed:  true,
		},
		{
			name:        "opened handle identity mismatch",
			preInfo:     originalInfo,
			openedInfo:  otherInfo,
			currentInfo: originalInfo,
			wantMessage: "changed while opening",
			wantClosed:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runOpenFileNoFollowByVerificationFailureCase(t, tt)
		})
	}
}

func TestOpenFileNoFollowByVerificationJoinsCloseErrorOnFailure(t *testing.T) {
	closeErr := errors.New("close failed")
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "trace.ndjson" {
				t.Fatalf("unexpected lstat path: %q", name)
			}
			return &verificationFileInfo{name: name, mode: 0o600, sys: "trace"}, nil
		},
		open: func(name string) (File, error) {
			if name != "trace.ndjson" {
				t.Fatalf("unexpected open path: %q", name)
			}
			return &fakeFile{
				stat: func() (fs.FileInfo, error) {
					return &verificationFileInfo{name: name, mode: 0o600, sys: "trace"}, nil
				},
				close: func() error {
					return closeErr
				},
			}, nil
		},
	}
	file, err := openFileNoFollowByVerification(root, "trace.ndjson")
	if file != nil {
		t.Fatal("expected failure to return no file")
	}
	if err == nil || !errors.Is(err, closeErr) {
		t.Fatalf("expected close error to be joined, got %v", err)
	}
}

func TestRequireOpenedNoFollowOSFileWrapsUnsupportedAndCloseErrors(t *testing.T) {
	for _, platform := range []string{"darwin", "windows"} {
		t.Run(platform, func(t *testing.T) {
			closeErr := errors.New("close failed")
			file := &fakeFile{close: func() error { return closeErr }}

			osFile, err := requireOpenedNoFollowOSFile(file, platform)
			if osFile != nil {
				t.Fatal("expected unsupported handle type to return no file")
			}
			if !errors.Is(err, ErrOpenFileNoFollowUnsupported) {
				t.Fatalf("expected unsupported sentinel, got %v", err)
			}
			if !errors.Is(err, closeErr) {
				t.Fatalf("expected close error to remain recoverable, got %v", err)
			}
			if !strings.Contains(err.Error(), "unsupported on "+platform) {
				t.Fatalf("expected platform-specific unsupported error, got %v", err)
			}
		})
	}
}

func withOpenFileNoFollowByVerificationBeforeOpen(t *testing.T, hook func()) {
	t.Helper()

	previousHook := openFileNoFollowByVerificationBeforeOpen
	called := false
	openFileNoFollowByVerificationBeforeOpen = func() {
		if called {
			t.Fatal("before-open verification hook called more than once")
		}
		called = true
		hook()
	}
	t.Cleanup(func() {
		openFileNoFollowByVerificationBeforeOpen = previousHook
	})
}

func runOpenFileNoFollowByVerificationFailureCase(t *testing.T, tc openFileNoFollowByVerificationFailureCase) {
	t.Helper()

	lstatCalls := 0
	closed := false
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			expectVerificationName(t, "lstat", name)
			lstatCalls++
			if lstatCalls == 1 {
				return tc.preInfo, tc.preErr
			}
			return tc.currentInfo, tc.currentErr
		},
		open: func(name string) (File, error) {
			expectVerificationName(t, "open", name)
			if tc.openErr != nil {
				return nil, tc.openErr
			}
			return &fakeFile{
				stat: func() (fs.FileInfo, error) {
					return tc.openedInfo, tc.openedErr
				},
				close: func() error {
					closed = true
					return tc.closeErr
				},
			}, nil
		},
	}

	file, err := openFileNoFollowByVerification(root, "trace.ndjson")
	if file != nil {
		if closeFileErr := file.Close(); closeFileErr != nil {
			t.Fatalf("close unexpected file: %v", closeFileErr)
		}
		t.Fatal("expected invalid verification state to return no file")
	}
	assertOpenFileNoFollowByVerificationFailure(t, tc, err, closed)
}

func expectVerificationName(t *testing.T, op, name string) {
	t.Helper()
	if name != "trace.ndjson" {
		t.Fatalf("unexpected %s path: %q", op, name)
	}
}

func assertOpenFileNoFollowByVerificationFailure(t *testing.T, tc openFileNoFollowByVerificationFailureCase, err error, closed bool) {
	t.Helper()

	if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
		t.Fatalf("expected error matching %v, got %v", tc.wantErr, err)
	}
	if tc.wantMessage != "" && (err == nil || !strings.Contains(err.Error(), tc.wantMessage)) {
		t.Fatalf("expected error containing %q, got %v", tc.wantMessage, err)
	}
	if tc.closeErr != nil && !errors.Is(err, tc.closeErr) {
		t.Fatalf("expected close error to remain recoverable, got %v", err)
	}
	if closed != tc.wantClosed {
		t.Fatalf("unexpected close state: got %t want %t", closed, tc.wantClosed)
	}
}

type verificationFileInfo struct {
	name string
	mode fs.FileMode
	sys  any
}

func (f *verificationFileInfo) Name() string       { return f.name }
func (f *verificationFileInfo) Size() int64        { return 0 }
func (f *verificationFileInfo) Mode() fs.FileMode  { return f.mode }
func (f *verificationFileInfo) ModTime() time.Time { return time.Unix(0, 0) }
func (f *verificationFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f *verificationFileInfo) Sys() any           { return f.sys }
