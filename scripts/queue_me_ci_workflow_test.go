package scripts

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type queueCIManifestWorkflow struct {
	Name               string                     `json:"name"`
	Path               string                     `json:"path"`
	Jobs               map[string]string          `json:"jobs"`
	SuppressionLocator *queueCISuppressionLocator `json:"suppressionLocator"`
}

type queueCISuppressionLocator struct {
	JobID       string `json:"jobID"`
	NamePrefix  string `json:"namePrefix"`
	RunnerLabel string `json:"runnerLabel"`
}

type queueCIWorkflowConfig struct {
	Name string                      `yaml:"name"`
	Jobs map[string]queueCIJobConfig `yaml:"jobs"`
}

type queueCIJobConfig struct {
	Name        string            `yaml:"name"`
	RunsOn      string            `yaml:"runs-on"`
	Uses        string            `yaml:"uses"`
	Needs       yaml.Node         `yaml:"needs"`
	If          string            `yaml:"if"`
	Permissions yaml.Node         `yaml:"permissions"`
	Outputs     map[string]string `yaml:"outputs"`
	Strategy    struct {
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
			name, jobs, locator := queueCIWorkflowJobs(t, workflowPath, make(map[string]bool))
			if manifest.Name != name {
				t.Errorf("workflow name = %q, trusted manifest has %q", name, manifest.Name)
			}
			if !reflect.DeepEqual(manifest.Jobs, jobs) {
				t.Errorf("trusted CI job manifest is stale; workflow defines %#v, manifest has %#v", jobs, manifest.Jobs)
			}
			if !reflect.DeepEqual(manifest.SuppressionLocator, locator) {
				t.Errorf("trusted CI locator manifest is stale; workflow defines %#v, manifest has %#v", locator, manifest.SuppressionLocator)
			}
		})
	}
}

func queueCIWorkflowJobs(t *testing.T, workflowPath string, visiting map[string]bool) (string, map[string]string, *queueCISuppressionLocator) {
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
	var locator *queueCISuppressionLocator
	for id, job := range workflow.Jobs {
		if id == "suppression-evidence" {
			var err error
			locator, err = queueCILocatorManifest(workflowPath, id, job, workflow.Jobs["verify-checks"])
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		if job.Uses != "" {
			queueCIReusable(t, id, job, visiting, jobs)
			continue
		}
		for name, runner := range queueCIExpandedJob(t, id, job) {
			queueCIAddJob(t, jobs, name, runner)
		}
	}
	return workflow.Name, jobs, locator
}

func queueCILocatorManifest(workflowPath, id string, job, producer queueCIJobConfig) (*queueCISuppressionLocator, error) {
	const artifactOutput = "${{ needs.verify-checks.outputs.pr_report_artifact_id }}"
	// This is the one reviewed dynamic display name. Do not admit arbitrary
	// expressions or treat every job with a similar prefix as a locator.
	if workflowPath != ".github/workflows/ci.yml" || id != "suppression-evidence" ||
		job.Name != "suppression-artifact-"+artifactOutput || job.RunsOn != "ubuntu-latest" ||
		job.Uses != "" || job.Strategy.Matrix != nil || job.Needs.Kind != yaml.ScalarNode ||
		job.Needs.Value != "verify-checks" || job.If != "${{ github.event_name == 'pull_request' }}" ||
		job.Permissions.Kind != yaml.MappingNode || len(job.Permissions.Content) != 0 ||
		producer.Outputs["pr_report_artifact_id"] != "${{ steps.upload_pr_report_inputs.outputs.artifact-id }}" {
		return nil, fmt.Errorf("ci job %s has an unsupported suppression locator shape; extend the manifest contract explicitly", id)
	}
	return &queueCISuppressionLocator{JobID: id, NamePrefix: "suppression-artifact-", RunnerLabel: job.RunsOn}, nil
}

func TestQueueMeCISuppressionLocatorContractRejectsOtherDynamicShapes(t *testing.T) {
	const source = `jobs:
  verify-checks:
    outputs:
      pr_report_artifact_id: ${{ steps.upload_pr_report_inputs.outputs.artifact-id }}
  suppression-evidence:
    name: suppression-artifact-${{ needs.verify-checks.outputs.pr_report_artifact_id }}
    needs: verify-checks
    if: ${{ github.event_name == 'pull_request' }}
    runs-on: ubuntu-latest
    permissions: {}
`
	tests := []struct {
		name, from, to string
	}{
		{"different expression", "needs.verify-checks.outputs.pr_report_artifact_id", "github.event.pull_request.title"},
		{"different producer", "needs.verify-checks.outputs.pr_report_artifact_id", "needs.other.outputs.pr_report_artifact_id"},
		{"literal name", "${{ needs.verify-checks.outputs.pr_report_artifact_id }}", "123"},
		{"different prefix", "name: suppression-artifact-", "name: extra-suppression-artifact-"},
		{"extra expression", "name: suppression-artifact-", "name: ${{ github.actor }}-suppression-artifact-"},
		{"different dependency", "needs: verify-checks", "needs: verify"},
		{"different condition", "github.event_name == 'pull_request'", "always()"},
		{"different runner", "runs-on: ubuntu-latest", "runs-on: windows-latest"},
		{"dynamic runner", "runs-on: ubuntu-latest", "runs-on: ${{ matrix.os }}"},
		{"write permission", "permissions: {}", "permissions: {contents: write}"},
		{"inherited permissions", "    permissions: {}\n", ""},
		{"different upload", "steps.upload_pr_report_inputs.outputs.artifact-id", "steps.untrusted.outputs.artifact-id"},
		{"reusable job", "    permissions: {}", "    permissions: {}\n    uses: ./.github/workflows/ci-tests.yml"},
		{"matrix locator", "    permissions: {}", "    permissions: {}\n    strategy:\n      matrix:\n        os: [ubuntu-latest]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var workflow queueCIWorkflowConfig
			if err := yaml.Unmarshal([]byte(strings.Replace(source, tt.from, tt.to, 1)), &workflow); err != nil {
				t.Fatalf("parse mutated locator fixture: %v", err)
			}
			if _, err := queueCILocatorManifest(".github/workflows/ci.yml", "suppression-evidence", workflow.Jobs["suppression-evidence"], workflow.Jobs["verify-checks"]); err == nil {
				t.Fatal("unsupported dynamic locator shape was admitted")
			}
		})
	}
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
	_, nested, locator := queueCIWorkflowJobs(t, workflowPath, visiting)
	if locator != nil {
		t.Fatalf("CI job %s has an unsupported nested suppression locator", id)
	}
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
