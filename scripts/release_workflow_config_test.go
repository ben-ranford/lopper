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
	On   workflowOnConfig             `yaml:"on"`
	Jobs map[string]workflowJobConfig `yaml:"jobs"`
}

type workflowJobConfig struct {
	Steps []workflowStepConfig `yaml:"steps"`
}

type workflowStepConfig struct {
	Name             string            `yaml:"name"`
	ID               string            `yaml:"id"`
	If               string            `yaml:"if"`
	Run              string            `yaml:"run"`
	Shell            string            `yaml:"shell"`
	WorkingDirectory string            `yaml:"working-directory"`
	Env              map[string]string `yaml:"env"`
	With             map[string]string `yaml:"with"`
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
	manualStep := workflowStepByName(t, workflow.Jobs, "prepare-release", "Prepare manual release")
	assertWorkflowStepRunContainsAll(t, manualStep, "manual release preparation step", []string{
		`existing_target="$(jq -r '.target_commitish // empty' "${release_json}")"`,
		`if ! fetch_error="$(git fetch --force origin "refs/tags/${tag}:refs/tags/${tag}" 2>&1)"; then`,
		`if [[ "${fetch_error}" == *"couldn't find remote ref refs/tags/${tag}"* ]]; then`,
		`echo "Remote tag ${tag} does not exist; validating the release target instead."`,
		`echo "::error::Failed to fetch existing release tag ${tag}." >&2
      printf '%s\n' "${fetch_error}" >&2
      exit 1`,
	})
	if strings.Contains(manualStep.Run, `git fetch --force origin "refs/tags/${tag}:refs/tags/${tag}" >/dev/null 2>&1 || true`) {
		t.Fatal("manual release flow must not swallow operational failures while fetching an existing release tag")
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

func TestReleaseWorkflowManualDispatchStrictlyValidatesExistingReleaseCommit(t *testing.T) {
	t.Parallel()

	var workflow struct {
		Jobs map[string]workflowJobConfig `yaml:"jobs"`
	}
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	manualStep := workflowStepByName(t, workflow.Jobs, "prepare-release", "Prepare manual release")
	assertWorkflowStepRunContainsAll(t, manualStep, "manual release preparation step", []string{
		`release_lookup_error="$(mktemp)"`,
		`if ! grep -q "HTTP 404" "${release_lookup_error}"; then`,
		`tag_fetched="false"`,
		`if [[ ! "${existing_target}" =~ ^[0-9a-f]{40}$ ]]; then`,
		`existing_commit="$(gh api "repos/${GITHUB_REPOSITORY}/commits/${existing_target}" --jq '.sha')"`,
		`if [[ ! "${existing_commit}" =~ ^[0-9a-f]{40}$ ]]; then`,
		`if [ "${tag_fetched}" = "false" ] && [ "${existing_commit}" != "${existing_target}" ]; then`,
		`if [ "${existing_commit}" != "${resolved_sha}" ]; then`,
	})
	guardIndex := strings.Index(manualStep.Run, `if [[ ! "${existing_target}" =~ ^[0-9a-f]{40}$ ]]; then`)
	lookupIndex := strings.Index(manualStep.Run, `existing_commit="$(gh api "repos/${GITHUB_REPOSITORY}/commits/${existing_target}" --jq '.sha')"`)
	if guardIndex < 0 || lookupIndex < 0 || guardIndex > lookupIndex {
		t.Fatal("manual release flow must validate target_commitish as a full commit SHA before the commits API lookup")
	}
	if strings.Contains(manualStep.Run, `git rev-parse -q --verify "${existing_target}^{commit}"`) {
		t.Fatal("manual release flow must resolve a release target that is absent from the local checkout")
	}
	if strings.Contains(manualStep.Run, `[ -n "${existing_commit}" ] &&`) {
		t.Fatal("manual release flow must never skip existing release mismatch validation")
	}
}

func TestReleaseWorkflowRejectsBranchValuedMissingTagFallback(t *testing.T) {
	t.Parallel()

	var workflow struct {
		Jobs map[string]workflowJobConfig `yaml:"jobs"`
	}
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	manualStep := workflowStepByName(t, workflow.Jobs, "prepare-release", "Prepare manual release")
	const guardLine = `if [[ ! "${existing_target}" =~ ^[0-9a-f]{40}$ ]]; then`
	guardIndex := strings.Index(manualStep.Run, guardLine)
	if guardIndex < 0 {
		t.Fatal("manual release flow must guard target_commitish with a full-SHA check")
	}

	var guardLines []string
	for _, line := range strings.Split(manualStep.Run[guardIndex:], "\n") {
		guardLines = append(guardLines, line)
		if strings.TrimSpace(line) == "fi" {
			break
		}
	}
	guardScript := strings.Join(guardLines, "\n")
	cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+guardScript+"\nprintf 'guard-bypassed\\n'")
	cmd.Env = append(os.Environ(), "existing_target=main", "tag=v1.2.3")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("branch-valued target_commitish passed the missing-tag guard: %s", output)
	}
	if !strings.Contains(string(output), "Existing release v1.2.3 target_commitish must be a full 40-character hexadecimal commit SHA; got 'main'.") {
		t.Fatalf("branch-valued target_commitish error = %q", output)
	}
	if strings.Contains(string(output), "guard-bypassed") {
		t.Fatalf("branch-valued target_commitish bypassed the guard: %s", output)
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
		`push_url="https://x-access-token:${PUSH_TOKEN}@github.com/${GITHUB_REPOSITORY}.git"`,
		`git tag --force "${major_tag}" "${RELEASE_SHA}"`,
		`git tag --force "${minor_tag}" "${RELEASE_SHA}"`,
		`git push --force "${push_url}" "refs/tags/${major_tag}" "refs/tags/${minor_tag}"`,
	} {
		if !strings.Contains(step.Run, want) {
			t.Fatalf("action floating tag step must contain %q", want)
		}
	}
	if strings.Contains(step.Run, "git remote set-url origin") {
		t.Fatal("action floating tag step must not persist push credentials in .git/config")
	}

	workflowText := readConfig(t, ".github/workflows/release.yml")
	if !strings.Contains(workflowText, "- GitHub Action: \\`${GITHUB_REPOSITORY}@${tag}\\`") {
		t.Fatal("release notes must include the concrete GitHub Action ref")
	}
}

func TestReleaseWorkflowHomebrewPushUsesEphemeralAuthenticatedRemote(t *testing.T) {
	t.Parallel()

	var workflow struct {
		Jobs map[string]workflowJobConfig `yaml:"jobs"`
	}
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	step := workflowStepByName(t, workflow.Jobs, "update-homebrew-tap", "Commit and push formula changes")
	for _, want := range []string{
		`push_url="https://x-access-token:${HOMEBREW_TAP_TOKEN}@github.com/ben-ranford/homebrew-tap.git"`,
		`git fetch origin main:refs/remotes/origin/main`,
		`git rebase origin/main`,
		`git push "${push_url}" HEAD:main`,
	} {
		if !strings.Contains(step.Run, want) {
			t.Fatalf("homebrew push step must contain %q", want)
		}
	}
	if strings.Contains(step.Run, "git remote set-url origin") {
		t.Fatal("homebrew push step must not persist tap credentials in .git/config")
	}
}

func TestReleaseWorkflowStampsFeatureHistoryBeforeInjectingPushToken(t *testing.T) {
	t.Parallel()

	var workflow struct {
		Jobs map[string]workflowJobConfig `yaml:"jobs"`
	}
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	stampStep := workflowStepByName(t, workflow.Jobs, "stamp-feature-release-history", "Stamp first stable release history")
	if stampStep.ID != "stamp_history" {
		t.Fatalf("stamp history step id = %q, want stamp_history", stampStep.ID)
	}
	assertWorkflowStepEnvMissing(t, stampStep, "PUSH_TOKEN", "stamp history step must not expose PUSH_TOKEN to repository-controlled featureflag tooling")
	assertWorkflowStepRunContainsAll(t, stampStep, "stamp history step", []string{
		`"${RUNNER_TEMP}/featureflag" stamp-release --release "${RELEASE_TAG}"`,
		`"${RUNNER_TEMP}/featureflag" validate`,
		`echo "changed=true" >> "$GITHUB_OUTPUT"`,
	})
	if strings.Contains(stampStep.Run, "git push") {
		t.Fatal("stamp history step must not push while running repository-controlled tools")
	}

	commitStep := workflowStepByName(t, workflow.Jobs, "stamp-feature-release-history", "Commit stamped feature release history")
	if commitStep.If != "${{ steps.stamp_history.outputs.changed == 'true' }}" {
		t.Fatalf("commit stamped history step if = %q", commitStep.If)
	}
	if commitStep.Env["RELEASE_TAG"] != "${{ needs.prepare-release.outputs.tag }}" {
		t.Fatalf("commit stamped history step RELEASE_TAG env = %q", commitStep.Env["RELEASE_TAG"])
	}
	if !strings.Contains(commitStep.Run, `git commit -m "chore(flags): stamp ${RELEASE_TAG} feature release history"`) {
		t.Fatal("commit stamped history step must create the release history commit without a push token")
	}
	assertWorkflowStepEnvMissing(t, commitStep, "PUSH_TOKEN", "commit stamped history step must not expose PUSH_TOKEN")

	pushStep := workflowStepByName(t, workflow.Jobs, "stamp-feature-release-history", "Push stamped feature release history")
	if pushStep.If != "${{ steps.stamp_history.outputs.changed == 'true' }}" {
		t.Fatalf("push stamped history step if = %q", pushStep.If)
	}
	if pushStep.Env["PUSH_TOKEN"] != "${{ secrets.MAIN_SYNC_PAT || secrets.GITHUB_TOKEN }}" {
		t.Fatalf("push stamped history step PUSH_TOKEN env = %q", pushStep.Env["PUSH_TOKEN"])
	}
	assertWorkflowStepRunContainsAll(t, pushStep, "push stamped history step", []string{
		`push_url="https://x-access-token:${PUSH_TOKEN}@github.com/${GITHUB_REPOSITORY}.git"`,
		`git fetch origin main:refs/remotes/origin/main`,
		`git rebase origin/main`,
		`git push "${push_url}" HEAD:main`,
	})
	if strings.Contains(pushStep.Run, "git remote set-url origin") {
		t.Fatal("push stamped history step must not persist push credentials in .git/config")
	}
}

func TestReleaseWorkflowBuildsFeatureHistoryPatchWithTrustedMainTooling(t *testing.T) {
	t.Parallel()

	var workflow struct {
		Jobs map[string]workflowJobConfig `yaml:"jobs"`
	}
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	trustedCheckout := workflowStepByName(t, workflow.Jobs, "stamp-feature-release-history", "Checkout trusted main tooling")
	if trustedCheckout.With["ref"] != "main" {
		t.Fatalf("trusted tooling checkout ref = %q, want main", trustedCheckout.With["ref"])
	}
	if trustedCheckout.With["path"] != "" {
		t.Fatalf("trusted tooling checkout path = %q, want repository root", trustedCheckout.With["path"])
	}

	releaseCheckout := workflowStepByName(t, workflow.Jobs, "stamp-feature-release-history", "Checkout validated release data")
	if releaseCheckout.With["ref"] != "${{ needs.prepare-release.outputs.sha }}" {
		t.Fatalf("release data checkout ref = %q", releaseCheckout.With["ref"])
	}
	if releaseCheckout.With["path"] != "release-source" {
		t.Fatalf("release data checkout path = %q, want release-source", releaseCheckout.With["path"])
	}

	validateStep := workflowStepByName(t, workflow.Jobs, "stamp-feature-release-history", "Validate release data against trusted main")
	if validateStep.Env["RELEASE_SHA"] != "${{ needs.prepare-release.outputs.sha }}" {
		t.Fatalf("release data validation RELEASE_SHA env = %q", validateStep.Env["RELEASE_SHA"])
	}
	assertWorkflowStepRunContainsAll(t, validateStep, "release data validation step", []string{
		`release_commit="$(git -C release-source rev-parse HEAD)"`,
		`if [ "${release_commit}" != "${RELEASE_SHA}" ]; then`,
		`if ! git merge-base --is-ancestor "${release_commit}" HEAD; then`,
	})

	buildStep := workflowStepByName(t, workflow.Jobs, "stamp-feature-release-history", "Build trusted feature flag tool")
	if buildStep.WorkingDirectory != "" {
		t.Fatalf("trusted feature flag build working directory = %q, want trusted repository root", buildStep.WorkingDirectory)
	}
	if !strings.Contains(buildStep.Run, `go build -o "${RUNNER_TEMP}/featureflag" ./tools/featureflag`) {
		t.Fatal("feature history job must build featureflag from trusted main")
	}

	stampStep := workflowStepByName(t, workflow.Jobs, "stamp-feature-release-history", "Stamp first stable release history")
	if stampStep.WorkingDirectory != "release-source" {
		t.Fatalf("stamp history working directory = %q, want release-source", stampStep.WorkingDirectory)
	}
	assertWorkflowStepRunContainsAll(t, stampStep, "stamp history step", []string{
		`"${RUNNER_TEMP}/featureflag" stamp-release --release "${RELEASE_TAG}"`,
		`"${RUNNER_TEMP}/featureflag" validate`,
	})
	if strings.Contains(stampStep.Run, "go run ./tools/featureflag") {
		t.Fatal("release-source changes must not control the feature history executable")
	}

	for _, stepName := range []string{"Commit stamped feature release history", "Push stamped feature release history"} {
		step := workflowStepByName(t, workflow.Jobs, "stamp-feature-release-history", stepName)
		if step.WorkingDirectory != "release-source" {
			t.Fatalf("%s working directory = %q, want release-source", stepName, step.WorkingDirectory)
		}
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

func assertWorkflowStepRunContainsAll(t *testing.T, step workflowStepConfig, stepLabel string, wants []string) {
	t.Helper()

	for _, want := range wants {
		if !strings.Contains(step.Run, want) {
			t.Fatalf("%s must contain %q", stepLabel, want)
		}
	}
}

func assertWorkflowStepEnvMissing(t *testing.T, step workflowStepConfig, key string, message string) {
	t.Helper()

	if _, ok := step.Env[key]; ok {
		t.Fatal(message)
	}
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
