package safeio

import (
	"errors"
	"fmt"
)

// ErrOpenFileNoFollowUnsupported reports that the current environment cannot
// provide the guarantees required by OpenFileNoFollow.
var ErrOpenFileNoFollowUnsupported = errors.New("no-follow file open unsupported")

func openFileNoFollowUnsupportedError(platform string) error {
	return fmt.Errorf("%w on %s: cannot prove a pinned readable regular-file handle", ErrOpenFileNoFollowUnsupported, platform)
}

// ErrNoFollowFinalComponent reports that a no-follow open rejected the final
// path component because it was not the exact regular file that was requested.
var ErrNoFollowFinalComponent = errors.New("final component is not a regular file")

// ErrRootContainsSymlink reports that a rooted path traversal encountered a
// symlink while opening a confined root.
var ErrRootContainsSymlink = errors.New("root contains symlink")

// ErrPathContainsSymlink reports that a read path was rejected because one of
// its parent components resolved through a symlink.
var ErrPathContainsSymlink = errors.New("path contains symlink")

type RootContainsSymlinkError struct {
	Path string
}

func (e *RootContainsSymlinkError) Error() string {
	return ErrRootContainsSymlink.Error() + ": " + e.Path
}

func (e *RootContainsSymlinkError) Unwrap() error {
	return ErrRootContainsSymlink
}

type PathContainsSymlinkError struct {
	Path string
	Err  error
}

func (e *PathContainsSymlinkError) Error() string {
	return ErrPathContainsSymlink.Error() + ": " + e.Path
}

func (e *PathContainsSymlinkError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrPathContainsSymlink}
	}
	return []error{ErrPathContainsSymlink, e.Err}
}
