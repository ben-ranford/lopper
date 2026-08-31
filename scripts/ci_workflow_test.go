package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/gitexec"
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

type pullRequestTargetWorkflowOn struct {
	PullRequestTarget pullRequestTrigger `yaml:"pull_request_target"`
}

type pullRequestTargetTriggerWorkflow struct {
	On pullRequestTargetWorkflowOn `yaml:"on"`
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
	assertWorkflowJobPermissions(t, verify, "ci verify", map[string]string{"contents": "read", "issues": "read"})
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
		`inline-suppressions.json)`,
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
		`inline-suppressions.json)`,
		`PR report input exceeds the 128 KiB publication limit: ${name}`,
	})
	if got, want := shellArrayValues(t, validateInputs.Run, "required_files"), []string{"lopper-base-outcome.txt", "lopper-delta-outcome.txt"}; !slices.Equal(got, want) {
		t.Fatalf("required PR report inputs = %q, want %q", got, want)
	}

	assertWorkflowStepAbsent(t, workflow.Jobs, "publish-pr-reports", "Post SonarQube review comments (PR)")
	assertWorkflowEnvKeyAbsent(t, workflow.Jobs, "SONAR_TOKEN")
}

func TestInlineSuppressionTrackingWorkflowUsesTrustedPullRequestTarget(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/inline-suppression-tracking.yml", &workflow)
	workflowText := readConfig(t, ".github/workflows/inline-suppression-tracking.yml")

	for _, fragment := range []string{
		"pull_request_target:",
		"contents: read",
		"issues: write",
		"pull-requests: read",
		"concurrency:",
		"group: inline-suppression-tracking-${{ github.event.pull_request.number }}",
		"cancel-in-progress: false",
		"TRUSTED_TRACKER_REF: ${{ github.workflow_sha }}",
		"path: 'scripts/inline_suppression_tracker.js'",
		"ref: process.env.TRUSTED_TRACKER_REF",
		"flag: 'wx'",
		"require(process.env.SUPPRESSION_TRACKER_PATH)",
	} {
		if !strings.Contains(workflowText, fragment) {
			t.Fatalf("inline suppression tracking workflow missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"actions/checkout@",
		"actions/download-artifact@",
		"pr-report-inputs",
		"pull_request:\n",
		"github.event.pull_request.head",
	} {
		if strings.Contains(workflowText, forbidden) {
			t.Fatalf("inline suppression tracking workflow contains unsafe fragment %q", forbidden)
		}
	}

	// A pull request that closes without merging gets no further
	// opened/edited/synchronize/reopened/ready_for_review event, so without
	// a "closed" trigger its bot-created tracking issues would remain open
	// indefinitely for suppressions that never entered the repository.
	var trigger pullRequestTargetTriggerWorkflow
	readYAMLConfig(t, ".github/workflows/inline-suppression-tracking.yml", &trigger)
	if !slices.Contains(trigger.On.PullRequestTarget.Types, "closed") {
		t.Fatalf("inline suppression tracking workflow must trigger on the \"closed\" pull_request_target event, got types: %v", trigger.On.PullRequestTarget.Types)
	}

	track := workflowJobByName(t, workflow.Jobs, "track")
	assertWorkflowJobOmitsCheckout(t, track, "inline suppression tracking")
	assertWorkflowJobStepRunsOmitAllFold(t, track, "inline suppression tracking", []string{
		"go run ./",
		"make ",
		"npm ",
		"npx ",
		"git ",
		"scripts/",
		"./extensions/",
	})
	assertWorkflowStepOrder(t, track, "Materialize trusted suppression tracker", "Track inline suppressions from trusted diff")
}

func TestInlineSuppressionTrackerControllerFailsClosedBeforeMutations(t *testing.T) {
	t.Parallel()

	controller := readConfig(t, "scripts/inline_suppression_tracker.js")
	assertWorkflowStepRunContainsAll(t, workflowStepConfig{Run: controller}, "inline suppression tracker controller", []string{
		"MAX_CHANGED_FILES = 3000",
		"count > MAX_CHANGED_FILES",
		"files.length !== expectedCount",
		"diff patch is unavailable",
		"refusing to publish tracking mutations",
		"records.size > MAX_RECORDS",
		"pull.state && pull.state !== 'open'",
		"github.rest.pulls.listFiles",
		"github.rest.search.issuesAndPullRequests",
		"github.rest.issues.update",
		"github.rest.issues.create",
		"lopper-inline-suppression-pr:",
		"reconcileDisappearedSuppressions",
		"state: 'closed'",
	})
	assertWorkflowMarkerOrder(t, controller, "records.size > MAX_RECORDS", "return records;")
	assertWorkflowMarkerOrder(t, controller, "const { records, pull } = await recomputeSuppressionRecords({ github, context });", "await reconcileDisappearedSuppressions({ github, context, pull, records, core });")
	assertWorkflowMarkerOrder(t, controller, "await reconcileDisappearedSuppressions({ github, context, pull, records, core });", "await upsertTrackingIssue({ github, context, record, pullNumber: pull.number });")
	assertWorkflowStepRunOmitsAll(t, workflowStepConfig{Run: controller}, "inline suppression tracker controller", []string{
		"inline-suppressions.json",
		"pr-report-inputs",
		"isForkPullRequest",
		"skipped for fork pull request",
	})
}

func TestCIWorkflowEmitsInlineSuppressionRecordsFromVerifyJob(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	runCI := workflowStepByName(t, workflow.Jobs, "verify", "Run CI target")
	assertWorkflowStepEnv(t, runCI, "ci verify run target", map[string]string{
		"GH_EVENT_NAME":               "${{ github.event_name }}",
		"SUPPRESSION_TRACKING_OUTPUT": ".artifacts/inline-suppressions.json",
	})
	assertWorkflowStepRunOmitsAll(t, runCI, "ci verify run target", []string{
		`GH_TOKEN`,
	})
}

func TestCIWorkflowGatesMergeOnHeadAssociatedSuppressionTrackingResult(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	verify := workflowJobByName(t, workflow.Jobs, "verify")
	assertWorkflowStepOrder(t, verify, "Run CI target", "Verify inline suppression tracking issues were published", "Prove regression tests for fix PRs")

	gate := workflowStepByName(t, workflow.Jobs, "verify", "Verify inline suppression tracking issues were published")
	if gate.If != "${{ github.event_name == 'pull_request' && !env.ACT }}" {
		t.Fatalf("suppression tracking gate must only run for pull_request events, got if: %q", gate.If)
	}
	assertWorkflowStepEnv(t, gate, "suppression tracking gate", map[string]string{
		"GH_TOKEN":          "${{ github.token }}",
		"PR_NUMBER":         "${{ github.event.pull_request.number }}",
		"SUPPRESSIONS_FILE": ".artifacts/inline-suppressions.json",
	})
	assertWorkflowStepRunContainsAll(t, gate, "suppression tracking gate", []string{
		"jq -r '.suppressions[].fingerprint'",
		`author:github-actions[bot]`,
		`lopper-inline-suppression-pr:${PR_NUMBER}`,
		"sleep 15",
		"exit 1",
	})
	// The wait must share one deadline across every fingerprint (outer
	// `seq 1 40` loop polling all still-missing fingerprints per round),
	// not an independent 40-round wait nested inside a per-fingerprint
	// loop -- otherwise a persistently failed tracker could sleep for
	// MAX_RECORDS x 10 minutes instead of reporting promptly.
	assertWorkflowMarkerOrder(t, gate.Run, `for _ in $(seq 1 40); do`, `for fingerprint in "${missing[@]}"; do`)
	assertWorkflowStepRunContainsAll(t, gate, "suppression tracking gate", []string{
		`missing=("${fingerprints[@]}")`,
		"still_missing=()",
	})
	// SUPPRESSIONS_FILE is produced by PR-controlled checked-out code (the
	// suppression detector and Makefile), so a PR could neuter it to
	// silently omit the artifact while still adding an untracked
	// suppression. The empty/missing-artifact early-outs must not be
	// trusted blindly; they must be corroborated against GitHub's own
	// (PR-uncontrollable) diff data first. This must be compared against
	// every run of the detector, not only when the artifact is fully
	// empty -- a PR could leave the detector partially working (already
	// has one published suppression, adds a second, tweaks the detector
	// to keep emitting only the first) so the artifact is nonempty but
	// under-reports.
	assertWorkflowStepRunContainsAll(t, gate, "suppression tracking gate", []string{
		`gh api "repos/${GITHUB_REPOSITORY}/pulls/${PR_NUMBER}/files" --paginate`,
		"suspect_count",
		`recorded_count="${#fingerprints[@]}"`,
		// The trusted tracker's fileExtension() lowercases before matching
		// (main.GO, component.TSX), so this scope filter must too, or a
		// failed/pending trusted run on such a file wouldn't be waited for.
		`; "i")`,
		// A tampered detector could repeat an already-tracked fingerprint
		// to pad recorded_count and mask a genuinely untracked one.
		`jq -r '.suppressions[].fingerprint' "${SUPPRESSIONS_FILE}"`,
		// GitHub omits `patch` for large/truncated files; treating that as
		// "no suspect lines" would let a PR pair an oversized file with a
		// tampered detector to make both counts read zero.
		"select(.patch == null)",
		"Refusing to trust an under-scanned diff",
		// A pure rename (status "renamed", zero additions/deletions) is also
		// patchless but genuinely has no suspect lines; the trusted tracker
		// exempts this exact shape, so the incomplete-patch scan must too or
		// every content-preserving rename fails this required check.
		`select(.status != \"renamed\" or .additions != 0 or .deletions != 0)`,
		// Comparing only cardinalities lets a PR swap a tracked suppression
		// for an untracked one of the same shape while the counts stay
		// equal; matching (file, exact line content) pairs instead ties the
		// independent scan to what was actually added.
		`suspect_pairs_file="$(mktemp)"`,
		`recorded_pairs_file="$(mktemp)"`,
		// comm requires sorted input, but deduplicating first (sort -u)
		// would collapse a PR that adds the same suspect line twice into
		// one suspect and let a tampered detector satisfy it by reporting
		// only one occurrence's record; plain `sort` preserves multiplicity
		// so comm computes a correct multiset difference instead.
		`comm -23 <(sort "${suspect_pairs_file}") <(sort "${recorded_pairs_file}")`,
		// jq's @tsv formatter escapes tab/newline/backslash/CR within a
		// field, but the suspect side reads content un-escaped straight
		// from the diff; comparing an escaped recorded pair against its
		// un-escaped suspect pair would never match a suppression whose
		// content contains one of those bytes. Emitting raw fields with an
		// explicit delimiter avoids introducing any escaping.
		`jq -j '.suppressions[] | .file, "\u0001", .content, "\n"'`,
		// The trusted tracker never publishes more than MAX_RECORDS (100)
		// entries per PR; a larger recorded_count is necessarily tampered
		// and must be rejected before the polling loop issues one gh issue
		// list request per fingerprint, or a PR-controlled detector could
		// emit thousands of fingerprints and exhaust the API quota/timeout.
		`if [ "${recorded_count}" -gt 100 ]; then`,
		// Checking only the single character before a candidate comment
		// delimiter misses a marker preceded by ordinary text inside an
		// otherwise-open string literal (e.g. `"Use //nolint to
		// suppress"`); blanking out quoted-region interiors before
		// matching mirrors the trusted tracker's isInsideQuotedRegion.
		"function mask_quoted_regions(s, initial_quote,    result, i, c, quote, n, narrow_single_quote)",
		`tolower(masked) ~ pat`,
		// Rust and C/C++ have no multi-character single-quoted strings, so
		// treating every apostrophe as a generic string delimiter would let
		// an unterminated Rust lifetime or C++ digit separator swallow the
		// rest of the line and hide a real suppression comment after it.
		`narrow_single_quote = (tolower(fname) ~ /\.(rs|c|cc|cpp|cxx|h|hh|hpp)$/)`,
		// Quote state resets per line by default; a multi-line string
		// (e.g. a JavaScript template literal) that closes partway
		// through a later added line must carry that state across, or the
		// closing delimiter looks like it opens a fresh quoted region and
		// masks a real suppression comment following it -- silently, since
		// the scan then reports no suspects rather than failing loudly.
		// Reset only at hunk boundaries, where a zero-context diff has
		// unseen content this scan never reads.
		`/^@@ / { quote_state = ""; next }`,
		`masked = mask_quoted_regions(content, quote_state)`,
		`quote_state = final_quote_state`,
		// Each record is a distinct occurrence and must have its own
		// distinct fingerprint; deduplicating with sort -u before counting
		// would let a tampered detector report two records with identical
		// (file, content) -- passing the exact-pair multiset comparison --
		// while reusing one already-tracked fingerprint for both.
		`unique_fingerprint_count="$(printf '%s\n' "${fingerprints[@]}" | sort -u | wc -l | tr -d ' ')"`,
		// Neither the exact-pair comparison (file, content) nor the
		// polling loop (fingerprint has an open issue) ties a record's own
		// fingerprint to its own file and content; PR-controlled code
		// could pair a genuinely-recorded (file, content) with an
		// unrelated, still-open fingerprint borrowed from a different
		// suppression. A fingerprint is a deterministic function of only
		// (file, content, occurrence), so it must be recomputed and
		// matched for some occurrence.
		//
		// MAX_RECORDS (100) bounds how many NEW records a PR can
		// introduce, not the occurrence ordinal among lines already
		// present in the complete file; a file with over 100 pre-existing
		// identical suppression lines legitimately produces occurrence
		// 101 or higher, so the search range must extend well past 100.
		// Doing that search in-process (Python) instead of one shasum
		// subprocess per candidate keeps a wide search range fast.
		"MAX_OCCURRENCE = 100000",
		"bound = hashlib.sha256(base).hexdigest().encode() == fingerprint",
		"for occurrence in range(2, MAX_OCCURRENCE + 1):",
	})
	assertWorkflowMarkerOrder(t, gate.Run, "suspect_count=", `recorded_count=0`)
	assertWorkflowMarkerOrder(t, gate.Run, `recorded_count=0`, `if [ "${recorded_count}" -gt 100 ]; then`)
	assertWorkflowMarkerOrder(t, gate.Run, `if [ "${recorded_count}" -gt 100 ]; then`, `missing_suspects="$(comm -23`)
	assertWorkflowMarkerOrder(t, gate.Run, `missing_suspects="$(comm -23`, `if [ "${missing_suspects:-0}" -gt 0 ]; then`)
	assertWorkflowMarkerOrder(t, gate.Run, `if [ "${missing_suspects:-0}" -gt 0 ]; then`, `missing=("${fingerprints[@]}")`)
	// Without a trailing marker-boundary check, a longer word like
	// "nolinter" or "nosection" would also match and could false-positive
	// this required check against an otherwise valid PR. And ".githooks/"
	// must be anchored to the repository root, matching isSourceFile()
	// and source_file_pattern exactly, or a nested .githooks/ directory
	// would similarly cause a false count mismatch.
	assertWorkflowStepRunContainsAll(t, gate, "suppression tracking gate", []string{
		`|${coverage_prefix}:[[:space:]]*ignore)))([^[:alnum:]_-]|$)"`,
		`test("^\\.githooks/|`,
	})
	// A comment delimiter needs no preceding whitespace -- it can follow
	// code directly -- so the leading boundary excludes only ":" (URL
	// schemes) rather than requiring whitespace or start-of-line; string
	// literals are handled separately by mask_quoted_regions.
	assertWorkflowStepRunContainsAll(t, gate, "suppression tracking gate", []string{
		`suspect_pattern="(^|[^:])`,
		`(//|/[*]+|#)`,
	})
	// SUPPRESSIONS_FILE is PR-controlled, and each fingerprint from it is
	// later interpolated directly into a `gh issue list --jq` expression;
	// an unvalidated value containing jq syntax (e.g. `")))] | 1 #`) could
	// make an empty search result look like a match, defeating the whole
	// verification. Every fingerprint must be validated as a 64-character
	// hex digest before any of them are used, closing that injection path.
	assertWorkflowMarkerOrder(t, gate.Run, `recorded_count="${#fingerprints[@]}"`, `if [[ ! "${fingerprint}" =~ ^[0-9a-f]{64}$ ]]; then`)
	assertWorkflowMarkerOrder(t, gate.Run, `if [[ ! "${fingerprint}" =~ ^[0-9a-f]{64}$ ]]; then`, `if [ "${recorded_count}" -gt 100 ]; then`)
	// A substring `contains` check on the tracking issue body accepts the
	// marker text anywhere, including inside an attacker-controlled echoed
	// "Source line"; the poll must require it in the canonical first-two-
	// lines header position, matching issueBodyIncludesMarker's contract.
	assertWorkflowStepRunContainsAll(t, gate, "suppression tracking gate", []string{
		`test(\"^<!-- lopper-inline-suppression:${fingerprint} -->\\n<!-- lopper-inline-suppression-pr:${PR_NUMBER} -->\")`,
	})
}

// TestCIWorkflowFingerprintBindingScriptExecutesCorrectly actually runs the
// embedded Python fingerprint-binding script (not just asserting on its
// source text): the script is indented to satisfy the YAML block scalar it
// lives in, and python3 -c rejects indented top-level code, so a purely
// textual assertion could never catch that mismatch -- it takes real
// execution, which is exactly how this bug first reached CI.
func TestCIWorkflowFingerprintBindingScriptExecutesCorrectly(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	gate := workflowStepByName(t, workflow.Jobs, "verify", "Verify inline suppression tracking issues were published")

	const startMarker = "fingerprint_binding_script='"
	const endMarker = `fingerprint_binding_dedented="$(printf '%s' "${fingerprint_binding_script}" | python3 -c 'import sys, textwrap; sys.stdout.write(textwrap.dedent(sys.stdin.read()))')"` + "\n"
	startIdx := strings.Index(gate.Run, startMarker)
	if startIdx == -1 {
		t.Fatalf("suppression tracking gate is missing the fingerprint-binding script")
	}
	endIdx := strings.Index(gate.Run[startIdx:], endMarker)
	if endIdx == -1 {
		t.Fatalf("suppression tracking gate is missing the fingerprint-binding dedent line")
	}
	invocationStart := startIdx + endIdx + len(endMarker)
	invocationEnd := strings.Index(gate.Run[invocationStart:], "\n")
	if invocationEnd == -1 {
		t.Fatalf("suppression tracking gate is missing the fingerprint-binding invocation line")
	}
	script := "set -euo pipefail\n" + gate.Run[startIdx:invocationStart+invocationEnd]

	dir := t.TempDir()
	suppressionsPath := filepath.Join(dir, "inline-suppressions.json")

	valid := suppressionFingerprint("main.go", "line one", 1)
	writeFile(t, suppressionsPath, `{"suppressions":[{"file":"main.go","content":"line one","fingerprint":"`+valid+`"}]}`)
	if output, err := runShellCommand(dir, script, map[string]string{"SUPPRESSIONS_FILE": suppressionsPath}); err != nil {
		t.Fatalf("expected a fingerprint bound to its own (file, content) to pass, output:\n%s", output)
	}

	reused := suppressionFingerprint("main.go", "different content", 1)
	writeFile(t, suppressionsPath, `{"suppressions":[{"file":"main.go","content":"line one","fingerprint":"`+reused+`"}]}`)
	output, err := runShellCommand(dir, script, map[string]string{"SUPPRESSIONS_FILE": suppressionsPath})
	if err == nil {
		t.Fatalf("expected a fingerprint reused from different content to be rejected, output:\n%s", output)
	}
	if !strings.Contains(output, "does not match its own recorded file and content") {
		t.Fatalf("expected a fingerprint-binding rejection message, got:\n%s", output)
	}
}

func assertWorkflowMarkerOrder(t *testing.T, script string, beforeMarker string, afterMarker string) {
	t.Helper()

	beforeIndex := strings.Index(script, beforeMarker)
	afterIndex := strings.Index(script, afterMarker)
	if beforeIndex == -1 || afterIndex == -1 {
		t.Fatalf("workflow script missing order marker %q or %q", beforeMarker, afterMarker)
	}
	if afterIndex < beforeIndex {
		t.Fatalf("workflow marker %q appeared before %q", afterMarker, beforeMarker)
	}
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
	assertManualDispatchTrigger(t, ".github/workflows/ci.yml")

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	verify := workflowJobByName(t, workflow.Jobs, "verify")
	expectedOrder := []string{
		"Resolve PR base ref",
		"Write PR body for regression proof",
		"Fetch PR base",
		"Run CI target",
		"Prove regression tests for fix PRs",
	}
	assertWorkflowStepOrder(t, verify, expectedOrder...)

	resolveBase := workflowStepByName(t, workflow.Jobs, "verify", "Resolve PR base ref")
	assertWorkflowStepRunContainsAll(t, resolveBase, "resolve PR base ref", []string{
		`printf 'BASE_REF=%s\n' "${base_ref}" >> "$GITHUB_ENV"`,
		`printf 'BASE_SHA=%s\n' "${PR_BASE_SHA}" >> "$GITHUB_ENV"`,
	})

	writeBody := workflowStepByName(t, workflow.Jobs, "verify", "Write PR body for regression proof")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "regression body writer action", got: writeBody.Uses, want: "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3"},
		{label: "regression body writer condition", got: writeBody.If, want: "${{ github.event_name == 'pull_request' }}"},
	})
	assertWorkflowStepRunContainsAll(t, workflowStepConfig{Run: writeBody.With["script"]}, "regression body writer", []string{
		`const bodyPath = path.join(process.env.RUNNER_TEMP, 'pr-regression-body.md');`,
		`core.exportVariable('PR_BODY_FILE', bodyPath);`,
	})

	fetchBase := workflowStepByName(t, workflow.Jobs, "verify", "Fetch PR base")
	assertWorkflowStepRunContainsAll(t, fetchBase, "fetch PR base", []string{
		`resolve_act_merge_base() {`,
		`base_sha="${BASE_SHA:-}"`,
		`if [ -z "${base_sha}" ]; then`,
		`base_sha="$(resolve_act_merge_base "${base_ref}")"`,
		`git fetch --no-tags origin "${base_sha}"`,
		`git fetch --no-tags origin "${base_ref}"`,
		`git rev-parse --verify -q --end-of-options "${base_sha}^{commit}"`,
		`git merge-base --is-ancestor "${base_sha}" HEAD`,
		`printf 'MEMORY_BENCH_BASE=%s\n' "${base_sha}" >> "$GITHUB_ENV"`,
	})

	lopperBase := workflowStepByName(t, workflow.Jobs, "verify", "Run lopper self-analysis (base branch)")
	assertWorkflowStepRunContainsAll(t, lopperBase, "lopper immutable base", []string{
		`base_sha="${MEMORY_BENCH_BASE:?prepared PR memory benchmark base is required}"`,
		`git worktree add --detach .artifacts/base "${base_sha}"`,
	})
	assertWorkflowStepRunOmitsAll(t, lopperBase, "lopper immutable base", []string{
		`git worktree add --detach .artifacts/base "origin/${base_ref}"`,
	})

	proof := workflowStepByName(t, workflow.Jobs, "verify", "Prove regression tests for fix PRs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "regression proof condition", got: proof.If, want: "${{ github.event_name == 'pull_request' && github.event.pull_request.user.login != 'renovate[bot]' }}"},
		{label: "regression proof title env", got: proof.Env["PR_TITLE"], want: "${{ github.event.pull_request.title }}"},
		{label: "regression proof base env", got: proof.Env["PR_BASE_SHA"], want: "${{ github.event.pull_request.base.sha }}"},
		{label: "regression proof exemption label env", got: proof.Env["PR_REGRESSION_EXEMPT_LABEL"], want: "${{ contains(github.event.pull_request.labels.*.name, 'regression-exempt') }}"},
	})
	assertWorkflowStepRunContainsAll(t, proof, "regression proof step", []string{
		`go run ./tools/regressionproof --repo . --body-file "$PR_BODY_FILE" --title "$PR_TITLE" --base-sha "$PR_BASE_SHA" --regression-exempt-label "$PR_REGRESSION_EXEMPT_LABEL"`,
	})
}

func TestCIWorkflowUsesActOnlyMergeBasePRBaseFallback(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	fetchBase := workflowStepByName(t, workflow.Jobs, "verify", "Fetch PR base")
	assertWorkflowStepRunContainsAll(t, fetchBase, "fetch PR base act fallback", []string{
		`if [ -n "${ACT:-}" ]; then`,
		`base_sha="$(resolve_act_merge_base "${base_ref}")"`,
		`ACT pull_request event payload omitted PR base SHA; resolved immutable merge base ${base_sha} from ${base_ref}.`,
		`echo "::error::ACT pull_request event payload omitted PR base SHA and base ref '${requested_ref}' is unavailable; cannot resolve immutable merge base." >&2`,
		`echo "::error::ACT pull_request event payload omitted PR base SHA and base ref '${requested_ref}' is unrelated to HEAD; cannot resolve immutable merge base." >&2`,
	})
}

func TestCIWorkflowFailsClosedWithoutHostedPRBaseSHA(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	fetchBase := workflowStepByName(t, workflow.Jobs, "verify", "Fetch PR base")
	assertWorkflowStepRunContainsAll(t, fetchBase, "fetch PR base hosted guard", []string{
		`echo "::error::PR base SHA is unavailable; cannot prepare memory benchmark base." >&2`,
		`git rev-parse --verify -q --end-of-options "${base_sha}^{commit}" >/dev/null`,
		`git merge-base --is-ancestor "${base_sha}" HEAD`,
		`echo "::error::PR base SHA '${base_sha}' is not an ancestor of HEAD; memory benchmark gate cannot run safely." >&2`,
	})
	assertWorkflowStepRunOmitsAll(t, fetchBase, "fetch PR base hosted guard", []string{
		`git merge-base -- "${base_sha}" HEAD`,
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
	assertWorkflowStepRunOmitsAll(t, runCI, "ci verify run target", []string{
		`MEMORY_BENCH_BASE="origin/${base_ref}"`,
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

func TestCIWorkflowVerifyRollingUsesImmutablePRBaseSHA(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	verifyRolling := workflowJobByName(t, workflow.Jobs, "verify-rolling")
	assertWorkflowStepOrder(t, verifyRolling, "Resolve PR base ref", "Fetch PR base", "Run CI target with rolling defaults")

	resolveBase := workflowStepByName(t, workflow.Jobs, "verify-rolling", "Resolve PR base ref")
	assertWorkflowStepRunContainsAll(t, resolveBase, "resolve rolling PR base ref", []string{
		`printf 'BASE_REF=%s\n' "${base_ref}" >> "$GITHUB_ENV"`,
		`printf 'BASE_SHA=%s\n' "${PR_BASE_SHA}" >> "$GITHUB_ENV"`,
	})

	fetchBase := workflowStepByName(t, workflow.Jobs, "verify-rolling", "Fetch PR base")
	assertWorkflowStepRunContainsAll(t, fetchBase, "fetch rolling PR base", []string{
		`resolve_act_merge_base() {`,
		`base_sha="${BASE_SHA:-}"`,
		`base_sha="$(resolve_act_merge_base "${base_ref}")"`,
		`git fetch --no-tags origin "${base_sha}"`,
		`git fetch --no-tags origin "${base_ref}"`,
		`git rev-parse --verify -q --end-of-options "${base_sha}^{commit}" >/dev/null`,
		`git merge-base --is-ancestor "${base_sha}" HEAD`,
		`printf 'MEMORY_BENCH_BASE=%s\n' "${base_sha}" >> "$GITHUB_ENV"`,
	})
	assertWorkflowStepRunOmitsAll(t, fetchBase, "fetch rolling PR base", []string{
		`git fetch --no-tags --depth=1 origin "${base_ref}"`,
	})

	runCI := workflowStepByName(t, workflow.Jobs, "verify-rolling", "Run CI target with rolling defaults")
	assertWorkflowStepRunContainsAll(t, runCI, "run rolling CI target", []string{
		`export MEMORY_BENCH_BASE="${MEMORY_BENCH_BASE:?prepared PR memory benchmark base is required}"`,
		`export MEMORY_BENCH_ENFORCE=0`,
		`make ci BUILD_CHANNEL="${BUILD_CHANNEL}"`,
	})
	assertWorkflowStepRunOmitsAll(t, runCI, "run rolling CI target", []string{
		`export MEMORY_BENCH_BASE="origin/${base_ref}"`,
	})
}

func TestCIWorkflowVerifyUsesMergeBaseFallbackAndImmutableBaseWhenPRBaseRefDrifts(t *testing.T) {
	t.Parallel()
	exercisePRBaseWorkflowJob(t, prBaseWorkflowJobConfig{
		jobName:     "verify",
		runStepName: "Run CI target",
		runLabel:    "verify",
		checkLopper: true,
	})
}

func TestCIWorkflowVerifyRollingUsesMergeBaseFallbackWhenPRBaseRefDrifts(t *testing.T) {
	t.Parallel()
	exercisePRBaseWorkflowJob(t, prBaseWorkflowJobConfig{
		jobName:      "verify-rolling",
		runStepName:  "Run CI target with rolling defaults",
		runLabel:     "verify-rolling",
		buildChannel: "rolling",
	})
}

type prBaseScenario struct {
	baseSHA       string
	driftSHA      string
	headParentSHA string
	worktree      string
}

type prBaseWorkflowJobConfig struct {
	jobName      string
	runStepName  string
	runLabel     string
	buildChannel string
	checkLopper  bool
}

func exercisePRBaseWorkflowJob(t *testing.T, cfg prBaseWorkflowJobConfig) {
	t.Helper()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	fetchBase := workflowStepByName(t, workflow.Jobs, cfg.jobName, "Fetch PR base")
	runCI := workflowStepByName(t, workflow.Jobs, cfg.jobName, cfg.runStepName)

	scenario := newPRBaseScenario(t)
	assertScenarioUsesDistinctMergeBase(t, scenario)
	assertActFetchStepResolvesImmutableBase(t, scenario, fetchBase.Run, cfg.runLabel)
	assertRunStepUsesImmutableBase(t, scenario, runCI.Run, cfg)

	if cfg.checkLopper {
		lopperBase := workflowStepByName(t, workflow.Jobs, cfg.jobName, "Run lopper self-analysis (base branch)")
		assertLopperStepUsesImmutableBase(t, scenario, lopperBase.Run, cfg.runLabel)
	}
}

func newPRBaseScenario(t *testing.T) prBaseScenario {
	t.Helper()

	origin := filepath.Join(t.TempDir(), "origin.git")
	runGitCommand(t, t.TempDir(), "init", "--bare", origin)

	seed := filepath.Join(t.TempDir(), "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatalf("create seed repo: %v", err)
	}
	runGitCommand(t, seed, "init", "-b", "main")
	runGitCommand(t, seed, "config", "user.name", "Ben Ranford")
	runGitCommand(t, seed, "config", "user.email", "84072202+ben-ranford@users.noreply.github.com")

	writeFile(t, filepath.Join(seed, "README.md"), "base\n")
	runGitCommand(t, seed, "add", "README.md")
	runGitCommand(t, seed, "commit", "-m", "base")
	baseSHA := strings.TrimSpace(runGitCommand(t, seed, "rev-parse", "HEAD"))

	runGitCommand(t, seed, "checkout", "-b", "feature")
	writeFile(t, filepath.Join(seed, "feature.txt"), "feature-1\n")
	runGitCommand(t, seed, "add", "feature.txt")
	runGitCommand(t, seed, "commit", "-m", "feature commit one")
	headParentSHA := strings.TrimSpace(runGitCommand(t, seed, "rev-parse", "HEAD"))
	writeFile(t, filepath.Join(seed, "feature.txt"), "feature-2\n")
	runGitCommand(t, seed, "add", "feature.txt")
	runGitCommand(t, seed, "commit", "-m", "feature commit two")

	runGitCommand(t, seed, "checkout", "main")
	writeFile(t, filepath.Join(seed, "main.txt"), "drift\n")
	runGitCommand(t, seed, "add", "main.txt")
	runGitCommand(t, seed, "commit", "-m", "main drift")
	driftSHA := strings.TrimSpace(runGitCommand(t, seed, "rev-parse", "HEAD"))

	runGitCommand(t, seed, "remote", "add", "origin", origin)
	runGitCommand(t, seed, "push", origin, "main", "feature")

	worktree := filepath.Join(t.TempDir(), "worktree")
	runGitCommand(t, t.TempDir(), "clone", origin, worktree)
	runGitCommand(t, worktree, "checkout", "feature")

	return prBaseScenario{
		baseSHA:       baseSHA,
		driftSHA:      driftSHA,
		headParentSHA: headParentSHA,
		worktree:      worktree,
	}
}

func readFileText(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func assertScenarioUsesDistinctMergeBase(t *testing.T, scenario prBaseScenario) {
	t.Helper()

	if scenario.baseSHA == scenario.headParentSHA {
		t.Fatal("test setup must keep merge base distinct from HEAD^ for multi-commit PRs")
	}
	if scenario.baseSHA == scenario.driftSHA {
		t.Fatal("test setup must drift the PR base ref")
	}
	if output, err := runShellCommand(scenario.worktree, `git merge-base --is-ancestor "$DRIFT_SHA" HEAD`, map[string]string{"DRIFT_SHA": scenario.driftSHA}); err == nil {
		t.Fatalf("drifted base ref unexpectedly remained an ancestor of the PR head:\n%s", output)
	}
}

func assertActFetchStepResolvesImmutableBase(t *testing.T, scenario prBaseScenario, script string, runLabel string) {
	t.Helper()

	assertFetchStepResolvesImmutableBase(t, scenario, script, runLabel, defaultShellEnv())
}

func assertFetchStepResolvesImmutableBase(t *testing.T, scenario prBaseScenario, script string, runLabel string, baseEnv []string) {
	t.Helper()

	githubEnv := filepath.Join(t.TempDir(), "github.env")
	output, err := runShellCommandWithBaseEnv(scenario.worktree, script, baseEnv, map[string]string{
		"ACT":        "1",
		"BASE_REF":   "main",
		"BASE_SHA":   "",
		"GITHUB_ENV": githubEnv,
	})
	if err != nil {
		t.Fatalf("run %s fetch step: %v\n%s", runLabel, err, output)
	}
	if !strings.Contains(output, "resolved immutable merge base "+scenario.baseSHA+" from main") {
		t.Fatalf("%s fetch step did not report resolved merge base %q:\n%s", runLabel, scenario.baseSHA, output)
	}

	envText := readFileText(t, githubEnv)
	assertImmutableBaseValue(t, envText, scenario.baseSHA, scenario.headParentSHA, scenario.driftSHA, runLabel+" exported env")
}

func TestCIWorkflowActFallbackIgnoresHostileInheritedBaseSHA(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	scenario := newPRBaseScenario(t)
	assertScenarioUsesDistinctMergeBase(t, scenario)
	hostileEnv := append(defaultShellEnv(), "BASE_SHA="+scenario.driftSHA)

	for _, cfg := range []struct {
		jobName  string
		runLabel string
	}{
		{jobName: "verify", runLabel: "verify"},
		{jobName: "verify-rolling", runLabel: "verify-rolling"},
	} {
		fetchBase := workflowStepByName(t, workflow.Jobs, cfg.jobName, "Fetch PR base")
		assertFetchStepResolvesImmutableBase(t, scenario, fetchBase.Run, cfg.runLabel, hostileEnv)
	}
}

func TestCIWorkflowFetchStepsHonorExplicitPRBaseSHA(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	scenario := newPRBaseScenario(t)
	assertScenarioUsesDistinctMergeBase(t, scenario)
	hostileEnv := append(defaultShellEnv(), "BASE_SHA="+scenario.driftSHA)

	for _, cfg := range []struct {
		jobName  string
		runLabel string
	}{
		{jobName: "verify", runLabel: "verify"},
		{jobName: "verify-rolling", runLabel: "verify-rolling"},
	} {
		fetchBase := workflowStepByName(t, workflow.Jobs, cfg.jobName, "Fetch PR base")
		githubEnv := filepath.Join(t.TempDir(), "github.env")
		output, err := runShellCommandWithBaseEnv(scenario.worktree, fetchBase.Run, hostileEnv, map[string]string{
			"ACT":        "1",
			"BASE_REF":   "main",
			"BASE_SHA":   scenario.baseSHA,
			"GITHUB_ENV": githubEnv,
		})
		if err != nil {
			t.Fatalf("run %s fetch step with explicit base SHA: %v\n%s", cfg.runLabel, err, output)
		}
		if strings.Contains(output, "resolved immutable merge base") {
			t.Fatalf("%s fetch step unexpectedly used ACT fallback despite explicit base SHA:\n%s", cfg.runLabel, output)
		}

		envText := readFileText(t, githubEnv)
		assertImmutableBaseValue(t, envText, scenario.baseSHA, scenario.headParentSHA, scenario.driftSHA, cfg.runLabel+" explicit base exported env")
	}
}

func assertRunStepUsesImmutableBase(t *testing.T, scenario prBaseScenario, script string, cfg prBaseWorkflowJobConfig) {
	t.Helper()

	fakeBin := filepath.Join(t.TempDir(), "fakebin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("create fake bin dir: %v", err)
	}
	makeLog := filepath.Join(t.TempDir(), "make-env.txt")
	writeExecutableFile(t, filepath.Join(fakeBin, "make"), buildFakeMakeScript(makeLog, cfg.buildChannel != ""))

	env := map[string]string{
		"GH_EVENT_NAME":     "pull_request",
		"MEMORY_BENCH_BASE": scenario.baseSHA,
		"PATH":              fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
	}
	if cfg.buildChannel != "" {
		runnerTemp := filepath.Join(t.TempDir(), "runner-temp")
		if err := os.MkdirAll(runnerTemp, 0o755); err != nil {
			t.Fatalf("create runner temp: %v", err)
		}
		env["BUILD_CHANNEL"] = cfg.buildChannel
		env["RUNNER_TEMP"] = runnerTemp
	}

	if output, err := runShellCommand(scenario.worktree, script, env); err != nil {
		t.Fatalf("run %s CI step: %v\n%s", cfg.runLabel, err, output)
	}

	makeEnvText := readFileText(t, makeLog)
	for _, want := range []string{
		"MEMORY_BENCH_BASE=" + scenario.baseSHA,
		"MEMORY_BENCH_ENFORCE=0",
	} {
		if !strings.Contains(makeEnvText, want+"\n") {
			t.Fatalf("%s run step missing %q:\n%s", cfg.runLabel, want, makeEnvText)
		}
	}
	if cfg.buildChannel != "" && !strings.Contains(makeEnvText, "BUILD_CHANNEL="+cfg.buildChannel+"\n") {
		t.Fatalf("%s run step missing build channel %q:\n%s", cfg.runLabel, cfg.buildChannel, makeEnvText)
	}
	assertImmutableBaseValue(t, makeEnvText, scenario.baseSHA, scenario.headParentSHA, scenario.driftSHA, cfg.runLabel+" run step")
}

func assertLopperStepUsesImmutableBase(t *testing.T, scenario prBaseScenario, script string, runLabel string) {
	t.Helper()

	lopperLog := filepath.Join(t.TempDir(), "lopper.log")
	if err := os.MkdirAll(filepath.Join(scenario.worktree, "bin"), 0o755); err != nil {
		t.Fatalf("create fake lopper bin dir: %v", err)
	}
	writeExecutableFile(t, filepath.Join(scenario.worktree, "bin", "lopper"), buildFakeLopperScript(lopperLog))

	if output, err := runShellCommand(scenario.worktree, script, map[string]string{"MEMORY_BENCH_BASE": scenario.baseSHA}); err != nil {
		t.Fatalf("run %s lopper base step: %v\n%s", runLabel, err, output)
	}

	lopperText := readFileText(t, lopperLog)
	if !strings.Contains(lopperText, "repo=.artifacts/base\n") {
		t.Fatalf("%s lopper step did not analyse the detached base worktree:\n%s", runLabel, lopperText)
	}
	assertImmutableBaseValue(t, lopperText, scenario.baseSHA, scenario.headParentSHA, scenario.driftSHA, runLabel+" lopper base step")
}

func buildFakeMakeScript(logPath string, includeBuildChannel bool) string {
	script := "#!/bin/sh\nset -eu\nprintf 'MEMORY_BENCH_BASE=%s\\n' \"$MEMORY_BENCH_BASE\" > " + shellQuote(logPath) + "\nprintf 'MEMORY_BENCH_ENFORCE=%s\\n' \"$MEMORY_BENCH_ENFORCE\" >> " + shellQuote(logPath) + "\n"
	if includeBuildChannel {
		script += "printf 'BUILD_CHANNEL=%s\\n' \"$BUILD_CHANNEL\" >> " + shellQuote(logPath) + "\n"
	}
	return script
}

func buildFakeLopperScript(logPath string) string {
	return "#!/bin/sh\nset -eu\nrepo=''\nwhile [ \"$#\" -gt 0 ]; do\n  case \"$1\" in\n    --repo)\n      repo=\"$2\"\n      shift 2\n      ;;\n    *)\n      shift\n      ;;\n  esac\ndone\nprintf 'repo=%s\\nsha=%s\\n' \"$repo\" \"$(git -C \"$repo\" rev-parse HEAD)\" >> " + shellQuote(logPath) + "\nprintf '{}\\n'\n"
}

func assertImmutableBaseValue(t *testing.T, text string, wantBaseSHA string, forbiddenHeadParentSHA string, forbiddenDriftSHA string, label string) {
	t.Helper()

	if !strings.Contains(text, wantBaseSHA) {
		t.Fatalf("%s missing immutable base SHA %q:\n%s", label, wantBaseSHA, text)
	}
	for _, forbidden := range []struct {
		label string
		sha   string
	}{
		{label: "HEAD^", sha: forbiddenHeadParentSHA},
		{label: "drifted base ref", sha: forbiddenDriftSHA},
	} {
		if strings.Contains(text, forbidden.sha) {
			t.Fatalf("%s must not contain %s SHA %q:\n%s", label, forbidden.label, forbidden.sha, text)
		}
	}
}

func runShellCommand(dir, script string, env map[string]string) (string, error) {
	return runShellCommandWithBaseEnv(dir, script, defaultShellEnv(), env)
}

func runShellCommandWithBaseEnv(dir, script string, baseEnv []string, env map[string]string) (string, error) {
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = dir
	cmd.Env = overlayShellEnv(baseEnv, env)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func defaultShellEnv() []string {
	return append(gitexec.SanitizedEnv(), "PATH="+os.Getenv("PATH"))
}

func overlayShellEnv(base []string, env map[string]string) []string {
	overrides := make(map[string]struct{}, len(env))
	for key := range env {
		overrides[key] = struct{}{}
	}

	result := make([]string, 0, len(base)+len(env))
	for _, entry := range base {
		key, _, hasValue := strings.Cut(entry, "=")
		if hasValue {
			if _, present := overrides[key]; present {
				continue
			}
		}
		result = append(result, entry)
	}
	for key, value := range env {
		result = append(result, key+"="+value)
	}
	return result
}

func shellQuote(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'"
}

func TestCIWorkflowVerifiesVSCodePackageContractAfterInstallingDependencies(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)

	vscodeSmoke := workflowJobByName(t, workflow.Jobs, "vscode-smoke")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "VS Code smoke condition", got: vscodeSmoke.If, want: ""},
	})
	if len(vscodeSmoke.Needs) != 0 {
		t.Fatalf("VS Code smoke must not depend on a skip-producing change filter, got needs %v", vscodeSmoke.Needs)
	}
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

func assertManualDispatchTrigger(t *testing.T, workflowPath string) {
	t.Helper()

	workflowText := readConfig(t, workflowPath)
	if !strings.Contains(workflowText, "workflow_dispatch:\n  pull_request:") {
		t.Fatalf("%s must support manual dispatch without losing pull_request triggers", workflowPath)
	}
}
