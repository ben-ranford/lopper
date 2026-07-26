//go:build !windows

package safeio

func fallbackAtomicReplacement(request atomicReplacementFallbackRequest) error {
	return request.renameErr
}
