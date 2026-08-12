//go:build !windows

package safeio

func publishAtomicFileIfAbsent(root Root, tempRel, targetRel string) (bool, error) {
	return false, root.Link(tempRel, targetRel)
}
