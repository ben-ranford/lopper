//go:build windows

package pathutil

import (
	"path/filepath"
	"strings"
)

func equalPathOS(left, right string) bool {
	return strings.EqualFold(filepath.Clean(filepath.FromSlash(left)), filepath.Clean(filepath.FromSlash(right)))
}
