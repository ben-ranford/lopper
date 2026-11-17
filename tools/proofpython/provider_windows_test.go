//go:build windows

package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestNativeProviderEnvironmentCannotChooseCompilerOrAliases(t *testing.T) {
	goPath := `D:\hostedtoolcache\windows\go\1.27.2\x64\bin\go.exe`
	env := pinnedGoEnvironment([]string{"PATH=C:\\workspace", "GOROOT=C:\\workspace", "GOFLAGS=-toolexec=untrusted", "BASH_ENV=C:\\workspace\\hook", "PYTHONHOME=C:\\workspace", "SERIES=untrusted", "TEMP=C:\\Temp"}, goPath)
	joined := strings.Join(env, "\n")
	for _, bad := range []string{"workspace", "toolexec", "BASH_ENV", "PYTHONHOME", "SERIES="} {
		if strings.Contains(joined, bad) {
			t.Fatalf("untrusted environment crossed boundary: %s", bad)
		}
	}
	for _, want := range []string{"GOTOOLCHAIN=local", "GOENV=off", "GOWORK=off", "GOFLAGS=\n", "GOCACHEPROG=\n", "GOAUTH=off", "REGRESSION_PROOF_GO_ROOT="} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing pinned selection %s", want)
		}
	}
}

func TestWindowsJoinedProofRejectsMalformedAnchorBeforeLaunch(t *testing.T) {
	err := joinedProof(context.Background(), []string{`C:\missing-receipt`, "bad", `C:\untrusted\go.exe`, "bad", `C:\repo`, "test", "./internal/githubaction"})
	if err == nil {
		t.Fatal("malformed trust input launched proof")
	}
	cmd := exec.CommandContext(context.Background(), `C:\missing-executable`)
	commandErr, joinErr := runJoined(cmd)
	if commandErr == nil || joinErr == nil {
		t.Fatalf("unstarted command certified: %v / %v", commandErr, joinErr)
	}
}
