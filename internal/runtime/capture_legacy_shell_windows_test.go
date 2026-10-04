//go:build windows

package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// This diagnostic restores the incident's shell fixture, not its old runtime or
// runner image. A passing bounded sample cannot establish the historical cause.
func TestWindowsLegacyShellReadinessDiagnostic(t *testing.T) {
	if os.Getenv("LOPPER_WINDOWS_LEGACY_SHELL_DIAGNOSTIC") != "1" {
		t.Skip("opt-in native Windows startup diagnosis")
	}
	for attempt := 1; attempt <= 10; attempt++ {
		if !t.Run(fmt.Sprintf("attempt-%02d", attempt), runWindowsLegacyShellDiagnostic) {
			break
		}
	}
}

func runWindowsLegacyShellDiagnostic(t *testing.T) {
	t.Helper()
	markerPath := filepath.Join(t.TempDir(), "child.pid")
	scriptPath := filepath.Join(filepath.Dir(markerPath), "spawn-child.ps1")
	// Keep the original operations, arguments and error policy. The fixed phase
	// records identify returned calls; they do not assert that those calls worked.
	const script = `param([string]$marker)
[Console]::Error.WriteLine('legacy-shell phase=script-entry')
$child = Start-Process -FilePath 'cmd.exe' -ArgumentList '/c ping -n 30 127.0.0.1 >NUL' -PassThru
[Console]::Error.WriteLine('legacy-shell phase=start-process-returned pid=' + $child.Id)
[System.IO.File]::WriteAllText($marker, $child.Id.ToString())
[Console]::Error.WriteLine('legacy-shell phase=marker-write-returned')
Wait-Process -Id $child.Id
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatalf("write legacy child process script: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath, markerPath)
	t.Logf("historical shell fixture under current runtime: executable=%q args=%q readiness_deadline=%s", cmd.Path, cmd.Args[1:], windowsChildMarkerStartupTimeout)
	assertWindowsDescendantCancellation(t, ctx, cancel, cmd, markerPath, true)
}
