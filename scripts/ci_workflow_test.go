package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

type pullRequestTrigger struct {
	Types []string `yaml:"types"`
}

type pullRequestWorkflowOn struct {
	PullRequest pullRequestTrigger `yaml:"pull_request"`
}

type pullRequestTriggerWorkflow struct {
	On pullRequestWorkflowOn `yaml:"on"`
}

type workflowActionCheck struct {
	jobName   string
	stepName  string
	wantUses  string
	stepLabel string
}

func TestCIWorkflowPinsPrivilegedVerifyActions(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	verify := workflowJobByName(t, workflow.Jobs, "verify")
	assertWorkflowJobPermissions(t, verify, "ci verify", map[string]string{"contents": "read"})
	assertWorkflowJobCheckoutsDisablePersistedCredentials(t, verify, "ci verify")
	assertWorkflowStepOrder(t, verify, "Run coverage gate", "Stage PR report inputs", "Upload PR report inputs", "Upload binary artifact", "Fail workflow on coverage gate")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "ci verify trusted PR report output", got: verify.Outputs["pr_report_artifact_id"], want: "${{ steps.upload_pr_report_inputs.outputs.artifact-id }}"},
	})

	for _, check := range []workflowActionCheck{
		{"verify", "Checkout", "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1", "verify checkout"},
		{"verify", "Setup Go", "actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e", "verify setup-go"},
		{"verify", "Upload PR report inputs", "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a", "verify PR report upload"},
		{"verify", "Upload binary artifact", "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a", "verify binary upload"},
		{"publish-pr-reports", "Download PR report inputs", "actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c", "PR report download"},
		{"publish-pr-reports", "Comment memory benchmark report on PR", "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3", "memory benchmark comment"},
		{"publish-pr-reports", "Comment lopper report on PR", "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3", "lopper report comment"},
		{"publish-pr-reports", "Comment on coverage failure", "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3", "coverage failure comment"},
	} {
		step := workflowStepByName(t, workflow.Jobs, check.jobName, check.stepName)
		if step.Uses != check.wantUses {
			t.Fatalf("%s uses %q, want %q", check.stepLabel, step.Uses, check.wantUses)
		}
	}

	immutableAction := regexp.MustCompile(`^[^@[:space:]]+@[0-9a-f]{40}$`)
	for _, jobName := range []string{"verify", "publish-pr-reports"} {
		for _, step := range workflow.Jobs[jobName].Steps {
			if step.Uses != "" && !immutableAction.MatchString(step.Uses) {
				t.Fatalf("%s step %q must use an immutable action SHA: %q", jobName, step.Name, step.Uses)
			}
		}
	}
}

func TestCIWorkflowIsolatesPRPublicationCredentials(t *testing.T) {
	t.Parallel()

	const hardenedShell = "/usr/bin/env -u BASH_ENV -u ENV -u PROMPT_COMMAND -u PS4 -u SHELLOPTS -u BASHOPTS /bin/bash --noprofile --norc -euo pipefail {0}"

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	stageInputs := workflowStepByName(t, workflow.Jobs, "verify", "Stage PR report inputs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "PR report staging shell", got: stageInputs.Shell, want: hardenedShell},
	})
	assertWorkflowStepEnv(t, stageInputs, "PR report staging", map[string]string{
		"LOPPER_BASE_OUTCOME":  "${{ steps.lopper_base.outcome }}",
		"LOPPER_DELTA_OUTCOME": "${{ steps.lopper_delta.outcome }}",
		"PATH":                 "/usr/bin:/bin",
	})
	assertWorkflowStepRunContainsAll(t, stageInputs, "ci PR report staging", []string{
		`write_bounded_output() {`,
		`copy_bounded_report() {`,
		`report_root="${RUNNER_TEMP}/pr-report-inputs"`,
		`write_bounded_output "${report_root}/lopper-base-outcome.txt" 64 "${LOPPER_BASE_OUTCOME}"`,
		`write_bounded_output "${report_root}/lopper-delta-outcome.txt" 64 "${LOPPER_DELTA_OUTCOME}"`,
		`src=".artifacts/${report}"`,
		`limit_bytes=1048576`,
		`coverage-package-failures.txt)`,
		`limit_bytes=131072`,
		`coverage-status.txt|coverage-total.txt|memory-bench-status.txt)`,
		`copy_bounded_report "${src}" "${report_root}/${report}" "${limit_bytes}"`,
	})
	uploadInputs := workflowStepByName(t, workflow.Jobs, "verify", "Upload PR report inputs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "PR report upload step id", got: uploadInputs.ID, want: "upload_pr_report_inputs"},
	})
	assertCIArtifactAction(t, uploadInputs, "PR report upload", "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a", map[string]string{
		"name":              "pr-report-inputs",
		"path":              "${{ runner.temp }}/pr-report-inputs",
		"if-no-files-found": "error",
	})

	publication := workflowJobByName(t, workflow.Jobs, "publish-pr-reports")
	assertWorkflowJobNeeds(t, publication, "PR report publication", workflowJobNeeds{"verify"})
	assertWorkflowJobPermissions(t, publication, "PR report publication", map[string]string{
		"actions":       "read",
		"contents":      "read",
		"issues":        "write",
		"pull-requests": "write",
	})
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "PR report publication guard", got: publication.If, want: "${{ always() && github.event_name == 'pull_request' && needs.verify.outputs.pr_report_artifact_id != '' }}"},
	})
	assertWorkflowJobEnvEmpty(t, publication, "PR report publication")
	assertWorkflowJobOmitsCheckout(t, publication, "PR report publication")
	assertWorkflowJobStepRunsOmitAllFold(t, publication, "PR report publication", []string{
		"go run ./",
		"make ",
		"npm ",
		"npx ",
		"scripts/",
		"./extensions/",
		"git ",
	})
	assertWorkflowStepOrder(t, publication, "Download PR report inputs", "Validate PR report inputs", "Comment memory benchmark report on PR", "Comment lopper report on PR", "Comment on coverage failure")
	coverageComment := workflowStepByName(t, workflow.Jobs, "publish-pr-reports", "Comment on coverage failure")
	if !coverageComment.ContinueOnError {
		t.Fatal("coverage comment publication must not fail an otherwise-green CI run")
	}

	downloadInputs := workflowStepByName(t, workflow.Jobs, "publish-pr-reports", "Download PR report inputs")
	assertWorkflowArtifactDownloadByID(t, downloadInputs, workflowArtifactDownloadExpectation{
		label: "PR report download", wantID: "${{ needs.verify.outputs.pr_report_artifact_id }}", wantPath: "pr-report-inputs",
		wantRepo: "${{ github.repository }}", wantRunID: "${{ github.run_id }}", wantToken: "${{ github.token }}",
	})

	validateInputs := workflowStepByName(t, workflow.Jobs, "publish-pr-reports", "Validate PR report inputs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "PR report validation shell", got: validateInputs.Shell, want: hardenedShell},
	})
	assertWorkflowStepEnv(t, validateInputs, "PR report validation", map[string]string{
		"PATH":        "/usr/bin:/bin",
		"REPORT_ROOT": "pr-report-inputs",
	})
	assertWorkflowStepRunContainsAll(t, validateInputs, "PR report validation", []string{
		`find -P "${REPORT_ROOT}" -mindepth 1 -maxdepth 1 ! -type f -print -quit`,
		`allowed_files=(`,
		`required_files=(`,
		`path="${REPORT_ROOT}/${required}"`,
		`Unexpected PR report input: ${name}`,
		`PR report input exceeds the 1 MiB publication limit: ${name}`,
	})
	if got, want := shellArrayValues(t, validateInputs.Run, "required_files"), []string{"lopper-base-outcome.txt", "lopper-delta-outcome.txt"}; !slices.Equal(got, want) {
		t.Fatalf("required PR report inputs = %q, want %q", got, want)
	}

	assertWorkflowStepAbsent(t, workflow.Jobs, "publish-pr-reports", "Post SonarQube review comments (PR)")
	assertWorkflowEnvKeyAbsent(t, workflow.Jobs, "SONAR_TOKEN")
}

func assertCIArtifactAction(t *testing.T, step workflowStepConfig, label string, wantUses string, wantInputs map[string]string) {
	t.Helper()
	if step.Uses != wantUses {
		t.Fatalf("%s action = %q, want %q", label, step.Uses, wantUses)
	}
	for name, want := range wantInputs {
		if got := step.With[name]; got != want {
			t.Fatalf("%s %s = %q, want %q", label, name, got, want)
		}
	}
}

func shellArrayValues(t *testing.T, script string, name string) []string {
	t.Helper()
	marker := name + "=("
	start := strings.Index(script, marker)
	if start == -1 {
		t.Fatalf("shell array %q is missing", name)
	}
	body := script[start+len(marker):]
	end := strings.Index(body, "\n)")
	if end == -1 {
		t.Fatalf("shell array %q is unterminated", name)
	}
	values := make([]string, 0)
	for _, line := range strings.Split(body[:end], "\n") {
		if value := strings.TrimSpace(line); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func assertWorkflowStepAbsent(t *testing.T, jobs map[string]workflowJobConfig, jobName string, stepName string) {
	t.Helper()

	job, ok := jobs[jobName]
	if !ok {
		t.Fatalf("workflow must define job %s", jobName)
	}
	for _, step := range job.Steps {
		if step.Name == stepName {
			t.Fatalf("%s must not define step %q", jobName, stepName)
		}
	}
}

func assertWorkflowEnvKeyAbsent(t *testing.T, jobs map[string]workflowJobConfig, key string) {
	t.Helper()

	for jobName, job := range jobs {
		if _, present := job.Env[key]; present {
			t.Fatalf("%s must not be scoped to job %q", key, jobName)
		}
		for _, step := range job.Steps {
			if _, present := step.Env[key]; present {
				t.Fatalf("%s must not be scoped to step %q in job %q", key, step.Name, jobName)
			}
		}
	}
}

func TestCIWorkflowRunsRegressionProofGateInVerifyJob(t *testing.T) {
	t.Parallel()
	assertPullRequestTriggerTypes(t, ".github/workflows/ci.yml")

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	verify := workflowJobByName(t, workflow.Jobs, "verify")
	expectedOrder := []string{
		ciWorkflowResolveBaseStepName,
		"Write PR body for regression proof",
		ciWorkflowFetchBaseStepName,
		"Run CI target",
		"Prove regression tests for fix PRs",
	}
	assertWorkflowStepOrder(t, verify, expectedOrder...)

	resolveBase := workflowStepByName(t, workflow.Jobs, "verify", ciWorkflowResolveBaseStepName)
	assertCIWorkflowPreparedBaseResolverStep(t, resolveBase, "resolve PR base ref")

	writeBody := workflowStepByName(t, workflow.Jobs, "verify", "Write PR body for regression proof")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "regression body writer action", got: writeBody.Uses, want: "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3"},
		{label: "regression body writer condition", got: writeBody.If, want: "${{ github.event_name == 'pull_request' }}"},
	})
	assertWorkflowStepRunContainsAll(t, workflowStepConfig{Run: writeBody.With["script"]}, "regression body writer", []string{
		`const bodyPath = path.join(process.env.RUNNER_TEMP, 'pr-regression-body.md');`,
		`core.exportVariable('PR_BODY_FILE', bodyPath);`,
	})

	fetchBase := workflowStepByName(t, workflow.Jobs, "verify", ciWorkflowFetchBaseStepName)
	assertCIWorkflowPreparedBaseFetchStep(t, fetchBase, "fetch PR base")

	runCI := workflowStepByName(t, workflow.Jobs, "verify", "Run CI target")
	assertCIWorkflowPreparedBaseRunStep(t, runCI, "run CI target")

	proof := workflowStepByName(t, workflow.Jobs, "verify", "Prove regression tests for fix PRs")
	assertCIWorkflowRegressionProofUsesPreparedBaseSHA(t, proof, "regression proof step")
}

func TestCIWorkflowPreparesImmutableMemoryBenchmarkBase(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	testCases := []struct {
		jobName     string
		runStepName string
	}{
		{jobName: "verify", runStepName: "Run CI target"},
		{jobName: "verify-rolling", runStepName: "Run CI target with rolling defaults"},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.jobName, func(t *testing.T) {
			t.Parallel()

			job := workflowJobByName(t, workflow.Jobs, tc.jobName)
			assertWorkflowStepOrder(t, job, ciWorkflowResolveBaseStepName, ciWorkflowFetchBaseStepName, tc.runStepName)

			resolver := workflowStepByName(t, workflow.Jobs, tc.jobName, ciWorkflowResolveBaseStepName)
			assertCIWorkflowPreparedBaseResolverStep(t, resolver, tc.jobName+" PR base resolver")

			fetchBase := workflowStepByName(t, workflow.Jobs, tc.jobName, ciWorkflowFetchBaseStepName)
			assertCIWorkflowPreparedBaseFetchStep(t, fetchBase, tc.jobName+" PR base fetch")

			runCI := workflowStepByName(t, workflow.Jobs, tc.jobName, tc.runStepName)
			assertCIWorkflowPreparedBaseRunStep(t, runCI, tc.jobName+" CI run")
		})
	}
}

func TestCIWorkflowUsesImmutableBaseSHAForLopperReport(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	verify := workflowJobByName(t, workflow.Jobs, "verify")
	assertWorkflowStepOrder(t, verify, ciWorkflowFetchBaseStepName, "Run lopper self-analysis (base branch)", "Run lopper delta summary (PR)")

	baseReport := workflowStepByName(t, workflow.Jobs, "verify", "Run lopper self-analysis (base branch)")
	assertWorkflowStepRunContainsAll(t, baseReport, "lopper base report immutable checkout", []string{
		`base_sha="${BASE_SHA:?prepared PR base SHA is required}"`,
		`git worktree add --detach .artifacts/base "${base_sha}"`,
		`bin/lopper analyse --top 10 --repo .artifacts/base --language all --format json > .artifacts/lopper-base.json`,
	})
	assertWorkflowStepRunOmitsAll(t, baseReport, "lopper base report immutable checkout", []string{
		`base_ref="${BASE_REF:-main}"`,
		`origin/${base_ref}`,
		`origin/main`,
	})
}

func TestCIWorkflowOnlyAllowsMemoryApprovalForStatusOne(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	runCI := workflowStepByName(t, workflow.Jobs, "verify", "Run CI target")
	assertWorkflowStepRunContainsAll(t, runCI, "ci verify run target", []string{
		`export MEMORY_BENCH_BASE="${MEMORY_BENCH_BASE:?prepared PR memory benchmark base is required}"`,
		`export MEMORY_BENCH_ENFORCE=0`,
		`make ci`,
	})

	failUnapproved := workflowStepByName(t, workflow.Jobs, "verify", "Fail on unapproved memory regression")
	assertWorkflowStepRunContainsAll(t, failUnapproved, "ci unapproved memory regression gate", []string{
		`status="$(tr -d '[:space:]' < .artifacts/memory-bench-status.txt)"`,
		`if [ "$status" = "1" ]; then`,
	})
	assertWorkflowStepRunOmitsAll(t, failUnapproved, "ci unapproved memory regression gate", []string{
		`if [ "$status" = "2" ]`,
		`if [ "$status" -eq 2 ]`,
	})
}

func TestCIWorkflowACTPullRequestFallbackUsesTrueMergeBase(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	resolveBase := workflowStepByName(t, workflow.Jobs, "verify", ciWorkflowResolveBaseStepName)
	fetchBase := workflowStepByName(t, workflow.Jobs, "verify", ciWorkflowFetchBaseStepName)
	repo, baseSHA, headParentSHA := createCIWorkflowACTMergeBaseRepo(t)
	if headParentSHA == baseSHA {
		t.Fatalf("test fixture is invalid: HEAD^ = %q, want a distinct feature commit before HEAD", headParentSHA)
	}

	resolveEnvPath := filepath.Join(t.TempDir(), "resolve.env")
	resolveOutput, err := runCIWorkflowShellStep(t, repo, resolveBase.Run, map[string]string{
		"ACT":         "1",
		"GITHUB_ENV":  resolveEnvPath,
		"PR_BASE_REF": "",
		"PR_BASE_SHA": "",
	})
	if err != nil {
		t.Fatalf("resolve PR base ref failed for ACT fallback: %v\n%s", err, resolveOutput)
	}
	if !strings.Contains(resolveOutput, "will resolve the immutable local base from merge-base(origin/main, HEAD).") {
		t.Fatalf("resolve PR base ref output = %q, want merge-base fallback notice", resolveOutput)
	}
	resolvedEnv := readCIWorkflowEnvFile(t, resolveEnvPath)
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "ACT fallback base source", got: resolvedEnv["BASE_SOURCE"], want: "act-merge-base"},
		{label: "ACT fallback base sha", got: resolvedEnv["BASE_SHA"], want: ""},
		{label: "ACT fallback base ref", got: resolvedEnv["BASE_REF"], want: "main"},
	})

	fetchEnvPath := filepath.Join(t.TempDir(), "fetch.env")
	fetchOutput, err := runCIWorkflowShellStep(t, repo, fetchBase.Run, map[string]string{
		"ACT":         "1",
		"GITHUB_ENV":  fetchEnvPath,
		"BASE_SOURCE": resolvedEnv["BASE_SOURCE"],
		"BASE_SHA":    resolvedEnv["BASE_SHA"],
		"BASE_REF":    resolvedEnv["BASE_REF"],
	})
	if err != nil {
		t.Fatalf("fetch PR base failed for ACT fallback: %v\n%s", err, fetchOutput)
	}
	if !strings.Contains(fetchOutput, "using merge-base(origin/main, HEAD) ("+baseSHA+") as the immutable local base") {
		t.Fatalf("fetch PR base output = %q, want merge-base fallback notice for %s", fetchOutput, baseSHA)
	}
	fetchedEnv := readCIWorkflowEnvFile(t, fetchEnvPath)
	if got := fetchedEnv["BASE_SHA"]; got != baseSHA {
		t.Fatalf("ACT fallback prepared base SHA = %q, want %q", got, baseSHA)
	}
	if got := fetchedEnv["MEMORY_BENCH_BASE"]; got != baseSHA {
		t.Fatalf("ACT fallback memory benchmark base = %q, want %q", got, baseSHA)
	}
	if got := fetchedEnv["MEMORY_BENCH_BASE"]; got == headParentSHA {
		t.Fatalf("ACT fallback selected HEAD^ = %q, want true merge-base %q", got, baseSHA)
	}
}

func TestCIWorkflowACTPullRequestFallbackFailsClosedWithoutLocalMain(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	resolveBase := workflowStepByName(t, workflow.Jobs, "verify", ciWorkflowResolveBaseStepName)
	fetchBase := workflowStepByName(t, workflow.Jobs, "verify", ciWorkflowFetchBaseStepName)
	repo := initCIWorkflowGitRepo(t)
	commitCIWorkflowGitFile(t, repo, "fixture.txt", "base\n", "base")
	commitCIWorkflowTrackedFile(t, repo, "fixture.txt", "head\n", "head")

	resolveEnvPath := filepath.Join(t.TempDir(), "resolve.env")
	resolveOutput, err := runCIWorkflowShellStep(t, repo, resolveBase.Run, map[string]string{
		"ACT":         "1",
		"GITHUB_ENV":  resolveEnvPath,
		"PR_BASE_REF": "",
		"PR_BASE_SHA": "",
	})
	if err != nil {
		t.Fatalf("resolve PR base ref failed for default ACT base: %v\n%s", err, resolveOutput)
	}
	resolvedEnv := readCIWorkflowEnvFile(t, resolveEnvPath)
	output, err := runCIWorkflowShellStep(t, repo, fetchBase.Run, map[string]string{
		"ACT":         "1",
		"GITHUB_ENV":  filepath.Join(t.TempDir(), "fetch.env"),
		"BASE_SOURCE": resolvedEnv["BASE_SOURCE"],
		"BASE_SHA":    resolvedEnv["BASE_SHA"],
		"BASE_REF":    resolvedEnv["BASE_REF"],
	})
	if err == nil {
		t.Fatal("fetch PR base succeeded without origin/main")
	}
	if !strings.Contains(output, "ACT local base branch 'origin/main' could not be fetched") {
		t.Fatalf("fetch PR base output = %q, want fail-closed default-base error", output)
	}
}

func TestCIWorkflowRegressionProofUsesPreparedBaseSHA(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	proof := workflowStepByName(t, workflow.Jobs, "verify", "Prove regression tests for fix PRs")
	assertCIWorkflowRegressionProofUsesPreparedBaseSHA(t, proof, "regression proof prepared base")
}

func TestCIWorkflowFetchPRBaseRejectsNonAncestorBaseSHA(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	fetchBase := workflowStepByName(t, workflow.Jobs, "verify", ciWorkflowFetchBaseStepName)
	repo, nonAncestorSHA := createCIWorkflowDivergedRepo(t)
	output, err := runCIWorkflowShellStep(t, repo, fetchBase.Run, map[string]string{
		"ACT":         "1",
		"GITHUB_ENV":  filepath.Join(t.TempDir(), "non-ancestor.env"),
		"BASE_SOURCE": "github-event",
		"BASE_SHA":    nonAncestorSHA,
	})
	if err == nil {
		t.Fatal("fetch PR base succeeded for a non-ancestor base SHA")
	}
	if !strings.Contains(output, "is not an ancestor of HEAD") {
		t.Fatalf("fetch PR base output = %q, want non-ancestor failure", output)
	}
}

func TestCIWorkflowHostedPullRequestWithoutBaseSHAFailsClosed(t *testing.T) {
	t.Parallel()

	assertCIWorkflowHostedPullRequestWithoutBaseSHAFailsClosed(t)
}

func TestCIWorkflowHostedPullRequestWithoutBaseSHAIgnoresInheritedBaseSHA(t *testing.T) {
	t.Setenv("BASE_SHA", "1d020ed342f184786ba7525d456939f701193cfb")

	assertCIWorkflowHostedPullRequestWithoutBaseSHAFailsClosed(t)
}

func assertCIWorkflowHostedPullRequestWithoutBaseSHAFailsClosed(t *testing.T) {
	t.Helper()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	fetchBase := workflowStepByName(t, workflow.Jobs, "verify", ciWorkflowFetchBaseStepName)
	repo, _ := createCIWorkflowGitRepo(t)
	output, err := runCIWorkflowShellStep(t, repo, fetchBase.Run, map[string]string{
		"GITHUB_ENV": filepath.Join(t.TempDir(), "hosted.env"),
	})
	if err == nil {
		t.Fatal("fetch PR base succeeded without a hosted PR base SHA")
	}
	if !strings.Contains(output, "PR base SHA is unavailable; cannot prepare memory benchmark base.") {
		t.Fatalf("fetch PR base output = %q, want hosted missing-base failure", output)
	}
}

func TestCIWorkflowVerifiesVSCodePackageContractAfterInstallingDependencies(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	vscodeSmoke := workflowJobByName(t, workflow.Jobs, "vscode-smoke")
	assertWorkflowStepOrder(t, vscodeSmoke, "Install extension dependencies", "Verify VS Code extension package contract", "Run VS Code smoke tests")
	contract := workflowStepByName(t, workflow.Jobs, "vscode-smoke", "Verify VS Code extension package contract")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "VS Code extension package contract", got: contract.Run, want: "go test ./scripts -run '^(TestVSCodeExtensionIconPackageContract|TestVSCodeExtensionPackagingHonorsBraceGlobIgnore)$' -count=1"},
	})
}

func TestPRMetadataWorkflowPassesMaintainerExemptionLabel(t *testing.T) {
	t.Parallel()
	assertPullRequestTriggerTypes(t, ".github/workflows/pr-metadata.yml")

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/pr-metadata.yml", &workflow)
	validation := workflowStepByName(t, workflow.Jobs, "validate", "Validate PR title and template")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "PR metadata exemption label env", got: validation.Env["PR_REGRESSION_EXEMPT_LABEL"], want: "${{ contains(github.event.pull_request.labels.*.name, 'regression-exempt') }}"},
	})
	assertWorkflowStepRunContainsAll(t, validation, "PR metadata validation", []string{
		`--regression-exempt-label "$PR_REGRESSION_EXEMPT_LABEL"`,
	})
}

func assertPullRequestTriggerTypes(t *testing.T, workflowPath string) {
	t.Helper()

	var workflow pullRequestTriggerWorkflow
	readYAMLConfig(t, workflowPath, &workflow)
	want := []string{"opened", "edited", "synchronize", "reopened", "ready_for_review", "labeled", "unlabeled"}
	got := workflow.On.PullRequest.Types
	if len(got) != len(want) {
		t.Fatalf("%s pull_request types = %v, want %v", workflowPath, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s pull_request types = %v, want %v", workflowPath, got, want)
		}
	}
}

const (
	ciWorkflowResolveBaseStepName = "Resolve PR base ref"
	ciWorkflowFetchBaseStepName   = "Fetch PR base"
)

func assertCIWorkflowPreparedBaseResolverStep(t *testing.T, step workflowStepConfig, label string) {
	t.Helper()

	assertWorkflowStepEnv(t, step, label, map[string]string{
		"PR_BASE_REF": "${{ github.event.pull_request.base.ref }}",
		"PR_BASE_SHA": "${{ github.event.pull_request.base.sha }}",
	})
	assertWorkflowStepRunContainsAll(t, step, label, []string{
		`base_ref="${PR_BASE_REF:-main}"`,
		`base_sha="${PR_BASE_SHA:-}"`,
		`base_source="github-event"`,
		`if [ -n "${ACT:-}" ] && [ -z "${base_sha}" ]; then`,
		`base_source="act-merge-base"`,
		`printf 'BASE_SOURCE=%s\n' "${base_source}"`,
		`printf 'BASE_SHA=%s\n' "${base_sha}"`,
		`printf 'BASE_REF=%s\n' "${base_ref}"`,
		`} >> "$GITHUB_ENV"`,
	})
	assertWorkflowStepRunOmitsAll(t, step, label, []string{
		`refs/remotes/origin/HEAD`,
		`git remote set-head origin --auto`,
		`HEAD^`,
	})
}

func assertCIWorkflowPreparedBaseFetchStep(t *testing.T, step workflowStepConfig, label string) {
	t.Helper()

	assertWorkflowStepRunContainsAll(t, step, label, []string{
		`base_ref="${BASE_REF:-}"`,
		`base_sha="${BASE_SHA:-}"`,
		`base_source="${BASE_SOURCE:-github-event}"`,
		`if [ "${base_source}" = "act-merge-base" ]; then`,
		`if ! git fetch --no-tags origin "${base_ref}"; then`,
		`git rev-parse --verify -q --end-of-options "origin/${base_ref}^{commit}"`,
		`base_sha="$(git merge-base "origin/${base_ref}" HEAD 2>/dev/null)"`,
		`printf 'BASE_SHA=%s\n' "${base_sha}"`,
		`printf 'MEMORY_BENCH_BASE=%s\n' "${base_sha}"`,
		`git fetch --no-tags origin "${base_sha}"`,
		`if [ -n "${base_ref}" ]; then`,
		`git fetch --no-tags origin "${base_ref}"`,
		`git rev-parse --verify -q --end-of-options "${base_sha}^{commit}"`,
		`git merge-base --is-ancestor "${base_sha}" HEAD`,
		`} >> "$GITHUB_ENV"`,
	})
	assertWorkflowStepRunOmitsAll(t, step, label, []string{
		`base_ref="${BASE_REF:-main}"`,
		`origin/main`,
		`--depth=1 origin "${base_sha}"`,
		`--depth=1 origin "${base_ref}"`,
		`git merge-base -- "${base_sha}" HEAD >/dev/null`,
		`MEMORY_BENCH_BASE="origin/${base_ref}"`,
		`HEAD^`,
	})
}

func assertCIWorkflowPreparedBaseRunStep(t *testing.T, step workflowStepConfig, label string) {
	t.Helper()

	assertWorkflowStepRunContainsAll(t, step, label, []string{
		`run_ci() {`,
		`make ci "BUILD_CHANNEL=${BUILD_CHANNEL}"`,
		`export MEMORY_BENCH_BASE="${MEMORY_BENCH_BASE:?prepared PR memory benchmark base is required}"`,
		`export MEMORY_BENCH_ENFORCE=0`,
		`PATH="$(go env GOPATH)/bin:$PATH"`,
		`run_ci`,
	})
	assertWorkflowStepRunOmitsAll(t, step, label, []string{
		`MEMORY_BENCH_BASE="origin/${base_ref}"`,
	})
}

func assertCIWorkflowRegressionProofUsesPreparedBaseSHA(t *testing.T, step workflowStepConfig, label string) {
	t.Helper()

	assertWorkflowStringValues(t, []workflowStringValue{
		{label: label + " condition", got: step.If, want: "${{ github.event_name == 'pull_request' && github.event.pull_request.user.login != 'renovate[bot]' }}"},
		{label: label + " title env", got: step.Env["PR_TITLE"], want: "${{ github.event.pull_request.title }}"},
		{label: label + " exemption label env", got: step.Env["PR_REGRESSION_EXEMPT_LABEL"], want: "${{ contains(github.event.pull_request.labels.*.name, 'regression-exempt') }}"},
	})
	if _, present := step.Env["PR_BASE_SHA"]; present {
		t.Fatal("regression proof step must not consume the raw pull_request.base.sha after PR base resolution")
	}
	assertWorkflowStepRunContainsAll(t, step, label, []string{
		`base_sha="${BASE_SHA:?prepared PR base SHA is required}"`,
		`go run ./tools/regressionproof --repo . --body-file "$PR_BODY_FILE" --title "$PR_TITLE" --base-sha "$base_sha" --regression-exempt-label "$PR_REGRESSION_EXEMPT_LABEL"`,
	})
	assertWorkflowStepRunOmitsAll(t, step, label, []string{
		`${{ github.event.pull_request.base.sha }}`,
		`--base-sha "$PR_BASE_SHA"`,
	})
}

func createCIWorkflowGitRepo(t *testing.T) (string, string) {
	t.Helper()

	repo := initCIWorkflowGitRemoteClone(t)
	baseSHA := commitCIWorkflowGitFile(t, repo, "fixture.txt", "base\n", "base")
	runCIWorkflowGit(t, repo, "push", "-u", "origin", "main")
	commitCIWorkflowTrackedFile(t, repo, "fixture.txt", "head\n", "head")
	return repo, baseSHA
}

func createCIWorkflowDivergedRepo(t *testing.T) (string, string) {
	t.Helper()

	repo := initCIWorkflowGitRemoteClone(t)
	commitCIWorkflowGitFile(t, repo, "fixture.txt", "base\n", "base")
	runCIWorkflowGit(t, repo, "push", "-u", "origin", "main")
	runCIWorkflowGit(t, repo, "checkout", "-b", "topic-base", "main")
	nonAncestorSHA := commitCIWorkflowGitFile(t, repo, "topic.txt", "topic\n", "topic")
	runCIWorkflowGit(t, repo, "push", "-u", "origin", "topic-base")
	runCIWorkflowGit(t, repo, "checkout", "main")
	commitCIWorkflowTrackedFile(t, repo, "fixture.txt", "head\n", "head")
	return repo, nonAncestorSHA
}

func initCIWorkflowGitRepo(t *testing.T) string {
	t.Helper()

	repo := t.TempDir()
	runCIWorkflowGit(t, repo, "init", "-b", "main")
	runCIWorkflowGit(t, repo, "config", "user.name", "CI Workflow Test")
	runCIWorkflowGit(t, repo, "config", "user.email", "ci-workflow-test@example.com")
	return repo
}

func initCIWorkflowGitRemoteClone(t *testing.T) string {
	t.Helper()

	parent := t.TempDir()
	remote := filepath.Join(parent, "origin.git")
	runCIWorkflowGitInDir(t, parent, "init", "--bare", "--initial-branch=main", remote)

	seed := filepath.Join(parent, "seed")
	runCIWorkflowGitInDir(t, parent, "clone", remote, seed)
	configureCIWorkflowGitIdentity(t, seed)
	commitCIWorkflowGitFile(t, seed, "seed.txt", "seed\n", "seed")
	runCIWorkflowGit(t, seed, "push", "-u", "origin", "main")

	repo := filepath.Join(parent, "repo")
	runCIWorkflowGitInDir(t, parent, "clone", remote, repo)
	configureCIWorkflowGitIdentity(t, repo)
	return repo
}

func configureCIWorkflowGitIdentity(t *testing.T, repo string) {
	t.Helper()

	runCIWorkflowGit(t, repo, "config", "user.name", "CI Workflow Test")
	runCIWorkflowGit(t, repo, "config", "user.email", "ci-workflow-test@example.com")
}

func createCIWorkflowACTMergeBaseRepo(t *testing.T) (string, string, string) {
	t.Helper()

	repo := initCIWorkflowGitRemoteClone(t)
	baseSHA := commitCIWorkflowGitFile(t, repo, "fixture.txt", "base\n", "base")
	runCIWorkflowGit(t, repo, "push", "-u", "origin", "main")
	runCIWorkflowGit(t, repo, "checkout", "-b", "feature/merge-base-proof", "main")
	headParentSHA := commitCIWorkflowTrackedFile(t, repo, "fixture.txt", "feature-one\n", "feature one")
	commitCIWorkflowTrackedFile(t, repo, "fixture.txt", "feature-two\n", "feature two")
	return repo, baseSHA, headParentSHA
}

func commitCIWorkflowGitFile(t *testing.T, repo string, relativePath string, contents string, message string) string {
	t.Helper()

	if err := os.WriteFile(filepath.Join(repo, relativePath), []byte(contents), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", relativePath, err)
	}
	runCIWorkflowGit(t, repo, "add", relativePath)
	runCIWorkflowGit(t, repo, "commit", "-m", message)
	return strings.TrimSpace(runCIWorkflowGit(t, repo, "rev-parse", "HEAD"))
}

func commitCIWorkflowTrackedFile(t *testing.T, repo string, relativePath string, contents string, message string) string {
	t.Helper()

	if err := os.WriteFile(filepath.Join(repo, relativePath), []byte(contents), 0o644); err != nil {
		t.Fatalf("write tracked fixture %s: %v", relativePath, err)
	}
	runCIWorkflowGit(t, repo, "commit", "-am", message)
	return strings.TrimSpace(runCIWorkflowGit(t, repo, "rev-parse", "HEAD"))
}

func runCIWorkflowGit(t *testing.T, repo string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func runCIWorkflowGitInDir(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed in %s: %v\n%s", strings.Join(args, " "), dir, err, output)
	}
	return string(output)
}

func runCIWorkflowShellStep(t *testing.T, repo string, script string, env map[string]string) (string, error) {
	t.Helper()

	cmd := exec.Command("/bin/bash", "-euo", "pipefail", "-c", script)
	cmd.Dir = repo
	cmd.Env = minimalCIWorkflowShellStepEnv()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func minimalCIWorkflowShellStepEnv() []string {
	keys := []string{"PATH", "HOME", "TMPDIR"}
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			values = append(values, key+"="+value)
		}
	}
	return values
}

func readCIWorkflowEnvFile(t *testing.T, path string) map[string]string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read GITHUB_ENV file %s: %v", path, err)
	}
	values := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("malformed GITHUB_ENV line %q", line)
		}
		values[key] = value
	}
	return values
}
