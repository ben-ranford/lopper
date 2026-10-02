package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSuppressionVerifyRequiresExplicitEvidence(t *testing.T) {
	t.Parallel()
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	step := workflowStepByName(t, workflow.Jobs, "verify", "Require explicit suppression evidence")
	const clean = `{"schema":"lopper-inline-suppressions-v1","suppressions":[]}`
	cases := []struct {
		name    string
		content string
		missing bool
		link    bool
		valid   bool
	}{
		{name: "clean", content: clean, valid: true},
		{name: "missing", missing: true},
		{name: "empty"},
		{name: "malformed", content: "{"},
		{name: "wrong schema", content: `{"schema":"other","suppressions":[]}`},
		{name: "missing schema", content: `{"suppressions":[]}`},
		{name: "wrong records type", content: `{"schema":"lopper-inline-suppressions-v1","suppressions":{}}`},
		{name: "extra field", content: `{"schema":"lopper-inline-suppressions-v1","suppressions":[],"ignore":true}`},
		{name: "two documents", content: clean + "\n" + clean},
		{name: "oversized", content: clean + strings.Repeat(" ", 131072)},
		{name: "too many records", content: `{"schema":"lopper-inline-suppressions-v1","suppressions":[` + strings.Repeat("{},", 100) + `{}]}`},
		{name: "symlink", content: clean, link: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "inline-suppressions.json")
			if !tc.missing {
				writeFile(t, path, tc.content)
			}
			if tc.link {
				link := filepath.Join(dir, "linked.json")
				if err := os.Symlink(path, link); err != nil {
					t.Fatalf("create evidence symlink: %v", err)
				}
				path = link
			}
			output, err := runShellCommand(dir, step.Run, map[string]string{"SUPPRESSIONS_FILE": path})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v, output:\n%s", tc.valid, err, output)
			}
		})
	}
}
