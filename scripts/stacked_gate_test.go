package scripts

import (
	"os/exec"
	"testing"
)

func TestProtectedCIAllowsOnlySkippedMaintenance(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node is required for the real protected CI auditor")
	}
	command := exec.Command(node, "--test", "testdata/queue_waiting/maintenance.cjs")
	command.Dir = repoPath(t, "scripts")
	if output, runErr := command.CombinedOutput(); runErr != nil {
		t.Fatalf("protected CI maintenance admission: %v\n%s", runErr, output)
	}
}

func TestSonarMaintenanceOfflineContracts(t *testing.T) {
	command := exec.Command("python3", "-B", "-m", "unittest", "sonar_maintenance_test", "sonar_maintenance_source_test")
	command.Dir = repoPath(t, "scripts")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("offline Sonar maintenance contracts: %v\n%s", err, output)
	}
}
