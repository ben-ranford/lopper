//go:build darwin

package safeio

import "testing"

func TestOpenFileNoFollowFailsClosedOnDarwin(t *testing.T) {
	assertOpenFileNoFollowFailsClosed(t, "darwin")
}
