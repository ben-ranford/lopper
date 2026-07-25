//go:build linux

package safeio

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sync"
)

var (
	openFileNoFollowSupportedOnce sync.Once
	openFileNoFollowSupported     bool

	mkdirTempNoFollowSupport  = os.MkdirTemp
	writeFileNoFollowSupport  = os.WriteFile
	removeAllNoFollowSupport  = os.RemoveAll
	openRootNoFollowSupport   = os.OpenRoot
	openRootFileNoFollowProbe = openRootFileNoFollow
	closeRootNoFollowSupport  = func(root *os.Root) error { return root.Close() }
	closeFileNoFollowSupport  = func(file *os.File) error { return file.Close() }
	readAllNoFollowSupport    = io.ReadAll
)

func OpenFileNoFollowSupported() bool {
	openFileNoFollowSupportedOnce.Do(func() {
		openFileNoFollowSupported = linuxOpenFileNoFollowSupported()
	})
	return openFileNoFollowSupported
}

func linuxOpenFileNoFollowSupported() (supported bool) {
	tempDir, err := mkdirTempNoFollowSupport("", "lopper-safeio-nofollow-*")
	if err != nil {
		return false
	}
	defer func() {
		if removeErr := removeAllNoFollowSupport(tempDir); removeErr != nil {
			supported = false
		}
	}()

	const probeName = "probe.ndjson"
	const probeContent = "probe\n"

	probePath := filepath.Join(tempDir, probeName)
	if err := writeFileNoFollowSupport(probePath, []byte(probeContent), 0o600); err != nil {
		return false
	}

	root, err := openRootNoFollowSupport(tempDir)
	if err != nil {
		return false
	}
	defer func() {
		if closeErr := closeRootNoFollowSupport(root); closeErr != nil {
			supported = false
		}
	}()

	file, err := openRootFileNoFollowProbe(root, probeName)
	if err != nil {
		return false
	}
	defer func() {
		if closeErr := closeFileNoFollowSupport(file); closeErr != nil {
			supported = false
		}
	}()

	data, err := readAllNoFollowSupport(file)
	if err != nil {
		return false
	}

	return bytes.Equal(data, []byte(probeContent))
}
