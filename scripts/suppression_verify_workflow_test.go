package scripts

import (
	"encoding/base64"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestSuppressionVerifyWorkflowUsesTrustedPullRequestTarget asserts the
// structural properties that make this gate immune to the exact bypass the
// Codex finding described: ci.yml's pull_request-triggered "verify" job runs
// from the pull request's own (potentially tampered) workflow file, so a PR
// could previously delete or neuter its suppression-verification step and
// have its own required check report success trivially. This workflow
// instead triggers on pull_request_target (always resolved from the base
// branch), performs no checkout, executes no PR-controlled code, and derives
// its only PR-controlled input (the "pr-report-inputs" artifact) through a
// head-SHA-bound lookup against this repository's own genuine "ci" workflow
// runs -- never anything the pull request event payload could forge.
func TestSuppressionVerifyWorkflowUsesTrustedPullRequestTarget(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/suppression-verify.yml", &workflow)
	workflowText := readConfig(t, ".github/workflows/suppression-verify.yml")

	var trigger pullRequestTargetTriggerWorkflow
	readYAMLConfig(t, ".github/workflows/suppression-verify.yml", &trigger)
	wantTypes := []string{"opened", "edited", "synchronize", "reopened", "ready_for_review", "labeled", "unlabeled"}
	if !slices.Equal(trigger.On.PullRequestTarget.Types, wantTypes) {
		t.Fatalf("suppression verify workflow pull_request_target types = %v, want %v", trigger.On.PullRequestTarget.Types, wantTypes)
	}

	for _, fragment := range []string{
		"pull_request_target:",
		"permissions: {}",
		"concurrency:",
		"group: suppression-verify-${{ github.event.pull_request.number }}",
		"cancel-in-progress: true",
	} {
		if !strings.Contains(workflowText, fragment) {
			t.Fatalf("suppression verify workflow missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"actions/checkout@",
		"github.event.pull_request.head.ref",
	} {
		if strings.Contains(workflowText, forbidden) {
			t.Fatalf("suppression verify workflow contains unsafe fragment %q", forbidden)
		}
	}

	verify := workflowJobByName(t, workflow.Jobs, "verify")
	assertWorkflowJobOmitsCheckout(t, verify, "suppression verify")
	assertWorkflowJobStepRunsOmitAllFold(t, verify, "suppression verify", []string{
		"go run ./",
		"npm install",
		"npx ",
		"./extensions/",
	})
	if got, want := verify.Permissions, (map[string]string{
		"contents":      "read",
		"actions":       "read",
		"checks":        "write",
		"issues":        "read",
		"pull-requests": "read",
	}); !maps.Equal(got, want) {
		t.Fatalf("suppression verify job permissions = %v, want %v", got, want)
	}
	assertWorkflowStepOrder(t, verify,
		"Publish pending suppression-verify check",
		"Resolve trusted ci artifact for this pull request head",
		"Download PR report inputs",
		"Validate PR report inputs",
		"Verify inline suppression tracking issues were published",
		"Report suppression verification check conclusion",
	)

	// pull_request_target's own job checks are reported against the
	// default branch's HEAD SHA, not the PR's head commit -- unlike
	// pull_request, where GitHub associates the run with the merge ref.
	// Without an explicit head_sha-bound check run, this workflow could
	// never actually gate the PR it is meant to verify, silently defeating
	// the "dedicated required status check" this gate exists to provide.
	pending := workflowStepByName(t, workflow.Jobs, "verify", "Publish pending suppression-verify check")
	assertWorkflowStepRunContainsAll(t, workflowStepConfig{Run: pending.With["script"]}, "publish pending check", []string{
		"checks.create",
		"head_sha: context.payload.pull_request.head.sha",
		"status: 'in_progress'",
		"core.setOutput('check-id'",
	})

	report := workflowStepByName(t, workflow.Jobs, "verify", "Report suppression verification check conclusion")
	if report.If != "always()" {
		t.Fatalf("report check conclusion step must always run, got if: %q", report.If)
	}
	assertWorkflowStepRunContainsAll(t, workflowStepConfig{Run: report.With["script"]}, "report check conclusion", []string{
		"checks.update",
		"status: 'completed'",
		"VERIFY_OUTCOME",
	})

	resolve := workflowStepByName(t, workflow.Jobs, "verify", "Resolve trusted ci artifact for this pull request head")
	assertWorkflowStepRunContainsAll(t, workflowStepConfig{Run: resolve.With["script"]}, "resolve trusted ci artifact", []string{
		"workflow_id: 'ci.yml'",
		"event: 'pull_request'",
		"head_sha: headSha",
		"listWorkflowRunArtifacts",
		"!candidate.expired",
		"core.setOutput('artifact-id'",
		"core.setOutput('run-id'",
	})

	download := workflowStepByName(t, workflow.Jobs, "verify", "Download PR report inputs")
	if got, want := download.With["artifact-ids"], "${{ steps.resolve_artifact.outputs.artifact-id }}"; got != want {
		t.Fatalf("download PR report inputs artifact-ids = %q, want %q", got, want)
	}
	if got, want := download.With["run-id"], "${{ steps.resolve_artifact.outputs.run-id }}"; got != want {
		t.Fatalf("download PR report inputs run-id = %q, want %q", got, want)
	}
}

func TestCIWorkflowGatesMergeOnHeadAssociatedSuppressionTrackingResult(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/suppression-verify.yml", &workflow)

	verify := workflowJobByName(t, workflow.Jobs, "verify")
	assertWorkflowStepOrder(t, verify,
		"Resolve trusted ci artifact for this pull request head",
		"Download PR report inputs",
		"Validate PR report inputs",
		"Verify inline suppression tracking issues were published",
	)

	gate := workflowStepByName(t, workflow.Jobs, "verify", "Verify inline suppression tracking issues were published")
	if gate.If != "" {
		t.Fatalf("suppression tracking gate must run unconditionally on pull_request_target, got if: %q", gate.If)
	}
	assertWorkflowStepEnv(t, gate, "suppression tracking gate", map[string]string{
		"GH_TOKEN":          "${{ github.token }}",
		"PR_NUMBER":         "${{ github.event.pull_request.number }}",
		"SUPPRESSIONS_FILE": "${{ runner.temp }}/pr-report-inputs/inline-suppressions.json",
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
		// GitHub's pull-files endpoint returns at most 3000 files -- the
		// same MAX_CHANGED_FILES limit the trusted tracker enforces -- so
		// a PR with more changed files than that can never have all of
		// them retrieved here; a suppression beyond the first 3000 would
		// be invisible to this scan while the trusted tracker correctly
		// refuses to publish anything for the oversized diff.
		`changed_files="$(gh api "repos/${GITHUB_REPOSITORY}/pulls/${PR_NUMBER}" --jq '.changed_files')"`,
		`if [ "${changed_files:-0}" -gt 3000 ]; then`,
		`pr_files_count="$(echo "${pr_files_json}" | jq 'length')"`,
		`if [ "${pr_files_count:-0}" -ne "${changed_files:-0}" ]; then`,
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
		// explicit delimiter avoids introducing any escaping. Including
		// .line ties each record to the specific added-line position
		// the diff data from GitHub proves this pull request actually
		// added, not merely whatever line a tampered artifact claims.
		`jq -j '.suppressions[] | .file, "\u0001", (.line | tostring), "\u0001", .content, "\n"'`,
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
		"function mask_quoted_regions(s, initial_quote,    result, i, c, quote, n, narrow_single_quote, past_comment_start, shell_language, no_escapes_in_this_quote, strict_hash_boundary_language)",
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
		`masked = mask_quoted_regions(content, quote_state)`,
		`quote_state = final_quote_state`,
		// GitHub's default 3-line patch context still cannot reveal a
		// construct opened further above a hunk than that window reaches;
		// seed each hunk's starting state from the complete blob fetched
		// above (bundled as the first ARGV file, read while FNR == NR)
		// instead of resetting to "no open quote" at the boundary.
		`gh api "repos/${GITHUB_REPOSITORY}/git/blobs/${file_sha}" --jq '.content' | base64 -d >"${content_tmp}"`,
		"FNR == NR {",
		"full_lines[FNR] = $0",
		"function seed_quote_state(   idx, prior_count, seed)",
		`quote_state = seed_quote_state()`,
		`' "${content_tmp}" -`,
		`rm -f "${content_tmp}"`,
		// An apostrophe inside a genuine line comment (e.g. "don't") is
		// comment prose, not code; a real, unquoted line-comment
		// delimiter marks that a quote left dangling from there to end
		// of line must not carry into the next line -- but does not by
		// itself change how the rest of the line is masked, since a
		// well-formed quoted span later in the same comment (e.g. a
		// backtick-quoted example) still needs its interior masked, or
		// example marker text becomes indistinguishable from a real one.
		`if ((c == "/" && substr(s, i + 1, 1) == "/") || c == "#") {`,
		`past_comment_start = 1`,
		`final_quote_state = (past_comment_start ? "" : quote)`,
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
		// matched exactly.
		//
		// Searching for any occurrence for which the hash happens to
		// match (rather than deriving the one true occurrence from the
		// complete head file) would accept a fingerprint that was valid
		// on an earlier head but has since gone stale -- e.g. an
		// identical suppression published at occurrence 1 whose true
		// ordinal becomes 2 once an identical line lands in the base
		// branch ahead of it.
		"occurrence = sum(1 for candidate in lines[:line] if candidate == content)",
		"if occurrence < 1:",
		`suffix = (b"\noccurrence:%d" % occurrence) if occurrence > 1 else b""`,
		"expected = hashlib.sha256(base + suffix).hexdigest().encode()",
		"if expected != fingerprint:",
	})
	// The occurrence bundle must be built from each record's file, looked
	// up by the SHA GitHub's own pull-files response provides (not a
	// PR-controlled path), and fail closed if a record names a file
	// outside this pull request's changed files.
	assertWorkflowStepRunContainsAll(t, gate, "suppression tracking gate", []string{
		`mapfile -t occurrence_bundle_files < <(jq -r '.suppressions[].file' "${SUPPRESSIONS_FILE}" | sort -u)`,
		`occurrence_bundle_file_sha="$(echo "${pr_files_json}" | jq -r --arg f "${occurrence_bundle_file_name}" '[.[] | select(.filename == $f)][0].sha // empty')"`,
		`gh api "repos/${GITHUB_REPOSITORY}/git/blobs/${occurrence_bundle_file_sha}" --jq '.content' | base64 -d`,
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
		`|${coverage_prefix}:[[:space:]]*ignore))"`,
		`test("^\\.githooks/|`,
	})
	// A comment delimiter needs no preceding whitespace -- it can follow
	// code directly -- so the leading boundary excludes only ":" (URL
	// schemes) rather than requiring whitespace or start-of-line; string
	// literals are handled separately by mask_quoted_regions.
	assertWorkflowStepRunContainsAll(t, gate, "suppression tracking gate", []string{
		`suspect_pattern_all="(^|[^:])`,
		`(//|/[*]+|#)`,
	})
	// Python/Ruby/YAML/shell only ever treat "#" as a comment delimiter, so
	// "//" there is not a comment start (e.g. Python floor division); the
	// active pattern must be chosen per file extension, mirroring
	// HASH_ONLY_EXTENSIONS/SLASH_STYLE_EXTENSIONS in the trusted JS tracker,
	// rather than always matching every comment style universally. Python
	// and Ruby further need the lenient "#" boundary while YAML/shell need
	// the strict one, mirroring STRICT_HASH_BOUNDARY_EXTENSIONS.
	assertWorkflowStepRunContainsAll(t, gate, "suppression tracking gate", []string{
		`suspect_pattern_hash_strict="(^|[[:space:]])(#[[:space:]]*${marker_names})([^[:alnum:]_-]|$)"`,
		`suspect_pattern_hash_lenient="(^|[^:])(#[[:space:]]*${marker_names})([^[:alnum:]_-]|$)"`,
		`suspect_pattern_slash="(^|[^:])((//|/[*]+)[[:space:]]*${marker_names})([^[:alnum:]_-]|$)"`,
		`strict_hash_file_pattern=`,
		`lenient_hash_file_pattern=`,
		`slash_style_file_pattern=`,
		`active_suspect_pattern="${suspect_pattern_hash_strict}"`,
		`active_suspect_pattern="${suspect_pattern_hash_lenient}"`,
		`active_suspect_pattern="${suspect_pattern_slash}"`,
		`active_suspect_pattern="${suspect_pattern_all}"`,
		`awk -v pat="${active_suspect_pattern}" -v fname="${filename}"`,
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

// fingerprintBindingInvocation extracts the fingerprint-binding script
// assignment plus its immediately following invocation line from the gate's
// run text, so callers can execute the real embedded script rather than
// asserting on its source text.
func fingerprintBindingInvocation(t *testing.T, run string) string {
	t.Helper()

	const startMarker = "fingerprint_binding_script='"
	const endMarker = `fingerprint_binding_dedented="$(printf '%s' "${fingerprint_binding_script}" | python3 -c 'import sys, textwrap; sys.stdout.write(textwrap.dedent(sys.stdin.read()))')"` + "\n"
	startIdx := strings.Index(run, startMarker)
	if startIdx == -1 {
		t.Fatalf("suppression tracking gate is missing the fingerprint-binding script")
	}
	endIdx := strings.Index(run[startIdx:], endMarker)
	if endIdx == -1 {
		t.Fatalf("suppression tracking gate is missing the fingerprint-binding dedent line")
	}
	invocationStart := startIdx + endIdx + len(endMarker)
	invocationEnd := strings.Index(run[invocationStart:], "\n")
	if invocationEnd == -1 {
		t.Fatalf("suppression tracking gate is missing the fingerprint-binding invocation line")
	}
	return run[startIdx : invocationStart+invocationEnd]
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
	readYAMLConfig(t, ".github/workflows/suppression-verify.yml", &workflow)
	gate := workflowStepByName(t, workflow.Jobs, "verify", "Verify inline suppression tracking issues were published")

	invocation := fingerprintBindingInvocation(t, gate.Run)
	dir := t.TempDir()
	suppressionsPath := filepath.Join(dir, "inline-suppressions.json")
	bundlePath := filepath.Join(dir, "occurrence-bundle")
	writeFile(t, bundlePath, "\x01main.go\nline one\n")

	// The extracted invocation line itself sets OCCURRENCE_BUNDLE_FILE from
	// this bash variable (OCCURRENCE_BUNDLE_FILE="${occurrence_bundle_file}"
	// python3 ...), so it must be defined here rather than passed only via
	// the process environment.
	script := "set -euo pipefail\noccurrence_bundle_file=" + shellQuote(bundlePath) + "\n" + invocation

	valid := suppressionFingerprint("main.go", "line one", 1)
	writeFile(t, suppressionsPath, `{"suppressions":[{"file":"main.go","content":"line one","fingerprint":"`+valid+`","line":1}]}`)
	if output, err := runShellCommand(dir, script, map[string]string{"SUPPRESSIONS_FILE": suppressionsPath}); err != nil {
		t.Fatalf("expected a fingerprint bound to its own (file, content, true occurrence) to pass, output:\n%s", output)
	}

	reused := suppressionFingerprint("main.go", "different content", 1)
	writeFile(t, suppressionsPath, `{"suppressions":[{"file":"main.go","content":"line one","fingerprint":"`+reused+`","line":1}]}`)
	output, err := runShellCommand(dir, script, map[string]string{"SUPPRESSIONS_FILE": suppressionsPath})
	if err == nil {
		t.Fatalf("expected a fingerprint reused from different content to be rejected, output:\n%s", output)
	}
	if !strings.Contains(output, "does not match its own recorded file, content, and true occurrence") {
		t.Fatalf("expected a fingerprint-binding rejection message, got:\n%s", output)
	}
}

// TestCIWorkflowMissingSuspectsCheckRejectsARecordBoundToTheWrongLine
// actually runs the real recorded_pairs_file build and the missing_suspects
// comparison against a SUPPRESSIONS_FILE record whose (file, content) is
// genuinely a suspect but whose claimed line is not the one GitHub's own
// diff data shows was actually added -- reproducing the exact scenario the
// line-binding Codex finding described: a tampered artifact could
// previously bind a real (file, content) pair to an unrelated, unchanged
// line, letting the true occurrence at the real position drift undetected.
func TestCIWorkflowMissingSuspectsCheckRejectsARecordBoundToTheWrongLine(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/suppression-verify.yml", &workflow)
	gate := workflowStepByName(t, workflow.Jobs, "verify", "Verify inline suppression tracking issues were published")

	const recordedLine = `jq -j '.suppressions[] | .file, "\u0001", (.line | tostring), "\u0001", .content, "\n"' "${SUPPRESSIONS_FILE}" > "${recorded_pairs_file}"`
	if !strings.Contains(gate.Run, recordedLine) {
		t.Fatalf("suppression tracking gate is missing the recorded_pairs_file build")
	}

	const missingLine = `missing_suspects="$(comm -23 <(sort "${suspect_pairs_file}") <(sort "${recorded_pairs_file}") | wc -l | tr -d ' ')"`
	if !strings.Contains(gate.Run, missingLine) {
		t.Fatalf("suppression tracking gate is missing the missing_suspects comparison")
	}

	script := "set -euo pipefail\nrecorded_pairs_file=\"$(mktemp)\"\n" + recordedLine + "\n" + missingLine + "\nprintf '%s' \"${missing_suspects}\"\n"

	dir := t.TempDir()
	suppressionsPath := filepath.Join(dir, "inline-suppressions.json")
	suspectPairsPath := filepath.Join(dir, "suspect-pairs")
	// The real suspect scan would produce this for a genuine "// noqa"
	// added at line 7 of some_file.go.
	writeFile(t, suspectPairsPath, "some_file.go\x017\x01value := unsafe() //noqa\n")

	fingerprint := suppressionFingerprint("some_file.go", "value := unsafe() //noqa", 1)
	wrongLineJSON := `{"suppressions":[{"file":"some_file.go","content":"value := unsafe() //noqa","fingerprint":"` + fingerprint + `","line":3}]}`
	writeFile(t, suppressionsPath, wrongLineJSON)
	output, err := runShellCommand(dir, script, map[string]string{
		"SUPPRESSIONS_FILE":  suppressionsPath,
		"suspect_pairs_file": suspectPairsPath,
	})
	if err != nil {
		t.Fatalf("expected the comparison to run, output:\n%s", output)
	}
	if strings.TrimSpace(output) == "0" {
		t.Fatalf("expected a record bound to the wrong line to still count as a missing suspect, output:\n%q", output)
	}

	correctLineJSON := `{"suppressions":[{"file":"some_file.go","content":"value := unsafe() //noqa","fingerprint":"` + fingerprint + `","line":7}]}`
	writeFile(t, suppressionsPath, correctLineJSON)
	output, err = runShellCommand(dir, script, map[string]string{
		"SUPPRESSIONS_FILE":  suppressionsPath,
		"suspect_pairs_file": suspectPairsPath,
	})
	if err != nil {
		t.Fatalf("expected the comparison to run, output:\n%s", output)
	}
	if strings.TrimSpace(output) != "0" {
		t.Fatalf("expected a record bound to the correct line to satisfy the suspect, output:\n%q", output)
	}
}

// TestCIWorkflowFingerprintBindingRejectsAStaleOccurrenceAfterBaseDrift
// reproduces the exact scenario the occurrence-ordinal Codex finding
// described: an identical suppression published at occurrence 1 on an
// earlier head, where the base branch has since gained an identical line
// ahead of this pull request's own, making the true occurrence 2. A
// fingerprint that is merely valid for *some* ordinal (the old occurrence-1
// shape) must not satisfy the gate; only the one true occurrence -- derived
// from the complete head file, not searched for -- may.
func TestCIWorkflowFingerprintBindingRejectsAStaleOccurrenceAfterBaseDrift(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/suppression-verify.yml", &workflow)
	gate := workflowStepByName(t, workflow.Jobs, "verify", "Verify inline suppression tracking issues were published")

	invocation := fingerprintBindingInvocation(t, gate.Run)
	dir := t.TempDir()
	suppressionsPath := filepath.Join(dir, "inline-suppressions.json")
	bundlePath := filepath.Join(dir, "occurrence-bundle")
	// An identical line now appears at both line 1 and line 4: the pull
	// request's own added line (line 4) is now the *second* occurrence,
	// even though it was the first (and only) one when originally
	// published.
	writeFile(t, bundlePath, "\x01main.go\ntarget line\nother\ncode\ntarget line\n")

	script := "set -euo pipefail\noccurrence_bundle_file=" + shellQuote(bundlePath) + "\n" + invocation

	stale := suppressionFingerprint("main.go", "target line", 1)
	writeFile(t, suppressionsPath, `{"suppressions":[{"file":"main.go","content":"target line","fingerprint":"`+stale+`","line":4}]}`)
	output, err := runShellCommand(dir, script, map[string]string{"SUPPRESSIONS_FILE": suppressionsPath})
	if err == nil {
		t.Fatalf("expected a stale occurrence-1 fingerprint to be rejected once the true occurrence became 2, output:\n%s", output)
	}
	if !strings.Contains(output, "true occurrence 2") {
		t.Fatalf("expected a true-occurrence-2 rejection message, got:\n%s", output)
	}

	correct := suppressionFingerprint("main.go", "target line", 2)
	writeFile(t, suppressionsPath, `{"suppressions":[{"file":"main.go","content":"target line","fingerprint":"`+correct+`","line":4}]}`)
	if output, err := runShellCommand(dir, script, map[string]string{"SUPPRESSIONS_FILE": suppressionsPath}); err != nil {
		t.Fatalf("expected the true occurrence-2 fingerprint to pass, output:\n%s", output)
	}

	forged := suppressionFingerprint("main.go", "never appears in the file", 1)
	writeFile(t, suppressionsPath, `{"suppressions":[{"file":"main.go","content":"never appears in the file","fingerprint":"`+forged+`","line":1}]}`)
	output, err = runShellCommand(dir, script, map[string]string{"SUPPRESSIONS_FILE": suppressionsPath})
	if err == nil {
		t.Fatalf("expected a record whose content never appears in the head file to be rejected, output:\n%s", output)
	}
	if !strings.Contains(output, "does not actually appear at its claimed line") {
		t.Fatalf("expected a does-not-appear rejection message, got:\n%s", output)
	}
}

// TestCIWorkflowFlattensPaginatedPullFilesResponse actually runs the
// pr_files_json fetch against a fake `gh` that reproduces `gh api
// --paginate`'s real, documented behavior for a multi-page result:
// concatenated raw JSON bodies ("[...][...]"), not one combined array. It
// asserts on the resulting value's own content -- not just the surrounding
// script's exit code -- because the count-mismatch check downstream uses
// `if [ ... -ne ... ]`, and bash's `[` treats a non-numeric comparison as
// merely "false" rather than a script-ending error; the real damage (per
// the Codex finding) is silent, further downstream, where a per-file
// lookup keyed off pr_files_json would only ever see the first page.
func TestCIWorkflowFlattensPaginatedPullFilesResponse(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/suppression-verify.yml", &workflow)
	gate := workflowStepByName(t, workflow.Jobs, "verify", "Verify inline suppression tracking issues were published")

	const start = `pr_files_json="$(gh api`
	startIdx := strings.Index(gate.Run, start)
	if startIdx == -1 {
		t.Fatalf("suppression tracking gate is missing the pr_files_json fetch")
	}
	lineEnd := strings.Index(gate.Run[startIdx:], "\n")
	if lineEnd == -1 {
		t.Fatalf("suppression tracking gate pr_files_json fetch is unterminated")
	}
	line := gate.Run[startIdx : startIdx+lineEnd]
	script := "set -euo pipefail\n" + line + "\nprintf '%s' \"${pr_files_json}\"\n"

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	// Reproduces two 3-item and 2-item pages concatenated as gh actually
	// emits them for `--paginate`, not wrapped in an outer array.
	fakeGH := `#!/usr/bin/env bash
printf '[{"filename":"a.go"},{"filename":"b.go"},{"filename":"c.go"}][{"filename":"d.go"},{"filename":"e.go"}]'
`
	writeFileMode(t, filepath.Join(binDir, "gh"), fakeGH, 0o755)

	output, err := runShellCommand(dir, script, map[string]string{
		"PATH":              binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"GITHUB_REPOSITORY": "octo/lopper",
		"PR_NUMBER":         "1",
	})
	if err != nil {
		t.Fatalf("expected the pr_files_json fetch to run, output:\n%s", output)
	}

	jqCmd := exec.Command("jq", "-e", ". | type == \"array\" and length == 5")
	jqCmd.Stdin = strings.NewReader(output)
	if jqErr := jqCmd.Run(); jqErr != nil {
		t.Fatalf("expected pr_files_json to be one flat 5-element array combining both pages, got:\n%q", output)
	}
}

// TestCIWorkflowSuspectScanSeedsQuoteStateFromBlobContent actually runs the
// embedded per-file suspect scan (not just asserting on its source text)
// against a synthetic file whose closing template-literal backtick sits
// further above a hunk than the patch's own context window reaches -- the
// exact gap a purely textual assertion could never catch, since the awk
// program is syntactically identical whether or not the seeding logic is
// wired up correctly to the blob it just fetched.
// runSuspectScan executes the real per-file suspect scan loop body from the
// "verify" gate against a synthetic file_json fixture, stubbing `gh` to
// serve the given blob content for any `gh api` call.
func runSuspectScan(t *testing.T, varsBlock, loopBody, filename, sha, patch, blob string) (string, error) {
	t.Helper()

	fileJSON, err := json.Marshal(map[string]string{"filename": filename, "sha": sha, "patch": patch})
	if err != nil {
		t.Fatalf("marshal file_json fixture: %v", err)
	}

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	// Stands in for the real `gh api .../git/blobs/<sha> --jq '.content'`
	// call: the script under test only cares that this prints the blob's
	// base64 content to stdout, not that it actually reached GitHub.
	fakeGH := "#!/usr/bin/env bash\nif [ \"$1\" = api ]; then printf '%s' \"$BLOB_CONTENT_B64\"; exit 0; fi\nexit 1\n"
	writeFileMode(t, filepath.Join(binDir, "gh"), fakeGH, 0o755)

	script := "set -euo pipefail\n" + varsBlock + "\nfile_json=" + shellQuote(string(fileJSON)) + "\n" + loopBody
	return runShellCommand(dir, script, map[string]string{
		"PATH":              binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"BLOB_CONTENT_B64":  base64.StdEncoding.EncodeToString([]byte(blob)),
		"GITHUB_REPOSITORY": "octo/lopper",
	})
}

// TestCIWorkflowSuspectScanDetectsMarkersAcrossLanguageQuotingRules actually
// runs the embedded per-file suspect scan (not just asserting on its source
// text) against synthetic files covering three independent Codex-described
// scenarios: a template-literal backtick opening further above a hunk than
// the patch's own context window reveals, a hash marker immediately after
// code with no space in a language that needs no free-standing rule, and a
// single-quoted shell string whose trailing backslash is a literal
// character, not an escape. Table-driven so the shared scan-and-assert
// shape exists exactly once.
func TestCIWorkflowSuspectScanDetectsMarkersAcrossLanguageQuotingRules(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/suppression-verify.yml", &workflow)
	gate := workflowStepByName(t, workflow.Jobs, "verify", "Verify inline suppression tracking issues were published")
	varsBlock, loopBody := extractSuspectScanVarsAndLoop(t, gate)

	cases := []struct {
		name     string
		filename string
		blob     string
		patch    string
		want     string
	}{
		{
			// The opening backtick sits 3 lines above the hunk -- further
			// back than the patch's own 3-line context window reveals (the
			// hunk below starts at "line2", one line after the opener).
			name:     "seeds quote state from blob content beyond the patch context window",
			filename: "tpl.js",
			blob:     "package main\n\nvar tpl = `line1\nline2\nline3\nline4\ntail`;\n",
			patch: "@@ -4,4 +4,4 @@\n line2\n line3\n line4\n-tail`;\n+tail`; //" +
				"eslint-disable-line rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard\n",
			want: "tpl.js\x017\x01tail`; //eslint-disable-line",
		},
		{
			name:     "does not require a free-standing hash in Python",
			filename: "calc.py",
			blob:     "value = safe()\n",
			patch:    "@@ -1 +1 @@\n-value = safe()\n+value = unsafe()#noqa rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard\n",
			want:     "calc.py\x011\x01value = unsafe()#noqa",
		},
		{
			// POSIX shell single-quoted strings have no escape character:
			// the backslash before the closing quote is literal, so the
			// string closes there and the real marker after it is a
			// suspect, not masked as still-quoted content.
			name:     "honors shell single-quote escaping rules",
			filename: "build.sh",
			blob:     "echo foo\n",
			patch:    "@@ -1 +1 @@\n-echo foo\n+echo 'foo\\' #noqa rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard\n",
			want:     "build.sh\x011\x01echo 'foo\\' #noqa",
		},
		{
			// A CRLF-encoded file preserves a trailing "\r" as part of
			// each diff line's content; the suspect record built from it
			// must strip that CR, matching what the trusted tracker
			// computes for the same line, or the two never agree on the
			// exact content that was actually added.
			name:     "strips a trailing CR from a CRLF-encoded file",
			filename: "main.go",
			blob:     "package main\r\n",
			patch:    "@@ -1 +1 @@\r\n-package main\r\n+package main //nolint:staticcheck // rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard\r\n",
			want:     "main.go\x011\x01package main //nolint:staticcheck",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			output, err := runSuspectScan(t, varsBlock, loopBody, tc.filename, "deadbeef", tc.patch, tc.blob)
			if err != nil {
				t.Fatalf("expected the suspect scan to run, output:\n%s", output)
			}
			if !strings.Contains(output, tc.want) {
				t.Fatalf("expected %q in the suspect scan output, got:\n%q", tc.want, output)
			}
			if strings.Contains(output, "\r") {
				t.Fatalf("expected no bare CR in the suspect scan output, got:\n%q", output)
			}
		})
	}
}

// TestCIWorkflowSuspectScanRequiresAFreeStandingHashInAHashOnlyLanguage
// actually runs the embedded per-file suspect scan against a YAML file
// whose added line contains "#" only as part of a URL fragment identifier,
// not a comment, proving the independent scan agrees with the trusted
// tracker and the shell detector that a hash-only language's "#" must be
// free-standing to count.
func TestCIWorkflowSuspectScanRequiresAFreeStandingHashInAHashOnlyLanguage(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/suppression-verify.yml", &workflow)
	gate := workflowStepByName(t, workflow.Jobs, "verify", "Verify inline suppression tracking issues were published")
	varsBlock, loopBody := extractSuspectScanVarsAndLoop(t, gate)

	fragmentBlob := "url: https://example.test/#noqa\n"
	fragmentPatch := "@@ -0,0 +1 @@\n+url: https://example.test/#noqa\n"
	output, err := runSuspectScan(t, varsBlock, loopBody, "deploy.yaml", "deadbeef", fragmentPatch, fragmentBlob)
	if err != nil {
		t.Fatalf("expected the suspect scan to run for the URL fragment case, output:\n%s", output)
	}
	if strings.Contains(output, "deploy.yaml\x01") {
		t.Fatalf("expected no suspect for a \"#\" embedded in a URL fragment, output:\n%q", output)
	}

	genuineBlob := "url: https://example.test/path # noqa\n"
	genuinePatch := "@@ -0,0 +1 @@\n+url: https://example.test/path # noqa\n"
	output, err = runSuspectScan(t, varsBlock, loopBody, "deploy.yaml", "deadbeef", genuinePatch, genuineBlob)
	if err != nil {
		t.Fatalf("expected the suspect scan to run for the free-standing hash case, output:\n%s", output)
	}
	if !strings.Contains(output, "deploy.yaml\x01") {
		t.Fatalf("expected a suspect for a genuine free-standing \"#\" marker, output:\n%q", output)
	}
}

// TestCIWorkflowSuspectScanSkipsBlobFetchForMarkerlessPatches actually runs
// the embedded per-file suspect scan against a patch containing no
// marker-shaped text at all, proving the blob fetch (a `gh api` call) is
// skipped entirely rather than paid for on every scanned file -- a PR with
// many changed source files but no suppressions could otherwise force up to
// 3,000 sequential authenticated requests in this required check.
func TestCIWorkflowSuspectScanSkipsBlobFetchForMarkerlessPatches(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/suppression-verify.yml", &workflow)
	gate := workflowStepByName(t, workflow.Jobs, "verify", "Verify inline suppression tracking issues were published")
	varsBlock, loopBody := extractSuspectScanVarsAndLoop(t, gate)

	patch := "@@ -1 +1 @@\n-value = safe()\n+value = still_safe()\n"
	fileJSON, err := json.Marshal(map[string]string{"filename": "calc.py", "sha": "deadbeef", "patch": patch})
	if err != nil {
		t.Fatalf("marshal file_json fixture: %v", err)
	}

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	ghCallsPath := filepath.Join(dir, "gh-calls")
	// Records each invocation instead of just succeeding, so the test can
	// assert on whether it was called at all.
	fakeGH := "#!/usr/bin/env bash\necho called >> " + shellQuote(ghCallsPath) + "\nexit 1\n"
	writeFileMode(t, filepath.Join(binDir, "gh"), fakeGH, 0o755)

	script := "set -euo pipefail\n" + varsBlock + "\nfile_json=" + shellQuote(string(fileJSON)) + "\n" + loopBody

	output, err := runShellCommand(dir, script, map[string]string{
		"PATH":              binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"GITHUB_REPOSITORY": "octo/lopper",
	})
	if err != nil {
		t.Fatalf("expected the suspect scan to run without needing gh at all, output:\n%s", output)
	}
	if _, statErr := os.Stat(ghCallsPath); statErr == nil {
		t.Fatalf("expected no gh invocation for a patch with no marker-shaped text, output:\n%q", output)
	}
}

// sourceFileTestLine extracts the "source_file_test=" line from the gate's
// run text, so callers can prefix a real patch-completeness check script
// with the same source-file predicate the workflow itself uses.
func sourceFileTestLine(t *testing.T, run string) string {
	t.Helper()

	const varsStart = `source_file_test=`
	varsStartIdx := strings.Index(run, varsStart)
	if varsStartIdx == -1 {
		t.Fatalf("suppression tracking gate is missing the source-file test")
	}
	varsLineEnd := strings.Index(run[varsStartIdx:], "\n")
	if varsLineEnd == -1 {
		t.Fatalf("suppression tracking gate source-file test is unterminated")
	}
	return run[varsStartIdx : varsStartIdx+varsLineEnd]
}

// TestCIWorkflowIncompletePatchCheckExemptsRemovedFiles actually runs the
// real incomplete-patch gate against a synthetic pull-files response where
// a large deleted source file has its patch omitted (as GitHub does for
// large diffs), proving a removed file no longer fails the required check
// even though it can never contain an added suppression line.
func TestCIWorkflowIncompletePatchCheckExemptsRemovedFiles(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/suppression-verify.yml", &workflow)
	gate := workflowStepByName(t, workflow.Jobs, "verify", "Verify inline suppression tracking issues were published")

	varsLine := sourceFileTestLine(t, gate.Run)

	const checkStart = `incomplete_count="$(echo "${pr_files_json}"`
	const checkEnd = "\nfi\n"
	checkStartIdx := strings.Index(gate.Run, checkStart)
	if checkStartIdx == -1 {
		t.Fatalf("suppression tracking gate is missing the incomplete-patch check")
	}
	checkEndRelIdx := strings.Index(gate.Run[checkStartIdx:], checkEnd)
	if checkEndRelIdx == -1 {
		t.Fatalf("suppression tracking gate is missing the incomplete-patch check end marker")
	}
	checkBlock := gate.Run[checkStartIdx : checkStartIdx+checkEndRelIdx+len(checkEnd)]

	dir := t.TempDir()

	removedFileJSON, err := json.Marshal([]map[string]any{
		{"filename": "big.go", "status": "removed", "additions": 0, "deletions": 5000, "patch": nil},
	})
	if err != nil {
		t.Fatalf("marshal removed-file fixture: %v", err)
	}
	script := "set -euo pipefail\n" + varsLine + "\npr_files_json=" + shellQuote(string(removedFileJSON)) + "\n" + checkBlock
	if output, err := runShellCommand(dir, script, nil); err != nil {
		t.Fatalf("expected a removed file with an omitted patch to pass, output:\n%s", output)
	}

	incompleteAddedJSON, err := json.Marshal([]map[string]any{
		{"filename": "big.go", "status": "added", "additions": 5000, "deletions": 0, "patch": nil},
	})
	if err != nil {
		t.Fatalf("marshal incomplete-added-file fixture: %v", err)
	}
	script = "set -euo pipefail\n" + varsLine + "\npr_files_json=" + shellQuote(string(incompleteAddedJSON)) + "\n" + checkBlock
	output, err := runShellCommand(dir, script, nil)
	if err == nil {
		t.Fatalf("expected an added file with an omitted patch to still fail closed, output:\n%s", output)
	}
	if !strings.Contains(output, "omitted the patch for") {
		t.Fatalf("expected an omitted-patch rejection message, got:\n%s", output)
	}
}

// TestCIWorkflowRejectsANonNullButTruncatedPatch actually runs the real
// truncated-patch gate against a synthetic pull-files response where a
// file's patch is present but its parsed hunks account for fewer
// additions/deletions than the file's own reported totals -- the exact
// scenario the Codex finding described: GitHub can truncate a patch
// without setting it to null, and a suppression beyond the truncated
// portion would otherwise escape the independent scan entirely while the
// trusted tracker's own patchLineStats/assertCompletePatch already refuses
// to trust the same input.
func TestCIWorkflowRejectsANonNullButTruncatedPatch(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/suppression-verify.yml", &workflow)
	gate := workflowStepByName(t, workflow.Jobs, "verify", "Verify inline suppression tracking issues were published")

	varsLine := sourceFileTestLine(t, gate.Run)

	const checkStart = "truncated_patch_count=0"
	const checkEnd = `if [ "${truncated_patch_count}" -gt 0 ]; then` + "\n  exit 1\nfi\n"
	checkStartIdx := strings.Index(gate.Run, checkStart)
	if checkStartIdx == -1 {
		t.Fatalf("suppression tracking gate is missing the truncated-patch check")
	}
	checkEndRelIdx := strings.Index(gate.Run[checkStartIdx:], checkEnd)
	if checkEndRelIdx == -1 {
		t.Fatalf("suppression tracking gate is missing the truncated-patch check end marker")
	}
	checkBlock := gate.Run[checkStartIdx : checkStartIdx+checkEndRelIdx+len(checkEnd)]

	dir := t.TempDir()

	// The file reports 5 additions, but the patch itself shows only 1 --
	// GitHub truncated it without setting patch to null.
	truncatedJSON, err := json.Marshal([]map[string]any{
		{
			"filename":  "big.go",
			"status":    "modified",
			"additions": 5,
			"deletions": 0,
			"patch":     "@@ -1,1 +1,1 @@\n-old line\n+new line",
		},
	})
	if err != nil {
		t.Fatalf("marshal truncated-patch fixture: %v", err)
	}
	script := "set -euo pipefail\n" + varsLine + "\npr_files_json=" + shellQuote(string(truncatedJSON)) + "\n" + checkBlock
	output, err := runShellCommand(dir, script, nil)
	if err == nil {
		t.Fatalf("expected a non-null but truncated patch to fail closed, output:\n%s", output)
	}
	if !strings.Contains(output, "truncated patch") {
		t.Fatalf("expected a truncated-patch rejection message, got:\n%s", output)
	}

	// The complete, matching patch for the same file must still pass.
	completeJSON, err := json.Marshal([]map[string]any{
		{
			"filename":  "big.go",
			"status":    "modified",
			"additions": 1,
			"deletions": 1,
			"patch":     "@@ -1,1 +1,1 @@\n-old line\n+new line",
		},
	})
	if err != nil {
		t.Fatalf("marshal complete-patch fixture: %v", err)
	}
	script = "set -euo pipefail\n" + varsLine + "\npr_files_json=" + shellQuote(string(completeJSON)) + "\n" + checkBlock
	if output, err := runShellCommand(dir, script, nil); err != nil {
		t.Fatalf("expected a complete, matching patch to pass, output:\n%s", output)
	}
}
