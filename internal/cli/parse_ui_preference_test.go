package cli

import (
	"path/filepath"
	"testing"
)

func TestParseUIPreference(t *testing.T) {
	for _, choice := range []string{"stave", "legacy", "ask"} {
		req := mustParseArgs(t, []string{"tui", "--repo", t.TempDir(), "--ui-preference=" + choice})
		if req.TUI.UIPreference != choice {
			t.Fatalf("preference=%q", req.TUI.UIPreference)
		}
	}
	for _, args := range [][]string{
		{"--ui-preference="}, {"--ui-preference=wrong"},
		{"--ui-preference=stave", "--snapshot=-"},
		{"--ui-preference=stave", "-o", "report.txt"},
		{"--ui-preference=stave", "--enable-feature=stave-tui-preview"},
		{"--ui-preference=ask", "--disable-feature=LOP-FEAT-0029"},
	} {
		if _, err := ParseArgs(append([]string{"tui", "--repo", t.TempDir()}, args...)); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestParseUIPreferenceExplicitSources(t *testing.T) {
	for _, source := range []string{"config", "cli"} {
		for _, decision := range []string{"enable", "disable"} {
			repo := t.TempDir()
			args := []string{"tui", "--repo", repo}
			if source == "config" {
				writeFile(t, filepath.Join(repo, parseConfigFileName), "features:\n  "+decision+": [LOP-FEAT-0029]\n")
				args = append(args, "--ui-preference=ask")
			} else {
				args = append(args, "--"+decision+"-feature=stave-tui-preview")
			}
			req := mustParseArgs(t, args)
			if !req.TUI.StaveExplicit {
				t.Fatalf("lost explicit source %s %s", source, decision)
			}
		}
	}
	req := mustParseArgs(t, []string{"tui", "--repo", t.TempDir(), "--enable-feature=dart-source-attribution"})
	if req.TUI.StaveExplicit {
		t.Fatal("unrelated feature suppressed invitation")
	}
}
