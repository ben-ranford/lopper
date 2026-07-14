package scripts

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type workflowInput struct {
	Default     string `yaml:"default"`
	Description string `yaml:"description"`
	Required    bool   `yaml:"required"`
}

type workflowDispatchConfig struct {
	Inputs map[string]workflowInput `yaml:"inputs"`
}

type workflowOnConfig struct {
	WorkflowDispatch workflowDispatchConfig `yaml:"workflow_dispatch"`
}

type workflowConfig struct {
	On workflowOnConfig `yaml:"on"`
}

type workflowJobConfig struct {
	Steps []workflowStepConfig `yaml:"steps"`
}

type workflowStepConfig struct {
	Name  string            `yaml:"name"`
	ID    string            `yaml:"id"`
	Uses  string            `yaml:"uses"`
	Run   string            `yaml:"run"`
	Shell string            `yaml:"shell"`
	Env   map[string]string `yaml:"env"`
	With  map[string]string `yaml:"with"`
}

func TestReleasePleaseWritesRootChangelog(t *testing.T) {
	t.Parallel()

	var config struct {
		Packages map[string]struct {
			ChangelogPath string `json:"changelog-path"`
			ExtraFiles    []struct {
				Path string `json:"path"`
			} `json:"extra-files"`
		} `json:"packages"`
	}
	readJSONConfig(t, "release-please-config.json", &config)

	rootPackage, ok := config.Packages["."]
	if !ok {
		t.Fatal("release-please config must define the root package")
	}
	if rootPackage.ChangelogPath != "CHANGELOG.md" {
		t.Fatalf("root package changelog path = %q, want CHANGELOG.md", rootPackage.ChangelogPath)
	}
	if rootPackage.ChangelogPath == "extensions/vscode-lopper/CHANGELOG.md" {
		t.Fatal("root release notes must not be written to the VS Code extension changelog")
	}

	extraFiles := map[string]bool{}
	for _, extraFile := range rootPackage.ExtraFiles {
		extraFiles[extraFile.Path] = true
	}
	for _, path := range []string{
		"extensions/vscode-lopper/package.json",
		"extensions/vscode-lopper/package-lock.json",
	} {
		if !extraFiles[path] {
			t.Fatalf("release-please config should keep syncing %s", path)
		}
	}
}

func TestGraduateFeatureWorkflowTargetsCurrentSeries(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/graduate-feature.yml", &workflow)

	milestone, ok := workflow.On.WorkflowDispatch.Inputs["milestone"]
	if !ok {
		t.Fatal("graduate-feature workflow must define the milestone input")
	}
	if milestone.Default != "v1.7.0" {
		t.Fatalf("graduate-feature milestone default = %q, want v1.7.0", milestone.Default)
	}

	workflowText := readConfig(t, ".github/workflows/graduate-feature.yml")
	if !strings.Contains(workflowText, `target_series_label="target-series:${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.x"`) {
		t.Fatal("graduate-feature workflow must derive target-series labels from the milestone")
	}
	if strings.Contains(workflowText, "--label target-series:") {
		t.Fatal("graduate-feature workflow must not hardcode a target-series label")
	}
}

func TestGraduateFeatureWorkflowCreatesTemplateCompatiblePRBody(t *testing.T) {
	t.Parallel()

	workflowText := readConfig(t, ".github/workflows/graduate-feature.yml")
	for _, want := range []string{
		"cat > .artifacts/graduate-feature-pr.md <<PR_BODY",
		"## Summary",
		"## Changes",
		"## Validation",
		"Commands and checks run:",
		"Additional manual validation:",
		"## Risk and compatibility",
		"- Breaking changes:",
		"- Migration required:",
		"- Performance impact:",
		"- Memory benchmark impact:",
		"## Checklist",
		"- [x] Tests added/updated for behavior changes",
		"- [x] Docs updated (README/docs/schema) if needed",
		"- [x] \\`memory-approved\\` requested/applied if intentional memory benchmark regressions exceed CI thresholds",
		"- [x] No unrelated changes included",
		"- [x] Ready for review",
		"--body-file .artifacts/graduate-feature-pr.md",
	} {
		if !strings.Contains(workflowText, want) {
			t.Fatalf("graduate-feature workflow must create template-compatible PR body containing %q", want)
		}
	}

	for _, staleHeading := range []string{
		"## Graduation evidence",
		"## Compatibility and rollback",
		"## Release lock notes",
	} {
		if strings.Contains(workflowText, staleHeading) {
			t.Fatalf("graduate-feature workflow must not use stale non-template body heading %q", staleHeading)
		}
	}
}

func TestReleaseWorkflowManualDispatchUsesResolvedSourceRef(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	tag, ok := workflow.On.WorkflowDispatch.Inputs["tag"]
	if !ok {
		t.Fatal("release workflow must define the tag input")
	}
	if !tag.Required {
		t.Fatal("manual release dispatch must require a release tag or version")
	}
	if strings.Contains(strings.ToLower(tag.Description), "existing release tag") {
		t.Fatal("manual release dispatch should not require a pre-existing release tag")
	}

	sourceSHA, ok := workflow.On.WorkflowDispatch.Inputs["source_sha"]
	if !ok {
		t.Fatal("release workflow must define the source_sha input")
	}
	if strings.Contains(strings.ToLower(sourceSHA.Description), "defaults to tag") {
		t.Fatal("manual release source should not fall back to the tag name")
	}

	workflowText := readConfig(t, ".github/workflows/release.yml")
	if strings.Contains(workflowText, "inputs.source_sha || inputs.tag") {
		t.Fatal("manual release jobs must not fall back to tag names for source checkout")
	}
	if !strings.Contains(workflowText, "gh release create \"${tag}\"") {
		t.Fatal("manual release flow must create a draft GitHub release when one is missing")
	}
	if !strings.Contains(workflowText, "--target \"${resolved_sha}\"") {
		t.Fatal("manual release flow must target the resolved source SHA")
	}
	if !strings.Contains(workflowText, "Existing release ${tag} points to ${existing_commit}, but this run resolved ${resolved_sha}.") {
		t.Fatal("manual release flow must fail when an existing release tag points to a different commit")
	}
	if !strings.Contains(workflowText, "needs.prepare-release.outputs.sha") {
		t.Fatal("downstream release jobs must use the resolved prepare-release SHA")
	}
	if !strings.Contains(workflowText, "needs.prepare-release.outputs.release_created != 'true' && (github.event_name != 'workflow_dispatch' || github.event.inputs.tag == '')") {
		t.Fatal("skip-release must ignore manual dispatches that publish a requested tag")
	}
	if !strings.Contains(workflowText, "Release Please did not create a release on this push; the release PR was created or updated instead.") {
		t.Fatal("skip-release must log the push-specific message when no release is published")
	}
	if strings.Contains(workflowText, "Release Please did not create a release; the release PR was created or updated instead.") {
		t.Fatal("skip-release log must not use the stale message that also appears during manual tag dispatches")
	}
}

func TestReleaseWorkflowPublishesActionFloatingTags(t *testing.T) {
	t.Parallel()

	var workflow struct {
		Jobs map[string]workflowJobConfig `yaml:"jobs"`
	}
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	step := workflowStepByName(t, workflow.Jobs, "publish", "Update GitHub Action floating tags")
	if step.Shell != "bash" {
		t.Fatalf("action floating tag step shell = %q, want bash", step.Shell)
	}
	if step.Env["RELEASE_TAG"] != "${{ needs.prepare-release.outputs.tag }}" {
		t.Fatalf("action floating tag step RELEASE_TAG env = %q", step.Env["RELEASE_TAG"])
	}
	if step.Env["RELEASE_SHA"] != "${{ needs.prepare-release.outputs.sha }}" {
		t.Fatalf("action floating tag step RELEASE_SHA env = %q", step.Env["RELEASE_SHA"])
	}

	for _, want := range []string{
		`^v([0-9]+)[.]([0-9]+)[.]([0-9]+)$`,
		`major_tag="v${BASH_REMATCH[1]}"`,
		`minor_tag="v${BASH_REMATCH[1]}.${BASH_REMATCH[2]}"`,
		`git tag --force "${major_tag}" "${RELEASE_SHA}"`,
		`git tag --force "${minor_tag}" "${RELEASE_SHA}"`,
		`git push --force origin "refs/tags/${major_tag}" "refs/tags/${minor_tag}"`,
	} {
		if !strings.Contains(step.Run, want) {
			t.Fatalf("action floating tag step must contain %q", want)
		}
	}

	workflowText := readConfig(t, ".github/workflows/release.yml")
	if !strings.Contains(workflowText, "- GitHub Action: \\`${GITHUB_REPOSITORY}@${tag}\\`") {
		t.Fatal("release notes must include the concrete GitHub Action ref")
	}
}

func TestRenovateDoesNotAutomergeMajorUpdates(t *testing.T) {
	t.Parallel()

	var config struct {
		PackageRules []struct {
			MatchUpdateTypes []string `json:"matchUpdateTypes"`
			Automerge        *bool    `json:"automerge"`
		} `json:"packageRules"`
	}
	readJSONConfig(t, "renovate.json", &config)

	automergeByUpdateType := map[string][]bool{}
	for _, rule := range config.PackageRules {
		if rule.Automerge == nil {
			continue
		}
		for _, updateType := range rule.MatchUpdateTypes {
			automergeByUpdateType[updateType] = append(automergeByUpdateType[updateType], *rule.Automerge)
		}
	}

	for _, enabled := range automergeByUpdateType["major"] {
		if enabled {
			t.Fatal("major updates must not be covered by an automerge=true Renovate rule")
		}
	}
	if !hasAutomerge(automergeByUpdateType["major"], false) {
		t.Fatal("major updates should have an explicit automerge=false Renovate rule")
	}
	for _, updateType := range []string{"minor", "patch"} {
		if !hasAutomerge(automergeByUpdateType[updateType], true) {
			t.Fatalf("%s updates should retain Renovate automerge=true", updateType)
		}
	}
}

func TestRenovateTidiesGoModuleUpdates(t *testing.T) {
	t.Parallel()

	var config struct {
		PackageRules []struct {
			MatchManagers     []string `json:"matchManagers"`
			PostUpdateOptions []string `json:"postUpdateOptions"`
		} `json:"packageRules"`
	}
	readJSONConfig(t, "renovate.json", &config)

	for _, rule := range config.PackageRules {
		if slices.Contains(rule.MatchManagers, "gomod") && slices.Contains(rule.PostUpdateOptions, "gomodTidy") {
			return
		}
	}
	t.Fatal("Go module updates must run gomodTidy before CI and automerge")
}

func TestDarwinReleaseJobsAssertHostArchitecture(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		path         string
		stepName     string
		expectedArch string
	}{
		{
			path:         ".github/workflows/release-orchestration.yml",
			stepName:     "Assert Darwin arm64 host",
			expectedArch: "arm64",
		},
		{
			path:         ".github/workflows/release.yml",
			stepName:     "Assert Darwin amd64 host",
			expectedArch: "amd64",
		},
		{
			path:         ".github/workflows/rolling.yml",
			stepName:     "Assert Darwin amd64 host",
			expectedArch: "amd64",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()

			workflowText := readConfig(t, tc.path)
			if !strings.Contains(workflowText, tc.stepName) {
				t.Fatalf("%s must contain step %q", tc.path, tc.stepName)
			}
			wantCheck := `host_goarch}" != "` + tc.expectedArch + `"`
			if !strings.Contains(workflowText, wantCheck) {
				t.Fatalf("%s must fail early unless GOARCH is %s", tc.path, tc.expectedArch)
			}
		})
	}
}

func TestMakefileReleasePackagesRuntimeHooks(t *testing.T) {
	t.Parallel()

	makefile := readConfig(t, "Makefile")
	for _, want := range []string{
		`mkdir -p "$$output_dir/share/lopper/scripts"`,
		`cp -R scripts/runtime "$$output_dir/share/lopper/scripts/"`,
	} {
		if !strings.Contains(makefile, want) {
			t.Fatalf("release target must package runtime hook assets with %q", want)
		}
	}

	for _, path := range []string{
		"scripts/runtime/require-hook.cjs",
		"scripts/runtime/loader.mjs",
		"scripts/runtime/sitecustomize.py",
	} {
		if _, err := os.Stat(repoPath(t, path)); err != nil {
			t.Fatalf("runtime hook asset %s must exist: %v", path, err)
		}
	}
}

func TestReleaseImageTagScriptSanitizesAndValidatesTags(t *testing.T) {
	t.Parallel()

	imageTags := "  v1.2.3 \r\n\tlatest\t\n\n \r\n v1.2.3-rc.1 \n"
	testCases := []struct {
		name   string
		suffix string
		want   string
	}{
		{
			name:   "amd64",
			suffix: "-amd64",
			want: "ghcr.io/example/lopper:v1.2.3-amd64\n" +
				"ghcr.io/example/lopper:latest-amd64\n" +
				"ghcr.io/example/lopper:v1.2.3-rc.1-amd64\n",
		},
		{
			name:   "arm64",
			suffix: "-arm64",
			want: "ghcr.io/example/lopper:v1.2.3-arm64\n" +
				"ghcr.io/example/lopper:latest-arm64\n" +
				"ghcr.io/example/lopper:v1.2.3-rc.1-arm64\n",
		},
		{
			name: "manifest",
			want: "ghcr.io/example/lopper:v1.2.3\n" +
				"ghcr.io/example/lopper:latest\n" +
				"ghcr.io/example/lopper:v1.2.3-rc.1\n",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			output := runReleaseImageTagScript(t, imageTags, tc.suffix)
			if output != tc.want {
				t.Fatalf("script output = %q, want %q", output, tc.want)
			}
		})
	}
}

func TestReleaseImageTagScriptRejectsMalformedTagsBeforeOutput(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		imageTags string
	}{
		{
			name:      "internal whitespace",
			imageTags: "v1.2.3\nbad tag\nlatest",
		},
		{
			name:      "slash",
			imageTags: "release/candidate",
		},
		{
			name:      "leading dash",
			imageTags: "-latest",
		},
		{
			name:      "too long",
			imageTags: strings.Repeat("a", 129),
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd := releaseImageTagScriptCommand(t, tc.imageTags, "-amd64")
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("script succeeded with malformed tag; output: %s", output)
			}
			if !strings.Contains(string(output), "::error::Malformed image tag rejected.") {
				t.Fatalf("script error = %q, want malformed tag error", output)
			}
			if strings.Contains(string(output), "ghcr.io/example/lopper:") {
				t.Fatalf("script emitted image refs before rejecting malformed input: %s", output)
			}
		})
	}
}

func TestReleaseOrchestrationImageTagStepsUseSanitizer(t *testing.T) {
	t.Parallel()

	var workflow struct {
		Jobs map[string]workflowJobConfig `yaml:"jobs"`
	}
	readYAMLConfig(t, ".github/workflows/release-orchestration.yml", &workflow)

	testCases := []struct {
		jobName  string
		stepName string
		suffix   string
	}{
		{
			jobName:  "publish-ghcr-amd64",
			stepName: "Compute amd64 image tags",
			suffix:   "-amd64",
		},
		{
			jobName:  "publish-ghcr-arm64",
			stepName: "Compute arm64 image tags",
			suffix:   "-arm64",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.jobName, func(t *testing.T) {
			t.Parallel()

			step := workflowStepByName(t, workflow.Jobs, tc.jobName, tc.stepName)
			assertArchImageTagStep(t, step, tc.stepName, tc.suffix)
		})
	}

	manifestStep := workflowStepByName(t, workflow.Jobs, "publish-ghcr-manifest", "Publish multi-arch manifests")
	assertManifestImageTagStep(t, manifestStep)
}

func assertArchImageTagStep(t *testing.T, step workflowStepConfig, stepName string, suffix string) {
	t.Helper()

	if step.Shell != "bash" {
		t.Fatalf("%s shell = %q, want bash", stepName, step.Shell)
	}
	if step.Env["IMAGE_NAME"] != "${{ steps.image.outputs.name }}" {
		t.Fatalf("%s IMAGE_NAME env = %q, want image output", stepName, step.Env["IMAGE_NAME"])
	}
	if step.Env["IMAGE_ARCH_SUFFIX"] != suffix {
		t.Fatalf("%s IMAGE_ARCH_SUFFIX env = %q, want %q", stepName, step.Env["IMAGE_ARCH_SUFFIX"], suffix)
	}
	if !strings.Contains(step.Run, "bash scripts/release-image-tags.sh > image-tags.txt") {
		t.Fatalf("%s must generate tags through scripts/release-image-tags.sh", stepName)
	}
	if strings.Contains(step.Run, "while IFS= read -r tag") {
		t.Fatalf("%s must not use the stale unsanitized tag loop", stepName)
	}
}

func assertManifestImageTagStep(t *testing.T, manifestStep workflowStepConfig) {
	t.Helper()

	sanitizerIndex := strings.Index(manifestStep.Run, "bash scripts/release-image-tags.sh > image-tags.txt")
	dockerIndex := strings.Index(manifestStep.Run, "docker buildx imagetools create")
	if sanitizerIndex < 0 {
		t.Fatal("manifest publish step must sanitize image tags before use")
	}
	if dockerIndex < 0 {
		t.Fatal("manifest publish step must create Docker manifests")
	}
	if sanitizerIndex > dockerIndex {
		t.Fatal("manifest publish step must validate tags before Docker manifest operations")
	}
	if !strings.Contains(manifestStep.Run, "done < image-tags.txt") {
		t.Fatal("manifest publish step must consume sanitized image tags")
	}
	if strings.Contains(manifestStep.Run, "done <<< \"$IMAGE_TAGS\"") {
		t.Fatal("manifest publish step must not use the stale unsanitized image tag input loop")
	}
}

func TestHomebrewTapWorkflowsContainRequiredFormulaValidationCommands(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		command string
		message string
	}{
		{
			name:    "trust local tap",
			command: "brew trust ben-ranford/tap",
			message: "must trust the local Homebrew tap before auditing formulae",
		},
		{
			name:    "disable linux sandbox",
			command: "export HOMEBREW_NO_SANDBOX_LINUX=1",
			message: "must disable the Linux Homebrew sandbox before build-from-source formula validation",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			for _, path := range []string{
				".github/workflows/ci.yml",
				".github/workflows/release.yml",
				".github/workflows/rolling.yml",
			} {
				path := path
				t.Run(path, func(t *testing.T) {
					t.Parallel()

					workflowText := readConfig(t, path)
					if !strings.Contains(workflowText, tc.command) {
						t.Fatalf("%s %s", path, tc.message)
					}
				})
			}
		})
	}
}

func TestRollingWorkflowUsesFreshPrivilegedTapUpdateJob(t *testing.T) {
	t.Parallel()
	assertHomebrewTapWorkflowUsesFreshPrivilegedJob(t, homebrewTapWorkflowCase{
		workflowPath:       ".github/workflows/rolling.yml",
		validationJobName:  "validate-homebrew-tap-rolling",
		updateJobName:      "update-homebrew-tap-rolling",
		checkoutUses:       "actions/checkout@v7",
		regenerateStepName: "Regenerate lopper-rolling formula",
		pushStepName:       "Regenerate and push rolling formula changes",
		formulaPath:        "Formula/lopper-rolling.rb",
		sourceURLLine:      `source_url="https://github.com/${SOURCE_REPO}/archive/refs/tags/${ROLLING_TAG}.tar.gz"`,
		commitMessageLine:  `git_safe -C "${tap_repo}" commit --no-verify -m "lopper-rolling ${ROLLING_TAG}"`,
		updateNeedsLine:    "  update-homebrew-tap-rolling:\n    needs:\n      - prepare-rolling\n      - publish-rolling\n      - validate-homebrew-tap-rolling",
	})
}

func TestReleaseWorkflowUsesFreshPrivilegedTapUpdateJob(t *testing.T) {
	t.Parallel()
	assertHomebrewTapWorkflowUsesFreshPrivilegedJob(t, homebrewTapWorkflowCase{
		workflowPath:       ".github/workflows/release.yml",
		validationJobName:  "validate-homebrew-tap",
		updateJobName:      "update-homebrew-tap",
		checkoutUses:       "actions/checkout@de0fac2e4500dabe0009e67214ff5f5447ce83dd",
		regenerateStepName: "Regenerate lopper formula",
		pushStepName:       "Regenerate and push formula changes",
		formulaPath:        "Formula/lopper.rb",
		sourceURLLine:      `source_url="https://github.com/${SOURCE_REPO}/archive/refs/tags/${RELEASE_TAG}.tar.gz"`,
		commitMessageLine:  `git_safe -C "${tap_repo}" commit --no-verify -m "lopper ${RELEASE_TAG}"`,
		updateNeedsLine:    "  update-homebrew-tap:\n    needs:\n      - prepare-release\n      - publish\n      - validate-homebrew-tap",
	})
}

type homebrewTapWorkflowCase struct {
	workflowPath       string
	validationJobName  string
	updateJobName      string
	checkoutUses       string
	regenerateStepName string
	pushStepName       string
	formulaPath        string
	sourceURLLine      string
	commitMessageLine  string
	updateNeedsLine    string
}

func assertHomebrewTapWorkflowUsesFreshPrivilegedJob(t *testing.T, tc homebrewTapWorkflowCase) {
	t.Helper()

	var workflow struct {
		Jobs map[string]workflowJobConfig `yaml:"jobs"`
	}
	readYAMLConfig(t, tc.workflowPath, &workflow)
	workflowText := readConfig(t, tc.workflowPath)

	if !strings.Contains(workflowText, tc.updateNeedsLine) {
		t.Fatalf("%s must make %s depend on the tokenless validation job", tc.workflowPath, tc.updateJobName)
	}

	assertTokenlessValidationJob(t, workflow.Jobs, tc)
	assertFreshPrivilegedTapUpdateJob(t, workflow.Jobs, tc)
}

func assertTokenlessValidationJob(t *testing.T, jobs map[string]workflowJobConfig, tc homebrewTapWorkflowCase) {
	t.Helper()

	validationCheckout := workflowStepByName(t, jobs, tc.validationJobName, "Checkout tap repository")
	if validationCheckout.Uses != tc.checkoutUses {
		t.Fatalf("%s validation tap checkout uses %q, want %q", tc.workflowPath, validationCheckout.Uses, tc.checkoutUses)
	}
	if _, ok := validationCheckout.With["token"]; ok {
		t.Fatalf("%s validation tap checkout must not pass HOMEBREW_TAP_TOKEN to actions/checkout", tc.workflowPath)
	}
	if validationCheckout.With["persist-credentials"] != "false" {
		t.Fatalf("%s validation tap checkout persist-credentials = %q, want false", tc.workflowPath, validationCheckout.With["persist-credentials"])
	}

	validationJob, ok := jobs[tc.validationJobName]
	if !ok {
		t.Fatalf("workflow must define job %s", tc.validationJobName)
	}
	assertJobDoesNotReceiveTapToken(t, validationJob, tc)
	assertValidationRegenerationStep(t, jobs, tc)
}

func assertJobDoesNotReceiveTapToken(t *testing.T, job workflowJobConfig, tc homebrewTapWorkflowCase) {
	t.Helper()

	for _, step := range job.Steps {
		for key, value := range step.Env {
			if strings.Contains(key, "HOMEBREW_TAP_TOKEN") || strings.Contains(value, "HOMEBREW_TAP_TOKEN") {
				t.Fatalf("%s validation step %q must not receive HOMEBREW_TAP_TOKEN", tc.workflowPath, step.Name)
			}
		}
	}
}

func assertValidationRegenerationStep(t *testing.T, jobs map[string]workflowJobConfig, tc homebrewTapWorkflowCase) {
	t.Helper()

	validationRegenerate := workflowStepByName(t, jobs, tc.validationJobName, tc.regenerateStepName)
	for _, want := range []string{
		tc.sourceURLLine,
		`cat > ` + tc.formulaPath + ` <<RUBY`,
	} {
		if !strings.Contains(validationRegenerate.Run, want) {
			t.Fatalf("%s validation regenerate step must contain %q", tc.workflowPath, want)
		}
	}
}

func assertFreshPrivilegedTapUpdateJob(t *testing.T, jobs map[string]workflowJobConfig, tc homebrewTapWorkflowCase) {
	t.Helper()

	updateJob, ok := jobs[tc.updateJobName]
	if !ok {
		t.Fatalf("workflow must define job %s", tc.updateJobName)
	}
	for _, step := range updateJob.Steps {
		if step.Name == "Checkout tap repository" {
			t.Fatalf("%s privileged job must use a fresh git clone instead of actions/checkout", tc.workflowPath)
		}
	}

	pushStep := workflowStepByName(t, jobs, tc.updateJobName, tc.pushStepName)
	if pushStep.Env["HOMEBREW_TAP_TOKEN"] != "${{ secrets.HOMEBREW_TAP_TOKEN }}" {
		t.Fatalf("%s privileged tap update step HOMEBREW_TAP_TOKEN env = %q", tc.workflowPath, pushStep.Env["HOMEBREW_TAP_TOKEN"])
	}
	if pushStep.Shell != "/usr/bin/env -u BASH_ENV -u ENV -u PROMPT_COMMAND -u PS4 -u SHELLOPTS -u BASHOPTS /usr/bin/bash --noprofile --norc -euo pipefail {0}" {
		t.Fatalf("%s privileged tap update step shell = %q", tc.workflowPath, pushStep.Shell)
	}

	assertPrivilegedStepContainsRequiredHardening(t, pushStep, tc)
	assertPrivilegedStepOmitsUnsafePatterns(t, pushStep, tc)
}

func assertPrivilegedStepContainsRequiredHardening(t *testing.T, pushStep workflowStepConfig, tc homebrewTapWorkflowCase) {
	t.Helper()

	for _, want := range []string{
		"git_bin=/usr/bin/git",
		"curl_bin=/usr/bin/curl",
		"sha256sum_bin=/usr/bin/sha256sum",
		"base64_bin=/usr/bin/base64",
		"tr_bin=/usr/bin/tr",
		"mktemp_bin=/usr/bin/mktemp",
		"export PATH=/usr/bin:/bin",
		`if [ -z "${HOMEBREW_TAP_TOKEN:-}" ]; then`,
		`git_home="$("${mktemp_bin}" -d)"`,
		`work_root="$("${mktemp_bin}" -d)"`,
		`tap_repo="${work_root}/homebrew-tap"`,
		`trap 'rm -rf "${git_home}" "${work_root}"' EXIT`,
		"auth_header=\"$(printf 'x-access-token:%s' \"${HOMEBREW_TAP_TOKEN}\" | \"${base64_bin}\" | \"${tr_bin}\" -d '\\n')\"",
		"unset HOMEBREW_TAP_TOKEN",
		"env -i",
		`HOME="${git_home}"`,
		`PATH="${PATH}"`,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=5",
		"GIT_CONFIG_KEY_4=http.https://github.com/.extraheader",
		`GIT_CONFIG_VALUE_4="AUTHORIZATION: basic ${auth_header}"`,
		`git_safe clone --origin origin --branch main --single-branch https://github.com/ben-ranford/homebrew-tap.git "${tap_repo}"`,
		tc.sourceURLLine,
		`cat > "${tap_repo}/` + tc.formulaPath + `" <<RUBY`,
		`git_safe -C "${tap_repo}" add ` + tc.formulaPath,
		`git_safe -C "${tap_repo}" diff --cached --quiet`,
		tc.commitMessageLine,
		`git_network -C "${tap_repo}" fetch origin main:refs/remotes/origin/main`,
		`git_safe -C "${tap_repo}" rebase origin/main`,
		`git_safe -C "${tap_repo}" rebase --abort || true`,
		`git_network -C "${tap_repo}" push origin HEAD:main`,
		"/bin/sleep 2",
	} {
		if !strings.Contains(pushStep.Run, want) {
			t.Fatalf("%s privileged tap update step must contain %q", tc.workflowPath, want)
		}
	}
}

func assertPrivilegedStepOmitsUnsafePatterns(t *testing.T, pushStep workflowStepConfig, tc homebrewTapWorkflowCase) {
	t.Helper()

	for _, forbidden := range []string{
		"brew audit --strict --online",
		"brew install --build-from-source",
		"brew test ben-ranford/tap/",
		"RUNNER_TEMP",
		"patch_path=",
		"commit_sha=",
		"format-patch --stdout",
		`am --no-verify`,
		"FORMULA_PATCH_PATH",
		"git remote set-url origin ",
		"x-access-token:${HOMEBREW_TAP_TOKEN}@github.com/ben-ranford/homebrew-tap.git",
		`-c "http.https://github.com/.extraheader=AUTHORIZATION: basic ${auth_header}"`,
		"env -u HOMEBREW_TAP_TOKEN",
		"git_local()",
	} {
		if strings.Contains(pushStep.Run, forbidden) {
			t.Fatalf("%s privileged tap update step must not contain %q", tc.workflowPath, forbidden)
		}
	}
}

func hasAutomerge(values []bool, want bool) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func runReleaseImageTagScript(t *testing.T, imageTags string, suffix string) string {
	t.Helper()

	cmd := releaseImageTagScriptCommand(t, imageTags, suffix)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run release image tag script: %v\n%s", err, output)
	}
	return string(output)
}

func releaseImageTagScriptCommand(t *testing.T, imageTags string, suffix string) *exec.Cmd {
	t.Helper()

	cmd := exec.Command("bash", repoPath(t, "scripts/release-image-tags.sh"))
	cmd.Env = append(os.Environ(), "IMAGE_NAME=ghcr.io/example/lopper", "IMAGE_TAGS="+imageTags, "IMAGE_ARCH_SUFFIX="+suffix)
	return cmd
}

func workflowStepByName(t *testing.T, jobs map[string]workflowJobConfig, jobName string, stepName string) workflowStepConfig {
	t.Helper()

	job, ok := jobs[jobName]
	if !ok {
		t.Fatalf("workflow must define job %s", jobName)
	}
	for _, step := range job.Steps {
		if step.Name == stepName {
			return step
		}
	}
	t.Fatalf("%s must define step %q", jobName, stepName)
	return workflowStepConfig{}
}

func readJSONConfig(t *testing.T, path string, target any) {
	t.Helper()

	data := readConfig(t, path)
	if err := json.Unmarshal([]byte(data), target); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
}

func readYAMLConfig(t *testing.T, path string, target any) {
	t.Helper()

	data := readConfig(t, path)
	if err := yaml.Unmarshal([]byte(data), target); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
}

func readConfig(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(repoPath(t, path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func repoPath(t *testing.T, path string) string {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	return filepath.Join(filepath.Dir(filename), "..", path)
}
