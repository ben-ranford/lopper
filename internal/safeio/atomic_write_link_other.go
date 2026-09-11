//go:build !windows

package safeio

func platformIdentityBoundLinkUnsupported(error) bool {
	return false
}
