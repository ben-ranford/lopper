//go:build linux

package safeio

func OpenFileNoFollowSupported() bool {
	openNoFollowSupportOnce.Do(func() {
		openNoFollowSupported = openNoFollowProbe()
	})
	return openNoFollowSupported
}
