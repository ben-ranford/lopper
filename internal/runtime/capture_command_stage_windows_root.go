//go:build windows

package runtime

func platformRuntimeExecutableStageRoot(string) (string, error) {
	return "", nil
}
