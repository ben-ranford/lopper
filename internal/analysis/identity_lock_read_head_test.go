package analysis

import (
	"path/filepath"
	"testing"
)

func TestIdentityLockReadersWarnForMissingFiles(t *testing.T) {
	repo := t.TempDir()
	for _, tc := range []struct {
		name    string
		collect func(string, string, identityIndex, *identityWarningCollector) int
	}{
		{name: packageLockFileName, collect: collectPackageLockIdentityEvidenceAtPath},
		{name: pnpmLockFileName, collect: collectPNPMLockIdentityEvidenceAtPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			warnings := newIdentityWarningCollector(repo)
			if got := tc.collect(repo, filepath.Join(repo, tc.name), identityIndex{}, warnings); got != 0 {
				t.Fatalf("expected no evidence, got %d", got)
			}
			assertWarningsExact(t, repo, warnings.list(), []string{"identity manifest read failed for " + tc.name + ": not found"})
		})
	}
}
