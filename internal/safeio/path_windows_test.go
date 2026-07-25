//go:build windows

package safeio

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
)

type windowsRawPathRejectionCase struct {
	name string
	path string
	want string
}

func windowsRawPathRejectionCases() []windowsRawPathRejectionCase {
	return []windowsRawPathRejectionCase{
		{name: "drive relative", path: `C:cache`, want: "drive-relative"},
		{name: "rooted without drive", path: `\cache`, want: "include a drive or UNC share"},
		{name: "rooted without drive forward slash", path: `/cache`, want: "include a drive or UNC share"},
		{name: "namespace", path: `\\?\C:\cache`, want: "device or namespace forms"},
		{name: "incomplete unc", path: `\\server`, want: "UNC host and share"},
		{name: "drive trailing dot alias", path: `C:\cache.`, want: "trailing dot or space aliases"},
		{name: "drive trailing space alias", path: `C:\cache `, want: "trailing dot or space aliases"},
		{name: "drive reserved name", path: `C:\cache\NUL.txt`, want: "reserved DOS device names"},
		{name: "relative trailing space alias", path: `cache `, want: "trailing dot or space aliases"},
		{name: "relative trailing dot alias nested", path: `cache.\child`, want: "trailing dot or space aliases"},
		{name: "relative reserved con", path: `CON`, want: "reserved DOS device names"},
		{name: "relative reserved nul nested", path: `sub\NUL.txt`, want: "reserved DOS device names"},
		{name: "reserved superscript", path: `sub\\COM¹.txt`, want: "reserved DOS device names"},
		{name: "reserved nul stream", path: `sub\\NUL:stream`, want: "reserved DOS device names"},
		{name: "reserved com superscript stream", path: `sub\\COM¹:stream`, want: "reserved DOS device names"},
		{name: "reserved conin stream", path: `sub\\CONIN$:stream`, want: "reserved DOS device names"},
	}
}

func TestWriteRootRejectsRawUnsupportedWindowsRelativePathsBeforeRootOperations(t *testing.T) {
	paths := []struct {
		name string
		path string
		want string
	}{
		{name: "drive relative", path: `C:cache`, want: "drive-relative"},
		{name: "rooted without drive", path: `\cache`, want: "include a drive or UNC share"},
		{name: "rooted without drive forward slash", path: `/cache`, want: "include a drive or UNC share"},
		{name: "namespace", path: `\\?\C:\cache`, want: "device or namespace forms"},
		{name: "incomplete unc", path: `\\server`, want: "UNC host and share"},
		{name: "trailing space leaf", path: `cache `, want: "trailing dot or space aliases"},
		{name: "trailing dot nested", path: `cache.\child`, want: "trailing dot or space aliases"},
		{name: "trailing space nested", path: `sub\cache \child`, want: "trailing dot or space aliases"},
		{name: "reserved con leaf", path: `CON`, want: "reserved DOS device names"},
		{name: "reserved nul nested", path: `sub\NUL.txt`, want: "reserved DOS device names"},
		{name: "reserved nul stream", path: `sub\NUL:stream`, want: "reserved DOS device names"},
		{name: "reserved com superscript stream", path: `sub\COM¹:stream`, want: "reserved DOS device names"},
		{name: "reserved conin stream", path: `sub\CONIN$:stream`, want: "reserved DOS device names"},
	}
	operations := []struct {
		name string
		run  func(*WriteRoot, string) error
	}{
		{name: "mkdir all", run: func(root *WriteRoot, path string) error {
			return root.MkdirAll(path, 0o750)
		}},
		{name: "durable mkdir all", run: func(root *WriteRoot, path string) error {
			return root.MkdirAllDurable(path, 0o750)
		}},
		{name: "lstat", run: func(root *WriteRoot, path string) error {
			_, err := root.Lstat(path)
			return err
		}},
		{name: "chmod", run: func(root *WriteRoot, path string) error {
			return root.Chmod(path, 0o600)
		}},
		{name: "read and open", run: func(root *WriteRoot, path string) error {
			_, _, err := root.ReadRegularFile(path)
			return err
		}},
		{name: "replacement", run: func(root *WriteRoot, path string) error {
			return root.WriteFileReplacing(path, []byte("after"), 0o600)
		}},
		{name: "write creating parents", run: func(root *WriteRoot, path string) error {
			return root.WriteFileCreatingParents(path, []byte("after"), 0o600, 0o750)
		}},
		{name: "create temp", run: func(root *WriteRoot, path string) error {
			_, _, err := CreateTempFileWithinRoot(root.root, path, 0o600)
			return err
		}},
		{name: "cleanup temp", run: func(root *WriteRoot, path string) error {
			return root.CleanupTempFile(path, &fakeFile{close: closeWithoutError})
		}},
		{name: "move source", run: func(root *WriteRoot, path string) error {
			return MoveFileWithinRoot(root.root, path, "target", 0o750, 0o600)
		}},
		{name: "move target", run: func(root *WriteRoot, path string) error {
			return MoveFileWithinRoot(root.root, "source", path, 0o750, 0o600)
		}},
	}

	for _, pathCase := range paths {
		for _, operation := range operations {
			t.Run(pathCase.name+"/"+operation.name, func(t *testing.T) {
				rootCalls := 0
				unexpectedErr := errors.New("unexpected root operation")
				recordCall := func() {
					rootCalls++
				}
				root := &WriteRoot{
					rootAbs: `C:\root`,
					root: &fakeRoot{
						lstat: func(string) (fs.FileInfo, error) {
							recordCall()
							return nil, unexpectedErr
						},
						chmod: func(string, os.FileMode) error {
							recordCall()
							return unexpectedErr
						},
						mkdir: func(string, os.FileMode) error {
							recordCall()
							return unexpectedErr
						},
						open: func(string) (File, error) {
							recordCall()
							return nil, unexpectedErr
						},
						openFile: func(string, int, os.FileMode) (File, error) {
							recordCall()
							return nil, unexpectedErr
						},
						openRoot: func(string) (Root, error) {
							recordCall()
							return nil, unexpectedErr
						},
						rename: func(string, string) error {
							recordCall()
							return unexpectedErr
						},
						remove: func(string) error {
							recordCall()
							return unexpectedErr
						},
					},
				}

				err := operation.run(root, pathCase.path)
				if err == nil || !strings.Contains(err.Error(), pathCase.want) {
					t.Fatalf("expected %q rejection, got %v", pathCase.want, err)
				}
				if rootCalls != 0 {
					t.Fatalf("expected rejection before root operations, got %d calls", rootCalls)
				}
			})
		}
	}
}

func TestResolveRelativeTargetPreservesStructuralWindowsComponents(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		policy  rootedTargetPolicy
		want    string
		wantErr string
	}{
		{name: "root target", path: `.`, policy: allowRootTarget, want: `.`},
		{name: "current directory component", path: `.\cache`, policy: rejectRootTarget, want: `cache`},
		{name: "parent directory component", path: `cache\..\child`, policy: rejectRootTarget, want: `child`},
		{name: "parent escape remains rejected", path: `..\cache`, policy: rejectRootTarget, wantErr: "path escapes root"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveRelativeTarget(tc.path, tc.policy)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected %q error, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveRelativeTarget(%q): %v", tc.path, err)
			}
			if got != tc.want {
				t.Fatalf("resolveRelativeTarget(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestResolveAbsolutePathRejectsRawUnsupportedWindowsPathsBeforeAbs(t *testing.T) {
	for _, tc := range windowsRawPathRejectionCases() {
		t.Run(tc.name, func(t *testing.T) {
			absCalls := 0
			withFileSystem(t, &fakeFileSystem{
				abs: func(string) (string, error) {
					absCalls++
					return `C:\normalized`, nil
				},
			})

			_, err := resolveAbsolutePath("target", tc.path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q rejection, got %v", tc.want, err)
			}
			if absCalls != 0 {
				t.Fatalf("expected rejection before Abs, got %d calls", absCalls)
			}
		})
	}
}
