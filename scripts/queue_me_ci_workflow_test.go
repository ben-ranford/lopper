package scripts

import (
	"encoding/json"
	"os/exec"
	"path"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type queueCIManifestWorkflow struct {
	Name string            `json:"name"`
	Path string            `json:"path"`
	Jobs map[string]string `json:"jobs"`
}

type queueCIWorkflowConfig struct {
	Name string                      `yaml:"name"`
	Jobs map[string]queueCIJobConfig `yaml:"jobs"`
}

type queueCIJobConfig struct {
	Name     string `yaml:"name"`
	RunsOn   string `yaml:"runs-on"`
	Uses     string `yaml:"uses"`
	Strategy struct {
		Matrix map[string][]string `yaml:"matrix"`
	} `yaml:"strategy"`
}

var queueCIMatrixOSExpression = regexp.MustCompile(`\$\{\{\s*matrix\.os\s*\}\}`)

// This verifies the trusted admission manifest against executable workflow
// definitions, including matrix legs and jobs within local reusable workflows.
func TestQueueMeCIWorkflowManifestContract(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to inspect the trusted queue CI manifest")
	}
	const script = "process.stdout.write(JSON.stringify(require('./queue_me_ci').testables.WORKFLOWS))"
	command := exec.Command(node, "-e", script)
	command.Dir = repoPath(t, "scripts")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("read queue CI manifest: %v\n%s", err, output)
	}
	var workflows []queueCIManifestWorkflow
	if err := json.Unmarshal(output, &workflows); err != nil {
		t.Fatalf("parse queue CI manifest: %v", err)
	}
	byPath := make(map[string]queueCIManifestWorkflow, len(workflows))
	for _, workflow := range workflows {
		if _, exists := byPath[workflow.Path]; exists {
			t.Fatalf("duplicate trusted CI workflow %q", workflow.Path)
		}
		byPath[workflow.Path] = workflow
	}
	// These are the independently admitted workflow entry points, not a mirrored
	// job list. A missing workflow must fail even if its manifest entry is removed.
	paths := []string{".github/workflows/ci.yml", ".github/workflows/windows-runtime.yml"}
	if len(byPath) != len(paths) {
		t.Fatalf("trusted CI workflow count = %d, want %d", len(byPath), len(paths))
	}
	for _, workflowPath := range paths {
		t.Run(workflowPath, func(t *testing.T) {
			manifest, exists := byPath[workflowPath]
			if !exists {
				t.Fatalf("trusted CI manifest omits %s", workflowPath)
			}
			name, jobs := queueCIWorkflowJobs(t, workflowPath, make(map[string]bool))
			if manifest.Name != name {
				t.Errorf("workflow name = %q, trusted manifest has %q", name, manifest.Name)
			}
			if !reflect.DeepEqual(manifest.Jobs, jobs) {
				t.Errorf("trusted CI job manifest is stale; workflow defines %#v, manifest has %#v", jobs, manifest.Jobs)
			}
		})
	}
}

func queueCIWorkflowJobs(t *testing.T, workflowPath string, visiting map[string]bool) (string, map[string]string) {
	t.Helper()
	if visiting[workflowPath] {
		t.Fatalf("recursive local CI workflow %s", workflowPath)
	}
	visiting[workflowPath] = true
	defer delete(visiting, workflowPath)
	var workflow queueCIWorkflowConfig
	if err := yaml.Unmarshal([]byte(readConfig(t, workflowPath)), &workflow); err != nil {
		t.Fatalf("parse trusted CI workflow %s: %v", workflowPath, err)
	}
	if len(workflow.Jobs) == 0 {
		t.Fatalf("trusted CI workflow %s has no jobs", workflowPath)
	}
	jobs := make(map[string]string)
	for id, job := range workflow.Jobs {
		if job.Uses != "" {
			queueCIReusable(t, id, job, visiting, jobs)
			continue
		}
		for name, runner := range queueCIExpandedJob(t, id, job) {
			queueCIAddJob(t, jobs, name, runner)
		}
	}
	return workflow.Name, jobs
}

func queueCIReusable(t *testing.T, id string, job queueCIJobConfig, visiting map[string]bool, jobs map[string]string) {
	t.Helper()
	if !strings.HasPrefix(job.Uses, "./.github/workflows/") || job.RunsOn != "" || job.Strategy.Matrix != nil {
		t.Fatalf("CI job %s has an unsupported reusable workflow shape; extend the manifest contract explicitly", id)
	}
	workflowPath := strings.TrimPrefix(job.Uses, "./")
	if path.Clean(workflowPath) != workflowPath || strings.Contains(workflowPath, "${{") {
		t.Fatalf("CI job %s has a nonliteral reusable workflow path %q", id, job.Uses)
	}
	prefix := queueCIJobName(id, job)
	if strings.Contains(prefix, "${{") {
		t.Fatalf("CI job %s has an unsupported dynamic name %q", id, prefix)
	}
	_, nested := queueCIWorkflowJobs(t, workflowPath, visiting)
	for name, runner := range nested {
		queueCIAddJob(t, jobs, prefix+" / "+name, runner)
	}
}

func queueCIJobName(id string, job queueCIJobConfig) string {
	if job.Name != "" {
		return job.Name
	}
	return id
}

func queueCIExpandedJob(t *testing.T, id string, job queueCIJobConfig) map[string]string {
	t.Helper()
	platforms := []string{""}
	if matrix := job.Strategy.Matrix; matrix != nil {
		if len(matrix) != 1 || len(matrix["os"]) == 0 {
			t.Fatalf("CI job %s has an unsupported matrix; extend the manifest contract explicitly", id)
		}
		platforms = matrix["os"]
	}
	jobs := make(map[string]string)
	for _, platform := range platforms {
		if job.Strategy.Matrix != nil && platform == "" {
			t.Fatalf("CI job %s has an empty matrix platform", id)
		}
		name := queueCIMatrixOSExpression.ReplaceAllString(queueCIJobName(id, job), platform)
		if job.Name == "" && platform != "" {
			name += " (" + platform + ")"
		}
		runner := queueCIMatrixOSExpression.ReplaceAllString(job.RunsOn, platform)
		queueCIAddJob(t, jobs, name, runner)
	}
	return jobs
}

func queueCIAddJob(t *testing.T, jobs map[string]string, name, runner string) {
	t.Helper()
	if name == "" || runner == "" || strings.Contains(name+runner, "${{") {
		t.Fatalf("unsupported CI job name/runner %q/%q; extend the manifest contract explicitly", name, runner)
	}
	if _, duplicate := jobs[name]; duplicate {
		t.Fatalf("duplicate CI job display name %q", name)
	}
	jobs[name] = runner
}
