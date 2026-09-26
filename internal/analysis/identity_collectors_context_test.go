package analysis

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// Missing files make unexpected reads observable as path-specific warnings.
func TestIdentityCollectorsStopReadingAfterCancellation(t *testing.T) {
	cases := []struct {
		name, filename string
		collect        func(context.Context, string, identityIndex, []string, *identityWarningCollector)
	}{
		{"go-mod", "go.mod", func(ctx context.Context, repo string, index identityIndex, paths []string, warnings *identityWarningCollector) {
			collectGoIdentityEvidenceFromSnapshot(ctx, repo, index, identityManifestSnapshot{goModFiles: paths}, warnings)
		}},
		{"go-work", "go.work", func(ctx context.Context, repo string, index identityIndex, paths []string, warnings *identityWarningCollector) {
			collectGoIdentityEvidenceFromSnapshot(ctx, repo, index, identityManifestSnapshot{goWorkFiles: paths}, warnings)
		}},
		{"maven", "pom.xml", func(ctx context.Context, repo string, index identityIndex, paths []string, warnings *identityWarningCollector) {
			collectJVMIdentityEvidenceFromSnapshot(ctx, repo, index, identityManifestSnapshot{pomFiles: paths}, warnings)
		}},
		{"gradle-build", "build.gradle", func(ctx context.Context, repo string, index identityIndex, paths []string, warnings *identityWarningCollector) {
			collectJVMIdentityEvidenceFromSnapshot(ctx, repo, index, identityManifestSnapshot{gradleBuildFiles: paths}, warnings)
		}},
		{"gradle-lock", "gradle.lockfile", func(ctx context.Context, repo string, index identityIndex, paths []string, warnings *identityWarningCollector) {
			collectJVMIdentityEvidenceFromSnapshot(ctx, repo, index, identityManifestSnapshot{gradleLockFiles: paths}, warnings)
		}},
		{"cargo-manifest", "Cargo.toml", func(ctx context.Context, repo string, index identityIndex, paths []string, warnings *identityWarningCollector) {
			collectCargoIdentityEvidenceFromSnapshot(ctx, repo, index, identityManifestSnapshot{cargoManifestFiles: paths}, warnings)
		}},
		{"cargo-lock", "Cargo.lock", func(ctx context.Context, repo string, index identityIndex, paths []string, warnings *identityWarningCollector) {
			collectCargoIdentityEvidenceFromSnapshot(ctx, repo, index, identityManifestSnapshot{cargoLockFiles: paths}, warnings)
		}},
		{"nuget-project", "project.csproj", func(ctx context.Context, repo string, index identityIndex, paths []string, warnings *identityWarningCollector) {
			collectDotNetIdentityEvidenceFromSnapshot(ctx, repo, index, identityManifestSnapshot{dotnetProjectFiles: paths}, warnings)
		}},
		{"nuget-central", "Directory.Packages.props", func(ctx context.Context, repo string, index identityIndex, paths []string, warnings *identityWarningCollector) {
			collectDotNetIdentityEvidenceFromSnapshot(ctx, repo, index, identityManifestSnapshot{dotnetCentralFiles: paths}, warnings)
		}},
		{"nuget-lock", "packages.lock.json", func(ctx context.Context, repo string, index identityIndex, paths []string, warnings *identityWarningCollector) {
			collectDotNetIdentityEvidenceFromSnapshot(ctx, repo, index, identityManifestSnapshot{dotnetLockFiles: paths}, warnings)
		}},
		{"python", "requirements.txt", func(ctx context.Context, repo string, index identityIndex, paths []string, warnings *identityWarningCollector) {
			collectPythonIdentityEvidenceFromPaths(ctx, repo, index, paths, warnings)
		}},
		{"composer", "composer.json", func(ctx context.Context, repo string, index identityIndex, paths []string, warnings *identityWarningCollector) {
			collectComposerIdentityEvidenceFromPaths(ctx, repo, index, paths, warnings)
		}},
		{"pub", "pubspec.yaml", func(ctx context.Context, repo string, index identityIndex, paths []string, warnings *identityWarningCollector) {
			collectPubIdentityEvidenceFromPaths(ctx, repo, index, paths, warnings)
		}},
		{"ruby", "Gemfile.lock", func(ctx context.Context, repo string, index identityIndex, paths []string, warnings *identityWarningCollector) {
			collectRubyIdentityEvidenceFromPaths(ctx, repo, index, paths, warnings)
		}},
		{"elixir", "mix.lock", func(ctx context.Context, repo string, index identityIndex, paths []string, warnings *identityWarningCollector) {
			collectElixirIdentityEvidenceFromPaths(ctx, repo, index, paths, warnings)
		}},
		{"Package.resolved", "Package.resolved", func(ctx context.Context, repo string, index identityIndex, paths []string, warnings *identityWarningCollector) {
			collectSwiftIdentityEvidenceFromPaths(ctx, repo, index, paths, nil, nil, warnings)
		}},
		{"Podfile.lock", "Podfile.lock", func(ctx context.Context, repo string, index identityIndex, paths []string, warnings *identityWarningCollector) {
			collectSwiftIdentityEvidenceFromPaths(ctx, repo, index, nil, paths, nil, warnings)
		}},
		{"Cartfile.resolved", "Cartfile.resolved", func(ctx context.Context, repo string, index identityIndex, paths []string, warnings *identityWarningCollector) {
			collectSwiftIdentityEvidenceFromPaths(ctx, repo, index, nil, nil, paths, warnings)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			paths := []string{filepath.Join(repo, "first", tc.filename), filepath.Join(repo, "second", tc.filename)}
			for _, checks := range []int{0, 1, 2, 3} {
				t.Run(fmt.Sprint(checks), func(t *testing.T) {
					warnings := newIdentityWarningCollector(repo)
					tc.collect(&catalogCancelContext{remaining: checks}, repo, make(identityIndex), paths, warnings)
					messages := warnings.list()
					want := min(checks, 2)
					switch tc.name {
					case "cargo-lock":
						want = max(0, checks-2)
					case "pub", "elixir":
						want = checks / 2
					case "composer":
						want = (checks + 1) / 2
					}
					if len(messages) != want {
						t.Fatalf("got %d reads, want %d before cancellation: %v", len(messages), want, messages)
					}
				})
			}
		})
	}
}
