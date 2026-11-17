package jvm

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report/model"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestJVMRetainsPOMWithSingleReadAndDecode(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "pom.xml"), `<project><properties><v>1</v></properties><dependencies><dependency><groupId>example</groupId><artifactId>x</artifactId><version>${v}</version></dependency></dependencies></project>`)
	root, err := safeio.OpenRootNoFollow(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	}()
	catalog := newMavenManifestCatalog()
	decodes := 0
	catalog.decode = func(data []byte) (shared.ParsedPOM, error) {
		decodes++
		if err := os.Remove(filepath.Join(repo, "pom.xml")); err != nil {
			t.Fatal(err)
		}
		return shared.DecodePOM(data)
	}
	counted := &mavenReadCountRoot{Root: root}
	dependencies, _, _, _, err := collectDeclaredDependenciesWithinRoot(context.Background(), repo, counted, catalog)
	if err != nil || decodes != 1 || counted.opens != 1 || len(catalog.entries) != 1 || len(dependencies) != 1 {
		t.Fatalf("decode reuse: calls=%d opens=%d dependencies=%v entries=%d err=%v", decodes, counted.opens, dependencies, len(catalog.entries), err)
	}
	if catalog.entries[0].Properties()["v"] != "1" || catalog.entries[0].Dependencies()[0].Version != "${v}" {
		t.Fatal("identity evidence was resolved using inventory policy")
	}
}
func TestJVMRetainsPOMParseFailureWithoutSecondDecode(t *testing.T) {
	catalog := newMavenManifestCatalog()
	calls := 0
	catalog.decode = func(data []byte) (shared.ParsedPOM, error) { calls++; return shared.DecodePOM(data) }
	_, warnings := parsePomDependencyContentWithEvidence("pom.xml", "<project>", catalog)
	if calls != 1 || len(warnings) != 1 || len(catalog.entries) != 1 {
		t.Fatalf("calls=%d warnings=%v entries=%d", calls, warnings, len(catalog.entries))
	}
	stage, kind := catalog.entries[0].Failure()
	if stage != "parse" || kind != "xml" {
		t.Fatalf("failure=%s/%s", stage, kind)
	}
}

type mavenReadCountRoot struct {
	safeio.Root
	opens   int
	openErr error
}

func (r *mavenReadCountRoot) Open(name string) (safeio.File, error) {
	if name == "pom.xml" {
		r.opens++
		if r.openErr != nil {
			return nil, r.openErr
		}
	}
	return r.Root.Open(name)
}

func TestJVMRetainsPOMReadFailureWithoutDecode(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "pom.xml"), "<project/>")
	root, err := safeio.OpenRootNoFollow(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	}()
	catalog := newMavenManifestCatalog()
	decodes := 0
	catalog.decode = func(data []byte) (shared.ParsedPOM, error) { decodes++; return shared.DecodePOM(data) }
	counted := &mavenReadCountRoot{Root: root, openErr: fs.ErrPermission}
	_, _, _, warnings, err := collectDeclaredDependenciesWithinRoot(context.Background(), repo, counted, catalog)
	if err != nil || counted.opens != 1 || decodes != 0 || len(catalog.entries) != 1 || len(warnings) != 1 {
		t.Fatalf("read failure retention: opens=%d decode=%d entries=%d warnings=%v err=%v", counted.opens, decodes, len(catalog.entries), warnings, err)
	}
	stage, kind := catalog.entries[0].Failure()
	if stage != "read" || kind != "permission" {
		t.Fatalf("failure=%s/%s", stage, kind)
	}
}

func TestMavenCollectorPreservesRetentionAndReadFailures(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "pom.xml"), "<project/>")
	root, err := safeio.OpenRootNoFollow(repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	catalog := newMavenManifestCatalog()
	catalog.entries = []model.MavenManifest{mavenFullCatalogEntry(t)}
	operational := errors.New("read close operation failed")
	reader := &mavenReadCountRoot{Root: root, openErr: errors.Join(fs.ErrPermission, operational)}
	deps, _, _, _, err := collectDeclaredDependenciesWithinRoot(context.Background(), repo, reader, catalog)
	if !errors.Is(err, model.ErrMavenEvidenceLimit) || !errors.Is(err, operational) || !errors.Is(err, fs.ErrPermission) || len(deps) != 0 {
		t.Fatalf("walker/collector lost failure: deps=%v error=%v catalog=%v", deps, err, catalog.err)
	}
}
func mavenFullCatalogEntry(t *testing.T) model.MavenManifest {
	t.Helper()
	entry, err := model.NewMavenManifest("seed/pom.xml", map[string]string{"x": ""}, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	size, err := model.MavenEvidenceSize([]model.MavenManifest{entry})
	if err != nil {
		t.Fatal(err)
	}
	entry, err = model.NewMavenManifest("seed/pom.xml", map[string]string{"x": strings.Repeat("x", model.MavenEvidenceByteLimit-size)}, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return entry
}
