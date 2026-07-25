//go:build darwin || windows

package safeio

import (
	"io"
	"testing"
)

func TestOpenFileNoFollowRejectsVerificationSurfaceMutations(t *testing.T) {
	for _, tc := range openNoFollowMutationCases() {
		t.Run(tc.name, func(t *testing.T) {
			assertOpenFileNoFollowMutationRejected(t, "verified open surface", tc, func(t *testing.T, tracePath string) (io.Closer, error) {
				t.Helper()
				return OpenFileNoFollow(tracePath)
			})
		})
	}
}
