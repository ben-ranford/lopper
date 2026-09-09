package scripts

import "testing"

func TestCIWorkflowCancelsSupersededRuns(t *testing.T) {
	t.Parallel()
	var concurrency struct {
		Concurrency struct {
			Group            string `yaml:"group"`
			CancelInProgress bool   `yaml:"cancel-in-progress"`
		} `yaml:"concurrency"`
	}
	readYAMLConfig(t, ".github/workflows/ci.yml", &concurrency)
	if concurrency.Concurrency.Group != "${{ github.workflow }}-${{ github.event.pull_request.number || github.run_id }}" || !concurrency.Concurrency.CancelInProgress {
		t.Fatal("CI must cancel superseded runs per PR and keep manual dispatches independent")
	}
	assertPullRequestTriggerTypes(t, ".github/workflows/ci.yml")
}

func TestCIVerificationAggregatesEveryJob(t *testing.T) {
	t.Parallel()
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	for _, aggregate := range []string{"verify", "verify-rolling"} {
		t.Run(aggregate, func(t *testing.T) {
			t.Parallel()
			job := workflowJobByName(t, workflow.Jobs, aggregate)
			assertWorkflowJobNeeds(t, job, aggregate, workflowJobNeeds{aggregate + "-checks", aggregate + "-tests"})
			if job.If != "${{ always() }}" || job.RunsOn != "ubuntu-latest" || len(job.Permissions) != 0 {
				t.Fatal("aggregate must run after every result without write credentials")
			}
			assertWorkflowJobOmitsCheckout(t, job, aggregate)
			gate := workflowStepByName(t, workflow.Jobs, aggregate, "Require every verification job")
			assertWorkflowStepEnv(t, gate, aggregate, map[string]string{
				"CHECKS_RESULT": "${{ needs." + aggregate + "-checks.result }}",
				"TESTS_RESULT":  "${{ needs." + aggregate + "-tests.result }}",
			})
			for _, checks := range []string{"success", "failure", "cancelled", "skipped", ""} {
				for _, tests := range []string{"success", "failure", "cancelled", "skipped", ""} {
					output, err := runShellCommand(t.TempDir(), gate.Run, map[string]string{"CHECKS_RESULT": checks, "TESTS_RESULT": tests})
					if (err == nil) != (checks == "success" && tests == "success") {
						t.Fatalf("checks=%q tests=%q: %v\n%s", checks, tests, err, output)
					}
				}
			}
		})
	}
	if workflow.Jobs["verify"].Outputs["pr_report_artifact_id"] != "${{ needs.verify-checks.outputs.pr_report_artifact_id }}" {
		t.Fatal("verify must forward the producer's exact artifact ID")
	}
	if workflow.Jobs["verify-rolling"].Name != "verify (rolling)" {
		t.Fatal("rolling required check name changed")
	}
}

func TestCIParallelJobsPreserveSourceAndBuildChannels(t *testing.T) {
	t.Parallel()
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	for jobName, channel := range map[string]string{"verify-tests": "dev", "verify-rolling-tests": "rolling"} {
		job := workflowJobByName(t, workflow.Jobs, jobName)
		if job.Uses != "./.github/workflows/ci-tests.yml" || job.With["source_sha"] != "${{ github.sha }}" || job.With["build_channel"] != channel || len(job.Needs) != 0 {
			t.Fatalf("%s must independently test the exact %s source", jobName, channel)
		}
		assertWorkflowJobPermissions(t, job, jobName, map[string]string{"contents": "read"})
	}
	for _, jobName := range []string{"verify-checks", "verify-rolling-checks"} {
		job := workflowJobByName(t, workflow.Jobs, jobName)
		if len(job.Needs) != 0 {
			t.Fatalf("%s must overlap the test job", jobName)
		}
		checkout := workflowStepByName(t, workflow.Jobs, jobName, "Checkout")
		if checkout.With["ref"] != "${{ github.sha }}" {
			t.Fatalf("%s must pin the same event source as tests", jobName)
		}
	}
	var reusable workflowConfig
	readYAMLConfig(t, ".github/workflows/ci-tests.yml", &reusable)
	checkout := workflowStepByName(t, reusable.Jobs, "tests", "Checkout exact CI source")
	if checkout.With["ref"] != "${{ inputs.source_sha }}" || checkout.With["persist-credentials"] != "false" || checkout.With["fetch-depth"] != "0" {
		t.Fatal("parallel tests must use the supplied source and full history without persisted credentials")
	}
	tests := workflowStepByName(t, reusable.Jobs, "tests", "Run normal and leak tests")
	assertWorkflowStepEnv(t, tests, "parallel tests", map[string]string{"BUILD_CHANNEL": "${{ inputs.build_channel }}"})
	if tests.Run != "make ci-tests BUILD_CHANNEL=\"${BUILD_CHANNEL}\"" {
		t.Fatal("parallel tests must execute the complete test partition")
	}
	cleanup := workflowStepByName(t, reusable.Jobs, "tests", "Check runtime artifacts after tests")
	if cleanup.If != "${{ always() }}" || cleanup.Run != "make runtime-pycache-check" {
		t.Fatal("runtime artifacts must be checked in the test workspace, including failures")
	}
}

func TestCICachesSeparateVerificationFromSmoke(t *testing.T) {
	t.Parallel()
	var workflow, reusable, release workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	readYAMLConfig(t, ".github/workflows/ci-tests.yml", &reusable)
	readYAMLConfig(t, ".github/workflows/release-source-ci.yml", &release)
	wantChecks := "go.sum\nMakefile\n.github/workflows/ci.yml\n"
	for _, job := range []string{"verify-checks", "verify-rolling-checks"} {
		setup := workflowStepByName(t, workflow.Jobs, job, "Setup Go")
		if setup.With["cache-dependency-path"] != wantChecks {
			t.Fatalf("%s must key compiler/tool cache by module and CI tool inputs", job)
		}
	}
	setupRelease := workflowStepByName(t, release.Jobs, "verify-source-ci", "Setup Go")
	if setupRelease.With["cache-dependency-path"] != wantChecks {
		t.Fatal("trusted full-source CI must warm the verification cache key")
	}
	setupTests := workflowStepByName(t, reusable.Jobs, "tests", "Setup Go")
	if setupTests.With["cache-dependency-path"] != "go.sum\nMakefile\n.github/workflows/ci-tests.yml\n" {
		t.Fatal("test-only cache must be separate from checks and smoke")
	}
	for _, job := range []string{"os-smoke", "vscode-smoke"} {
		setup := workflowStepByName(t, workflow.Jobs, job, "Setup Go")
		if setup.With["cache-dependency-path"] == wantChecks {
			t.Fatalf("%s must not populate the larger verification cache", job)
		}
	}
}
