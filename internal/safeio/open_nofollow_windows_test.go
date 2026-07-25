package safeio

import "testing"

func TestOpenFileNoFollowFailsClosedOnWindows(t *testing.T) {
	assertOpenFileNoFollowFailsClosed(t, "windows")
}
