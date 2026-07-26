//go:build linux

package safeio

func OpenFileNoFollowSupported() bool {
	openNoFollowSupportMu.Lock()
	if openNoFollowSupportCached {
		supported := openNoFollowSupported
		openNoFollowSupportMu.Unlock()
		return supported
	}
	openNoFollowSupportMu.Unlock()

	supported, cacheable := openNoFollowProbe()
	if cacheable {
		openNoFollowSupportMu.Lock()
		openNoFollowSupported = supported
		openNoFollowSupportCached = true
		openNoFollowSupportMu.Unlock()
	}

	return supported
}
