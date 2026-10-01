//go:build unix && !darwin

package runtime

func runtimeProcessGroupExited(_ int, _ error) bool {
	return false
}
