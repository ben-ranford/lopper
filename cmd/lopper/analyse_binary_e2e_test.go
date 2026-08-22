package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/version"
)

const (
	binaryBuildTimeout = 2 * time.Minute
	binaryRunTimeout   = 30 * time.Second
)

func TestAnalyseBinaryHermeticE2E(t *testing.T) {
	moduleRoot := mustModuleRoot(t)
	workspaceRoot := t.TempDir()
	fixtureRepo := filepath.Join(moduleRoot, "testdata", "js", "esm")
	repoPath := filepath.Join(workspaceRoot, "repo")
	copyDir(t, fixtureRepo, repoPath)

	binaryPath := buildBinaryInWorkspace(t, moduleRoot, workspaceRoot)
	got := runAnalyseBinary(t, binaryPath, workspaceRoot, []string{
		"analyse", "lodash",
		"--repo", repoPath,
		"--format", "json",
		"--cache=false",
	})
	assertGeneratedAtUTC(t, got)
	normalizeReport(t, got)

	goldenPath := filepath.Join(moduleRoot, "testdata", "cli", expectedGoldenReportName())
	goldenReport, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden report: %v", err)
	}
	want := decodeJSON(t, goldenReport)
	if !reflect.DeepEqual(got, want) {
		gotJSON, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatalf("marshal actual report: %v", err)
		}
		t.Fatalf("unexpected report output\nwant:\n%s\n\ngot:\n%s", string(goldenReport), string(gotJSON))
	}
}

func TestAnalyseBinaryCPPQualifiedHeaderProvenance(t *testing.T) {
	moduleRoot := mustModuleRoot(t)
	workspaceRoot := t.TempDir()
	binaryPath := buildBinaryInWorkspace(t, moduleRoot, workspaceRoot)

	t.Run("external lookalikes are reported", func(t *testing.T) {
		repoPath := filepath.Join(workspaceRoot, "cpp-lookalikes")
		externalIncludeRoot := filepath.Join(workspaceRoot, "vendor", "include")
		writeFile(t, filepath.Join(repoPath, "compile_commands.json"), `[
  {"directory":"`+repoPath+`","file":"src/main.cpp","arguments":["c++","-I","`+externalIncludeRoot+`","-c","src/main.cpp"]}
]`)
		writeFile(t, filepath.Join(repoPath, "src", "main.cpp"), `#include <asm/vendor/sdk.hpp>
#include <asm-generic/vendor/sdk.hpp>
#include <backward/hash_map>
#include <parallel/base.h>
int main() { return 0; }
`)
		for _, header := range []string{
			"asm/vendor/sdk.hpp",
			"asm-generic/vendor/sdk.hpp",
			"backward/hash_map",
			"parallel/base.h",
		} {
			writeFile(t, filepath.Join(externalIncludeRoot, filepath.FromSlash(header)), "// lookalike\n")
		}

		report := runAnalyseBinary(t, binaryPath, workspaceRoot, []string{
			"analyse", "--top", "10",
			"--repo", repoPath,
			"--language", "cpp",
			"--format", "json",
			"--cache=false",
		})

		assertDependencyCount(t, report, "asm", 1)
		assertDependencyCount(t, report, "asm-generic", 1)
		assertDependencyCount(t, report, "backward", 1)
		assertDependencyCount(t, report, "parallel", 1)
	})

	t.Run("canonical compiler headers stay suppressed", func(t *testing.T) {
		repoPath := filepath.Join(workspaceRoot, "cpp-canonical")
		writeFile(t, filepath.Join(repoPath, "src", "main.cpp"), `#include <asm/errno.h>
#include <asm-generic/errno.h>
#include <asm-generic/bitops/atomic.h>
#include <backward/hash_map>
#include <linux/netfilter_ipv4/ip_tables.h>
#include <parallel/base.h>
#include <tr1/float.h>
#include <tr1/random.h>
#include <tr1/stdarg.h>
#include <tr1/stdlib.h>
#include <tr1/wchar.h>
#include <tr1/wctype.h>
int main() { return 0; }
`)

		report := runAnalyseBinary(t, binaryPath, workspaceRoot, []string{
			"analyse", "--top", "10",
			"--repo", repoPath,
			"--language", "cpp",
			"--format", "json",
			"--cache=false",
		})

		assertDependencyCount(t, report, "asm", 0)
		assertDependencyCount(t, report, "asm-generic", 0)
		assertDependencyCount(t, report, "backward", 0)
		assertDependencyCount(t, report, "linux", 0)
		assertDependencyCount(t, report, "parallel", 0)
		assertDependencyCount(t, report, "tr1", 0)
	})

	t.Run("declared extension lookalikes without provenance are reported", func(t *testing.T) {
		repoPath := filepath.Join(workspaceRoot, "cpp-declared-lookalikes")
		writeFile(t, filepath.Join(repoPath, "vcpkg.json"), `{"name":"fixture","version-string":"1.0.0","dependencies":["parallel"]}`)
		writeFile(t, filepath.Join(repoPath, "src", "main.cpp"), `#include <parallel/base.h>
#include <acme/base.h>
int main() { return 0; }
`)

		report := runAnalyseBinary(t, binaryPath, workspaceRoot, []string{
			"analyse", "--top", "10",
			"--repo", repoPath,
			"--language", "cpp",
			"--format", "json",
			"--cache=false",
		})

		assertDependencyCount(t, report, "parallel", 1)
		assertDependencyCount(t, report, "acme", 1)
	})
}

func buildBinary(t *testing.T, moduleRoot string, binaryPath string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), binaryBuildTimeout)
	defer cancel()

	args := []string{
		"build",
		"-trimpath",
		"-buildvcs=false",
		"-ldflags",
		"-X github.com/ben-ranford/lopper/internal/version.buildChannel=" + version.Current().BuildChannel,
		"-o",
		binaryPath,
		"./cmd/lopper",
	}
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = moduleRoot

	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("go build timed out after %s", binaryBuildTimeout)
	}
	if err != nil {
		t.Fatalf("go build failed: %v\n%s", err, string(output))
	}
}

func buildBinaryInWorkspace(t *testing.T, moduleRoot, workspaceRoot string) string {
	t.Helper()
	binDir := filepath.Join(workspaceRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin dir: %v", err)
	}
	binaryPath := filepath.Join(binDir, "lopper")
	buildBinary(t, moduleRoot, binaryPath)
	return binaryPath
}

func mustModuleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	return root
}

func runAnalyseBinary(t *testing.T, binaryPath, workspaceRoot string, args []string) map[string]any {
	t.Helper()
	homePath := filepath.Join(workspaceRoot, "home")
	xdgConfigPath := filepath.Join(workspaceRoot, "xdg-config")
	xdgCachePath := filepath.Join(workspaceRoot, "xdg-cache")
	for _, dir := range []string{homePath, xdgConfigPath, xdgCachePath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), binaryRunTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, args...)
	cmd.Dir = workspaceRoot
	cmd.Env = []string{
		"HOME=" + homePath,
		"TMPDIR=" + workspaceRoot,
		"TMP=" + workspaceRoot,
		"TEMP=" + workspaceRoot,
		"TZ=UTC",
		"LANG=C",
		"LC_ALL=C",
		"TERM=dumb",
		"NO_COLOR=1",
		"CLICOLOR=0",
		"CLICOLOR_FORCE=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(homePath, ".gitconfig"),
		"GIT_TERMINAL_PROMPT=0",
		"XDG_CONFIG_HOME=" + xdgConfigPath,
		"XDG_CACHE_HOME=" + xdgCachePath,
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("lopper analyse timed out after %s", binaryRunTimeout)
	}
	if err != nil {
		t.Fatalf("lopper analyse failed: %v stderr=%q", err, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected no stderr output, got %q", stderr.String())
	}
	return decodeJSON(t, stdout.Bytes())
}

func copyDir(t *testing.T, src string, dst string) {
	t.Helper()
	if err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(dst, relPath)
		if entry.IsDir() {
			return os.MkdirAll(targetPath, 0o755)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(targetPath, content, 0o644)
	}); err != nil {
		t.Fatalf("copy fixture repo: %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func decodeJSON(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode json: %v\n%s", err, string(data))
	}
	return payload
}

func assertGeneratedAtUTC(t *testing.T, payload map[string]any) {
	t.Helper()
	value, ok := payload["generatedAt"].(string)
	if !ok || value == "" {
		t.Fatalf("expected generatedAt string, got %#v", payload["generatedAt"])
	}
	timestamp, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("parse generatedAt %q: %v", value, err)
	}
	if timestamp.Location() != time.UTC {
		t.Fatalf("expected generatedAt in UTC, got %q", value)
	}
}

func normalizeReport(t *testing.T, payload map[string]any) {
	t.Helper()
	payload["generatedAt"] = "<generatedAt>"
	payload["repoPath"] = "<repo>"

	cacheValue, ok := payload["cache"].(map[string]any)
	if !ok {
		t.Fatalf("expected cache object, got %#v", payload["cache"])
	}
	cacheValue["path"] = "<repo>/.lopper-cache"
}

func expectedGoldenReportName() string {
	if version.Current().BuildChannel == "rolling" {
		return "analyse_binary_e2e.rolling.golden.json"
	}
	return "analyse_binary_e2e.golden.json"
}

func assertDependencyCount(t *testing.T, payload map[string]any, name string, want float64) {
	t.Helper()
	dependencies, ok := payload["dependencies"].([]any)
	if !ok {
		t.Fatalf("expected dependencies array, got %#v", payload["dependencies"])
	}
	for _, raw := range dependencies {
		dependency, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if dependency["name"] != name {
			continue
		}
		if dependency["totalExportsCount"] != want {
			t.Fatalf("expected %s totalExportsCount=%v, got %#v", name, want, dependency)
		}
		return
	}
	if want != 0 {
		t.Fatalf("missing dependency %q in %#v", name, payload["dependencies"])
	}
}
