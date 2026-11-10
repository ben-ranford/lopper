//go:build windows

package safeio

import "os"

func regularReadOpenFlags() (int, error) {
	// os.Root confines this relative leaf away from Windows device namespaces.
	return os.O_RDONLY, nil
}
