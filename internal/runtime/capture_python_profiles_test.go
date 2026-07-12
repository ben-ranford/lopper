package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapturePythonRunnerProfilesMatchPytestDependencyTrace(t *testing.T) {
	pythonPath, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}

	repo := t.TempDir()
	sitePackages := filepath.Join(t.TempDir(), "lib", "python3.12", "site-packages")
	if err := os.MkdirAll(filepath.Join(sitePackages, "thirdparty"), 0o750); err != nil {
		t.Fatalf("create third-party package: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sitePackages, "thirdparty", "__init__.py"), []byte("VALUE = 1\n"), 0o600); err != nil {
		t.Fatalf("write third-party package: %v", err)
	}
	testModule := "import unittest\nimport thirdparty\n\nclass ImportTest(unittest.TestCase):\n    def test_import(self):\n        self.assertEqual(thirdparty.VALUE, 1)\n"
	if err := os.WriteFile(filepath.Join(repo, "test_imports.py"), []byte(testModule), 0o600); err != nil {
		t.Fatalf("write unittest module: %v", err)
	}

	toolDir := setupFakeRuntimeTools(t)
	writeRuntimeProfileTool(t, toolDir, "pytest", "#!/bin/sh\nexec \"$LOPPER_TEST_PYTHON\" -c 'import thirdparty'\n")
	writeRuntimeProfileTool(t, toolDir, "python3", "#!/bin/sh\nexec \"$LOPPER_TEST_PYTHON\" \"$@\"\n")
	writeRuntimeProfileTool(t, toolDir, "uv", "#!/bin/sh\nshift\nif [ \"${1:-}\" = -- ]; then shift; fi\nif [ \"${1:-}\" = pytest ]; then exec \"$LOPPER_TEST_PYTHON\" -c 'import thirdparty'; fi\nif [ \"${1:-}\" = python3 ]; then shift; exec \"$LOPPER_TEST_PYTHON\" \"$@\"; fi\nexit 64\n")
	t.Setenv("LOPPER_TEST_PYTHON", pythonPath)
	t.Setenv("PYTHONPATH", sitePackages)
	t.Setenv(runtimeBinDirsEnvKey, toolDir)

	commands := []string{
		"pytest",
		"python3 -m unittest test_imports",
		"uv run pytest",
		"uv run -- python3 -m unittest test_imports",
	}
	dependency := DependencyKey{Language: runtimeLanguagePython, Name: "thirdparty"}
	for index, command := range commands {
		tracePath := filepath.Join(repo, ".artifacts", "profile-"+string(rune('a'+index))+".ndjson")
		err := Capture(context.Background(), CaptureRequest{
			RepoPath:             repo,
			TracePath:            tracePath,
			Command:              command,
			Provider:             CaptureProviderPython,
			PythonRunnerProfiles: true,
		})
		if err != nil {
			t.Fatalf("capture %q: %v", command, err)
		}
		trace, err := Load(tracePath)
		if err != nil {
			t.Fatalf("load trace for %q: %v", command, err)
		}
		if trace.DependencyLoadsByLanguage[dependency] == 0 {
			t.Fatalf("expected %q to capture the same thirdparty dependency as pytest, got %#v", command, trace.DependencyLoadsByLanguage)
		}
	}
}

func TestCaptureUsesPATHSelectedPythonExecutable(t *testing.T) {
	if isWindowsRuntime() {
		t.Skip("PATH-selected wrapper fixture uses a Unix shell script")
	}
	pythonPath, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}

	repo := t.TempDir()
	selectedBin := t.TempDir()
	sitePackages := filepath.Join(t.TempDir(), "site-packages")
	if err := os.MkdirAll(sitePackages, 0o750); err != nil {
		t.Fatalf("create selected site-packages: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sitePackages, "specialdep.py"), []byte("VALUE = 1\n"), 0o600); err != nil {
		t.Fatalf("write selected dependency: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "pytest.py"), []byte("import specialdep\nassert specialdep.VALUE == 1\n"), 0o600); err != nil {
		t.Fatalf("write pytest module fixture: %v", err)
	}

	markerPath := filepath.Join(t.TempDir(), "selected-python.txt")
	wrapper := "#!/bin/sh\nprintf selected > \"$LOPPER_SELECTED_PYTHON_MARKER\"\nexport PYTHONPATH=\"$LOPPER_SELECTED_SITE_PACKAGES${PYTHONPATH:+:$PYTHONPATH}\"\nexec \"$LOPPER_TEST_PYTHON\" \"$@\"\n"
	writeRuntimeProfileTool(t, selectedBin, "python3", wrapper)
	t.Setenv(runtimeBinDirsEnvKey, "")
	t.Setenv("PATH", selectedBin+string(os.PathListSeparator)+"/usr/bin:/bin")
	t.Setenv("PYTHONPATH", "")
	t.Setenv("LOPPER_SELECTED_PYTHON_MARKER", markerPath)
	t.Setenv("LOPPER_SELECTED_SITE_PACKAGES", sitePackages)
	t.Setenv("LOPPER_TEST_PYTHON", pythonPath)

	tracePath := filepath.Join(repo, ".artifacts", "selected-python.ndjson")
	err = Capture(context.Background(), CaptureRequest{
		RepoPath:  repo,
		TracePath: tracePath,
		Command:   "python3 -m pytest",
		Provider:  CaptureProviderPython,
	})
	if err != nil {
		t.Fatalf("capture with PATH-selected Python: %v", err)
	}
	if content, err := os.ReadFile(markerPath); err != nil || string(content) != "selected" {
		t.Fatalf("expected selected Python wrapper marker, content=%q err=%v", content, err)
	}
	trace, err := Load(tracePath)
	if err != nil {
		t.Fatalf("load PATH-selected Python trace: %v", err)
	}
	dependency := DependencyKey{Language: runtimeLanguagePython, Name: "specialdep"}
	if trace.DependencyLoadsByLanguage[dependency] == 0 {
		t.Fatalf("expected selected Python dependency trace, got %#v", trace.DependencyLoadsByLanguage)
	}
}

func TestCaptureChainsProjectSitecustomizeExactlyOnce(t *testing.T) {
	pythonPath, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}

	repo := t.TempDir()
	sitePackages := filepath.Join(t.TempDir(), "site-packages")
	if err := os.MkdirAll(sitePackages, 0o750); err != nil {
		t.Fatalf("create site-packages: %v", err)
	}
	dependencySource := "import os\nif os.environ.get('SPECIALDEP_OK') != '1':\n    raise RuntimeError('project sitecustomize did not run')\nVALUE = 1\n"
	if err := os.WriteFile(filepath.Join(sitePackages, "specialdep.py"), []byte(dependencySource), 0o600); err != nil {
		t.Fatalf("write sitecustomize-dependent module: %v", err)
	}
	counterPath := filepath.Join(t.TempDir(), "sitecustomize-count.txt")
	projectHook := "import os\npath = os.environ['LOPPER_PROJECT_SITECUSTOMIZE_COUNT']\ncount = 0\ntry:\n    with open(path, 'r', encoding='utf-8') as handle:\n        count = int(handle.read() or '0')\nexcept FileNotFoundError:\n    pass\nwith open(path, 'w', encoding='utf-8') as handle:\n    handle.write(str(count + 1))\nos.environ['SPECIALDEP_OK'] = '1'\n"
	if err := os.WriteFile(filepath.Join(repo, "sitecustomize.py"), []byte(projectHook), 0o600); err != nil {
		t.Fatalf("write project sitecustomize: %v", err)
	}

	toolDir := setupFakeRuntimeTools(t)
	writeRuntimeProfileTool(t, toolDir, "pytest", "#!/bin/sh\nexec \"$LOPPER_TEST_PYTHON\" -c 'import specialdep'\n")
	t.Setenv("LOPPER_TEST_PYTHON", pythonPath)
	t.Setenv("LOPPER_PROJECT_SITECUSTOMIZE_COUNT", counterPath)
	t.Setenv("PYTHONPATH", strings.Join([]string{repo, sitePackages}, string(os.PathListSeparator)))
	t.Setenv(runtimeBinDirsEnvKey, toolDir)

	tracePath := filepath.Join(repo, ".artifacts", "sitecustomize.ndjson")
	err = Capture(context.Background(), CaptureRequest{
		RepoPath:  repo,
		TracePath: tracePath,
		Command:   "pytest",
		Provider:  CaptureProviderPython,
	})
	if err != nil {
		t.Fatalf("capture with project sitecustomize: %v", err)
	}
	if content, err := os.ReadFile(counterPath); err != nil || string(content) != "1" {
		t.Fatalf("expected project sitecustomize to run exactly once, content=%q err=%v", content, err)
	}
	trace, err := Load(tracePath)
	if err != nil {
		t.Fatalf("load project sitecustomize trace: %v", err)
	}
	dependency := DependencyKey{Language: runtimeLanguagePython, Name: "specialdep"}
	if trace.DependencyLoadsByLanguage[dependency] == 0 {
		t.Fatalf("expected tracing to remain active after project sitecustomize, got %#v", trace.DependencyLoadsByLanguage)
	}
}

func writeRuntimeProfileTool(t *testing.T, dir, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake runtime profile tool %q: %v", name, err)
	}
}
