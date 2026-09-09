package app

import (
	"os"
	"strings"
	"testing"
)

func TestDistributedDotnetLockfileDiscoveryDoesNotWalkEachManifestSubtree(t *testing.T) {
	source, err := os.ReadFile("lockfile_drift_scanner.go")
	if err != nil {
		t.Fatalf("read lockfile drift scanner: %v", err)
	}
	if strings.Contains(string(source), "findDotnetProjectLockfiles(snapshot.path)") {
		t.Fatal("distributed .NET lockfile discovery must not walk every central-manifest subtree")
	}
}
