package pathutil

import (
	"path/filepath"
	"strings"
)

var pathutilRel = filepath.Rel

func WithinRoot(root, candidate string) bool {
	if root == "" {
		return true
	}
	rel, err := pathutilRel(root, candidate)
	if err != nil {
		return false
	}
	return RelativeContained(rel)
}

func Equal(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	return equalPathOS(left, right)
}

func RelativeContained(rel string) bool {
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	return true
}
