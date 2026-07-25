package safeio

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenFileNoFollowByVerificationRejectsRenamedLeafReplacedBySymlinkToSameFile(t *testing.T) {
	rootDir := t.TempDir()
	tracePath := filepath.Join(rootDir, "trace.ndjson")
	if err := os.WriteFile(tracePath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	withOpenFileNoFollowByVerificationBeforeOpen(t, func() {
		renamedPath := filepath.Join(rootDir, "trace.real")
		if err := os.Rename(tracePath, renamedPath); err != nil {
			t.Fatalf("rename trace aside: %v", err)
		}
		if err := os.Symlink(filepath.Base(renamedPath), tracePath); err != nil {
			t.Fatalf("replace trace with symlink: %v", err)
		}
	})

	root := openTestRoot(t, rootDir)
	file, err := openFileNoFollowByVerification(root, filepath.Base(tracePath))
	if file != nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close unexpected file: %v", closeErr)
		}
		t.Fatal("expected symlink swap to fail before returning a file")
	}
	if err == nil || !errors.Is(err, ErrNoFollowFinalComponent) {
		t.Fatalf("expected leaf symlink rejection, got %v", err)
	}
}

func TestOpenFileNoFollowByVerificationRejectsDirectLeafSwap(t *testing.T) {
	rootDir := t.TempDir()
	tracePath := filepath.Join(rootDir, "trace.ndjson")
	replacementPath := filepath.Join(rootDir, "trace.other")
	if err := os.WriteFile(tracePath, []byte("before\n"), 0o600); err != nil {
		t.Fatalf("write original trace: %v", err)
	}
	if err := os.WriteFile(replacementPath, []byte("after\n"), 0o600); err != nil {
		t.Fatalf("write replacement trace: %v", err)
	}

	withOpenFileNoFollowByVerificationBeforeOpen(t, func() {
		if err := os.Remove(tracePath); err != nil {
			t.Fatalf("remove original trace: %v", err)
		}
		if err := os.Rename(replacementPath, tracePath); err != nil {
			t.Fatalf("swap replacement trace into place: %v", err)
		}
	})

	root := openTestRoot(t, rootDir)
	file, err := openFileNoFollowByVerification(root, filepath.Base(tracePath))
	if file != nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close unexpected file: %v", closeErr)
		}
		t.Fatal("expected direct leaf swap to fail before returning a file")
	}
	if err == nil || !strings.Contains(err.Error(), "changed while opening") {
		t.Fatalf("expected direct leaf swap rejection, got %v", err)
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

	tests := []struct {
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
	}{
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
			lstatCalls := 0
			closed := false
			root := &fakeRoot{
				lstat: func(name string) (fs.FileInfo, error) {
					if name != "trace.ndjson" {
						t.Fatalf("unexpected lstat path: %q", name)
					}
					lstatCalls++
					if lstatCalls == 1 {
						return tt.preInfo, tt.preErr
					}
					return tt.currentInfo, tt.currentErr
				},
				open: func(name string) (File, error) {
					if name != "trace.ndjson" {
						t.Fatalf("unexpected open path: %q", name)
					}
					if tt.openErr != nil {
						return nil, tt.openErr
					}
					return &fakeFile{
						stat: func() (fs.FileInfo, error) {
							return tt.openedInfo, tt.openedErr
						},
						close: func() error {
							closed = true
							return tt.closeErr
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
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected error matching %v, got %v", tt.wantErr, err)
			}
			if tt.wantMessage != "" && (err == nil || !strings.Contains(err.Error(), tt.wantMessage)) {
				t.Fatalf("expected error containing %q, got %v", tt.wantMessage, err)
			}
			if tt.closeErr != nil && !errors.Is(err, tt.closeErr) {
				t.Fatalf("expected close error to remain recoverable, got %v", err)
			}
			if closed != tt.wantClosed {
				t.Fatalf("unexpected close state: got %t want %t", closed, tt.wantClosed)
			}
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
