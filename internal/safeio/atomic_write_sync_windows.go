//go:build windows

package safeio

// Windows directory handles generally do not provide a reliable fsync-style
// durability primitive for rename-visible metadata through this interface.
// Keep file Sync calls in place and treat directory Sync as a no-op here.
func syncRootDirectory(Root) error {
	return nil
}
