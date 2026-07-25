//go:build !(linux || darwin || windows)

package safeio

func OpenFileNoFollowSupported() bool {
	return false
}
