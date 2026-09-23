package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseSonarEvidence(t *testing.T) {
	for _, scenario := range []string{"success", "missing", "stale", "moving", "failed", "pending", "cancelled", "timeout", "api-error", "issues", "hotspots", "gate-failed", "malformed", "invalid-sha"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			mock := `#!/usr/bin/env bash
set -eu
case "$*" in
 *project_analyses*)
  case "$SCENARIO" in
   missing) echo '{"analyses":[]}' ;;
   malformed) echo '{' ;;
   stale) echo '{"analyses":[{"key":"analysis","revision":"other"}]}' ;;
   *)
    key=analysis
    if [[ "$SCENARIO" == moving && -f "$STATE" ]]; then key=changed; fi
    touch "$STATE"
    printf '{"analyses":[{"key":"%s","revision":"%s"}]}' "$key" "$SOURCE_SHA" ;;
  esac ;;
 *ce/component*)
  case "$SCENARIO" in
   failed) echo '{"queue":[],"current":{"status":"FAILED","analysisId":"analysis"}}' ;;
   cancelled) echo '{"queue":[],"current":{"status":"CANCELED","analysisId":"analysis"}}' ;;
   pending) echo '{"queue":[{}],"current":{"status":"SUCCESS","analysisId":"analysis"}}' ;;
   timeout) exit 28 ;;
   api-error) exit 22 ;;
   *) echo '{"queue":[],"current":{"status":"SUCCESS","analysisId":"analysis"}}' ;;
  esac ;;
 *qualitygates*)
  if [[ "$SCENARIO" == gate-failed ]]; then status=ERROR; else status=OK; fi
  printf '{"projectStatus":{"status":"%s"}}' "$status" ;;
 *issues/search*)
  if [[ "$SCENARIO" == issues ]]; then total=1; else total=0; fi
  printf '{"total":%s,"issues":[]}' "$total" ;;
 *hotspots/search*)
  if [[ "$SCENARIO" == hotspots ]]; then total=1; else total=0; fi
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
			if (err == nil) != (scenario == "success") {
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
		for _, name := range []string{"publish-ghcr-images", "publish-ghcr-manifest", "build-and-push"} {
			if job, ok := workflow.Jobs[name]; ok {
				if job.If != "" || !strings.Contains(strings.Join(job.Needs, ","), "verify-publication-source") {
					t.Fatalf("%s/%s bypasses failed or skipped verification", path, name)
				}
			}
		}
	}
	var source workflowConfig
	readYAMLConfig(t, ".github/workflows/release-source-ci.yml", &source)
	step := workflowStepByName(t, source.Jobs, "verify-source-ci", "Verify exact source Sonar analysis")
	if step.If != "${{ inputs.build_channel == 'release' }}" || step.Env["SOURCE_SHA"] != "${{ inputs.source_sha }}" || step.Run != "bash scripts/verify-release-sonar.sh" {
		t.Fatal("source CI must bind Sonar proof to release source")
	}
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
