package scripts

import (
	"archive/zip"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestFeatureFlagEnforcementWorkflowSeparatesUntrustedExecutionFromPrivilegedPublication(t *testing.T) {
	t.Parallel()

	const hardenedShell = "/usr/bin/env -u BASH_ENV -u ENV -u PROMPT_COMMAND -u PS4 -u SHELLOPTS -u BASHOPTS /bin/bash --noprofile --norc -euo pipefail {0}"

	var enforcementWorkflow workflowConfig
	readYAMLConfig(t, ".github/workflows/feature-flag-enforcement.yml", &enforcementWorkflow)
	assertReadOnlyFeatureFlagEnforcementWorkflow(t, enforcementWorkflow, hardenedShell)

	var publicationWorkflow workflowConfig
	readYAMLConfig(t, ".github/workflows/feature-flag-comment-publish.yml", &publicationWorkflow)
	assertTrustedFeatureFlagPublicationWorkflow(t, publicationWorkflow, hardenedShell)
}

func TestFeatureFlagCommentResolverUsesTrustedPullIdentity(t *testing.T) {
	t.Parallel()

	resolver := featureFlagCommentResolverScript(t)
	run := featureFlagWorkflowRun([]map[string]any{{"number": 7}})
	pull := featureFlagPull(7, "open")
	result := runFeatureFlagResolverFixture(t, resolver, map[string]any{
		"run":             run,
		"pulls":           []map[string]any{pull},
		"associatedPulls": []map[string]any{featureFlagPull(8, "open")},
		"artifacts": []map[string]any{
			{"id": 19, "name": "feature-flag-comment-inputs-7", "size_in_bytes": 512, "expired": false},
			{"id": 20, "name": "feature-flag-comment-inputs-8", "size_in_bytes": 512, "expired": false},
		},
	})
	if !result.OK {
		t.Fatalf("resolver rejected trusted workflow_run pull request: %s", result.Error)
	}
	if got := result.Outputs["artifact-id"]; got != "19" {
		t.Fatalf("artifact-id = %q, want 19", got)
	}
	if got := result.Exported["PR_NUMBER"]; got != "7" {
		t.Fatalf("PR_NUMBER = %q, want 7", got)
	}
	if result.Calls["associatedPulls"] != 0 {
		t.Fatal("resolver must not use commit association fallback when workflow_run provides a PR")
	}
}

func TestFeatureFlagCommentResolverRejectsAmbiguousAssociatedPullRequests(t *testing.T) {
	t.Parallel()

	resolver := featureFlagCommentResolverScript(t)
	result := runFeatureFlagResolverFixture(t, resolver, map[string]any{
		"run":             featureFlagWorkflowRun(nil),
		"pulls":           []map[string]any{},
		"associatedPulls": []map[string]any{featureFlagPull(7, "open"), featureFlagPull(8, "open")},
		"artifacts":       []map[string]any{},
	})
	if result.OK {
		t.Fatal("resolver accepted an ambiguous commit-to-PR association")
	}
	if !strings.Contains(result.Error, "expected exactly one pull request") {
		t.Fatalf("resolver error = %q, want exact-one rejection", result.Error)
	}
}

func TestFeatureFlagCommentResolverFiltersCommitAssociationFallback(t *testing.T) {
	t.Parallel()

	resolver := featureFlagCommentResolverScript(t)
	closedPull := featureFlagPull(8, "closed")
	wrongHeadPull := featureFlagPull(9, "open")
	wrongHeadPull["head"].(map[string]any)["sha"] = "different-sha"
	result := runFeatureFlagResolverFixture(t, resolver, map[string]any{
		"run":   featureFlagWorkflowRun(nil),
		"pulls": []map[string]any{featureFlagPull(7, "open")},
		"associatedPulls": []map[string]any{
			closedPull,
			wrongHeadPull,
			featureFlagPull(7, "open"),
		},
		"artifacts": []map[string]any{
			{"id": 19, "name": "feature-flag-comment-inputs-7", "size_in_bytes": 512, "expired": false},
		},
	})
	if !result.OK {
		t.Fatalf("resolver rejected unique matching associated pull request: %s", result.Error)
	}
	if got := result.Exported["PR_NUMBER"]; got != "7" {
		t.Fatalf("PR_NUMBER = %q, want 7", got)
	}
	if result.Calls["associatedPulls"] != 1 {
		t.Fatal("resolver did not use commit association fallback exactly once")
	}
}

func TestFeatureFlagCommentResolverRejectsOversizedArtifactBeforeDownload(t *testing.T) {
	t.Parallel()

	resolver := featureFlagCommentResolverScript(t)
	result := runFeatureFlagResolverFixture(t, resolver, map[string]any{
		"run":             featureFlagWorkflowRun([]map[string]any{{"number": 7}}),
		"pulls":           []map[string]any{featureFlagPull(7, "open")},
		"associatedPulls": []map[string]any{},
		"artifacts": []map[string]any{
			{"id": 19, "name": "feature-flag-comment-inputs-7", "size_in_bytes": 2_200_001, "expired": false},
		},
	})
	if result.OK {
		t.Fatal("resolver accepted an oversized artifact")
	}
	if !strings.Contains(result.Error, "invalid or oversized") {
		t.Fatalf("resolver error = %q, want size-bound rejection", result.Error)
	}
}

func TestFeatureFlagCommentResolverUsesNamedEnforcementStepConclusion(t *testing.T) {
	t.Parallel()

	tests := []featureFlagEnforcementStepCase{
		{
			name: "unrelated failure does not become enforcement failure",
			jobs: []map[string]any{{"steps": []map[string]any{
				{"name": "Enforce feature flags on PRs", "conclusion": "success"},
				{"name": "Write release feature guidance", "conclusion": "failure"},
			}}},
			wantFailed: "false",
		},
		{
			name: "enforcement failure is preserved",
			jobs: []map[string]any{{"steps": []map[string]any{
				{"name": "Enforce feature flags on PRs", "conclusion": "failure"},
				{"name": "Write release feature guidance", "conclusion": "success"},
			}}},
			wantFailed: "true",
		},
		{
			name: "ambiguous enforcement steps fail closed",
			jobs: []map[string]any{{"steps": []map[string]any{
				{"name": "Enforce feature flags on PRs", "conclusion": "success"},
				{"name": "Enforce feature flags on PRs", "conclusion": "failure"},
			}}},
			wantErrorPart: "expected exactly one feature flag enforcement step",
		},
	}

	resolver := featureFlagCommentResolverScript(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertFeatureFlagEnforcementStepCase(t, resolver, tt)
		})
	}
}

func TestFeatureFlagCommentArchiveExtraction(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/feature-flag-comment-publish.yml", &workflow)
	extract := workflowStepByName(t, workflow.Jobs, "publish-comments", "Extract bounded comment inputs")
	validator := embeddedPythonScript(t, extract.Run, "/usr/bin/python3 - <<'PY'")

	tests := []featureFlagArchiveCase{
		{
			name: "valid bounded payload",
			members: []featureFlagZipMember{
				{name: "payload-version.txt", contents: "1\n"},
				{name: "feature-flag-enforcement.md", contents: "summary\n"},
			},
		},
		{
			name: "ambiguous duplicate member",
			members: []featureFlagZipMember{
				{name: "payload-version.txt", contents: "1\n"},
				{name: "payload-version.txt", contents: "1\n"},
			},
			wantError: "unsafe or oversized",
		},
		{
			name: "path traversal",
			members: []featureFlagZipMember{
				{name: "payload-version.txt", contents: "1\n"},
				{name: "../feature-flag-enforcement.md", contents: "summary\n"},
			},
			wantError: "unsafe or oversized",
		},
		{
			name: "oversized summary",
			members: []featureFlagZipMember{
				{name: "payload-version.txt", contents: "1\n"},
				{name: "feature-flag-enforcement.md", contents: strings.Repeat("x", 1_048_577)},
			},
			wantError: "unsafe or oversized",
		},
		{
			name: "symlink",
			members: []featureFlagZipMember{
				{name: "payload-version.txt", contents: "1\n"},
				{name: "feature-flag-enforcement.md", contents: "target", mode: os.ModeSymlink | 0o777},
			},
			wantError: "unsafe or oversized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertFeatureFlagArchiveCase(t, validator, tt)
		})
	}
}

type featureFlagResolverFixtureResult struct {
	OK       bool              `json:"ok"`
	Error    string            `json:"error"`
	Outputs  map[string]string `json:"outputs"`
	Exported map[string]string `json:"exported"`
	Calls    map[string]int    `json:"calls"`
}

type featureFlagZipMember struct {
	name     string
	contents string
	mode     os.FileMode
}

type featureFlagEnforcementStepCase struct {
	name          string
	jobs          []map[string]any
	wantFailed    string
	wantErrorPart string
}

type featureFlagArchiveCase struct {
	name      string
	members   []featureFlagZipMember
	wantError string
}

func assertFeatureFlagEnforcementStepCase(t *testing.T, resolver string, tt featureFlagEnforcementStepCase) {
	t.Helper()

	run := featureFlagWorkflowRun([]map[string]any{{"number": 7}})
	run["conclusion"] = "failure"
	result := runFeatureFlagResolverFixture(t, resolver, map[string]any{
		"run":             run,
		"pulls":           []map[string]any{featureFlagPull(7, "open")},
		"associatedPulls": []map[string]any{},
		"artifacts": []map[string]any{
			{"id": 19, "name": "feature-flag-comment-inputs-7", "size_in_bytes": 512, "expired": false},
		},
		"jobs": tt.jobs,
	})
	if tt.wantErrorPart != "" {
		assertFeatureFlagResolverError(t, result, tt.wantErrorPart)
		return
	}
	if !result.OK {
		t.Fatalf("resolver rejected named enforcement step fixture: %s", result.Error)
	}
	if got := result.Exported["ENFORCEMENT_FAILED"]; got != tt.wantFailed {
		t.Fatalf("ENFORCEMENT_FAILED = %q, want %q", got, tt.wantFailed)
	}
}

func assertFeatureFlagResolverError(t *testing.T, result featureFlagResolverFixtureResult, want string) {
	t.Helper()

	if result.OK {
		t.Fatal("resolver accepted an unsafe fixture")
	}
	if !strings.Contains(result.Error, want) {
		t.Fatalf("resolver error = %q, want %q", result.Error, want)
	}
}

func assertFeatureFlagArchiveCase(t *testing.T, validator string, tt featureFlagArchiveCase) {
	t.Helper()

	commentDir, output, err := runFeatureFlagArchiveValidator(t, validator, tt.members)
	if tt.wantError != "" {
		assertFeatureFlagArchiveError(t, output, err, tt.wantError)
		return
	}
	if err != nil {
		t.Fatalf("validator rejected bounded archive: %v\n%s", err, output)
	}
	assertExtractedFeatureFlagMembers(t, commentDir, tt.members)
}

func runFeatureFlagArchiveValidator(t *testing.T, validator string, members []featureFlagZipMember) (string, []byte, error) {
	t.Helper()

	root := t.TempDir()
	archiveDir := filepath.Join(root, "archive")
	commentDir := filepath.Join(root, "comments")
	if err := os.Mkdir(archiveDir, 0o700); err != nil {
		t.Fatalf("create archive directory: %v", err)
	}
	archivePath := filepath.Join(archiveDir, "payload.zip")
	writeFeatureFlagZip(t, archivePath, members)
	archiveInfo, err := os.Stat(archivePath)
	if err != nil {
		t.Fatalf("stat archive: %v", err)
	}

	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 is required to test artifact extraction")
	}
	command := exec.Command(python, "-c", validator)
	environment := []string{
		"ARCHIVE_DIR=" + archiveDir,
		"COMMENT_DIR=" + commentDir,
		"EXPECTED_ARTIFACT_SIZE=" + strconv.FormatInt(archiveInfo.Size(), 10),
	}
	command.Env = append(os.Environ(), environment...)
	output, commandErr := command.CombinedOutput()
	return commentDir, output, commandErr
}

func assertFeatureFlagArchiveError(t *testing.T, output []byte, err error, want string) {
	t.Helper()

	if err == nil {
		t.Fatalf("validator accepted unsafe archive; output: %s", output)
	}
	if !strings.Contains(string(output), want) {
		t.Fatalf("validator output = %q, want %q", output, want)
	}
}

func assertExtractedFeatureFlagMembers(t *testing.T, commentDir string, members []featureFlagZipMember) {
	t.Helper()

	for _, member := range members {
		contents, err := os.ReadFile(filepath.Join(commentDir, member.name))
		if err != nil {
			t.Fatalf("read extracted %s: %v", member.name, err)
		}
		if string(contents) != member.contents {
			t.Fatalf("extracted %s contents changed", member.name)
		}
	}
}

func featureFlagCommentResolverScript(t *testing.T) string {
	t.Helper()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/feature-flag-comment-publish.yml", &workflow)
	return workflowStepByName(t, workflow.Jobs, "publish-comments", "Resolve triggering pull request and artifact").With["script"]
}

func featureFlagWorkflowRun(pulls []map[string]any) map[string]any {
	return map[string]any{
		"id":              100,
		"run_attempt":     1,
		"conclusion":      "success",
		"head_sha":        "fixture-sha",
		"head_branch":     "fixture-branch",
		"head_repository": map[string]any{"full_name": "octo/fork"},
		"pull_requests":   pulls,
	}
}

func featureFlagPull(number int, state string) map[string]any {
	return map[string]any{
		"number": number,
		"state":  state,
		"title":  "feat: fixture",
		"base": map[string]any{
			"repo": map[string]any{"full_name": "octo/lopper"},
		},
		"head": map[string]any{
			"sha":  "fixture-sha",
			"ref":  "fixture-branch",
			"repo": map[string]any{"full_name": "octo/fork"},
		},
	}
}

func runFeatureFlagResolverFixture(t *testing.T, resolver string, fixture map[string]any) featureFlagResolverFixtureResult {
	t.Helper()

	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to test the feature flag comment resolver")
	}
	fixtureJSON, err := json.Marshal(fixture)
	if err != nil {
		t.Fatalf("marshal resolver fixture: %v", err)
	}
	const harness = `
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const fixture = JSON.parse(process.env.RESOLVER_FIXTURE);
const outputs = {};
const exported = {};
const calls = { artifacts: 0, associatedPulls: 0, jobs: 0, pulls: 0 };
const listArtifacts = async () => {};
const listAssociatedPulls = async () => {};
const listJobs = async () => {};
const pulls = new Map((fixture.pulls || []).map((pull) => [pull.number, pull]));
const github = {
  rest: {
    actions: {
      listWorkflowRunArtifacts: listArtifacts,
      listJobsForWorkflowRunAttempt: listJobs,
    },
    repos: { listPullRequestsAssociatedWithCommit: listAssociatedPulls },
    pulls: {
      get: async ({ pull_number }) => {
        calls.pulls += 1;
        if (!pulls.has(pull_number)) {
          throw new Error('fixture has no requested pull');
        }
        return { data: pulls.get(pull_number) };
      },
    },
  },
  paginate: async (method) => {
    if (method === listArtifacts) {
      calls.artifacts += 1;
      return fixture.artifacts || [];
    }
    if (method === listAssociatedPulls) {
      calls.associatedPulls += 1;
      return fixture.associatedPulls || [];
    }
    if (method === listJobs) {
      calls.jobs += 1;
      return fixture.jobs || [{ steps: [
        { name: 'Enforce feature flags on PRs', conclusion: 'success' },
      ] }];
    }
    throw new Error('unexpected paginated API method');
  },
};
const context = {
  repo: { owner: 'octo', repo: 'lopper' },
  payload: { workflow_run: fixture.run },
};
const core = {
  exportVariable: (name, value) => { exported[name] = String(value); },
  setOutput: (name, value) => { outputs[name] = String(value); },
};
(async () => {
  try {
    const execute = new AsyncFunction('github', 'context', 'core', process.env.RESOLVER_SCRIPT);
    await execute(github, context, core);
    console.log(JSON.stringify({ ok: true, outputs, exported, calls }));
  } catch (error) {
    console.log(JSON.stringify({ ok: false, error: error.message, outputs, exported, calls }));
  }
})();
`
	command := exec.Command(node, "-e", harness)
	environment := []string{
		"RESOLVER_SCRIPT=" + resolver,
		"RESOLVER_FIXTURE=" + string(fixtureJSON),
	}
	command.Env = append(os.Environ(), environment...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run resolver fixture: %v\n%s", err, output)
	}

	var result featureFlagResolverFixtureResult
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode resolver fixture result: %v\n%s", err, output)
	}
	return result
}

func writeFeatureFlagZip(t *testing.T, path string, members []featureFlagZipMember) {
	t.Helper()

	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create ZIP fixture: %v", err)
	}
	archive := zip.NewWriter(file)
	for _, member := range members {
		header := &zip.FileHeader{Name: member.name, Method: zip.Deflate}
		mode := member.mode
		if mode == 0 {
			mode = 0o644
		}
		header.SetMode(mode)
		writer, err := archive.CreateHeader(header)
		if err != nil {
			t.Fatalf("create ZIP member %s: %v", member.name, err)
		}
		if _, err := writer.Write([]byte(member.contents)); err != nil {
			t.Fatalf("write ZIP member %s: %v", member.name, err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("close ZIP fixture: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close ZIP fixture file: %v", err)
	}
}

func assertReadOnlyFeatureFlagEnforcementWorkflow(t *testing.T, enforcementWorkflow workflowConfig, hardenedShell string) {
	t.Helper()

	enforce := workflowJobByName(t, enforcementWorkflow.Jobs, "enforce")
	assertWorkflowJobPermissions(t, enforce, "feature flag enforcement", map[string]string{"contents": "read"})
	assertWorkflowJobCheckoutsDisablePersistedCredentials(t, enforce, "feature flag enforcement")
	assertWorkflowStepOrder(t, enforce, "Checkout", "Setup Go", "Reset feature flag comment staging", "Fetch PR base", "Capture base feature catalog", "Classify PR", "Enforce feature flags on PRs", "Write feature flag summary", "Validate and stage bounded comment inputs", "Upload bounded comment inputs", "Fail on feature flag enforcement errors")
	if _, ok := enforcementWorkflow.Jobs["publish-comments"]; ok {
		t.Fatal("pull_request workflow must not contain a privileged comment publisher")
	}

	checkout := workflowStepByName(t, enforcementWorkflow.Jobs, "enforce", "Checkout")
	setupGo := workflowStepByName(t, enforcementWorkflow.Jobs, "enforce", "Setup Go")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature flag enforcement checkout action", got: checkout.Uses, want: "actions/checkout@de0fac2e4500dabe0009e67214ff5f5447ce83dd"},
		{label: "feature flag enforcement checkout persist-credentials", got: checkout.With["persist-credentials"], want: "false"},
		{label: "feature flag enforcement setup-go action", got: setupGo.Uses, want: "actions/setup-go@4a3601121dd01d1626a1e23e37211e3254c1c06c"},
	})

	enforceFlags := workflowStepByName(t, enforcementWorkflow.Jobs, "enforce", "Enforce feature flags on PRs")
	if enforceFlags.ContinueOnError {
		t.Fatal("feature flag enforcement step must expose its real conclusion to the jobs API")
	}
	for _, stepName := range []string{
		"Fetch tags for release PR guidance",
		"Capture previous release feature catalog",
		"Write release feature guidance",
	} {
		step := workflowStepByName(t, enforcementWorkflow.Jobs, "enforce", stepName)
		if step.If != "${{ always() && steps.classify_pr.outputs.release_pr == 'true' }}" {
			t.Fatalf("%s must run after an enforcement failure when this is a release PR", stepName)
		}
	}
	failEnforcement := workflowStepByName(t, enforcementWorkflow.Jobs, "enforce", "Fail on feature flag enforcement errors")
	if failEnforcement.If != "${{ always() && steps.enforce_flags.outcome == 'failure' }}" {
		t.Fatal("feature flag failure step must preserve the failed enforcement job result")
	}

	validateUpload := workflowStepByName(t, enforcementWorkflow.Jobs, "enforce", "Validate and stage bounded comment inputs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature flag input validation id", got: validateUpload.ID, want: "validate_comment_inputs"},
		{label: "feature flag input validation shell", got: validateUpload.Shell, want: hardenedShell},
	})
	assertWorkflowStepEnv(t, validateUpload, "feature flag input validation", map[string]string{
		"COMMENT_DIR": "${{ runner.temp }}/feature-flag-comment-inputs",
		"PATH":        "/usr/bin:/bin",
	})
	assertWorkflowStepRunContainsAll(t, validateUpload, "feature flag input validation", []string{
		`find -P "${COMMENT_DIR}" -mindepth 1 -maxdepth 1 ! -type f -print -quit`,
		`if [ -f "${summary_path}" ] && [ ! -s "${summary_path}" ]; then`,
		`rm -f -- "${summary_path}"`,
		`printf '1\n' > "${COMMENT_DIR}/payload-version.txt"`,
		`file_count="$(find -P "${COMMENT_DIR}" -mindepth 1 -maxdepth 1 -type f | wc -l | tr -d '[:space:]')"`,
		`payload-version.txt`,
		`echo "::error::Comment payload version exceeds the 8-byte bound." >&2`,
		`feature-flag-enforcement.md|release-feature-flag-comment.md`,
		`stat --format=%s "${path}"`,
	})
	upload := workflowStepByName(t, enforcementWorkflow.Jobs, "enforce", "Upload bounded comment inputs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature flag comment upload guard", got: upload.If, want: "${{ always() && steps.validate_comment_inputs.outcome == 'success' }}"},
		{label: "feature flag comment upload action", got: upload.Uses, want: "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a"},
		{label: "feature flag comment upload name", got: upload.With["name"], want: "feature-flag-comment-inputs-${{ github.event.pull_request.number }}"},
		{label: "feature flag comment upload path", got: upload.With["path"], want: "${{ runner.temp }}/feature-flag-comment-inputs"},
		{label: "feature flag comment upload missing-file behavior", got: upload.With["if-no-files-found"], want: "error"},
		{label: "feature flag comment upload compression level", got: upload.With["compression-level"], want: "0"},
	})

	enforcementText := readConfig(t, ".github/workflows/feature-flag-enforcement.yml")
	for _, forbidden := range []string{
		"issues: write",
		"pull-requests: write",
		"actions/github-script@",
		"actions/checkout@v",
		"actions/setup-go@v",
		"publish-comments:",
		"enforcement_failed.txt",
		"feature_pr.txt",
		"release_pr.txt",
	} {
		if strings.Contains(enforcementText, forbidden) {
			t.Fatalf("untrusted feature flag workflow contains unsafe fragment %q", forbidden)
		}
	}

}

func assertTrustedFeatureFlagPublicationWorkflow(t *testing.T, publicationWorkflow workflowConfig, hardenedShell string) {
	t.Helper()

	publication := workflowJobByName(t, publicationWorkflow.Jobs, "publish-comments")
	assertWorkflowJobPermissions(t, publication, "feature flag comment publication", map[string]string{
		"actions":       "read",
		"issues":        "write",
		"pull-requests": "read",
	})
	assertWorkflowJobOmitsCheckout(t, publication, "feature flag comment publication")
	assertWorkflowJobStepRunsOmitAllFold(t, publication, "feature flag comment publication", []string{"go run ./", "make ", "npm ", "npx ", "git "})
	assertWorkflowStepOrder(t, publication, "Resolve triggering pull request and artifact", "Download bounded comment inputs", "Extract bounded comment inputs", "Validate bounded comment inputs", "Sync feature flag enforcement comment on PR", "Sync release feature guidance comment on PR")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature flag publication event guard", got: publication.If, want: "${{ github.event.workflow_run.event == 'pull_request' }}"},
	})

	resolvePR := workflowStepByName(t, publicationWorkflow.Jobs, "publish-comments", "Resolve triggering pull request and artifact")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature flag PR resolution id", got: resolvePR.ID, want: "resolve_pr"},
		{label: "feature flag PR resolution action", got: resolvePR.Uses, want: "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3"},
	})
	assertTextContainsAll(t, resolvePR.With["script"], "feature flag PR resolution", []string{
		"(run.pull_requests ?? []).length > 0",
		"candidateNumbers.length !== 1",
		"github.rest.actions.listWorkflowRunArtifacts",
		"const expectedArtifactName = `feature-flag-comment-inputs-${prNumber}`",
		"candidate.name === expectedArtifactName",
		"github.rest.pulls.get",
		"pull.state === 'open'",
		"pull.head.sha === run.head_sha",
		"pull.head.ref === run.head_branch",
		"pull.head.repo?.full_name?.toLowerCase() === expectedHeadRepository",
		"github.rest.repos.listPullRequestsAssociatedWithCommit",
		"const maxArtifactBytes = 2200000",
		"artifact.size_in_bytes > maxArtifactBytes",
		"core.setOutput('artifact-id', String(artifact.id))",
		"core.setOutput('artifact-size', String(artifact.size_in_bytes))",
		"github.rest.actions.listJobsForWorkflowRunAttempt",
		"attempt_number: run.run_attempt",
		"step.name === 'Enforce feature flags on PRs'",
		"enforcementSteps.length !== 1",
		"core.exportVariable('PR_NUMBER', String(prNumber))",
		"String(enforcementStep.conclusion === 'failure')",
		"String(/^feat(\\([^)]+\\))?(!)?:\\s+\\S/.test(pull.title))",
		"String(pull.head.ref.startsWith('release-please--branches--'))",
	})

	download := workflowStepByName(t, publicationWorkflow.Jobs, "publish-comments", "Download bounded comment inputs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature flag comment download action", got: download.Uses, want: "actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c"},
		{label: "feature flag comment download artifact ID", got: download.With["artifact-ids"], want: "${{ steps.resolve_pr.outputs.artifact-id }}"},
		{label: "feature flag comment download path", got: download.With["path"], want: "${{ runner.temp }}/feature-flag-comment-archive"},
		{label: "feature flag comment download token", got: download.With["github-token"], want: "${{ github.token }}"},
		{label: "feature flag comment download repository", got: download.With["repository"], want: "${{ github.repository }}"},
		{label: "feature flag comment download run ID", got: download.With["run-id"], want: "${{ github.event.workflow_run.id }}"},
		{label: "feature flag comment download decompression", got: download.With["skip-decompress"], want: "true"},
	})

	extractDownload := workflowStepByName(t, publicationWorkflow.Jobs, "publish-comments", "Extract bounded comment inputs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature flag publication extraction shell", got: extractDownload.Shell, want: hardenedShell},
	})
	assertWorkflowStepEnv(t, extractDownload, "feature flag publication extraction", map[string]string{
		"ARCHIVE_DIR":            "${{ runner.temp }}/feature-flag-comment-archive",
		"COMMENT_DIR":            "${{ runner.temp }}/feature-flag-comment-inputs",
		"EXPECTED_ARTIFACT_SIZE": "${{ steps.resolve_pr.outputs.artifact-size }}",
		"PATH":                   "/usr/bin:/bin",
	})
	assertWorkflowStepRunContainsAll(t, extractDownload, "feature flag publication extraction", []string{
		`/usr/bin/python3 - <<'PY'`,
		`max_archive_bytes = 2_200_000`,
		`actual_size != expected_size`,
		`with zipfile.ZipFile(archive_path) as archive:`,
		`member.filename not in limits`,
		`member.filename in seen`,
		`file_type not in (0, stat.S_IFREG)`,
		`member.file_size > limits[member.filename]`,
		`contents = source.read(limit + 1)`,
	})

	validateDownload := workflowStepByName(t, publicationWorkflow.Jobs, "publish-comments", "Validate bounded comment inputs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature flag publication validation shell", got: validateDownload.Shell, want: hardenedShell},
	})
	assertWorkflowStepEnv(t, validateDownload, "feature flag publication validation", map[string]string{
		"COMMENT_DIR": "${{ runner.temp }}/feature-flag-comment-inputs",
		"PATH":        "/usr/bin:/bin",
	})
	assertWorkflowStepRunContainsAll(t, validateDownload, "feature flag publication validation", []string{
		`find -P "${COMMENT_DIR}" -mindepth 1 -maxdepth 1 ! -type f -print -quit`,
		`file_count="$(find -P "${COMMENT_DIR}" -mindepth 1 -maxdepth 1 -type f | wc -l | tr -d '[:space:]')"`,
		`payload_version="$(tr -d '[:space:]' < "${path}")"`,
		`: "${payload_version:?missing comment payload version}"`,
		`feature_summary_path=""`,
		`release_summary_path=""`,
		`printf 'FEATURE_SUMMARY_PATH=%s\n' "${feature_summary_path}"`,
		`printf 'RELEASE_SUMMARY_PATH=%s\n' "${release_summary_path}"`,
	})

	featureComment := workflowStepByName(t, publicationWorkflow.Jobs, "publish-comments", "Sync feature flag enforcement comment on PR")
	releaseComment := workflowStepByName(t, publicationWorkflow.Jobs, "publish-comments", "Sync release feature guidance comment on PR")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature flag comment action", got: featureComment.Uses, want: "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3"},
		{label: "release feature comment action", got: releaseComment.Uses, want: "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3"},
	})
	assertTextContainsAll(t, featureComment.With["script"], "feature flag comment publication", []string{"process.env.PR_NUMBER", "issue_number: prNumber"})
	assertTextContainsAll(t, releaseComment.With["script"], "release feature comment publication", []string{"process.env.PR_NUMBER", "issue_number: prNumber"})
	for _, script := range []string{featureComment.With["script"], releaseComment.With["script"]} {
		for _, forbidden := range []string{".artifacts/", "go run ./tools/featureflag"} {
			if strings.Contains(script, forbidden) {
				t.Fatalf("trusted feature flag comment publisher contains %q", forbidden)
			}
		}
	}

	publicationText := readConfig(t, ".github/workflows/feature-flag-comment-publish.yml")
	for _, required := range []string{
		"workflow_run:",
		"- feature flag enforcement",
		"permissions: {}",
	} {
		if !strings.Contains(publicationText, required) {
			t.Fatalf("trusted feature flag publisher is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"actions/github-script@v",
		"actions/checkout@",
		"go run ./",
		"workflow_run.pull_requests[0]",
		"String(run.conclusion !== 'success')",
		"enforcement_failed.txt",
		"feature_pr.txt",
		"release_pr.txt",
	} {
		if strings.Contains(publicationText, forbidden) {
			t.Fatalf("trusted feature flag publisher contains unsafe fragment %q", forbidden)
		}
	}
}
