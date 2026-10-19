package scripts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestHookFixtureJoinsAutomaticMaintenance(t *testing.T) {
	t.Parallel()
	repo := newHookFixture(t)
	trace := filepath.Join(t.TempDir(), "maintenance.jsonl")
	output, err := hookCommandWithEnv(repo, []string{"GIT_TRACE2_EVENT=" + trace}, "git",
		"-c", "maintenance.loose-objects.enabled=true",
		"-c", "maintenance.loose-objects.auto=-1",
		"commit", "--allow-empty", "-m", "exercise automatic maintenance")
	if err != nil {
		t.Fatalf("commit with real automatic maintenance: %v\n%s", err, output)
	}
	assertMaintenanceJoined(t, trace)
	if output, err := hookCommand(repo, "git", "fsck", "--full"); err != nil {
		t.Fatalf("maintenance damaged fixture objects: %v\n%s", err, output)
	}
	assertHookWorktreeCleaned(t, repo)
}

type maintenanceTraceEvent struct {
	Event string   `json:"event"`
	SID   string   `json:"sid"`
	Argv  []string `json:"argv"`
	Code  int      `json:"code"`
}

func assertMaintenanceJoined(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root string
	packs := make(map[string]bool)
	maintenance := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var event maintenanceTraceEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("invalid Git trace: %v", err)
		}
		if root == "" && event.Event == "start" {
			root = event.SID
		}
		trackMaintenanceProcess(t, event, "pack-objects", packs)
		trackMaintenanceProcess(t, event, "maintenance", maintenance)
		if event.Event == "exit" && event.SID == root {
			assertCompletedMaintenanceProcesses(t, "packing", packs)
			assertCompletedMaintenanceProcesses(t, "maintenance", maintenance)
			return
		}
	}
	t.Fatal("Git trace did not record root command exit")
}

func trackMaintenanceProcess(t *testing.T, event maintenanceTraceEvent, command string, processes map[string]bool) {
	t.Helper()
	if event.Event == "start" && slices.Contains(event.Argv, command) {
		processes[event.SID] = false
	}
	if _, found := processes[event.SID]; found && event.Event == "exit" {
		if event.Code != 0 {
			t.Fatalf("automatic %s failed: %d", command, event.Code)
		}
		processes[event.SID] = true
	}
}

func assertCompletedMaintenanceProcesses(t *testing.T, command string, processes map[string]bool) {
	t.Helper()
	if len(processes) == 0 {
		t.Fatalf("commit returned before actual %s completed", command)
	}
	for sid, complete := range processes {
		if !complete {
			t.Fatalf("commit returned with an unjoined %s: %s", command, sid)
		}
	}
}
