package safeio

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteRootRepeatedCheckPreservesPublicationBoundaries(t *testing.T) {
	methods := []struct {
		name  string
		write func(*WriteRoot, string, []byte, os.FileMode, os.FileMode, func() error, func() error) error
	}{
		{"before mutation", (*WriteRoot).WriteFileCreatingParentsAfterParentReadyWithPreWriteCheck},
		{"before publish", (*WriteRoot).WriteFileCreatingParentsAfterParentReadyWithPublishCheck},
	}
	for _, method := range methods {
		for _, failOnCall := range []int{0, 1, 2} {
			t.Run(method.name+"/"+[]string{"nil check", "first check fails", "second check fails"}[failOnCall], func(t *testing.T) {
				rootDir := t.TempDir()
				root := openTestWriteRoot(t, rootDir, OpenWriteRoot)
				target := filepath.Join("nested", "result.txt")
				checkErr := errors.New("validation failed")
				calls := 0
				var check func() error
				if failOnCall != 0 {
					check = func() error {
						calls++
						if calls == failOnCall {
							return checkErr
						}
						return nil
					}
				}
				err := method.write(root, target, []byte("content"), 0o600, 0o700, nil, check)
				if (failOnCall == 0 && err != nil) || (failOnCall != 0 && !errors.Is(err, checkErr)) {
					t.Fatalf("write returned %v for failure on call %d", err, failOnCall)
				}
				if calls != failOnCall {
					t.Fatalf("check ran %d times, want %d", calls, failOnCall)
				}
				if failOnCall == 1 {
					if _, err := os.Stat(filepath.Join(rootDir, target)); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("target was published despite first check failure: %v", err)
					}
					return
				}
				assertFileContent(t, filepath.Join(rootDir, target), "content")
			})
		}
	}
}
