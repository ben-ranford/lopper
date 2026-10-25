package scripts

import (
	"flag"
	"log"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Compiler/test cache entries use the caller's Go cache. Each benchmark
// fixture retains its own HOME, repository, worktrees and assertion output.
var benchFixtureGoCache string

func TestMain(m *testing.M) {
	flag.Parse()
	if err := configureFixtureParallelism(flag.CommandLine); err != nil {
		log.Printf("configure fixture parallelism: %v", err)
		os.Exit(1)
	}
	if err := prepareGradleInferenceProof(); err != nil {
		log.Printf("prepare Gradle inference proof: %v", err)
		os.Exit(1)
	}
	cache, err := callerGoCache()
	if err != nil {
		log.Printf("resolve benchmark fixture Go cache: %v", err)
		os.Exit(1)
	}
	benchFixtureGoCache = cache
	os.Exit(m.Run())
}

// Fixture tests spend much of their time waiting for isolated subprocesses and
// watchdogs. Keep their scheduling budget separate from Go's CPU budget, while
// respecting an explicit go test -parallel value.
func configureFixtureParallelism(flags *flag.FlagSet) error {
	explicit := false
	flags.Visit(func(option *flag.Flag) {
		if option.Name == "test.parallel" {
			explicit = true
		}
	})
	if explicit {
		return nil
	}
	return flags.Set("test.parallel", "16")
}

func callerGoCache() (string, error) {
	if cache := os.Getenv("GOCACHE"); cache != "" {
		return cache, nil
	}
	goBinary, err := exec.LookPath("go")
	if err != nil {
		return "", err
	}
	output, err := exec.Command(goBinary, "env", "GOCACHE").Output()
	if err != nil {
		return "", err
	}
	cache := strings.TrimSpace(string(output))
	if cache == "" {
		return "", os.ErrNotExist
	}
	return cache, nil
}

func TestCallerGoCacheResolution(t *testing.T) {
	t.Run("explicit", func(t *testing.T) {
		want := "/caller/cache"
		t.Setenv("GOCACHE", want)
		got, err := callerGoCache()
		if err != nil {
			t.Fatalf("resolve explicit GOCACHE: %v", err)
		}
		if got != want {
			t.Fatalf("callerGoCache() = %q, want %q", got, want)
		}
	})

	t.Run("go default", func(t *testing.T) {
		t.Setenv("GOCACHE", "")
		goBinary, err := exec.LookPath("go")
		if err != nil {
			t.Fatalf("resolve Go binary: %v", err)
		}
		output, err := exec.Command(goBinary, "env", "GOCACHE").Output()
		if err != nil {
			t.Fatalf("query Go's default cache: %v", err)
		}
		got, err := callerGoCache()
		if err != nil {
			t.Fatalf("resolve default GOCACHE: %v", err)
		}
		if want := strings.TrimSpace(string(output)); got != want {
			t.Fatalf("callerGoCache() = %q, want Go default %q", got, want)
		}
	})

	t.Run("Go binary unavailable", func(t *testing.T) {
		t.Setenv("GOCACHE", "")
		t.Setenv("PATH", t.TempDir())
		if _, err := callerGoCache(); err == nil {
			t.Fatal("callerGoCache() succeeded without an explicit cache or Go binary")
		}
	})
}
