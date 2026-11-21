package scripts

import (
	"os"
	"strings"
	"testing"
)

// TestNativeCleanupReserveCoversUnfilteredSMBScans records the hosted failure
// where two unfiltered Get-SmbOpenFile scans outlive a 90s post-subject reserve.
// The owner then exits before writing the single original-control line. The
// open-file query stays unfiltered, and a late observation still has no control line.
func TestNativeCleanupReserveCoversUnfilteredSMBScans(t *testing.T) {
	owner := readReserveSource(t, "../tools/nativecleanupowner/main_windows.go")
	checker := readReserveSource(t, "../tools/nativecleanupcheck/main.go")
	helper := readReserveSource(t, "native-windows-hook-fixture.ps1")
	for _, source := range []struct {
		name string
		body string
		need string
	}{
		{"owner", owner, "const cleanupReserve = 4 * time.Minute"},
		{"owner budget", owner, "const ownerBudget = 15 * time.Minute"},
		{"checker", checker, "const originalCleanupReserve = 4 * time.Minute"},
		{"helper", helper, "AddSeconds(240)"},
		{"open files", helper, "Get-SmbOpenFile -IncludeHidden"},
	} {
		if !strings.Contains(source.body, source.need) {
			t.Errorf("%s missing %s", source.name, source.need)
		}
	}
	if strings.Contains(owner, "const cleanupReserve = 90 * time.Second") || strings.Contains(helper, "AddSeconds(90)") {
		t.Fatal("post-subject reserve still expires during the unfiltered SMB scans")
	}
}

func readReserveSource(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
