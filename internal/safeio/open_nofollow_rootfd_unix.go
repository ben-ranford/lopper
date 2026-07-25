//go:build linux

package safeio

import (
	"fmt"
	"os"
	"reflect"
	"runtime"
)

func osRootFD(root *os.Root) (int, error) {
	if root == nil {
		return 0, fmt.Errorf("no-follow file open unsupported on %s: nil root", runtime.GOOS)
	}

	rootValue := reflect.ValueOf(root)
	if rootValue.Kind() != reflect.Pointer || rootValue.IsNil() {
		return 0, fmt.Errorf("no-follow file open unsupported on %s: invalid root handle", runtime.GOOS)
	}

	rootState := rootValue.Elem().FieldByName("root")
	if !rootState.IsValid() || rootState.IsNil() {
		return 0, fmt.Errorf("no-follow file open unsupported on %s: missing root state", runtime.GOOS)
	}

	fdField := rootState.Elem().FieldByName("fd")
	if !fdField.IsValid() || fdField.Kind() != reflect.Int {
		return 0, fmt.Errorf("no-follow file open unsupported on %s: root fd unavailable", runtime.GOOS)
	}

	return int(fdField.Int()), nil
}
