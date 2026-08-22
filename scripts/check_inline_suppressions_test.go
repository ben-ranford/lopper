package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

const mainGoPath = "main.go"

func TestInlineSuppressionCheckRejectsStagedMarkers(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		path    string
		content string
		want    string
	}{
		{
			name:    "nosec",
			path:    mainGoPath,
			content: mainGoWithComment("nosec G404"),
			want:    "//" + "nosec",
		},
		{
			name:    "NOSEC uppercase",
			path:    mainGoPath,
			content: mainGoWithComment("NOSEC G404"),
			want:    "//" + "NOSEC",
		},
		{
			name:    "NOSONAR uppercase block comment",
			path:    mainGoPath,
			content: mainGoWithBlockComment("NOSONAR"),
			want:    "NOSONAR",
		},
		{
			name:    "nolint",
			path:    mainGoPath,
			content: mainGoWithComment("nolint:staticcheck"),
			want:    "//" + "nolint",
		},
		{
			name:    "ts-ignore",
			path:    "main.ts",
			content: "const value = 1;\n// @" + "ts-ignore\nconsole.log(value as string);\n",
			want:    "@" + "ts-ignore",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repoDir := newInlineSuppressionRepo(t)
			writeFile(t, filepath.Join(repoDir, tc.path), tc.content)
			runCommand(t, repoDir, "git", "add", tc.path)

			output, err := runSuppressionCheck(repoDir)
			if err == nil {
				t.Fatalf("expected suppression check to fail for %s, output:\n%s", tc.name, output)
			}
			if !strings.Contains(output, tc.want) {
				t.Fatalf("expected output to mention %q, got:\n%s", tc.want, output)
			}
			if !strings.Contains(output, "Inline suppression markers are not allowed in staged changes.") {
				t.Fatalf("expected staged change failure message, got:\n%s", output)
			}
		})
	}
}

func TestInlineSuppressionCheckRejectsWorkingTreeMarkers(t *testing.T) {
	t.Parallel()

	repoDir := newInlineSuppressionRepo(t)
	writeFile(t, filepath.Join(repoDir, mainGoPath), mainGoWithoutComment())
	runCommand(t, repoDir, "git", "add", mainGoPath)
	runCommand(t, repoDir, "git", "commit", "-m", "add source file")

	writeFile(t, filepath.Join(repoDir, mainGoPath), mainGoWithComment("nolint:staticcheck"))

	output, err := runSuppressionCheck(repoDir)
	if err == nil {
		t.Fatalf("expected working tree suppression check to fail, output:\n%s", output)
	}
	if !strings.Contains(output, "Inline suppression markers are not allowed in working tree changes.") {
		t.Fatalf("expected working tree failure message, got:\n%s", output)
	}
}

func TestInlineSuppressionCheckAllowsDocumentationMentions(t *testing.T) {
	t.Parallel()

	repoDir := newInlineSuppressionRepo(t)
	docContent := "# Policy\n\nDo not add `" + "//" + "nolint` or `" + "//" + "nosec` markers in source files.\n"
	writeFile(t, filepath.Join(repoDir, "docs", "policy.md"), docContent)
	runCommand(t, repoDir, "git", "add", "docs/policy.md")

	output, err := runSuppressionCheck(repoDir)
	if err != nil {
		t.Fatalf("expected documentation mention to pass, output:\n%s", output)
	}
	if !strings.Contains(output, "Inline suppression check passed (staged changes)") {
		t.Fatalf("expected pass message, got:\n%s", output)
	}
}

func TestInlineSuppressionCheckIgnoresQuotedMarkersInSource(t *testing.T) {
	t.Parallel()

	repoDir := newInlineSuppressionRepo(t)
	source := "package main\n\nconst marker = \"" + "//" + "nosec" + "\"\n"
	writeFile(t, filepath.Join(repoDir, mainGoPath), source)
	runCommand(t, repoDir, "git", "add", mainGoPath)

	output, err := runSuppressionCheck(repoDir)
	if err != nil {
		t.Fatalf("expected quoted marker to pass, output:\n%s", output)
	}
	if !strings.Contains(output, "Inline suppression check passed (staged changes)") {
		t.Fatalf("expected pass message, got:\n%s", output)
	}
}

func mainGoWithoutComment() string {
	return "package main\n\nfunc main() {\n\t_ = 1\n}\n"
}

func mainGoWithComment(comment string) string {
	return "package main\n\nfunc main() {\n\t_ = 1 //" + comment + "\n}\n"
}

func mainGoWithBlockComment(comment string) string {
	return "package main\n\nfunc main() {\n\t_ = 1 /* " + comment + " */\n}\n"
}

func newInlineSuppressionRepo(t *testing.T) string {
	t.Helper()

	repoDir := t.TempDir()
	scriptDir := filepath.Join(repoDir, "scripts")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	scriptPath := filepath.Join(cwd, "check-inline-suppressions.sh")
	scriptData, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	writeFileMode(t, filepath.Join(scriptDir, "check-inline-suppressions.sh"), string(scriptData), 0o755)

	runCommand(t, repoDir, "git", "init", "-b", "main")
	runCommand(t, repoDir, "git", "config", "user.name", "Test User")
	runCommand(t, repoDir, "git", "config", "user.email", "test@example.com")
	runCommand(t, repoDir, "git", "add", "scripts/check-inline-suppressions.sh")
	runCommand(t, repoDir, "git", "commit", "-m", "baseline")

	return repoDir
}

func runSuppressionCheck(repoDir string) (string, error) {
	cmd := exec.Command(filepath.Join(repoDir, "scripts", "check-inline-suppressions.sh"))
	cmd.Dir = repoDir
	cmd.Env = withoutGitEnv()
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func runCommand(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()

	if name == "git" {
		testutil.RunGit(t, dir, args...)
		return ""
	}

	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = withoutGitEnv()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, output)
	}
	return string(output)
}

func withoutGitEnv() []string {
	env := os.Environ()
	filtered := env[:0]
	for _, entry := range env {
		if strings.HasPrefix(entry, "GIT_") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	writeFileMode(t, path, content, 0o644)
}

func writeFileMode(t *testing.T, path string, content string, mode os.FileMode) {
	t.Helper()

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	tempFile, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		t.Fatalf("create temp file for %s: %v", path, err)
	}
	tempPath := tempFile.Name()
	t.Cleanup(func() {
		if err := os.Remove(tempPath); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove temp file %s: %v", tempPath, err)
		}
	})
	if _, err := tempFile.WriteString(content); err != nil {
		if closeErr := tempFile.Close(); closeErr != nil {
			t.Fatalf("write %s: %v; close after write failure: %v", tempPath, err, closeErr)
		}
		t.Fatalf("write %s: %v", tempPath, err)
	}
	if err := tempFile.Chmod(mode); err != nil {
		if closeErr := tempFile.Close(); closeErr != nil {
			t.Fatalf("chmod %s: %v; close after chmod failure: %v", tempPath, err, closeErr)
		}
		t.Fatalf("chmod %s: %v", tempPath, err)
	}
	if err := tempFile.Close(); err != nil {
		t.Fatalf("close %s: %v", tempPath, err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		t.Fatalf("rename %s to %s: %v", tempPath, path, err)
	}
}
