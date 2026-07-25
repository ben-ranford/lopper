//go:build darwin || windows

package safeio

func OpenFileNoFollowSupported() bool {
	return true
}
