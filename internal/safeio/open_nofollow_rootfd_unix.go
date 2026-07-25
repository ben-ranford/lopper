//go:build linux || darwin

package safeio

import (
	"fmt"
	"os"
	"reflect"
	"runtime"
)

func osRootFD(root *os.Root) (int, error) {
	descriptor, err := osRootDescriptorField(root)
	if err != nil {
		return 0, err
	}
	if descriptor.Kind() != reflect.Int {
		return 0, fmt.Errorf("no-follow file open unsupported on %s: root fd unavailable", runtime.GOOS)
	}

	return int(descriptor.Int()), nil
}
