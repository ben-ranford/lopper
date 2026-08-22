//go:build !darwin && !linux

package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestPersistProfileConfigKeepsOpenPermissionErrorWhenSearchOnlyFallbackUnsupported(t *testing.T) {
	originalOpen := openCommandOutputWriteRootFn
	openCommandOutputWriteRootFn = func(string) (*safeio.WriteRoot, error) {
		return nil, os.ErrPermission
	}
	t.Cleanup(func() {
		openCommandOutputWriteRootFn = originalOpen
	})

	for _, force := range []bool{false, true} {
		t.Run(profileForceName(force), func(t *testing.T) {
			_, err := persistProfileConfig("thresholds: {}\n", filepath.Join(t.TempDir(), "profile.yaml"), force)
			if !errors.Is(err, os.ErrPermission) {
				t.Fatalf("expected original permission error, got %v", err)
			}
			if errors.Is(err, safeio.ErrSearchOnlyWriteRootUnsupported) {
				t.Fatalf("expected unsupported fallback error to remain hidden, got %v", err)
			}
		})
	}
}

func TestPersistProfileConfigKeepsWritePermissionErrorWhenSearchOnlyFallbackUnsupported(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "profile.yaml")
	err := persistProfileConfigThroughDestination(outputPath, []byte("thresholds: {}\n"), func(commandOutputDestination, []byte) error {
		return os.ErrPermission
	}, (*safeio.WriteRoot).WriteFileAtomicallyIfAbsentUnderPinnedRoot)
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected original permission error, got %v", err)
	}
	if errors.Is(err, safeio.ErrSearchOnlyWriteRootUnsupported) {
		t.Fatalf("expected unsupported fallback error to remain hidden, got %v", err)
	}
}
