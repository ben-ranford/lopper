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

func TestParseUIPreferenceArgumentOrder(t *testing.T) {
	repo := t.TempDir()
	for _, tc := range []struct {
		name, choice string
		args         []string
	}{
		{"first", "stave", []string{"--ui-preference", "stave", "--repo", repo, "--language", "js-ts"}},
		{"middle", "legacy", []string{"--repo", repo, "--ui-preference", "legacy", "--language", "js-ts"}},
		{"last", "ask", []string{"--repo", repo, "--language", "js-ts", "--ui-preference", "ask"}},
		{"equals", "stave", []string{"--ui-preference=stave", "--repo", repo, "--language", "js-ts"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := mustParseArgs(t, append([]string{"tui"}, tc.args...))
			if req.TUI.UIPreference != tc.choice || req.RepoPath != repo || req.TUI.Language != "js-ts" {
				t.Fatalf("preference=%q repo=%q language=%q", req.TUI.UIPreference, req.RepoPath, req.TUI.Language)
			}
		})
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
