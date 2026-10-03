package scripts

import (
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/gitexec"
	"gopkg.in/yaml.v3"
)

type reuseWorkflowConfig struct {
	On          map[string]map[string][]string `yaml:"on"`
	Permissions map[string]string              `yaml:"permissions"`
	Jobs        map[string]workflowJobConfig   `yaml:"jobs"`
}

func TestReuseWorkflowReceivesEvidenceInvalidationEvents(t *testing.T) {
	t.Parallel()
	var workflow reuseWorkflowConfig
	readYAMLConfig(t, ".github/workflows/reuse-check.yml", &workflow)
	for event, actions := range map[string][]string{
		"pull_request_target": {"opened", "synchronize", "reopened", "edited", "ready_for_review"},
		"issue_comment":       {"created", "edited"},
		"workflow_run":        {"requested", "in_progress", "completed"},
	} {
		for _, action := range actions {
			if !slices.Contains(workflow.On[event]["types"], action) {
				t.Fatalf("controller must receive %s/%s to reconsider revision or review evidence", event, action)
			}
		}
	}
	if !slices.Equal(workflow.On["push"]["branches"], []string{"main"}) ||
		!slices.Equal(workflow.On["workflow_run"]["workflows"], []string{"Reuse review signal", "ci"}) {
		t.Fatal("base invalidation and review wakeups must come from the selected protected sources")
	}
	invalidate := workflowJobByName(t, workflow.Jobs, "invalidate-base")
	if invalidate.If != "${{ github.event_name == 'push' }}" || invalidate.Permissions["statuses"] != "write" {
		t.Fatal("main updates must have a trusted writer to invalidate old base evidence")
	}
}

func TestReuseWorkflowDefersPendingCIWithoutFailedHeadChecks(t *testing.T) {
	t.Parallel()
	// Execute the real publisher with a completed proof overtaken by running CI.
	// Kept in this Go test so the regression CLI can overlay it on the base
	// without depending on a candidate-only Python test module.
	const script = `
import copy, json, os, sys, tempfile
from pathlib import Path
from unittest.mock import patch
sys.path.insert(0, sys.argv[1])
import reuse_event as event
prefix = '/repos/ben-ranford/lopper'
head, base = 'b' * 40, 'a' * 40
snapshot = dict(version=1, repository='ben-ranford/lopper', repository_id=10,
    head_repository_id=20, pull_number=12, head=head, base=base, base_ref='main')
pull = dict(number=12, state='open',
    head=dict(sha=head, repo=dict(id=20)),
    base=dict(sha=base, ref='main', repo=dict(id=10, full_name='ben-ranford/lopper')))
run = dict(id=41, workflow_id=6, run_attempt=3, path='.github/workflows/ci.yml',
    name='ci', event='pull_request', status='in_progress', conclusion=None,
    head_sha=head, repository=dict(id=10), head_repository=dict(id=20), pull_requests=[pull])
receipt = dict(headSHA=head, baseSHA=base, runId=41, runAttempt=2,
    artifactId=101, suppressionCount=0)
result = dict(version=1, snapshot=snapshot, candidate='c' * 40,
    detector_exit=0, policy_paths=[])
class API:
    def __init__(self, terminal):
        self.posts = []
        current = copy.deepcopy(run)
        if terminal:
            current.update(status='completed', conclusion='failure')
        self.data = {
            prefix: dict(id=10, full_name='ben-ranford/lopper', default_branch='main'),
            prefix + '/pulls/12': pull,
            prefix + '/git/ref/heads/main': dict(object=dict(sha=base)),
            prefix + '/actions/workflows/ci.yml': dict(id=6, path='.github/workflows/ci.yml', name='ci'),
            prefix + '/actions/runs/41': current,
            prefix + '/actions/workflows/ci.yml/runs?per_page=100&page=1&head_sha=' + head + '&event=pull_request':
                dict(total_count=1, workflow_runs=[current]),
        }
    def request(self, path, data=None):
        if data is not None:
            assert path == prefix + '/statuses/' + head
            self.posts.append(data['state'])
            return {}
        return copy.deepcopy(self.data[path])
    def pages(self, path):
        assert path == prefix + '/pulls/12/reviews'
        return []
for terminal in (False, True):
    api = API(terminal)
    failed = False
    try:
        event.publish(api, snapshot, result, 'success', ['ben-ranford'], receipt, 'success')
    except event.EventError:
        failed = True
    assert failed == terminal, ('authenticated running CI must defer; terminal failure must fail', terminal, api.posts)
    assert api.posts == (['failure'] if terminal else ['pending']), api.posts
# Separate blocked, pending and completion invocations use fresh controller state. The
# completed CI supplies its new full receipt; the earlier controller is not rerun.
for readiness in ('blocked', 'waiting', 'ready'):
    api = API(False)
    if readiness == 'blocked':
        api.data[prefix + '/actions/runs/41'].update(status='completed', conclusion='cancelled', run_attempt=2)
    if readiness == 'ready':
        api.data[prefix + '/actions/runs/41'].update(status='completed', conclusion='success',
            run_started_at='2026-09-30T12:00:00Z', updated_at='2026-09-30T12:10:00Z')
        api.data[prefix + '/actions/artifacts/101'] = dict(id=101, name='pr-report-inputs-12',
            expired=False, size_in_bytes=100, created_at='2026-09-30T12:05:00Z',
            digest='sha256:' + 'a' * 64,
            workflow_run=dict(id=41, head_sha=head, repository_id=10, head_repository_id=20))
    with tempfile.TemporaryDirectory() as directory:
        root = Path(directory).resolve()
        payload = root / 'event.json'
        payload.write_text(json.dumps(dict(repository=dict(id=10), action='synchronize',
            number=12, pull_request=pull)))
        output = root / 'output'
        environment = dict(GITHUB_EVENT_PATH=str(payload), GITHUB_EVENT_NAME='pull_request_target',
            GITHUB_REPOSITORY='ben-ranford/lopper', GITHUB_REPOSITORY_ID='10',
            RUNNER_TEMP=str(root), GITHUB_OUTPUT=str(output), GH_TOKEN='unused-test-token')
        with patch.dict(os.environ, environment), patch.object(event, 'GitHub', return_value=api):
            assert event.main(['prepare', '--snapshot', str(root / 'reuse-snapshot.json'),
                '--refresh-actor', 'ben-ranford']) == 0
        outputs = dict(line.split('=', 1) for line in output.read_text().splitlines())
        assert json.loads(outputs['snapshot']) == snapshot
        assert outputs['readiness'] == readiness, outputs
        assert api.posts == (['pending', 'failure'] if readiness == 'blocked' else ['pending']), api.posts
        if readiness == 'ready':
            event.publish(api, snapshot, result, 'success', ['ben-ranford'], dict(receipt, runAttempt=3), 'success')
            assert api.posts == ['pending', 'success'], api.posts
`
	command := exec.Command("python3", "-B", "-c", script, repoPath(t, "scripts"))
	command.Env = gitexec.SanitizedEnv()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("real producer pending-CI lifecycle regression: %v\n%s", err, output)
	}
	var workflow reuseWorkflowConfig
	readYAMLConfig(t, ".github/workflows/reuse-check.yml", &workflow)
	for _, phase := range []string{"analyze", "suppression"} {
		if workflow.Jobs[phase].If != "${{ needs.prepare.outputs.readiness == 'ready' }}" {
			t.Fatalf("actual %s workflow gate would start proof before the tested ready output", phase)
		}
	}
	if workflow.Jobs["publish"].If != "${{ always() && needs.prepare.outputs.snapshot != '' && needs.prepare.outputs.readiness != 'waiting' && needs.prepare.outputs.readiness != 'blocked' }}" {
		t.Fatal("actual publisher gate would turn the tested waiting snapshot into a failed head check")
	}
}

func TestReuseWorkflowProtectsExecutableSourceAndCredentials(t *testing.T) {
	t.Parallel()
	var workflow reuseWorkflowConfig
	readYAMLConfig(t, ".github/workflows/reuse-check.yml", &workflow)
	if workflow.Permissions == nil || len(workflow.Permissions) != 0 {
		t.Fatal("controller must deny permissions by default and grant them per job")
	}
	prepare := workflowJobByName(t, workflow.Jobs, "prepare")
	for _, field := range []string{"snapshot", "base", "head", "pull_number", "readiness"} {
		if prepare.Outputs[field] != "${{ steps.prepare.outputs."+field+" }}" {
			t.Fatalf("prepared %s must retain its own trusted output binding", field)
		}
	}
	for event := range workflow.On {
		if !slices.Contains([]string{"pull_request_target", "workflow_run", "issue_comment", "push"}, event) {
			t.Fatalf("controller cannot execute from candidate-owned event %q", event)
		}
	}
	for name, job := range workflow.Jobs {
		assertReuseJobProtectedCheckout(t, name, job)
		assertReuseJobPermissionScope(t, name, job)
	}
	assertReuseAnalysisIsolation(t, workflow)
	if strings.Contains(readConfig(t, ".github/workflows/reuse-check.yml"), "secrets.") {
		t.Fatal("reuse control and analysis must not receive repository secrets")
	}
}

func assertReuseJobProtectedCheckout(t *testing.T, name string, job workflowJobConfig) {
	t.Helper()
	if name == "reuse-check" || job.Name == "reuse-check" {
		t.Fatal("reuse-check is reserved for the explicit commit status, not a native job check")
	}
	checkouts := 0
	for index, step := range job.Steps {
		if !strings.HasPrefix(step.Uses, "actions/checkout@") {
			continue
		}
		checkouts++
		if index != 0 {
			t.Fatalf("%s must begin with the context checkout before pinning its protected source", name)
		}
		if !maps.Equal(step.With, map[string]string{"fetch-depth": "0", "persist-credentials": "false"}) {
			t.Fatalf("%s must retain the default repository/ref, full history, and no persisted credentials: %v", name, step.With)
		}
	}
	if checkouts != 1 {
		t.Fatalf("%s needs one protected source checkout, got %d", name, checkouts)
	}
	assertReuseJobPinsProtectedRevision(t, name, job)
}

func assertReuseJobPinsProtectedRevision(t *testing.T, name string, job workflowJobConfig) {
	t.Helper()
	if len(job.Steps) < 2 || job.Steps[1].Name != "Pin immutable protected source" {
		t.Fatalf("%s must pin the revision immediately after checkout, before any other executable step", name)
	}
	wantRevision := "${{ github.workflow_sha }}"
	if name == "analyze" || name == "suppression" {
		wantRevision = "${{ needs.prepare.outputs.base }}"
	}
	pin := job.Steps[1]
	if !maps.Equal(pin.Env, map[string]string{"REUSE_SOURCE_SHA": wantRevision}) {
		t.Fatalf("%s must select its bound immutable source revision: %v", name, pin.Env)
	}
	if !strings.HasPrefix(pin.Run, "python3 -I -S -B - <<'PY'\n") {
		t.Fatalf("%s must bootstrap with isolated inline Python before importing repository code", name)
	}
}

func assertReuseJobPermissionScope(t *testing.T, name string, job workflowJobConfig) {
	t.Helper()
	for permission, access := range job.Permissions {
		statusPublisher := permission == "statuses" && access == "write" && slices.Contains([]string{"prepare", "publish", "invalidate-base"}, name)
		if access != "read" && !statusPublisher {
			t.Fatalf("unexpected %s permission %s:%s", name, permission, access)
		}
	}
}

func assertReuseAnalysisIsolation(t *testing.T, workflow reuseWorkflowConfig) {
	t.Helper()
	analyze := workflowJobByName(t, workflow.Jobs, "analyze")
	if analyze.Outputs["result"] != "${{ steps.analyze.outputs.result }}" {
		t.Fatal("analysis evidence must come from the protected analyzer step")
	}
	readSnapshot := workflowStepByName(t, workflow.Jobs, "analyze", "Read bound snapshot")
	if readSnapshot.Env["REUSE_SNAPSHOT"] != "${{ needs.prepare.outputs.snapshot }}" {
		t.Fatal("analysis must receive the same prepared snapshot as publication")
	}
	if !maps.Equal(analyze.Permissions, map[string]string{"contents": "read", "pull-requests": "read"}) {
		t.Fatalf("analysis permissions must remain read-only: %v", analyze.Permissions)
	}
	for _, step := range analyze.Steps {
		if strings.HasPrefix(step.Uses, "actions/cache") {
			t.Fatal("candidate analysis must not restore or publish a shared cache")
		}
	}
	setup := workflowStepByName(t, workflow.Jobs, "analyze", "Setup protected Go toolchain")
	if setup.With["go-version-file"] != "go.mod" || setup.With["cache"] != "false" {
		t.Fatal("Go setup must use the protected module without shared caching")
	}
}

func TestReuseWorkflowSuppressionEvidenceUsesBoundReadonlySource(t *testing.T) {
	t.Parallel()
	var workflow reuseWorkflowConfig
	readYAMLConfig(t, ".github/workflows/reuse-check.yml", &workflow)
	job := workflowJobByName(t, workflow.Jobs, "suppression")
	assertWorkflowJobPermissions(t, job, "suppression verifier", map[string]string{
		"actions": "read", "contents": "read", "pull-requests": "read",
	})
	if !slices.Equal(job.Needs, workflowJobNeeds{"prepare"}) || job.Outputs["receipt"] != "${{ steps.suppression.outputs.receipt }}" {
		t.Fatal("suppression verification must use the prepared revision and expose its own verified receipt")
	}
	step := workflowStepByName(t, workflow.Jobs, "suppression", "Verify exact CI suppression evidence")
	if step.ID != "suppression" || !strings.HasPrefix(step.Uses, "actions/github-script@") {
		t.Fatal("suppression receipt must come from the protected GitHub API verifier")
	}
	assertWorkflowStepEnv(t, step, "suppression verifier", map[string]string{
		"REUSE_SNAPSHOT": "${{ needs.prepare.outputs.snapshot }}", "GH_TOKEN": "${{ github.token }}",
	})
	script := step.With["script"]
	for _, binding := range []string{
		"require('./scripts/reuse_suppression.js')", "snapshot: JSON.parse(process.env.REUSE_SNAPSHOT)",
		"core.setOutput('receipt', JSON.stringify(receipt))",
		"if (!verify.isDeferred(error)) throw error", "core.setOutput('deferred', JSON.stringify(error.deferred))",
	} {
		if !strings.Contains(script, binding) {
			t.Fatalf("suppression verifier must retain protected source and receipt binding %q", binding)
		}
	}
	if strings.Contains(script, "${{") {
		t.Fatal("suppression verifier must receive revision data through the environment, not executable expressions")
	}
}

func TestReuseWorkflowPublishesFailuresAndPreservesAnalysisOutcome(t *testing.T) {
	t.Parallel()
	var workflow reuseWorkflowConfig
	readYAMLConfig(t, ".github/workflows/reuse-check.yml", &workflow)
	publish := workflowJobByName(t, workflow.Jobs, "publish")
	if !slices.Equal(publish.Needs, workflowJobNeeds{"prepare", "analyze", "suppression"}) {
		t.Fatal("publication must wait for revision binding and both read-only evidence jobs")
	}
	if publish.If != "${{ always() && needs.prepare.outputs.snapshot != '' && needs.prepare.outputs.readiness != 'waiting' && needs.prepare.outputs.readiness != 'blocked' }}" {
		t.Fatal("publication must skip authenticated prepare waiting, but handle every real evidence failure")
	}
	for _, name := range []string{"analyze", "suppression"} {
		if workflow.Jobs[name].If != "${{ needs.prepare.outputs.readiness == 'ready' }}" {
			t.Fatalf("%s must wait for authenticated current CI before attempting proof", name)
		}
	}
	assertWorkflowJobPermissions(t, publish, "reuse publisher", map[string]string{
		"actions": "read", "contents": "read", "pull-requests": "read", "statuses": "write",
	})
	assertReusePublicationSerializesCorrections(t)
	for _, name := range []string{"analyze", "suppression", "publish"} {
		assertReuseJobPreservesFailures(t, name, workflow.Jobs[name])
	}
	assertReusePublicationEvidenceBindings(t, workflow)
}

func assertReusePublicationEvidenceBindings(t *testing.T, workflow reuseWorkflowConfig) {
	t.Helper()
	step := workflowStepByName(t, workflow.Jobs, "publish", "Revalidate review and publish exact-head result")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "actual analysis conclusion", got: step.Env["REUSE_ANALYSIS_RESULT"], want: "${{ needs.analyze.result }}"},
		{label: "actual suppression conclusion", got: step.Env["REUSE_SUPPRESSION_RESULT"], want: "${{ needs.suppression.result }}"},
	})
	assertWorkflowStepRunContainsAll(t, step, "reuse publisher", []string{
		`--analysis-result "$REUSE_ANALYSIS_RESULT"`, `--suppression-result "$REUSE_SUPPRESSION_RESULT"`,
		`--suppression "$RUNNER_TEMP/reuse-suppression.json"`,
		`--deferred "$RUNNER_TEMP/reuse-deferred.json"`,
	})
	evidence := workflowStepByName(t, workflow.Jobs, "publish", "Read analysis evidence")
	if !maps.Equal(evidence.Env, map[string]string{
		"REUSE_RESULT": "${{ needs.analyze.outputs.result }}", "REUSE_SNAPSHOT": "${{ needs.prepare.outputs.snapshot }}",
		"REUSE_SUPPRESSION": "${{ needs.suppression.outputs.receipt }}",
		"REUSE_DEFERRED":    "${{ needs.suppression.outputs.deferred }}",
	}) {
		t.Fatal("publisher evidence must come from this run's bound snapshot and evidence jobs")
	}
}

func assertReusePublicationSerializesCorrections(t *testing.T) {
	t.Helper()
	var document yaml.Node
	readYAMLConfig(t, ".github/workflows/reuse-check.yml", &document)
	jobNode := workflowYAMLMappingValue(workflowYAMLMappingValue(&document, "jobs"), "publish")
	concurrency := workflowYAMLMappingValue(jobNode, "concurrency")
	group := workflowYAMLMappingValue(concurrency, "group")
	cancel := workflowYAMLMappingValue(concurrency, "cancel-in-progress")
	if group == nil || group.Value != "reuse-publish-${{ needs.prepare.outputs.pull_number }}" ||
		cancel == nil || cancel.Value != "false" {
		t.Fatal("all event types must serialize publication for the same PR without cancelling its correction")
	}
}

func assertReuseJobPreservesFailures(t *testing.T, name string, job workflowJobConfig) {
	t.Helper()
	if job.ContinueOnError {
		t.Fatalf("%s must not convert a failed job into success", name)
	}
	for _, step := range job.Steps {
		if step.ContinueOnError {
			t.Fatalf("%s step %q must retain its failing outcome", name, step.Name)
		}
	}
}

func TestReuseWorkflowEvidenceTransportPreservesLiteralData(t *testing.T) {
	t.Parallel()
	var workflow reuseWorkflowConfig
	readYAMLConfig(t, ".github/workflows/reuse-check.yml", &workflow)
	for _, testCase := range []struct{ name, result, receipt string }{
		{"missing evidence", "", ""},
		{"literal evidence", `{"policy_paths":["$(touch injected)","` + "`touch injected`" + `"]}`, `{"receipt":"$(touch injected)\nsecond line"}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			assertReuseEvidenceTransport(t, workflow, testCase.result, testCase.receipt)
		})
	}
}

func assertReuseEvidenceTransport(t *testing.T, workflow reuseWorkflowConfig, result, receipt string) {
	t.Helper()
	directory := t.TempDir()
	snapshot := `{"repository":"example/repo","literal":"$(touch injected)\nsecond line"}`
	environment := map[string]string{"RUNNER_TEMP": directory, "REUSE_SNAPSHOT": snapshot, "REUSE_RESULT": result, "REUSE_SUPPRESSION": receipt, "REUSE_DEFERRED": receipt}
	for _, target := range []struct{ job, step string }{
		{"analyze", "Read bound snapshot"}, {"publish", "Read analysis evidence"},
	} {
		step := workflowStepByName(t, workflow.Jobs, target.job, target.step)
		if output, err := runShellCommand(directory, step.Run, environment); err != nil {
			t.Fatalf("%s failed: %v\n%s", target.step, err, output)
		}
		assertReuseTransportedFile(t, directory, "reuse-snapshot.json", snapshot)
	}
	assertReuseOptionalEvidenceFile(t, directory, "reuse-result.json", result)
	assertReuseOptionalEvidenceFile(t, directory, "reuse-suppression.json", receipt)
	assertReuseOptionalEvidenceFile(t, directory, "reuse-deferred.json", receipt)
	if _, err := os.Stat(filepath.Join(directory, "injected")); !os.IsNotExist(err) {
		t.Fatal("transport executed candidate-derived text")
	}
}

func assertReuseOptionalEvidenceFile(t *testing.T, directory, name, value string) {
	t.Helper()
	if value == "" {
		value = "{}"
	}
	assertReuseTransportedFile(t, directory, name, value)
}

func assertReuseTransportedFile(t *testing.T, directory, name, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil || string(got) != want {
		t.Fatalf("%s changed during transport: %q (%v)", name, got, err)
	}
}

func TestReuseWorkflowDriverArgumentsPreservePathsAndJobOutcome(t *testing.T) {
	t.Parallel()
	var workflow reuseWorkflowConfig
	readYAMLConfig(t, ".github/workflows/reuse-check.yml", &workflow)
	for _, target := range []struct{ job, step, mode string }{
		{"invalidate-base", "Invalidate previous base evidence", "invalidate-base"},
		{"prepare", "Bind current revisions and invalidate prior result", "prepare"},
		{"analyze", "Analyze exact prospective merge", "analyze"},
		{"publish", "Revalidate review and publish exact-head result", "publish"},
	} {
		t.Run(target.mode, func(t *testing.T) {
			step := workflowStepByName(t, workflow.Jobs, target.job, target.step)
			assertReuseDriverArguments(t, step, target.mode)
		})
	}
}

func assertReuseDriverArguments(t *testing.T, step workflowStepConfig, mode string) {
	t.Helper()
	directory := t.TempDir()
	capture := filepath.Join(directory, "arguments")
	interpreter := filepath.Join(directory, "python3")
	if err := os.WriteFile(interpreter, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$REUSE_CAPTURE\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(directory, "runner temp")
	output, err := runShellCommand(directory, step.Run, map[string]string{
		"PATH":        directory + string(os.PathListSeparator) + os.Getenv("PATH"),
		"RUNNER_TEMP": temporary, "REUSE_CAPTURE": capture, "REUSE_ANALYSIS_RESULT": "cancelled",
		"REUSE_SUPPRESSION_RESULT": "failure",
	})
	if err != nil {
		t.Fatalf("driver invocation failed: %v\n%s", err, output)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	arguments := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	prefix := []string{"-E", "-S", "-B", "scripts/reuse_event.py", mode}
	if len(arguments) < len(prefix) || !slices.Equal(arguments[:len(prefix)], prefix) {
		t.Fatalf("unexpected driver executable or mode: %q", arguments)
	}
	assertReuseDriverOptions(t, arguments[len(prefix):], temporary)
}

func assertReuseDriverOptions(t *testing.T, options []string, temporary string) {
	t.Helper()
	if len(options)%2 != 0 {
		t.Fatalf("shell split a controller argument: %q", options)
	}
	for index := 0; index < len(options); index += 2 {
		want, checked := map[string]string{
			"--snapshot": filepath.Join(temporary, "reuse-snapshot.json"),
			"--result":   filepath.Join(temporary, "reuse-result.json"), "--analysis-result": "cancelled",
			"--suppression": filepath.Join(temporary, "reuse-suppression.json"), "--suppression-result": "failure",
			"--deferred": filepath.Join(temporary, "reuse-deferred.json"),
		}[options[index]]
		if checked && options[index+1] != want {
			t.Fatalf("%s changed in shell transport: %q, want %q", options[index], options[index+1], want)
		}
	}
}

func TestReuseWorkflowArtifactLocatorHasNoExecutionAuthority(t *testing.T) {
	t.Parallel()
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	job := workflowJobByName(t, workflow.Jobs, "suppression-evidence")
	assertWorkflowJobHasExplicitEmptyPermissions(t, job, "suppression artifact locator")
	assertWorkflowJobEnvEmpty(t, job, "suppression artifact locator")
	assertWorkflowJobOmitsCheckout(t, job, "suppression artifact locator")
	if !slices.Equal(job.Needs, workflowJobNeeds{"verify-checks"}) || len(job.Steps) != 1 {
		t.Fatal("artifact locator must be a single isolated step after the producer")
	}
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "direct artifact locator", got: job.Name, want: "suppression-artifact-${{ needs.verify-checks.outputs.pr_report_artifact_id }}"},
		{label: "artifact locator event", got: job.If, want: "${{ github.event_name == 'pull_request' }}"},
	})
	step := workflowStepByName(t, workflow.Jobs, "suppression-evidence", "Expose exact producer artifact locator")
	assertWorkflowStepEnv(t, step, "artifact locator", map[string]string{
		"ARTIFACT_ID": "${{ needs.verify-checks.outputs.pr_report_artifact_id }}",
	})
	if step.Uses != "" || len(step.With) != 0 || strings.Contains(step.Run, "${{") {
		t.Fatal("artifact locator cannot invoke actions or interpolate PR data into executable code")
	}
	if !strings.HasPrefix(step.Run, "python3 -I -S -B - <<'PYTHON'\n") {
		t.Fatal("artifact locator must validate data with isolated inline Python")
	}
	assertReuseJobPreservesFailures(t, "suppression-evidence", job)
}

func TestReuseWorkflowArtifactLocatorValidatesLiteralIDs(t *testing.T) {
	t.Parallel()
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	step := workflowStepByName(t, workflow.Jobs, "suppression-evidence", "Expose exact producer artifact locator")
	for _, artifactID := range []string{"7", "123456789", "", "0", "-1", "01", " 7", "7\n8", "$(touch injected)", "`touch injected`"} {
		t.Run(artifactID, func(t *testing.T) {
			assertReuseArtifactLocatorID(t, step, artifactID, artifactID == "7" || artifactID == "123456789")
		})
	}
}

func assertReuseArtifactLocatorID(t *testing.T, step workflowStepConfig, artifactID string, wantSuccess bool) {
	t.Helper()
	directory := t.TempDir()
	output, err := runShellCommand(directory, step.Run, map[string]string{"ARTIFACT_ID": artifactID})
	if (err == nil) != wantSuccess {
		t.Fatalf("artifact ID %q success = %t, want %t: %v\n%s", artifactID, err == nil, wantSuccess, err, output)
	}
	if _, err := os.Stat(filepath.Join(directory, "injected")); !os.IsNotExist(err) {
		t.Fatal("artifact locator executed artifact ID content")
	}
}

func TestReuseReviewSignalHasNoCandidateExecutionOrWriteAuthority(t *testing.T) {
	t.Parallel()
	var workflow reuseWorkflowConfig
	readYAMLConfig(t, ".github/workflows/reuse-review-signal.yml", &workflow)
	if workflow.Permissions == nil || len(workflow.Permissions) != 0 || len(workflow.On) != 1 {
		t.Fatal("review wakeups need no token permissions or unrelated triggers")
	}
	types := workflow.On["pull_request_review"]["types"]
	for _, action := range []string{"submitted", "edited", "dismissed"} {
		if !slices.Contains(types, action) {
			t.Fatalf("review wakeups must process %s so authorization changes are reconsidered", action)
		}
	}
	if len(workflow.Jobs) != 1 {
		t.Fatal("signal workflow must remain an isolated wakeup producer")
	}
	job := workflowJobByName(t, workflow.Jobs, "signal")
	assertWorkflowJobOmitsCheckout(t, job, "review signal")
	if job.If != "${{ github.event.review.user.login == 'ben-ranford' }}" {
		t.Fatal("owner review changes must wake analysis even when another maintainer dismisses the review")
	}
	if job.Name == "reuse-check" {
		t.Fatal("review signal must not impersonate the explicit reuse-check commit status")
	}
	if len(job.Permissions) != 0 {
		t.Fatal("signal job cannot override its no-authority permission scope")
	}
	assertReuseSignalStepsAreIsolated(t, job)
	upload := workflowStepByName(t, workflow.Jobs, "signal", "Upload review wakeup")
	if upload.With["name"] != "reuse-review-wakeup-${{ github.run_id }}-${{ github.run_attempt }}" ||
		upload.With["path"] != "reuse-review-wakeup.json" || upload.With["if-no-files-found"] != "error" {
		t.Fatal("wakeup upload must select its single record and bind the originating run attempt")
	}
}

func assertReuseSignalStepsAreIsolated(t *testing.T, job workflowJobConfig) {
	t.Helper()
	runSteps := 0
	for _, step := range job.Steps {
		if step.Run != "" {
			runSteps++
		}
		if step.Uses != "" && !strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
			t.Fatalf("signal must not execute candidate actions or fetch source: %q", step.Uses)
		}
	}
	if runSteps != 1 {
		t.Fatal("signal should execute only its inline bounded-record producer")
	}
}

func TestReuseReviewWakeupEmitsOnlyValidatedPullNumber(t *testing.T) {
	t.Parallel()
	var workflow reuseWorkflowConfig
	readYAMLConfig(t, ".github/workflows/reuse-review-signal.yml", &workflow)
	step := workflowStepByName(t, workflow.Jobs, "signal", "Record review wakeup")
	for _, number := range []any{7, 0, -1, true, "7", nil, map[string]any{"number": 7}} {
		assertReuseWakeupForNumber(t, step, number)
	}
}

func assertReuseWakeupForNumber(t *testing.T, step workflowStepConfig, number any) {
	t.Helper()
	directory := t.TempDir()
	event, err := json.Marshal(map[string]any{
		"pull_request": map[string]any{"number": number, "title": "$(touch injected)"},
		"review":       map[string]any{"body": "agent-reviewed: $(touch injected)", "state": "APPROVED"},
	})
	if err != nil {
		t.Fatal(err)
	}
	eventPath := filepath.Join(directory, "event.json")
	if err := os.WriteFile(eventPath, event, 0o600); err != nil {
		t.Fatal(err)
	}
	output, runErr := runShellCommand(directory, step.Run, map[string]string{"GITHUB_EVENT_PATH": eventPath})
	path := filepath.Join(directory, "reuse-review-wakeup.json")
	if number != 7 {
		assertReuseInvalidWakeupRejected(t, number, path, output, runErr)
		return
	}
	if runErr != nil {
		t.Fatalf("valid PR number failed: %v\n%s", runErr, output)
	}
	assertReuseWakeupRecord(t, path)
	if _, err := os.Stat(filepath.Join(directory, "injected")); !os.IsNotExist(err) {
		t.Fatal("review wakeup executed event content")
	}
}

func assertReuseInvalidWakeupRejected(t *testing.T, number any, path, output string, runErr error) {
	t.Helper()
	if runErr == nil {
		t.Fatalf("invalid PR number %#v produced a successful wakeup: %s", number, output)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid PR number %#v left a usable wakeup", number)
	}
}

func assertReuseWakeupRecord(t *testing.T, path string) {
	t.Helper()
	record, err := os.ReadFile(path)
	if err != nil || len(record) > 128 {
		t.Fatalf("wakeup is missing or oversized: %q (%v)", record, err)
	}
	var decoded map[string]int
	if err := json.Unmarshal(record, &decoded); err != nil || !maps.Equal(decoded, map[string]int{"pull_number": 7}) {
		t.Fatalf("wakeup must not carry review authority or candidate text: %s (%v)", record, err)
	}
}

type reuseBootstrapRepository struct {
	directory string
	ancestor  string
	latest    string
	side      string
}

func TestReuseWorkflowBootstrapSelectsAncestorWithoutExecutingUntrustedCode(t *testing.T) {
	t.Parallel()
	script := reuseProtectedPinScript(t)
	for _, origin := range []string{"https://github.com/ben-ranford/lopper", "https://github.com/ben-ranford/lopper.git"} {
		t.Run(origin, func(t *testing.T) {
			assertReuseBootstrapSelectsAncestor(t, script, origin)
		})
	}
}

func assertReuseBootstrapSelectsAncestor(t *testing.T, script, origin string) {
	t.Helper()
	repository := newReuseBootstrapRepository(t)
	runGitCommand(t, repository.directory, "remote", "set-url", "origin", origin)
	environment := reuseBootstrapPoisonedEnvironment(t, repository.directory)
	environment["REUSE_SOURCE_SHA"] = repository.ancestor
	if output, err := runShellCommand(repository.directory, script, environment); err != nil {
		t.Fatalf("protected ancestor selection failed: %v\n%s", err, output)
	}
	assertReuseBootstrapHead(t, repository, repository.ancestor, "protected ancestor\n")
	if branch := strings.TrimSpace(runGitCommand(t, repository.directory, "rev-parse", "--abbrev-ref", "HEAD")); branch != "HEAD" {
		t.Fatalf("immutable source must be detached, got %q", branch)
	}
	assertReuseBootstrapNoUntrustedExecution(t, repository.directory)
}

func TestReuseWorkflowBootstrapRejectsWrongRepositoryOrigin(t *testing.T) {
	t.Parallel()
	script := reuseProtectedPinScript(t)
	for _, testCase := range []struct {
		name string
		urls []string
	}{
		{"foreign owner", []string{"https://github.com/untrusted/lopper.git"}},
		{"lookalike host", []string{"https://github.com.evil/ben-ranford/lopper.git"}},
		{"multiple URLs", []string{"https://github.com/ben-ranford/lopper.git", "https://github.com/untrusted/lopper.git"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repository := newReuseBootstrapRepository(t)
			runGitCommand(t, repository.directory, "config", "--unset-all", "remote.origin.url")
			for _, origin := range testCase.urls {
				runGitCommand(t, repository.directory, "config", "--add", "remote.origin.url", origin)
			}
			assertReuseBootstrapRejectsOrigin(t, script, repository)
		})
	}
}

func assertReuseBootstrapRejectsOrigin(t *testing.T, script string, repository reuseBootstrapRepository) {
	t.Helper()
	environment := reuseBootstrapPoisonedEnvironment(t, repository.directory)
	environment["REUSE_SOURCE_SHA"] = repository.ancestor
	output, err := runShellCommand(repository.directory, script, environment)
	if err == nil || !strings.Contains(output, "Checkout origin is not the protected repository") {
		t.Fatalf("wrong repository origin must fail its identity guard: %v\n%s", err, output)
	}
	assertReuseBootstrapHead(t, repository, repository.latest, "current main\n")
	assertReuseBootstrapNoUntrustedExecution(t, repository.directory)
}

func TestReuseWorkflowBootstrapRejectsUnprotectedRevisions(t *testing.T) {
	t.Parallel()
	script := reuseProtectedPinScript(t)
	repository := newReuseBootstrapRepository(t)
	for _, testCase := range []struct{ name, revision string }{
		{"side branch", repository.side},
		{"missing object", strings.Repeat("f", 40)},
		{"short revision", repository.ancestor[:12]},
		{"option", "--help"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			output, err := runShellCommand(repository.directory, script, map[string]string{"REUSE_SOURCE_SHA": testCase.revision})
			if err == nil {
				t.Fatalf("unprotected revision %q was accepted: %s", testCase.revision, output)
			}
			assertReuseBootstrapHead(t, repository, repository.latest, "current main\n")
		})
	}
}

func TestReuseWorkflowBootstrapRejectsReplacementAncestry(t *testing.T) {
	t.Parallel()
	script := reuseProtectedPinScript(t)
	repository := newReuseBootstrapRepository(t)
	tree := strings.TrimSpace(runGitCommand(t, repository.directory, "rev-parse", repository.latest+"^{tree}"))
	replacement := strings.TrimSpace(runGitCommand(t, repository.directory, "commit-tree", tree, "-p", repository.side, "-m", "Counterfeit ancestry"))
	runGitCommand(t, repository.directory, "replace", repository.latest, replacement)
	// The replacement makes this side branch appear to belong to main unless Git ignores replacement objects.
	runGitCommand(t, repository.directory, "merge-base", "--is-ancestor", repository.side, "refs/remotes/origin/main")
	output, err := runShellCommand(repository.directory, script, map[string]string{"REUSE_SOURCE_SHA": repository.side})
	if err == nil {
		t.Fatalf("replacement object forged protected ancestry: %s", output)
	}
	assertReuseBootstrapHead(t, repository, repository.latest, "current main\n")
}

func reuseProtectedPinScript(t *testing.T) string {
	t.Helper()
	var workflow reuseWorkflowConfig
	readYAMLConfig(t, ".github/workflows/reuse-check.yml", &workflow)
	canonical := workflowStepByName(t, workflow.Jobs, "prepare", "Pin immutable protected source").Run
	for _, name := range []string{"invalidate-base", "prepare", "analyze", "suppression", "publish"} {
		pin := workflowStepByName(t, workflow.Jobs, name, "Pin immutable protected source")
		if pin.Run != canonical {
			t.Fatalf("%s must use the same tested protected-source bootstrap", name)
		}
	}
	return canonical
}

func newReuseBootstrapRepository(t *testing.T) reuseBootstrapRepository {
	t.Helper()
	directory := t.TempDir()
	runGitCommand(t, directory, "init", "-q", "--initial-branch=main", "--object-format=sha1")
	runGitCommand(t, directory, "config", "user.name", "Reuse workflow test")
	runGitCommand(t, directory, "config", "user.email", "reuse-test@example.invalid")
	runGitCommand(t, directory, "remote", "add", "origin", "https://github.com/ben-ranford/lopper.git")
	ancestor := commitReuseBootstrapSource(t, directory, "protected ancestor\n")
	latest := commitReuseBootstrapSource(t, directory, "current main\n")
	runGitCommand(t, directory, "update-ref", "refs/remotes/origin/main", latest)
	runGitCommand(t, directory, "checkout", "-q", "-b", "side", ancestor)
	side := commitReuseBootstrapSource(t, directory, "untrusted side\n")
	runGitCommand(t, directory, "checkout", "-q", "main")
	return reuseBootstrapRepository{directory: directory, ancestor: ancestor, latest: latest, side: side}
}

func commitReuseBootstrapSource(t *testing.T, directory, source string) string {
	t.Helper()
	writeFile(t, filepath.Join(directory, "source.txt"), source)
	runGitCommand(t, directory, "add", "source.txt")
	runGitCommand(t, directory, "commit", "-q", "-m", strings.TrimSpace(source))
	return strings.TrimSpace(runGitCommand(t, directory, "rev-parse", "HEAD"))
}

func reuseBootstrapPoisonedEnvironment(t *testing.T, directory string) map[string]string {
	t.Helper()
	hookMarker := filepath.Join(directory, "hook-ran")
	writeFileMode(t, filepath.Join(directory, ".git", "hooks", "post-checkout"), "#!/bin/sh\nprintf invoked > \"$REUSE_HOOK_MARKER\"\n", 0o700)
	poison := "open('python-injected', 'w').write('imported')\nraise RuntimeError('untrusted module imported')\n"
	writeFile(t, filepath.Join(directory, "subprocess.py"), poison)
	pythonPath := t.TempDir()
	writeFile(t, filepath.Join(pythonPath, "re.py"), poison)
	writeFile(t, filepath.Join(pythonPath, "sitecustomize.py"), poison)
	config := filepath.Join(t.TempDir(), "invalid.gitconfig")
	writeFile(t, config, "[invalid configuration\n")
	return map[string]string{
		"REUSE_HOOK_MARKER": hookMarker, "PYTHONPATH": pythonPath,
		"GIT_DIR": filepath.Join(directory, "missing.git"), "GIT_WORK_TREE": t.TempDir(),
		"GIT_CONFIG_COUNT": "not-an-integer", "GIT_CONFIG_GLOBAL": config,
		"GIT_CONFIG_SYSTEM": config, "GIT_CONFIG_NOSYSTEM": "0", "GIT_NO_REPLACE_OBJECTS": "0",
	}
}

func assertReuseBootstrapHead(t *testing.T, repository reuseBootstrapRepository, wantRevision, wantSource string) {
	t.Helper()
	if revision := strings.TrimSpace(runGitCommand(t, repository.directory, "rev-parse", "HEAD")); revision != wantRevision {
		t.Fatalf("selected revision = %s, want %s", revision, wantRevision)
	}
	if source := readFile(t, filepath.Join(repository.directory, "source.txt")); source != wantSource {
		t.Fatalf("selected source = %q, want %q", source, wantSource)
	}
}

func assertReuseBootstrapNoUntrustedExecution(t *testing.T, directory string) {
	t.Helper()
	for _, marker := range []string{"hook-ran", "python-injected"} {
		if _, err := os.Stat(filepath.Join(directory, marker)); !os.IsNotExist(err) {
			t.Fatalf("bootstrap executed untrusted code: %s (%v)", marker, err)
		}
	}
}
