package cli

import (
	"errors"
	"fmt"
	"github.com/ben-ranford/lopper/internal/app"
	"github.com/ben-ranford/lopper/internal/featureflags"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTUIRepositoryFeatureConsent(t *testing.T) {
	for _, command := range []string{"bare", "tui"} {
		for _, ref := range []string{"stave-tui-preview", "LOP-FEAT-0029"} {
			t.Run(command+"/"+ref, func(t *testing.T) {
				repo := t.TempDir()
				t.Chdir(repo)
				config := "features:\n  enable: [" + ref + "]\n"
				path := filepath.Join(repo, parseConfigFileName)
				writeFile(t, path, config)
				var args []string
				if command == "tui" {
					args = []string{"tui", "--repo", repo}
				}
				req := mustParseArgs(t, args)
				if !req.TUI.UseStavePreview || !req.TUI.Features.Enabled("stave-tui-preview") {
					t.Fatalf("repository consent: option=%t feature=%t", req.TUI.UseStavePreview, req.TUI.Features.Enabled("stave-tui-preview"))
				}
				assertFeatureConfigUnchanged(t, path, config)
			})
		}
	}
}

func TestParseTUIConfigDiscoveryAndPaths(t *testing.T) {
	for _, tt := range []struct {
		name, file, content string
		explicit            bool
	}{
		{"yml", ".lopper.yml", "features:\n  enable: [stave-tui-preview]\n", false},
		{"yaml", ".lopper.yaml", "features:\n  enable: [LOP-FEAT-0029]\n", false},
		{"json", "lopper.json", `{"features":{"enable":["LOP-FEAT-0029"]}}`, false},
		{"relative", "custom.yml", "features:\n  enable: [stave-tui-preview]\n", true},
		{"absolute", "custom.yml", "features:\n  enable: [stave-tui-preview]\n", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			path := filepath.Join(repo, tt.file)
			writeFile(t, path, tt.content)
			t.Chdir(t.TempDir())
			writeFile(t, parseConfigFileName, "features:\n  disable: [stave-tui-preview]\n")
			args := []string{"tui", "--repo", repo}
			if tt.explicit {
				ref := tt.file
				if tt.name == "absolute" {
					ref = path
				}
				args = append(args, "--config", ref)
			}
			req := mustParseArgs(t, args)
			assertTUIConfigChoice(t, req, true)
			assertFeatureConfigUnchanged(t, path, tt.content)
		})
	}
}

func TestParseTUIConfigCLIOverrides(t *testing.T) {
	for _, tt := range []struct {
		name, config string
		flags        []string
		enabled      bool
	}{
		{"config disable", "disable: [stave-tui-preview, dart-source-attribution]", nil, false},
		{"CLI rollback by code", "enable: [stave-tui-preview]\n  disable: [dart-source-attribution]", []string{"--disable-feature", "LOP-FEAT-0029"}, false},
		{"CLI rollback by name", "enable: [LOP-FEAT-0029]\n  disable: [dart-source-attribution]", []string{"--disable-feature", "stave-tui-preview"}, false},
		{"CLI enable by code", "disable: [stave-tui-preview, dart-source-attribution]", []string{"--enable-feature", "LOP-FEAT-0029"}, true},
		{"CLI enable by name", "disable: [LOP-FEAT-0029, dart-source-attribution]", []string{"--enable-feature", "stave-tui-preview"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			path := filepath.Join(repo, parseConfigFileName)
			config := "features:\n  " + tt.config + "\n"
			writeFile(t, path, config)
			req := mustParseArgs(t, append([]string{"tui", "--repo", repo}, tt.flags...))
			assertTUIConfigChoice(t, req, tt.enabled)
			if req.TUI.Features.Enabled("dart-source-attribution") {
				t.Fatal("unrelated config disable lost")
			}
			assertFeatureConfigUnchanged(t, path, config)
		})
	}
}

func TestParseTUIConfigErrors(t *testing.T) {
	for _, tt := range []struct {
		name, content, want string
		flags               []string
	}{
		{"missing explicit", "", "config file not found", []string{"--config", "missing.yml"}},
		{"malformed discovered", "features: [", "invalid YAML config", nil},
		{"unknown feature", "features:\n  enable: [missing]\n", "unknown feature: missing", nil},
		{"config conflict", "features:\n  enable: [stave-tui-preview]\n  disable: [LOP-FEAT-0029]\n", "both enabled and disabled", []string{"--enable-feature", "stave-tui-preview"}},
		{"CLI conflict", "features:\n  enable: [stave-tui-preview]\n", "both enabled and disabled", []string{"--enable-feature", "stave-tui-preview", "--disable-feature", "LOP-FEAT-0029"}},
		{"untrusted pack", "policy:\n  packs: [https://example.com/policy.yml]\n", "remote policy", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			if tt.content != "" {
				writeFile(t, filepath.Join(repo, parseConfigFileName), tt.content)
			}
			_, err := ParseArgs(append([]string{"tui", "--repo", repo}, tt.flags...))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want %q", err, tt.want)
			}
		})
	}
}

func TestParseTUIBuildDefaultsDoNotImplyConsent(t *testing.T) {
	for _, channel := range []featureflags.Channel{featureflags.ChannelDev, featureflags.ChannelRolling, featureflags.ChannelRelease} {
		t.Run(string(channel), func(t *testing.T) {
			registry := featureflags.DefaultRegistry()
			withFeatureRegistry(t, channel, &featureflags.ReleaseLock{Release: "v1.8.9", DefaultOn: []string{"LOP-FEAT-0029"}})
			featureRegistryProvider = func() *featureflags.Registry { return registry }
			t.Chdir(t.TempDir())
			assertTUIConfigChoice(t, mustParseArgs(t, nil), false)
			assertTUIConfigChoice(t, mustParseArgs(t, []string{"tui"}), false)
		})
	}
}

func TestParseTUIConfigPackConsentAndClearing(t *testing.T) {
	for _, clear := range []bool{false, true} {
		t.Run(fmt.Sprint(clear), func(t *testing.T) {
			repo := t.TempDir()
			writeFile(t, filepath.Join(repo, "policy.yml"), "features:\n  enable: [stave-tui-preview]\n")
			config := "policy:\n  packs: [policy.yml]\n"
			if clear {
				config += "features:\n  enable: []\n"
			}
			writeFile(t, filepath.Join(repo, parseConfigFileName), config)
			req := mustParseArgs(t, []string{"tui", "--repo", repo})
			assertTUIConfigChoice(t, req, !clear)
		})
	}
}

func TestParseTUIFeaturesBuildContextError(t *testing.T) {
	oldValidate := validateFeatureRegistry
	validateFeatureRegistry = func() error { return errors.New("registry invalid") }
	t.Cleanup(func() { validateFeatureRegistry = oldValidate })
	_, err := ParseArgs([]string{"tui", "--repo", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "registry invalid") {
		t.Fatalf("build context error = %v", err)
	}
}

func assertTUIConfigChoice(t *testing.T, req app.Request, want bool) {
	t.Helper()
	if req.TUI.UseStavePreview != want || req.TUI.Features.Enabled("stave-tui-preview") != want {
		t.Fatalf("Stave option=%t feature=%t, want %t", req.TUI.UseStavePreview, req.TUI.Features.Enabled("stave-tui-preview"), want)
	}
}
