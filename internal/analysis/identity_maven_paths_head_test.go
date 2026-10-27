//go:build !windows

package analysis

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	jvmlang "github.com/ben-ranford/lopper/internal/lang/jvm"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestMavenUnixFilenameLiveCachedRebaseParity(t *testing.T) {
	for _, name := range []string{"a:b", "what?", "quote\"", "back\\slash", "<pipe|star*>", "https:module", "line\nbreak", "tab\tname"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			repo := filepath.Join(root, name)
			writeMavenServiceFixture(t, repo, "1.2.3")
			result, err := jvmlang.NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, Dependency: "widgets"})
			if err != nil {
				t.Fatal(err)
			}
			cached := roundTripMavenReuseReport(t, result)
			if err := os.Remove(filepath.Join(repo, "pom.xml")); err != nil {
				t.Fatal(err)
			}
			reads, decodes := observeMavenIdentityIO(t)
			assertMavenUnixRebasedReports(t, root, name, result, cached)
			if *reads != 0 || *decodes != 0 {
				t.Fatalf("captured filename IO=%d/%d", *reads, *decodes)
			}
		})
	}
}

func assertMavenUnixRebasedReports(t *testing.T, root, name string, values ...report.Report) {
	t.Helper()
	for _, value := range values {
		documents, present, err := mergedMavenEvidence(root, []report.Report{value})
		if err != nil || !present {
			t.Fatalf("rebase=%v %v", present, err)
		}
		value.MavenManifests = documents
		annotateDependencyIdentities(root, &value)
		assertMavenServiceIdentity(t, value, "1.2.3", name+"/pom.xml")
	}
}

func TestMavenCapturedEvidenceSurvivesSourceReplacement(t *testing.T) {
	mutations := map[string]func(*testing.T, string){
		"new-content":     func(t *testing.T, root string) { writeMavenServiceFixture(t, root, "9.9.9") },
		"outside-symlink": replaceMavenPOMWithOutsideSymlink,
		"root-replaced":   replaceMavenRoot,
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "repo")
			writeMavenServiceFixture(t, root, "1.2.3")
			result, err := jvmlang.NewAdapter().Analyse(context.Background(), language.Request{RepoPath: root, Dependency: "widgets"})
			if err != nil {
				t.Fatal(err)
			}
			cached := roundTripMavenReuseReport(t, result)
			mutate(t, root)
			reads, decodes := observeMavenIdentityIO(t)
			for _, value := range []report.Report{result, cached} {
				annotateDependencyIdentities(root, &value)
				assertMavenServiceIdentity(t, value, "1.2.3", "pom.xml")
			}
			if *reads != 0 || *decodes != 0 {
				t.Fatalf("replacement reread=%d/%d", *reads, *decodes)
			}
		})
	}
}

func replaceMavenPOMWithOutsideSymlink(t *testing.T, root string) {
	t.Helper()
	outside := t.TempDir()
	writeMavenServiceFixture(t, outside, "9.9.9")
	path := filepath.Join(root, "pom.xml")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "pom.xml"), path); err != nil {
		t.Fatal(err)
	}
}

func replaceMavenRoot(t *testing.T, root string) {
	t.Helper()
	if err := os.Rename(root, root+"-original"); err != nil {
		t.Fatal(err)
	}
	writeMavenServiceFixture(t, root, "9.9.9")
}
