package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReleaseSonarEvidence(t *testing.T) {
	successes := []string{"success", "ce-main-success", "ce-pr-failed", "ce-pr-canceled", "ce-other-branch-failed", "ce-other-branch-canceled", "activity-old-failure", "activity-pr-failure", "activity-pr-canceled", "activity-other-branch-failed", "activity-other-branch-canceled", "historical", "next-page"}
	for _, scenario := range []string{
		"success", "ce-main-success", "ce-pr-failed", "ce-pr-canceled", "ce-other-branch-failed", "ce-other-branch-canceled",
		"activity-old-failure", "activity-pr-failure", "activity-pr-canceled", "activity-other-branch-failed", "activity-other-branch-canceled",
		"no-token", "activity-forbidden", "activity-source-missing", "activity-source-pruned", "activity-source-future", "activity-source-malformed",
		"activity-main-failed", "activity-main-explicit-failed", "activity-main-canceled", "activity-main-failed-after", "activity-main-equal-time", "activity-malformed", "activity-missing-time", "activity-truncated",
		"ce-forbidden", "queued-main", "queued-reanalysis", "queued-after", "queue-missing", "queue-malformed",
		"ce-main-failed", "ce-main-explicit-failed", "ce-main-canceled", "ce-main-failed-after", "ce-main-mismatched", "ce-current-missing", "ce-current-unknown", "ce-pr-unknown", "ce-empty-pr-failed",
		"missing", "stale", "moving", "failed", "pending", "cancelled", "timeout", "api-error", "issues", "accepted", "false-positive", "dispositions-api-error", "dispositions-malformed", "hotspots", "gate-failed", "malformed", "invalid-sha",
		"hotspots-reviewed-safe", "hotspots-reviewed-fixed", "hotspots-missing-total", "hotspots-missing-items", "hotspots-api-error",
		"gate-ignored", "gate-ignored-missing", "gate-ignored-null", "gate-ignored-string",
		"historical", "next-page", "history-missing", "history-issues", "history-accepted", "history-false-positive", "history-missing-disposition", "history-hotspots", "history-wrong-date", "history-api-error", "history-current-issues", "history-current-accepted", "history-current-false-positive", "history-current-hotspots", "history-gate-failed", "history-moving",
		"history-current-hotspots-reviewed-safe", "history-gate-ignored",
	} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			mock := `#!/usr/bin/env bash
set -eu
if [[ "$*" != *'ce/activity?'* && "$*" == *'--user'* ]]; then exit 24; fi
case "$*" in
 *ce/activity*)
  [[ "$*" == *'--user test-sonar-token:'* ]] || exit 24
  [[ "$SCENARIO" != activity-forbidden ]] || exit 22
  if [[ "$*" == *'status=SUCCESS&'* ]]; then
    [[ "$SCENARIO" != activity-source-missing ]] || { echo '{"tasks":[]}'; exit; }
    completed=2026-09-23T00:00:10+0000
    [[ "$SCENARIO" != activity-source-pruned ]] || completed=2026-01-01T00:00:00+0000
    [[ "$SCENARIO" != activity-source-future ]] || completed=2026-10-01T00:00:00+0000
    [[ "$SCENARIO" != activity-source-malformed ]] || completed=unknown
    printf '{"tasks":[{"status":"SUCCESS","analysisId":"analysis","executedAt":"%s"}]}' "$completed"
    exit
  fi
  [[ "$*" == *'status=FAILED,CANCELED&ps=1000'* ]] || exit 24
  case "$SCENARIO" in
    activity-main-failed) echo '{"tasks":[{"status":"FAILED","submittedAt":"2026-09-23T00:00:00+0000","executedAt":"2026-09-23T00:00:11+0000"}]}'; exit ;;
    activity-main-explicit-failed) echo '{"tasks":[{"status":"FAILED","branch":"main","executedAt":"2026-09-23T00:00:11+0000"}]}'; exit ;;
    activity-main-canceled) echo '{"tasks":[{"status":"CANCELED","submittedAt":"2026-09-23T00:00:00+0000"}]}'; exit ;;
    activity-main-failed-after)
      if [[ -f "$STATE.activity" ]]; then echo '{"tasks":[{"status":"FAILED","executedAt":"2026-09-23T00:00:11+0000"}]}'; exit; fi
      touch "$STATE.activity" ;;
    activity-old-failure) echo '{"tasks":[{"status":"FAILED","executedAt":"2026-09-23T00:00:09+0000"}]}'; exit ;;
    activity-main-equal-time) echo '{"tasks":[{"status":"FAILED","executedAt":"2026-09-23T00:00:10+0000"}]}'; exit ;;
    activity-pr-failure) echo '{"tasks":[{"status":"FAILED","pullRequest":"1757"}]}'; exit ;;
    activity-pr-canceled) echo '{"tasks":[{"status":"CANCELED","pullRequest":"1757"}]}'; exit ;;
    activity-other-branch-failed) echo '{"tasks":[{"status":"FAILED","branch":"feature"}]}'; exit ;;
    activity-other-branch-canceled) echo '{"tasks":[{"status":"CANCELED","branch":"feature"}]}'; exit ;;
    activity-malformed) echo '{}'; exit ;;
    activity-missing-time) echo '{"tasks":[{"status":"FAILED"}]}'; exit ;;
    activity-truncated) jq -n '{tasks: [range(0;1000) | {status:"FAILED", pullRequest:"1757"}]}'; exit ;;
  esac
  echo '{"tasks":[]}'; exit ;;
 *project_analyses*)
  case "$SCENARIO" in
   historical|history-*)
    latest=newer
    if [[ "$SCENARIO" == history-moving && -f "$STATE" ]]; then latest=changed; fi
    touch "$STATE"
    printf '{"analyses":[{"key":"%s","revision":"newer"},{"key":"analysis","revision":"%s","date":"2026-09-23T00:00:00+0000"}]}' "$latest" "$SOURCE_SHA" ;;
   next-page)
    if [[ "$*" == *'&p=1'* ]]; then
      echo '{"paging":{"total":501},"analyses":[{"key":"newer","revision":"newer"}]}'
    else
      printf '{"analyses":[{"key":"analysis","revision":"%s","date":"2026-09-23T00:00:00+0000"}]}' "$SOURCE_SHA"
    fi ;;
   timeout) exit 28 ;;
   api-error) exit 22 ;;
   failed|pending|cancelled) echo '{"paging":{"total":1},"analyses":[{"key":"older","revision":"other","date":"2026-09-22T00:00:00+0000"}]}' ;;
   missing) echo '{"analyses":[]}' ;;
   malformed) echo '{' ;;
   stale) echo '{"analyses":[{"key":"analysis","revision":"other"}]}' ;;
   *)
    key=analysis
    if [[ "$SCENARIO" == moving && -f "$STATE" ]]; then key=changed; fi
    touch "$STATE"
    printf '{"analyses":[{"key":"%s","revision":"%s","date":"2026-09-23T00:00:00+0000"}]}' "$key" "$SOURCE_SHA" ;;
  esac ;;
 *measures/search_history*)
  case "$SCENARIO" in
   historical|history-*)
    [[ "$*" == *'&to=2026-09-23T00%3A00%3A01Z&'* ]] || { echo "exclusive upper bound does not include the exact analysis timestamp: $*" >&2; exit 23; } ;;
  esac
  [[ "$SCENARIO" != history-api-error ]] || exit 22
  [[ "$SCENARIO" != history-missing ]] || { echo '{"measures":[]}'; exit; }
  [[ "$*" == *'metrics=violations,accepted_issues,false_positive_issues,security_hotspots&'* ]] || exit 23
  issues=0; accepted=0; false_positive=0; hotspots=0; date=2026-09-23T00:00:00+0000
  [[ "$SCENARIO" != history-issues ]] || issues=1
  [[ "$SCENARIO" != history-accepted ]] || accepted=1
  [[ "$SCENARIO" != history-false-positive ]] || false_positive=1
  [[ "$SCENARIO" != history-hotspots ]] || hotspots=1
  [[ "$SCENARIO" != history-wrong-date ]] || date=2026-09-22T00:00:00+0000
  printf '{"measures":[{"metric":"violations","history":[{"date":"%s","value":"%s"}]},{"metric":"accepted_issues","history":[{"date":"%s","value":"%s"}]},{"metric":"%s","history":[{"date":"%s","value":"%s"}]},{"metric":"security_hotspots","history":[{"date":"%s","value":"%s"}]}]}' "$date" "$issues" "$date" "$accepted" "$(if [[ "$SCENARIO" == history-missing-disposition ]]; then echo unrelated; else echo false_positive_issues; fi)" "$date" "$false_positive" "$date" "$hotspots" ;;
 *ce/component*)
  case "$SCENARIO" in
   ce-forbidden) exit 22 ;;
   queued-main|queued-reanalysis) echo '{"queue":[{"status":"PENDING"}]}'; exit ;;
   queued-after)
    if [[ -f "$STATE.queue" ]]; then echo '{"queue":[{"status":"IN_PROGRESS"}]}'; exit; fi
    touch "$STATE.queue" ;;
   queue-missing) echo '{}'; exit ;;
   queue-malformed) echo '{"queue":{}}'; exit ;;
   ce-main-success) echo '{"queue":[],"current":{"status":"SUCCESS","analysisId":"analysis"}}'; exit ;;
   ce-main-failed) echo '{"queue":[],"current":{"status":"FAILED"}}'; exit ;;
   ce-main-explicit-failed) echo '{"queue":[],"current":{"status":"FAILED","branch":"main"}}'; exit ;;
   ce-main-canceled) echo '{"queue":[],"current":{"status":"CANCELED"}}'; exit ;;
   ce-main-failed-after)
    if [[ -f "$STATE.queue" ]]; then echo '{"queue":[],"current":{"status":"FAILED"}}'; exit; fi
    touch "$STATE.queue" ;;
   ce-main-mismatched) echo '{"queue":[],"current":{"status":"SUCCESS","analysisId":"new-main"}}'; exit ;;
   ce-current-missing) echo '{"queue":[]}'; exit ;;
   ce-current-unknown) echo '{"queue":[],"current":{"status":"UNKNOWN"}}'; exit ;;
   ce-pr-unknown) echo '{"queue":[],"current":{"status":"UNKNOWN","pullRequest":"1757"}}'; exit ;;
   ce-empty-pr-failed) echo '{"queue":[],"current":{"status":"FAILED","pullRequest":""}}'; exit ;;
   ce-pr-failed) echo '{"queue":[],"current":{"status":"FAILED","pullRequest":"1757"}}'; exit ;;
   ce-pr-canceled) echo '{"queue":[],"current":{"status":"CANCELED","pullRequest":"1757"}}'; exit ;;
   ce-other-branch-failed) echo '{"queue":[],"current":{"status":"FAILED","branch":"feature"}}'; exit ;;
   ce-other-branch-canceled) echo '{"queue":[],"current":{"status":"CANCELED","branch":"feature"}}'; exit ;;
  esac
  echo '{"queue":[],"current":{"status":"SUCCESS","analysisId":"unrelated-pr-analysis","pullRequest":"1757"}}' ;;
 *qualitygates*)
  [[ "$*" == *'analysisId=analysis' ]] || exit 23
  if [[ "$SCENARIO" == gate-failed || "$SCENARIO" == history-gate-failed ]]; then status=ERROR; else status=OK; fi
  ignored=false
  case "$SCENARIO" in
   gate-ignored|history-gate-ignored) ignored=true ;;
   gate-ignored-missing) echo '{"projectStatus":{"status":"OK"}}'; exit ;;
   gate-ignored-null) ignored=null ;;
   gate-ignored-string) ignored='"false"' ;;
  esac
  printf '{"projectStatus":{"status":"%s","ignoredConditions":%s}}' "$status" "$ignored" ;;
 *issues/search*)
  if [[ "$*" == *'issueStatuses=ACCEPTED,FALSE_POSITIVE'* ]]; then
    case "$SCENARIO" in
      accepted|false-positive|history-current-accepted|history-current-false-positive) echo '{"total":1,"issues":[{}]}'; exit ;;
      dispositions-api-error) exit 22 ;;
      dispositions-malformed) echo '{"issues":[]}'; exit ;;
    esac
    echo '{"total":0,"issues":[]}'; exit
  fi
  if [[ "$SCENARIO" == issues || "$SCENARIO" == history-current-issues ]]; then total=1; else total=0; fi
  printf '{"total":%s,"issues":[]}' "$total" ;;
 *hotspots/search*)
  case "$SCENARIO" in
   hotspots-reviewed-safe|hotspots-reviewed-fixed|history-current-hotspots-reviewed-safe)
    # A status-filtered request hides already reviewed hotspots.
    if [[ "$*" == *'status=TO_REVIEW'* ]]; then echo '{"paging":{"total":0},"hotspots":[]}'; exit; fi
    resolution=SAFE
    [[ "$SCENARIO" != hotspots-reviewed-fixed ]] || resolution=FIXED
    printf '{"paging":{"total":1},"hotspots":[{"status":"REVIEWED","resolution":"%s"}]}' "$resolution"; exit ;;
   hotspots-missing-total) echo '{"paging":{},"hotspots":[]}'; exit ;;
   hotspots-missing-items) echo '{"paging":{"total":0}}'; exit ;;
   hotspots-api-error) exit 22 ;;
  esac
  if [[ "$SCENARIO" == hotspots || "$SCENARIO" == history-current-hotspots ]]; then total=1; else total=0; fi
  printf '{"paging":{"total":%s},"hotspots":[]}' "$total" ;;
 *) exit 1 ;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(mock), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "date"), []byte("#!/usr/bin/env bash\necho 1790726400\n"), 0700); err != nil {
				t.Fatal(err)
			}
			sha := strings.Repeat("a", 40)
			if scenario == "invalid-sha" {
				sha = "main"
			}
			token := "test-sonar-token"
			if scenario == "no-token" {
				token = ""
			}
			cmd := exec.Command("bash", repoPath(t, "scripts/verify-release-sonar.sh"))
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "SOURCE_SHA="+sha, "SONAR_TOKEN="+token, "SCENARIO="+scenario, "STATE="+filepath.Join(dir, "state"))
			output, err := cmd.CombinedOutput()
			if (err == nil) != slices.Contains(successes, scenario) {
				t.Fatalf("unexpected result: %v: %s", err, output)
			}
		})
	}
}

func TestPublicationPathsRequireSourceGate(t *testing.T) {
	for _, path := range []string{"release-orchestration.yml", "docker-ghcr.yml"} {
		var workflow workflowConfig
		readYAMLConfig(t, ".github/workflows/"+path, &workflow)
		gate := workflowJobByName(t, workflow.Jobs, "verify-publication-source")
		if gate.Uses != "./.github/workflows/release-source-ci.yml" || gate.If != "" {
			t.Fatalf("%s must always run source gate", path)
		}
		assertPublicationJobsRequireSourceGate(t, workflow, path)
	}
	var source workflowConfig
	readYAMLConfig(t, ".github/workflows/release-source-ci.yml", &source)
	step := workflowStepByName(t, source.Jobs, "verify-source-sonar", "Verify exact source Sonar analysis")
	if step.If != "${{ inputs.build_channel == 'release' }}" || step.Env["SOURCE_SHA"] != "${{ inputs.source_sha }}" || step.Run != `bash "${RUNNER_TEMP}/verify-release-sonar.sh"` {
		t.Fatal("source CI must bind Sonar proof to release source")
	}
}

func assertPublicationJobsRequireSourceGate(t *testing.T, workflow workflowConfig, path string) {
	t.Helper()
	for _, name := range []string{"publish-ghcr-images", "publish-ghcr-manifest", "build-and-push"} {
		job, ok := workflow.Jobs[name]
		if !ok {
			continue
		}
		if job.If != "" || !strings.Contains(strings.Join(job.Needs, ","), "verify-publication-source") {
			t.Fatalf("%s/%s bypasses failed or skipped verification", path, name)
		}
	}
}

func TestDockerPublicationPermissions(t *testing.T) {
	var workflow struct {
		Permissions    map[string]string `yaml:"permissions"`
		workflowConfig `yaml:",inline"`
	}
	readYAMLConfig(t, ".github/workflows/docker-ghcr.yml", &workflow)
	if len(workflow.Permissions) != 1 || workflow.Permissions["contents"] != "read" {
		t.Fatal("Docker workflow must default to read-only contents")
	}
	assertWorkflowJobPermissions(t, workflowJobByName(t, workflow.Jobs, "build-and-push"), "Docker publisher", map[string]string{"contents": "read", "packages": "write"})
	assertWorkflowJobPermissions(t, workflowJobByName(t, workflow.Jobs, "verify-publication-source"), "Docker source gate", map[string]string{"contents": "read"})
}

func TestRollingCannotPublishStableIdentifiers(t *testing.T) {
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release-orchestration.yml", &workflow)
	step := workflowStepByName(t, workflow.Jobs, "validate-publication-identifiers", "Validate publication channel and identifiers")
	for _, test := range []struct {
		name, channel, tag, tags string
		pass                     bool
	}{
		{"rolling", "rolling", "rolling-20260923123456-abcdef0", "rolling\nrolling-20260923123456-abcdef0", true},
		{"release", "release", "v1.8.9", "v1.8.9\nlatest", true},
		{"latest", "rolling", "rolling-20260923123456-abcdef0", "latest", false},
		{"stable-version", "rolling", "rolling-20260923123456-abcdef0", "v1.8.9", false},
		{"stable-release", "rolling", "v1.8.9", "rolling", false},
		{"unknown-channel", "other", "v1.8.9", "latest", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", step.Run)
			cmd.Env = append(os.Environ(), "BUILD_CHANNEL="+test.channel, "RELEASE_TAG="+test.tag, "IMAGE_TAGS="+test.tags)
			output, err := cmd.CombinedOutput()
			if (err == nil) != test.pass {
				t.Fatalf("unexpected identifier result: %v: %s", err, output)
			}
		})
	}
}

func TestReleaseSonarVerifierIsIsolatedFromSourceCI(t *testing.T) {
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release-source-ci.yml", &workflow)
	job := workflowJobByName(t, workflow.Jobs, "verify-source-sonar")
	assertWorkflowStepOrder(t, job,
		"Checkout trusted workflow revision",
		"Preserve trusted Sonar verifier",
		"Verify exact source Sonar analysis",
	)
	assertWorkflowJobPermissions(t, job, "Sonar verifier", map[string]string{"contents": "read"})
	if job.If != "${{ inputs.build_channel == 'release' }}" || len(job.Steps) != 3 {
		t.Fatal("the credential-bearing job must only prepare and run the trusted release verifier")
	}
	ci := workflowJobByName(t, workflow.Jobs, "verify-source-ci")
	assertWorkflowJobNeeds(t, ci, "source CI", workflowJobNeeds{"verify-source-sonar"})
	if ci.If != "${{ always() && (inputs.build_channel != 'release' || needs.verify-source-sonar.result == 'success') }}" {
		t.Fatal("release source CI must require successful Sonar proof; only rolling may skip it")
	}
	assertWorkflowJobOmitsText(t, ci, "secrets.", "selected source execution must not receive secrets")
	assertWorkflowStepOrder(t, ci,
		"Validate build channel",
		"Checkout exact release source",
		"Verify exact release source",
		"Setup Go",
		"Install shellcheck",
		"Resolve gosec version",
		"Install Go tooling",
		"Run exact source CI gate",
		"Verify demo assets",
	)
	checkout := workflowStepByName(t, workflow.Jobs, "verify-source-sonar", "Checkout trusted workflow revision")
	preserve := workflowStepByName(t, workflow.Jobs, "verify-source-sonar", "Preserve trusted Sonar verifier")
	verify := workflowStepByName(t, workflow.Jobs, "verify-source-sonar", "Verify exact source Sonar analysis")
	if checkout.With["ref"] != "${{ github.workflow_sha }}" || checkout.With["persist-credentials"] != "false" || checkout.Uses != "actions/checkout@de0fac2e4500dabe0009e67214ff5f5447ce83dd" {
		t.Fatal("Sonar verifier must come from the immutable workflow revision without persisted credentials")
	}
	if checkout.If != verify.If || preserve.If != verify.If {
		t.Fatal("trusted verifier preparation must run for every Sonar verification")
	}
	if verify.Env["PATH"] != "/usr/bin:/bin" || verify.Shell != "/usr/bin/env -u BASH_ENV -u ENV -u PROMPT_COMMAND -u PS4 -u SHELLOPTS -u BASHOPTS /bin/bash --noprofile --norc -euo pipefail {0}" {
		t.Fatal("trusted Sonar verification must use a sanitized shell and system-only PATH before source-controlled steps")
	}
	if verify.Env["SONAR_TOKEN"] != "${{ secrets.SONAR_TOKEN }}" {
		t.Fatal("authenticated task evidence must use the explicitly supplied Sonar token")
	}
	for _, scenario := range []string{"missing", "replaced"} {
		t.Run(scenario, func(t *testing.T) {
			workspace, runnerTemp := t.TempDir(), t.TempDir()
			workspace, err := filepath.EvalSymlinks(workspace)
			if err != nil {
				t.Fatal(err)
			}
			verifier := filepath.Join(workspace, "scripts", "verify-release-sonar.sh")
			sha := strings.Repeat("a", 40)
			writeFile(t, verifier, `test "$SOURCE_SHA" = "`+sha+`" && test "$PWD" = "$EXPECTED_WORKSPACE"`)
			run := func(script string) {
				t.Helper()
				cmd := exec.Command("bash", "-eu", "-c", script)
				cmd.Dir = workspace
				cmd.Env = append(os.Environ(), "RUNNER_TEMP="+runnerTemp, "SOURCE_SHA="+sha, "EXPECTED_WORKSPACE="+workspace)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("workflow step failed: %v: %s", err, output)
				}
			}
			run(preserve.Run)
			if err := os.Remove(verifier); err != nil {
				t.Fatal(err)
			}
			if scenario == "replaced" {
				writeFile(t, verifier, "exit 99\n")
			}
			run(verify.Run)
		})
	}
}

func TestReleaseSonarTokenScopeAndReadOnlyDispatch(t *testing.T) {
	for path, want := range map[string][]string{
		"release.yml": {
			"jobs.orchestrate-release.secrets.SONAR_TOKEN=${{ secrets.SONAR_TOKEN }}",
			"jobs.verify-release-source-ci.secrets.SONAR_TOKEN=${{ secrets.SONAR_TOKEN }}",
		},
		"docker-ghcr.yml":           {"jobs.verify-publication-source.secrets.SONAR_TOKEN=${{ secrets.SONAR_TOKEN }}"},
		"release-orchestration.yml": {"jobs.verify-publication-source.secrets.SONAR_TOKEN=${{ secrets.SONAR_TOKEN }}"},
		"release-source-ci.yml":     {"jobs.verify-source-sonar.steps.Verify exact source Sonar analysis#1.env.SONAR_TOKEN=${{ secrets.SONAR_TOKEN }}"},
		"rolling.yml":               {},
	} {
		var workflow yaml.Node
		readYAMLConfig(t, ".github/workflows/"+path, &workflow)
		var got []string
		for _, binding := range workflowCredentialBindings(t, &workflow) {
			if strings.Contains(binding, "=inherit") {
				t.Fatalf("%s must forward only named secrets", path)
			}
			if strings.Contains(binding, "SONAR_TOKEN") {
				got = append(got, binding)
			}
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("%s Sonar token bindings = %v, want %v", path, got, want)
		}
	}
	var source workflowConfig
	readYAMLConfig(t, ".github/workflows/release-source-ci.yml", &source)
	inputs := source.On.WorkflowDispatch.Inputs
	if len(inputs) != 2 || !inputs["source_sha"].Required || inputs["build_channel"].Default != "release" {
		t.Fatal("standalone source proof must require an exact source SHA and default to stable verification")
	}
	if len(source.Jobs) != 2 {
		t.Fatal("standalone proof must contain only isolated Sonar and source CI jobs")
	}
	for _, name := range []string{"verify-source-sonar", "verify-source-ci"} {
		assertWorkflowJobPermissions(t, workflowJobByName(t, source.Jobs, name), name, map[string]string{"contents": "read"})
	}
}

func TestReleaseSourceCIRejectsInvalidChannels(t *testing.T) {
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release-source-ci.yml", &workflow)
	job := workflowJobByName(t, workflow.Jobs, "verify-source-ci")
	if job.If != "${{ always() && (inputs.build_channel != 'release' || needs.verify-source-sonar.result == 'success') }}" {
		t.Fatal("unsupported channels must reach validation; release still requires successful Sonar proof")
	}
	assertWorkflowStepOrder(t, job, "Validate build channel", "Checkout exact release source")
	step := workflowStepByName(t, workflow.Jobs, "verify-source-ci", "Validate build channel")
	if step.If != "" || step.Env["BUILD_CHANNEL"] != "${{ inputs.build_channel }}" {
		t.Fatal("source gate must always validate the supplied channel before source checkout")
	}
	for _, channel := range []string{"release", "rolling", "", "other", "RELEASE", "release\nrolling"} {
		t.Run(channel, func(t *testing.T) {
			cmd := exec.Command("bash", "-eu", "-c", step.Run)
			cmd.Env = append(os.Environ(), "BUILD_CHANNEL="+channel)
			output, err := cmd.CombinedOutput()
			wantSuccess := channel == "release" || channel == "rolling"
			if (err == nil) != wantSuccess {
				t.Fatalf("unexpected channel validation result: %v: %s", err, output)
			}
		})
	}
}
