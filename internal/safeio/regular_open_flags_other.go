//go:build !unix && !windows

package safeio

import "errors"

func regularReadOpenFlags() (int, error) {
	return 0, errors.ErrUnsupported
}
