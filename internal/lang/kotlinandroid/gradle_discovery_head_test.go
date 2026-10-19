package kotlinandroid

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestStrictGradleStreamingKeepsBoundedLiveContents(t *testing.T) {
	repo := t.TempDir()
	const size = 1 << 20
	for i := range 17 {
		testutil.MustWriteFile(t, filepath.Join(repo, fmt.Sprintf("module-%02d", i), buildGradleName), strings.Repeat(" ", size))
	}
	root, err := safeio.OpenRootNoFollow(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	}()
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	parsed := 0
	budget := shared.NewGradleDiscoveryBudget(repo)
	err = streamGradleFilesContext(context.Background(), repo, root, budget, func(_ string, content []byte) error {
		runtime.GC()
		var current runtime.MemStats
		runtime.ReadMemStats(&current)
		if current.HeapAlloc > before.HeapAlloc+8*size {
			t.Fatalf("aggregate input retained: %d", current.HeapAlloc-before.HeapAlloc)
		}
		if len(content) != size {
			t.Fatalf("read %d bytes", len(content))
		}
		parsed++
		return nil
	})
	if err != nil || parsed != 17 {
		t.Fatalf("parsed=%d error=%v", parsed, err)
	}
}

func TestStrictGradlePreservesRootSkipAndPrecedence(t *testing.T) {
	for _, name := range []string{"project", "build", "target"} {
		t.Run(name, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), name)
			for i := range 2 {
				testutil.MustWriteFile(t, filepath.Join(repo, fmt.Sprintf("module-%d", i), buildGradleName), fmt.Sprintf("implementation 'org.example:widget:%d'\n", i+1))
			}
			testutil.MustWriteFile(t, filepath.Join(repo, "out", buildGradleName), "implementation 'org.example:hidden:9'\n")
			legacy, _, _, warnings := collectGradleDeclaredDependencyDescriptors(repo)
			strict, _, strictWarnings, err := collectDeclaredDependenciesContext(context.Background(), repo)
			if err != nil || !reflect.DeepEqual(strict, mergeDescriptors(legacy, nil)) || !reflect.DeepEqual(warnings, strictWarnings) {
				t.Fatalf("legacy=%v strict=%v warnings=%v/%v error=%v", legacy, strict, warnings, strictWarnings, err)
			}
		})
	}
}

func TestStrictGradleFailureDiscardsEarlierDescriptors(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "a", buildGradleName), "implementation 'org.example:good:1'\n")
	bad := filepath.Join(repo, "z", buildGradleName)
	testutil.MustWriteFile(t, bad, "")
	if err := os.Truncate(bad, shared.GradleDiscoveryFileBytes+1); err != nil {
		t.Fatal(err)
	}
	result, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 10})
	var typed *shared.GradleDiscoveryError
	if !errors.As(err, &typed) || !errors.Is(err, safeio.ErrFileTooLarge) || !reflect.DeepEqual(result, report.Report{}) {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestStrictGradleCancellationDiscardsStreamingResults(t *testing.T) {
	repo := t.TempDir()
	for _, name := range []string{"a", "b"} {
		testutil.MustWriteFile(t, filepath.Join(repo, name, buildGradleName), "implementation 'org.example:good:1'\n")
	}
	root, err := safeio.OpenRootNoFollow(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	parsed := 0
	err = streamGradleFilesContext(ctx, repo, root, shared.NewGradleDiscoveryBudget(repo), func(_ string, _ []byte) error { parsed++; cancel(); return nil })
	if !errors.Is(err, context.Canceled) || parsed != 1 {
		t.Fatalf("parsed=%d error=%v", parsed, err)
	}
	items, lookups, warnings, err := collectDeclaredDependenciesContext(ctx, repo)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(items, []dependencyDescriptor(nil)) || !reflect.DeepEqual(warnings, []string(nil)) || !reflect.DeepEqual(lookups, dependencyLookups{}) {
		t.Fatalf("canceled collection returned state: %v", err)
	}
}

func TestStrictGradleWorkLimitIsFatal(t *testing.T) {
	repo := t.TempDir()
	for i := range 2049 {
		testutil.MustWriteFile(t, filepath.Join(repo, fmt.Sprintf("module-%04d", i), buildGradleName), "")
	}
	root, err := safeio.OpenRootNoFollow(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	}()
	parsed := 0
	err = streamGradleFilesContext(context.Background(), repo, root, shared.NewGradleDiscoveryBudget(repo), func(_ string, _ []byte) error { parsed++; return nil })
	var typed *shared.GradleDiscoveryError
	if !errors.As(err, &typed) || !errors.Is(err, shared.ErrGradleDiscoveryLimit) || parsed != 2048 || typed.Limit != 2048 {
		t.Fatalf("parsed=%d error=%v", parsed, err)
	}
}
