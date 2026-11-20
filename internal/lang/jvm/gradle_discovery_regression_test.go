package jvm

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestStrictJVMGradleKnownNonRegularInputWarnsAndSkipsBeforeOpen(t *testing.T) {
	repo := t.TempDir()
	root := openJVMTestRoot(t, repo)
	guard := &jvmNonRegularOpenGuardRoot{Root: root, name: buildGradleName}
	collector := buildFileWarningCollector{
		repoPath: repo,
		catalog:  newMavenManifestCatalog(),
		parser: func(string, string) ([]dependencyDescriptor, []string) {
			t.Fatal("known non-regular Gradle input must not be parsed")
			return nil, nil
		},
		names: []string{buildGradleName},
		seen:  map[string]struct{}{"org.example:earlier": {}},
		descriptors: []dependencyDescriptor{{
			Name: "earlier", Group: "org.example", Artifact: "earlier",
		}},
		gradle: &jvmGradleDiscovery{
			budget:   shared.NewGradleDiscoveryBudget(repo),
			resolver: &shared.GradleCatalogResolver{},
		},
	}
	entry := &jvmNonRegularBuildDirEntry{name: buildGradleName, mode: fs.ModeNamedPipe}
	err := collector.visitWithinRootContext(context.Background(), guard, buildGradleName, filepath.Join(repo, buildGradleName), entry)
	if guard.openCalls != 0 {
		t.Fatalf("known FIFO reached blocking open path %d times (visit error: %v)", guard.openCalls, err)
	}
	if err != nil {
		t.Fatalf("known non-regular Gradle input should warn and skip: %v", err)
	}
	if len(collector.descriptors) != 1 || collector.descriptors[0].Artifact != "earlier" {
		t.Fatalf("known non-regular input changed prior descriptors: %#v", collector.descriptors)
	}
	if len(collector.warnings) != 1 || !strings.Contains(collector.warnings[0], buildGradleName) || !strings.Contains(collector.warnings[0], fs.ErrInvalid.Error()) {
		t.Fatalf("expected path-scoped non-regular warning, got %#v", collector.warnings)
	}
}

func TestStrictJVMGradleRegularObservedSymlinkReplacementIsFatalWithoutPartialResults(t *testing.T) {
	repo := t.TempDir()
	module := filepath.Join(repo, "a")
	if err := os.Mkdir(module, 0o755); err != nil {
		t.Fatalf("mkdir module: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(module, buildGradleName), `implementation 'org.example:early:1'`)
	buildPath := filepath.Join(repo, buildGradleName)
	testutil.MustWriteFile(t, buildPath, `implementation 'org.example:late:1'`)
	target := filepath.Join(repo, "replacement.gradle")
	testutil.MustWriteFile(t, target, `implementation 'org.example:unexpected:1'`)
	base := openJVMTestRoot(t, repo)
	root := &jvmReplaceRegularBuildInputRoot{Root: base, path: buildPath, target: target}
	collector := buildFileWarningCollector{
		repoPath: repo,
		catalog:  newMavenManifestCatalog(),
		parser: func(string, string) ([]dependencyDescriptor, []string) {
			return []dependencyDescriptor{{Name: "early", Group: "org.example", Artifact: "early"}}, nil
		},
		names:  []string{buildGradleName},
		seen:   make(map[string]struct{}),
		gradle: &jvmGradleDiscovery{budget: shared.NewGradleDiscoveryBudget(repo), resolver: &shared.GradleCatalogResolver{}},
	}
	descriptors, warnings, err := collectBuildFilesWithinRoot(context.Background(), repo, root, &collector)
	var discoveryErr *shared.GradleDiscoveryError
	if !errors.As(err, &discoveryErr) {
		t.Fatalf("regular-observed replacement must remain a typed fatal discovery error, got %v", err)
	}
	if !reflect.DeepEqual(descriptors, []dependencyDescriptor(nil)) || !reflect.DeepEqual(warnings, []string(nil)) {
		t.Fatalf("fatal replacement returned partial collection: descriptors=%#v warnings=%#v", descriptors, warnings)
	}
}

type jvmNonRegularOpenGuardRoot struct {
	safeio.Root
	name      string
	openCalls int
}

func (r *jvmNonRegularOpenGuardRoot) Lstat(name string) (fs.FileInfo, error) {
	if name == r.name {
		return &jvmNonRegularBuildFileInfo{name: name, mode: fs.ModeNamedPipe}, nil
	}
	return r.Root.Lstat(name)
}

func (r *jvmNonRegularOpenGuardRoot) Open(name string) (safeio.File, error) {
	if name == r.name {
		r.openCalls++
		return nil, fs.ErrInvalid
	}
	return r.Root.Open(name)
}

type jvmReplaceRegularBuildInputRoot struct {
	safeio.Root
	path   string
	target string
	done   bool
}

func (r *jvmReplaceRegularBuildInputRoot) replace(name string) error {
	if name == buildGradleName && !r.done {
		r.done = true
		if err := os.Remove(r.path); err != nil {
			return err
		}
		if err := os.Symlink(r.target, r.path); err != nil {
			return err
		}
	}
	return nil
}

func (r *jvmReplaceRegularBuildInputRoot) Open(name string) (safeio.File, error) {
	if err := r.replace(name); err != nil {
		return nil, err
	}
	return r.Root.Open(name)
}

func (r *jvmReplaceRegularBuildInputRoot) OpenFile(name string, flag int, perm os.FileMode) (safeio.File, error) {
	if err := r.replace(name); err != nil {
		return nil, err
	}
	return r.Root.OpenFile(name, flag, perm)
}

type jvmNonRegularBuildDirEntry struct {
	name string
	mode fs.FileMode
}

func (e *jvmNonRegularBuildDirEntry) Name() string      { return e.name }
func (*jvmNonRegularBuildDirEntry) IsDir() bool         { return false }
func (e *jvmNonRegularBuildDirEntry) Type() fs.FileMode { return e.mode }
func (e *jvmNonRegularBuildDirEntry) Info() (fs.FileInfo, error) {
	return (*jvmNonRegularBuildFileInfo)(e), nil
}

type jvmNonRegularBuildFileInfo struct {
	name string
	mode fs.FileMode
}

func (i *jvmNonRegularBuildFileInfo) Name() string       { return i.name }
func (i *jvmNonRegularBuildFileInfo) Size() int64        { return 0 }
func (i *jvmNonRegularBuildFileInfo) Mode() fs.FileMode  { return i.mode }
func (i *jvmNonRegularBuildFileInfo) ModTime() time.Time { return time.Time{} }
func (i *jvmNonRegularBuildFileInfo) IsDir() bool        { return i.mode.IsDir() }
func (*jvmNonRegularBuildFileInfo) Sys() any             { return nil }

func TestGradleDiscoveryRejectsIncompleteJVMResults(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, buildGradleName), "implementation 'org.example:early:1'\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "settings.gradle"), `dependencyResolutionManagement { versionCatalogs { create("libs") { from(files("missing.versions.toml")) } } }`)
	result, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 10})
	if err == nil || !reflect.DeepEqual(result, report.Report{}) {
		t.Fatalf("explicit missing catalog produced partial success: error=%v result=%+v", err, result)
	}
}

func TestGradleDiscoveryRejectsPartialResultsFromMalformedBuildFile(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, buildGradleName), "implementation 'org.example:early:1'\nimplementation(\n")

	result, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 10})
	var discoveryErr *shared.GradleDiscoveryError
	if !errors.As(err, &discoveryErr) {
		t.Fatalf("malformed build file must produce typed discovery failure, got %v", err)
	}
	if !reflect.DeepEqual(result, report.Report{}) {
		t.Fatalf("malformed build file returned partial report: %+v", result)
	}
}

func TestGradleDiscoveryKeepsLateJVMDeclarations(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "Main.java"), "import org.example.Widget;\nclass Main { Widget value; }\n")
	testutil.MustWriteFile(t, filepath.Join(repo, buildGradleName), strings.Repeat(" ", 2<<20)+"implementation 'org.example:late:1'\n")
	result, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 10})
	if err != nil || len(result.Dependencies) != 1 || result.Dependencies[0].Name != "late" {
		t.Fatalf("late declaration lost: error=%v dependencies=%+v", err, result.Dependencies)
	}
}
