//go:build windows

package pathutil

import "strings"

func equalPathOS(left, right string) bool {
	return strings.EqualFold(left, right)
}
