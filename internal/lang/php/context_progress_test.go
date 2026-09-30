package php

import (
	"strings"
	"testing"
)

func TestPHPContextTrackerDoesNotRescanConsumedPrefix(t *testing.T) {
	const stride = 64
	text := []byte(strings.Repeat(" ", stride*128))
	tracker := newPHPContextTracker(string(text), nil)
	for end := stride; end <= len(text); end += stride {
		tracker.advanceTo(end)
		if tracker.offset != end || len(tracker.frames) != 0 {
			t.Fatalf("context tracker rescanned consumed bytes at %d: offset=%d frames=%d", end, tracker.offset, len(tracker.frames))
		}
		// Poison only the consumed prefix. Reading it again would push brace frames,
		// exposing a rescan independently of host scheduling or race instrumentation.
		for i := end - stride; i < end; i++ {
			text[i] = '{'
		}
		tracker.text = string(text)
	}
}
