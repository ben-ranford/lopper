//go:build !windows

package pathutil

func equalPathOS(left, right string) bool {
	return left == right
}
