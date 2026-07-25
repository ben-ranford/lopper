//go:build !linux

package safeio

func OpenFileNoFollowSupported() bool {
	return false
}
