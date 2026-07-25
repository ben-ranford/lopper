//go:build linux || darwin || windows

package safeio

import (
	"fmt"
	"os"
	"reflect"
	"runtime"
)

// Go 1.26 does not expose an os.Root descriptor accessor. Keep the layout
// dependency isolated so callers can fail closed if the runtime changes.
func osRootDescriptorField(root *os.Root) (reflect.Value, error) {
	if root == nil {
		return reflect.Value{}, fmt.Errorf("no-follow file open unsupported on %s: nil root", runtime.GOOS)
	}

	rootValue := reflect.ValueOf(root)
	if rootValue.Kind() != reflect.Pointer || rootValue.IsNil() {
		return reflect.Value{}, fmt.Errorf("no-follow file open unsupported on %s: invalid root handle", runtime.GOOS)
	}

	rootState := rootValue.Elem().FieldByName("root")
	if !rootState.IsValid() || rootState.IsNil() {
		return reflect.Value{}, fmt.Errorf("no-follow file open unsupported on %s: missing root state", runtime.GOOS)
	}

	descriptor := rootState.Elem().FieldByName("fd")
	if !descriptor.IsValid() {
		return reflect.Value{}, fmt.Errorf("no-follow file open unsupported on %s: root descriptor unavailable", runtime.GOOS)
	}
	return descriptor, nil
}
