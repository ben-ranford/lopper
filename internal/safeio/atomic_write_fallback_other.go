//go:build !windows

package safeio

func fallbackAtomicReplacement(_ Root, fallback atomicReplacementFallback) error {
	return fallback.renameErr
}
