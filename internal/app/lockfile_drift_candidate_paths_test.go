package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func mustLockfileRule(t *testing.T, manager, manifest string) lockfileRule {
	t.Helper()
	for _, rule := range lockfileRules {
		if rule.manager == manager && rule.manifest == manifest {
			return rule
		}
	}
	t.Fatalf("missing lockfile rule for manager %q manifest %q", manager, manifest)
	return lockfileRule{}
}

func assertCandidatePaths(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("expected %d candidate paths, got %#v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected candidate %q at index %d, got %#v", want[i], i, got)
		}
	}
}

func TestLockfileManifestChangeCandidatePathsRuleRelevance(t *testing.T) {
	t.Run("requires a matching lockfile", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)

		snapshot, err := readLockfileDirSnapshot(repo, repo)
		if err != nil {
			t.Fatalf("read lockfile snapshot: %v", err)
		}
		got, err := lockfileManifestChangeCandidatePaths(snapshot, []lockfileRule{mustLockfileRule(t, "npm", manifestFileName)})
		if err != nil {
			t.Fatalf("lockfileManifestChangeCandidatePaths: %v", err)
		}
		assertCandidatePaths(t, got, nil)
	})

	t.Run("matches only relevant pyproject sections", func(t *testing.T) {
		cases := []struct {
			name    string
			content string
			want    []string
		}{
			{name: "non matching section", content: "[project]\nname = \"demo\"\n", want: nil},
			{name: "matching poetry section", content: "[tool.poetry]\nname = \"demo\"\n", want: []string{poetryLockName, pyprojectManifestName}},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				repo := t.TempDir()
				writeFile(t, filepath.Join(repo, pyprojectManifestName), tc.content)
				writeFile(t, filepath.Join(repo, poetryLockName), "# lock\n")

				snapshot, err := readLockfileDirSnapshot(repo, repo)
				if err != nil {
					t.Fatalf("read lockfile snapshot: %v", err)
				}
				got, err := lockfileManifestChangeCandidatePaths(snapshot, []lockfileRule{mustLockfileRule(t, "Poetry", pyprojectManifestName)})
				if err != nil {
					t.Fatalf("lockfileManifestChangeCandidatePaths: %v", err)
				}
				assertCandidatePaths(t, got, tc.want)
			})
		}
	})

	t.Run("includes distributed dotnet lockfiles", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, dotnetCentralManifest), "<Project><ItemGroup><PackageVersion Include=\"Newtonsoft.Json\" Version=\"13.0.3\" /></ItemGroup></Project>\n")
		writeFile(t, filepath.Join(repo, "src", "App", dotnetProjectManifest), "<Project></Project>\n")
		writeFile(t, filepath.Join(repo, "src", "App", dotnetLockfileName), "{}\n")

		snapshot, err := readLockfileDirSnapshot(repo, repo)
		if err != nil {
			t.Fatalf("read lockfile snapshot: %v", err)
		}
		got, err := lockfileManifestChangeCandidatePaths(snapshot, []lockfileRule{mustLockfileRule(t, ".NET", dotnetCentralManifest)})
		if err != nil {
			t.Fatalf("lockfileManifestChangeCandidatePaths: %v", err)
		}
		assertCandidatePaths(t, got, []string{dotnetCentralManifest, filepath.ToSlash(filepath.Join("src", "App", dotnetLockfileName))})
	})
}

func TestLockfileManifestChangeCandidatePathsDeduplicatesAndPropagatesErrors(t *testing.T) {
	t.Run("deduplicates repeated rules and lockfiles", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
		writeFile(t, filepath.Join(repo, lockfileName), "{}\n")

		snapshot, err := readLockfileDirSnapshot(repo, repo)
		if err != nil {
			t.Fatalf("read lockfile snapshot: %v", err)
		}
		rule := lockfileRule{
			manager:   "npm",
			manifest:  manifestFileName,
			lockfiles: []string{lockfileName, lockfileName},
		}
		got, err := lockfileManifestChangeCandidatePaths(snapshot, []lockfileRule{rule, rule})
		if err != nil {
			t.Fatalf("lockfileManifestChangeCandidatePaths: %v", err)
		}
		assertCandidatePaths(t, got, []string{lockfileName, manifestFileName})
	})

	t.Run("propagates manifest matcher failures", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
		writeFile(t, filepath.Join(repo, lockfileName), "{}\n")

		snapshot, err := readLockfileDirSnapshot(repo, repo)
		if err != nil {
			t.Fatalf("read lockfile snapshot: %v", err)
		}
		_, err = lockfileManifestChangeCandidatePaths(snapshot, []lockfileRule{{
			manager:   "custom",
			manifest:  manifestFileName,
			lockfiles: []string{lockfileName},
			manifestMatcher: func(string, string) (bool, error) {
				return false, errors.New("boom")
			},
		}})
		if err == nil || err.Error() != "boom" {
			t.Fatalf("expected matcher error, got %v", err)
		}
	})
}

func TestCollectLockfileManifestChangeCandidatePathsWalksRepoAndHonorsContext(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
	writeFile(t, filepath.Join(repo, lockfileName), "{}\n")
	writeFile(t, filepath.Join(repo, "nested", manifestFileName), demoPackageJSON)
	writeFile(t, filepath.Join(repo, "nested", lockfileName), "{}\n")

	got, err := collectLockfileManifestChangeCandidatePaths(context.Background(), repo, []lockfileRule{mustLockfileRule(t, "npm", manifestFileName)})
	if err != nil {
		t.Fatalf("collectLockfileManifestChangeCandidatePaths: %v", err)
	}
	assertCandidatePaths(t, got, []string{"nested/package-lock.json", "nested/package.json", "package-lock.json", "package.json"})

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := collectLockfileManifestChangeCandidatePaths(cancelledCtx, repo, []lockfileRule{mustLockfileRule(t, "npm", manifestFileName)}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestCollectLockfileManifestChangeCandidatePathsHandlesWalkErrorsAndDistributedDeduplication(t *testing.T) {
	t.Run("missing root propagates walk error", func(t *testing.T) {
		if _, err := collectLockfileManifestChangeCandidatePaths(context.Background(), filepath.Join(t.TempDir(), "missing"), []lockfileRule{mustLockfileRule(t, "npm", manifestFileName)}); err == nil {
			t.Fatal("expected missing root to fail candidate collection")
		}
	})

	t.Run("distributed dotnet walk returns unique candidates", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, dotnetCentralManifest), "<Project><ItemGroup><PackageVersion Include=\"Newtonsoft.Json\" Version=\"13.0.3\" /></ItemGroup></Project>\n")
		writeFile(t, filepath.Join(repo, "src", "App", dotnetProjectManifest), "<Project></Project>\n")
		writeFile(t, filepath.Join(repo, "src", "App", dotnetLockfileName), "{}\n")

		got, err := collectLockfileManifestChangeCandidatePaths(context.Background(), repo, []lockfileRule{mustLockfileRule(t, ".NET", dotnetCentralManifest)})
		if err != nil {
			t.Fatalf("collectLockfileManifestChangeCandidatePaths: %v", err)
		}
		assertCandidatePaths(t, got, []string{
			dotnetCentralManifest,
			filepath.ToSlash(filepath.Join("src", "App", dotnetProjectManifest)),
			filepath.ToSlash(filepath.Join("src", "App", dotnetLockfileName)),
		})
	})
}
