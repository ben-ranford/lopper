package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/featureflags"
)

func TestParseFeatureCLIOverridesConfiguration(t *testing.T) {
	for _, command := range []string{"analyse", "pr-review"} {
		t.Run(command, func(t *testing.T) { checkParseFeatureCLIOverridesConfiguration(t, command) })
	}
}

func checkParseFeatureCLIOverridesConfiguration(t *testing.T, command string) {
	t.Helper()
	withFeatureLayerRegistry(t, featureflags.ChannelDev, nil)
	for _, tt := range []struct {
		name, config, flag, ref string
		want                    bool
	}{
		{"disable name", "enable: [preview-flag]", "--disable-feature", "preview-flag", false},
		{"enable name", "disable: [preview-flag]", "--enable-feature", "preview-flag", true},
		{"disable code", "enable: [preview-flag]", "--disable-feature", "LOP-FEAT-0001", false},
		{"enable code", "disable: [preview-flag]", "--enable-feature", "LOP-FEAT-0001", true},
		{"disable alias", "enable: [LOP-FEAT-0001]", "--disable-feature", "old-preview-flag", false},
		{"enable alias", "disable: [LOP-FEAT-0001]", "--enable-feature", "old-preview-flag", true},
		{"disable config alias", "enable: [old-preview-flag]", "--disable-feature", "preview-flag", false},
		{"enable config alias", "disable: [old-preview-flag]", "--enable-feature", "preview-flag", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			features := parseFeatureLayerConfig(t, command, "features:\n  "+tt.config+"\n", tt.flag, tt.ref)
			if got := features.Enabled("LOP-FEAT-0001"); got != tt.want {
				t.Fatalf("override enabled=%t, want %t", got, tt.want)
			}
			if strings.Contains(tt.config+tt.ref, "old-preview-flag") && len(features.DeprecationWarnings()) != 1 {
				t.Fatalf("alias warning lost: %v", features.DeprecationWarnings())
			}
		})
	}
}

func withFeatureLayerRegistry(t *testing.T, channel featureflags.Channel, lock *featureflags.ReleaseLock) {
	t.Helper()
	withFeatureRegistry(t, channel, lock)
	registry, err := featureflags.NewRegistry([]featureflags.Flag{
		{Code: "LOP-FEAT-0001", Name: "preview-flag", DeprecatedNames: []string{"old-preview-flag"}, Lifecycle: featureflags.LifecyclePreview},
		{Code: "LOP-FEAT-0002", Name: "stable-flag", Lifecycle: featureflags.LifecycleStable},
		{Code: "LOP-FEAT-0003", Name: "retained-on", Lifecycle: featureflags.LifecyclePreview},
		{Code: "LOP-FEAT-0004", Name: "retained-off", Lifecycle: featureflags.LifecycleStable},
		{Code: "LOP-FEAT-0005", Name: "locked-preview", Lifecycle: featureflags.LifecyclePreview},
	})
	if err != nil {
		t.Fatal(err)
	}
	featureRegistryProvider = func() *featureflags.Registry { return registry }
}

func featureLayerArgs(command, repo string, flags ...string) []string {
	args := []string{command, "--repo", repo}
	if command == "pr-review" {
		args = append(args, "--base", "base", "--head", "head")
	} else {
		args = append(args, "lodash")
	}
	return append(args, flags...)
}

func parseFeatureLayerConfig(t *testing.T, command, config string, flags ...string) featureflags.Set {
	t.Helper()
	repo := t.TempDir()
	path := filepath.Join(repo, parseConfigFileName)
	writeFile(t, path, config)
	req := mustParseArgs(t, featureLayerArgs(command, repo, flags...))
	assertFeatureConfigUnchanged(t, path, config)
	if command == "pr-review" {
		return req.PRReview.Features
	}
	return req.Analyse.Features
}

func TestParseFeatureLayersPreserveDefaultsAndOtherChoices(t *testing.T) {
	for _, command := range []string{"analyse", "pr-review"} {
		for _, channel := range []featureflags.Channel{featureflags.ChannelDev, featureflags.ChannelRolling, featureflags.ChannelRelease} {
			t.Run(command+"/"+string(channel), func(t *testing.T) {
				lock := &featureflags.ReleaseLock{Release: "v1.4.2", DefaultOn: []string{"locked-preview", "preview-flag"}}
				withFeatureLayerRegistry(t, channel, lock)
				features := parseFeatureLayerConfig(t, command, "features:\n  enable: [preview-flag, retained-on]\n  disable: [retained-off]\n", "--disable-feature", "preview-flag")
				assertFeatureLayerChoices(t, features, map[string]bool{"preview-flag": false, "retained-on": true, "retained-off": false, "stable-flag": true, "locked-preview": channel != featureflags.ChannelDev})
			})
		}
	}
}

func TestParseFeatureLayersRejectInvalidSources(t *testing.T) {
	for _, command := range []string{"analyse", "pr-review"} {
		t.Run(command, func(t *testing.T) { checkParseFeatureLayersRejectInvalidSources(t, command) })
	}
}

func checkParseFeatureLayersRejectInvalidSources(t *testing.T, command string) {
	t.Helper()
	withFeatureLayerRegistry(t, featureflags.ChannelDev, nil)
	for _, tt := range []struct {
		name, config, want string
		flags              []string
	}{
		{"config conflict", "enable: [old-preview-flag]\n  disable: [LOP-FEAT-0001]", "both enabled and disabled", []string{"--enable-feature", "preview-flag"}},
		{"CLI conflict", "enable: [preview-flag]", "both enabled and disabled", []string{"--enable-feature", "old-preview-flag", "--disable-feature", "LOP-FEAT-0001"}},
		{"unknown lower enable", "enable: [missing]", "unknown feature: missing", []string{"--disable-feature", "missing"}},
		{"unknown lower disable", "disable: [missing]", "unknown feature: missing", []string{"--enable-feature", "missing"}},
		{"unknown CLI enable", "disable: [preview-flag]", "unknown feature: missing", []string{"--enable-feature", "missing"}},
		{"unknown CLI disable", "enable: [preview-flag]", "unknown feature: missing", []string{"--disable-feature", "missing"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			writeFile(t, filepath.Join(repo, parseConfigFileName), "features:\n  "+tt.config+"\n")
			_, err := ParseArgs(featureLayerArgs(command, repo, tt.flags...))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got error %v, want %q", err, tt.want)
			}
		})
	}
}

func TestParseFeatureLayersPreservePolicyListSemantics(t *testing.T) {
	for _, command := range []string{"analyse", "pr-review"} {
		for _, tt := range []struct {
			name, local string
			retained    bool
		}{
			{"later pack replaces list", "", true},
			{"local empty list clears packs", "features:\n  enable: []\n", false},
		} {
			t.Run(command+"/"+tt.name, func(t *testing.T) {
				withFeatureLayerRegistry(t, featureflags.ChannelDev, nil)
				repo := t.TempDir()
				writeFile(t, filepath.Join(repo, "first.yml"), "features:\n  enable: [preview-flag]\n  disable: [retained-off]\n")
				writeFile(t, filepath.Join(repo, "second.yml"), "features:\n  enable: [retained-on]\n")
				config := "policy:\n  packs: [first.yml, second.yml]\n" + tt.local
				path := filepath.Join(repo, parseConfigFileName)
				writeFile(t, path, config)
				req := mustParseArgs(t, featureLayerArgs(command, repo, "--enable-feature", "retained-off"))
				features := req.Analyse.Features
				if command == "pr-review" {
					features = req.PRReview.Features
				}
				assertFeatureLayerChoices(t, features, map[string]bool{"preview-flag": false, "retained-on": tt.retained, "retained-off": true})
				assertFeatureConfigUnchanged(t, path, config)
			})
		}
	}
}

func assertFeatureLayerChoices(t *testing.T, features featureflags.Set, choices map[string]bool) {
	t.Helper()
	for ref, want := range choices {
		if got := features.Enabled(ref); got != want {
			t.Errorf("%s enabled=%t, want %t", ref, got, want)
		}
	}
}

func assertFeatureConfigUnchanged(t *testing.T, path, config string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != config {
		t.Fatalf("CLI override rewrote config: %q", contents)
	}
}
