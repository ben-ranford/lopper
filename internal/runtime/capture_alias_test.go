package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

func TestCaptureNodePreservesSymlinkAliasAttributionAndRedactsEscapes(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("symlinked launcher fixture uses a POSIX runner shim")
	}
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available")
	}

	fixture := newRuntimeRepoAliasFixture(t)
	dependencyPath := filepath.Join(fixture.realRepo, "node_modules", "valid-dep")
	if err := os.MkdirAll(dependencyPath, 0o750); err != nil {
		t.Fatalf("mkdir node dependency: %v", err)
	}
	writeRuntimeAliasFile(t, filepath.Join(dependencyPath, "package.json"), `{"name":"valid-dep","main":"index.cjs"}`+"\n")
	writeRuntimeAliasFile(t, filepath.Join(dependencyPath, "index.cjs"), "module.exports = 1;\n")

	outsidePath := filepath.Join(fixture.fixtureRoot, "outside.cjs")
	escapePath := filepath.Join(fixture.realRepo, "escape.cjs")
	entrypoint := filepath.Join(fixture.aliasRepo, "main.cjs")
	writeRuntimeAliasFile(t, outsidePath, "module.exports = 1;\n")
	if err := os.Symlink(outsidePath, escapePath); err != nil {
		t.Fatalf("symlink node escape: %v", err)
	}
	directCallerSource := fmt.Sprintf("require(%q);\n", outsidePath)
	writeRuntimeAliasFile(t, filepath.Join(fixture.realRepo, "direct-caller.cjs"), directCallerSource)
	writeRuntimeAliasFile(t, filepath.Join(fixture.realRepo, "symlink-caller.cjs"), "require('./escape.cjs');\n")
	nodeSource := "require('valid-dep');\nrequire('./direct-caller.cjs');\nrequire('./symlink-caller.cjs');\n"
	writeRuntimeAliasFile(t, filepath.Join(fixture.realRepo, "main.cjs"), nodeSource)

	t.Setenv("LOPPER_TEST_NODE", nodePath)
	t.Setenv("LOPPER_TEST_NODE_ENTRY", entrypoint)
	nodeRunnerDir := setupFakeRuntimeToolScript(t, "npm", "#!/bin/sh\nexec \"$LOPPER_TEST_NODE\" --preserve-symlinks --preserve-symlinks-main \"$LOPPER_TEST_NODE_ENTRY\"\n")
	t.Setenv(runtimeBinDirsEnvKey, nodeRunnerDir)

	if err := Capture(context.Background(), CaptureRequest{
		RepoPath:  fixture.aliasRepo,
		TracePath: fixture.tracePath,
		Command:   npmTestCommand,
	}); err != nil {
		t.Fatalf("capture node through symlinked repo alias: %v", err)
	}

	events, artifact := readRuntimeAliasEvents(t, fixture.tracePath)
	assertRuntimeAliasArtifactPrivacy(t, artifact, fixture)
	validEvent := findRuntimeAliasEvent(events, "valid-dep")
	if validEvent == nil {
		t.Fatalf("expected valid-dep event, got %#v", events)
	}
	if validEvent.Parent != "main.cjs" || validEvent.Resolved != "node_modules/valid-dep/index.cjs" {
		t.Fatalf("expected alias-relative node attribution, got %#v", *validEvent)
	}
	if !hasRuntimeAliasEntrypoint(events, "main.cjs") {
		t.Fatalf("expected alias-relative node entrypoint, got %#v", events)
	}
	for _, parent := range []string{"direct-caller.cjs", "symlink-caller.cjs"} {
		if !hasRedactedRuntimeAliasEvent(events, parent) {
			t.Fatalf("expected escape event from %q to be redacted, got %#v", parent, events)
		}
	}
}

func TestCapturePythonPreservesSymlinkAliasAttributionAndRedactsEscapes(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("symlinked launcher fixture uses a POSIX runner shim")
	}
	pythonPath, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}

	fixture, pythonEnv := newRuntimePythonAliasFixture(t)

	t.Setenv("LOPPER_TEST_PYTHON", pythonPath)
	t.Setenv("LOPPER_TEST_PYTHON_ENTRY", pythonEnv.entrypoint)
	t.Setenv("PYTHONPATH", pythonEnv.pythonPath)
	pythonRunnerDir := setupFakeRuntimeToolScript(t, "pytest", "#!/bin/sh\nexec \"$LOPPER_TEST_PYTHON\" \"$LOPPER_TEST_PYTHON_ENTRY\"\n")
	t.Setenv(runtimeBinDirsEnvKey, pythonRunnerDir)

	if err := Capture(context.Background(), CaptureRequest{
		RepoPath:  fixture.aliasRepo,
		TracePath: fixture.tracePath,
		Command:   "pytest",
		Provider:  CaptureProviderPython,
	}); err != nil {
		t.Fatalf("capture python through symlinked repo alias: %v", err)
	}

	events, artifact := readRuntimeAliasEvents(t, fixture.tracePath)
	assertRuntimeAliasArtifactPrivacy(t, artifact, fixture)
	validEvent := findRuntimeAliasEvent(events, "validdep")
	if validEvent == nil {
		t.Fatalf("expected validdep event, got %#v", events)
	}
	if validEvent.Parent != "main.py" || validEvent.Entrypoint != "main.py" {
		t.Fatalf("expected alias-relative python attribution, got %#v", *validEvent)
	}
	assertRuntimeAliasParentsRedacted(t, events, []string{"outsidedep", "escapeddep"})
	assertNoPythonCacheDirs(t, []string{fixture.realRepo, fixture.fixtureRoot})
}

type runtimeRepoAliasFixture struct {
	fixtureRoot string
	realRepo    string
	aliasRepo   string
	tracePath   string
}

func newRuntimeRepoAliasFixture(t *testing.T) runtimeRepoAliasFixture {
	t.Helper()

	fixtureRoot := t.TempDir()
	realRepo := filepath.Join(fixtureRoot, "repo-real")
	aliasRepo := filepath.Join(fixtureRoot, "repo-alias")
	if err := os.MkdirAll(realRepo, 0o750); err != nil {
		t.Fatalf("mkdir real repo: %v", err)
	}
	if err := os.Symlink(realRepo, aliasRepo); err != nil {
		t.Fatalf("symlink repo alias: %v", err)
	}
	return runtimeRepoAliasFixture{
		fixtureRoot: fixtureRoot,
		realRepo:    realRepo,
		aliasRepo:   aliasRepo,
		tracePath:   filepath.Join(fixtureRoot, runtimeTraceNDJSON),
	}
}

func writeRuntimeAliasFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir parent for %q: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

func readRuntimeAliasEvents(t *testing.T, tracePath string) ([]Event, string) {
	t.Helper()

	content, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read runtime alias trace: %v", err)
	}
	artifact := string(content)
	var events []Event
	for index, line := range strings.Split(artifact, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode runtime alias event %d: %v", index+1, err)
		}
		events = append(events, event)
	}
	if len(events) == 0 {
		t.Fatal("expected runtime alias events")
	}
	return events, artifact
}

func assertRuntimeAliasArtifactPrivacy(t *testing.T, artifact string, fixture runtimeRepoAliasFixture) {
	t.Helper()

	for _, forbidden := range []string{
		fixture.fixtureRoot,
		fixture.realRepo,
		fixture.aliasRepo,
		"file://",
	} {
		if strings.Contains(artifact, forbidden) {
			t.Fatalf("runtime alias artifact leaked %q: %s", forbidden, artifact)
		}
	}
}

func findRuntimeAliasEvent(events []Event, module string) *Event {
	for index := range events {
		if events[index].Module == module {
			return &events[index]
		}
	}
	return nil
}

func hasRuntimeAliasEntrypoint(events []Event, entrypoint string) bool {
	for _, event := range events {
		if event.Entrypoint == entrypoint {
			return true
		}
	}
	return false
}

func hasRedactedRuntimeAliasEvent(events []Event, parent string) bool {
	for _, event := range events {
		if event.Parent == parent && event.Module == "" && event.Resolved == "" {
			return true
		}
	}
	return false
}

type runtimePythonAliasEnv struct {
	entrypoint string
	pythonPath string
}

func newRuntimePythonAliasFixture(t *testing.T) (runtimeRepoAliasFixture, runtimePythonAliasEnv) {
	t.Helper()

	fixture := newRuntimeRepoAliasFixture(t)
	sitePackages := filepath.Join(fixture.fixtureRoot, "python", "site-packages")
	for _, dependency := range []string{"validdep", "outsidedep", "escapeddep"} {
		writeRuntimeAliasFile(t, filepath.Join(sitePackages, dependency, "__init__.py"), "VALUE = 1\n")
	}

	outsideModules := filepath.Join(fixture.fixtureRoot, "outside-modules")
	writeRuntimeAliasFile(t, filepath.Join(outsideModules, "outside_caller.py"), "import outsidedep\n")
	escapedTarget := filepath.Join(outsideModules, "escaped_target.py")
	writeRuntimeAliasFile(t, escapedTarget, "import escapeddep\n")
	if err := os.Symlink(escapedTarget, filepath.Join(fixture.realRepo, "escaped_caller.py")); err != nil {
		t.Fatalf("symlink python escape: %v", err)
	}

	entrypoint := filepath.Join(fixture.aliasRepo, "main.py")
	writeRuntimeAliasFile(t, filepath.Join(fixture.realRepo, "main.py"), "import validdep\nimport outside_caller\nimport escaped_caller\n")
	return fixture, runtimePythonAliasEnv{
		entrypoint: entrypoint,
		pythonPath: strings.Join([]string{sitePackages, outsideModules}, string(os.PathListSeparator)),
	}
}

func assertRuntimeAliasParentsRedacted(t *testing.T, events []Event, modules []string) {
	t.Helper()

	for _, module := range modules {
		event := findRuntimeAliasEvent(events, module)
		if event == nil {
			t.Fatalf("expected %s event, got %#v", module, events)
		}
		if event.Parent != "" {
			t.Fatalf("expected %s escape parent to be redacted, got %#v", module, *event)
		}
	}
}

func assertNoPythonCacheDirs(t *testing.T, roots []string) {
	t.Helper()

	for _, root := range roots {
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() && entry.Name() == "__pycache__" {
				t.Errorf("capture created Python cache directory %q", path)
			}
			return nil
		}); err != nil {
			t.Fatalf("scan for Python cache artifacts: %v", err)
		}
	}
}
