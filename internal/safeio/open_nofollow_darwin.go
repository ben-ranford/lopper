//go:build darwin

package safeio

import "os"

func openRootFileNoFollow(root *os.Root, name string) (*os.File, error) {
	file, err := openFileNoFollowByVerification(&osRoot{root: root}, name)
	if err != nil {
		return nil, err
	}
	return requireOpenedNoFollowOSFile(file, "darwin")
}
