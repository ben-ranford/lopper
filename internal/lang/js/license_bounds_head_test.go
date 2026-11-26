package js

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestProbeLicenseFilesWithLimitsReportsSkippedLargeCandidateAfterLaterMatch(t *testing.T) {
	root := t.TempDir()
	large := filepath.Join(root, "COPYING")
	known := filepath.Join(root, "LICENSE")
	testutil.MustWriteFile(t, large, "MIT License\n"+strings.Repeat(" ", 32))
	testutil.MustWriteFile(t, known, "MIT License")

	probe, status := probeLicenseFilesWithLimits(root, 5, 16, 20)
	if probe == nil || probe.path != known || probe.spdx != "MIT" {
		t.Fatalf("expected later candidate license evidence, got %#v", probe)
	}
	if !status.skippedLarge || len(status.warning()) != 1 {
		t.Fatalf("expected incomplete detection warning after skipped candidate, got %#v", status)
	}
}

func TestProbeLicenseFilesWithLimitsReportsIncompleteDiscovery(t *testing.T) {
	root := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(root, "LICENSE"), "custom internal license text")
	testutil.MustWriteFile(t, filepath.Join(root, "COPYING"), "custom internal license text")

	probe, candidateLimited := probeLicenseFilesWithLimits(root, 1, 16, 256)
	if probe != nil || !candidateLimited.limited || len(candidateLimited.warning()) != 1 {
		t.Fatalf("expected candidate-limit status, got probe=%#v status=%#v", probe, candidateLimited)
	}

	probe, walkLimited := probeLicenseFilesWithLimits(root, 5, 1, 256)
	if probe != nil || !walkLimited.limited || len(walkLimited.warning()) != 1 {
		t.Fatalf("expected traversal-limit status, got probe=%#v status=%#v", probe, walkLimited)
	}
}
