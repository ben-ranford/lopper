package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseSonarEvidence(t *testing.T) {
	for _, scenario := range []string{"success", "missing", "stale", "moving", "failed", "pending", "cancelled", "timeout", "api-error", "issues", "hotspots", "gate-failed", "malformed", "invalid-sha", "historical", "next-page", "history-missing", "history-issues", "history-accepted", "history-hotspots", "history-wrong-date", "history-api-error", "history-current-issues", "history-current-hotspots", "history-gate-failed", "history-moving"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			mock := `#!/usr/bin/env bash
set -eu
case "$*" in
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
  [[ "$SCENARIO" != history-api-error ]] || exit 22
  [[ "$SCENARIO" != history-missing ]] || { echo '{"measures":[]}'; exit; }
  issues=0; accepted=0; hotspots=0; date=2026-09-23T00:00:00+0000
  [[ "$SCENARIO" != history-issues ]] || issues=1
  [[ "$SCENARIO" != history-accepted ]] || accepted=1
  [[ "$SCENARIO" != history-hotspots ]] || hotspots=1
  [[ "$SCENARIO" != history-wrong-date ]] || date=2026-09-22T00:00:00+0000
  printf '{"measures":[{"metric":"violations","history":[{"date":"%s","value":"%s"}]},{"metric":"accepted_issues","history":[{"date":"%s","value":"%s"}]},{"metric":"security_hotspots","history":[{"date":"%s","value":"%s"}]}]}' "$date" "$issues" "$date" "$accepted" "$date" "$hotspots" ;;
 *ce/component*)
  case "$SCENARIO" in
   historical|history-*|next-page) echo '{"queue":[],"current":{"status":"SUCCESS","analysisId":"newer"}}' ;;
   failed) echo '{"queue":[],"current":{"status":"FAILED","analysisId":"analysis"}}' ;;
   cancelled) echo '{"queue":[],"current":{"status":"CANCELED","analysisId":"analysis"}}' ;;
   pending) echo '{"queue":[{}],"current":{"status":"SUCCESS","analysisId":"analysis"}}' ;;
   timeout) exit 28 ;;
   api-error) exit 22 ;;
   *) echo '{"queue":[],"current":{"status":"SUCCESS","analysisId":"analysis"}}' ;;
  esac ;;
 *qualitygates*)
  if [[ "$SCENARIO" == gate-failed || "$SCENARIO" == history-gate-failed ]]; then status=ERROR; else status=OK; fi
  printf '{"projectStatus":{"status":"%s"}}' "$status" ;;
 *issues/search*)
  if [[ "$SCENARIO" == issues || "$SCENARIO" == history-current-issues ]]; then total=1; else total=0; fi
  printf '{"total":%s,"issues":[]}' "$total" ;;
 *hotspots/search*)
  if [[ "$SCENARIO" == hotspots || "$SCENARIO" == history-current-hotspots ]]; then total=1; else total=0; fi
  printf '{"paging":{"total":%s},"hotspots":[]}' "$total" ;;
 *) exit 1 ;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(mock), 0700); err != nil {
				t.Fatal(err)
			}
			sha := strings.Repeat("a", 40)
			if scenario == "invalid-sha" {
				sha = "main"
			}
			cmd := exec.Command("bash", repoPath(t, "scripts/verify-release-sonar.sh"))
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "SOURCE_SHA="+sha, "SCENARIO="+scenario, "STATE="+filepath.Join(dir, "state"))
			output, err := cmd.CombinedOutput()
			if (err == nil) != (scenario == "success" || scenario == "historical" || scenario == "next-page") {
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
	step := workflowStepByName(t, source.Jobs, "verify-source-ci", "Verify exact source Sonar analysis")
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

func TestReleaseSonarVerifierSurvivesSourceCheckout(t *testing.T) {
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release-source-ci.yml", &workflow)
	job := workflowJobByName(t, workflow.Jobs, "verify-source-ci")
	assertWorkflowStepOrder(t, job, "Checkout trusted workflow revision", "Preserve trusted Sonar verifier", "Checkout exact release source", "Verify exact source Sonar analysis")
	checkout := workflowStepByName(t, workflow.Jobs, "verify-source-ci", "Checkout trusted workflow revision")
	preserve := workflowStepByName(t, workflow.Jobs, "verify-source-ci", "Preserve trusted Sonar verifier")
	verify := workflowStepByName(t, workflow.Jobs, "verify-source-ci", "Verify exact source Sonar analysis")
	if checkout.With["ref"] != "${{ github.workflow_sha }}" || checkout.With["persist-credentials"] != "false" || checkout.Uses != "actions/checkout@de0fac2e4500dabe0009e67214ff5f5447ce83dd" {
		t.Fatal("Sonar verifier must come from the immutable workflow revision without persisted credentials")
	}
	if checkout.If != verify.If || preserve.If != verify.If {
		t.Fatal("trusted verifier preparation must run for every Sonar verification")
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
