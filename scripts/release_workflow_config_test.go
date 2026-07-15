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
	If          string               `yaml:"if"`
	Env         map[string]string    `yaml:"env"`
	Needs       workflowNeeds        `yaml:"needs"`
	Outputs     map[string]string    `yaml:"outputs"`
	Permissions map[string]string    `yaml:"permissions"`
	Steps       []workflowStepConfig `yaml:"steps"`
}

type workflowNeeds []string

func (n *workflowNeeds) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var need string
		if err := node.Decode(&need); err != nil {
			return err
		}
		*n = workflowNeeds{need}
		return nil
	}
	return node.Decode((*[]string)(n))
}

type workflowStepConfig struct {
	Name             string            `yaml:"name"`
	ID               string            `yaml:"id"`
	If               string            `yaml:"if"`
	Run              string            `yaml:"run"`
	Shell            string            `yaml:"shell"`
	WorkingDirectory string            `yaml:"working-directory"`
	Env              map[string]string `yaml:"env"`
	Uses             string            `yaml:"uses"`
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
	if _, ok := workflow.On.WorkflowDispatch.Inputs["source_sha"]; ok {
		t.Fatal("release workflow must not expose a source_sha manual dispatch input")
	}

	workflowText := readConfig(t, ".github/workflows/release.yml")
	if strings.Contains(workflowText, "inputs.source_sha || inputs.tag") {
		t.Fatal("manual release jobs must not fall back to tag names for source checkout")
	}
	if strings.Contains(workflowText, "gh release create \"${tag}\"") {
		t.Fatal("manual release flow must not create a new GitHub release during a retry")
	}
	if !strings.Contains(workflowText, "Manual release promotion can only retry an existing GitHub release for ${tag}.") {
		t.Fatal("manual release flow must require an existing GitHub release to retry")
	}
	if !strings.Contains(workflowText, `resolved_sha="$(git rev-list -n 1 "refs/tags/${tag}")"`) {
		t.Fatal("manual release flow must resolve the source SHA from the requested release tag")
	}
	if !strings.Contains(workflowText, "GitHub release ${tag} targets ${release_target_sha}, but tag ${tag} points to ${resolved_sha}.") {
		t.Fatal("manual release flow must fail when release metadata disagrees with the tag target")
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

func TestReleaseWorkflowManualDispatchTreatsBranchTargetsAsMutableRetryMetadata(t *testing.T) {
	t.Parallel()

	workflowText := readConfig(t, ".github/workflows/release.yml")

	for _, want := range []string{
		`if [[ "${release_target}" == refs/heads/* || "${release_target}" == refs/remotes/* ]]; then`,
		`elif git show-ref --verify --quiet "refs/heads/${release_target}" || git show-ref --verify --quiet "refs/remotes/origin/${release_target}"; then`,
		`echo "GitHub release ${tag} target_commitish ${release_target} is branch-valued; using tag ${tag} as the immutable retry source."`,
		`elif [ "${release_target_kind}" = "immutable" ]; then`,
	} {
		if !strings.Contains(workflowText, want) {
			t.Fatalf("manual release flow must classify branch-valued target_commitish metadata: missing %q", want)
		}
	}
}

func TestReleaseWorkflowManualDispatchValidatesTagBeforeFetch(t *testing.T) {
	t.Parallel()

	workflowText := readConfig(t, ".github/workflows/release.yml")
	validation := `git check-ref-format --normalize "refs/tags/${tag}" >/dev/null 2>&1`
	fetch := `git fetch --force origin "refs/tags/${tag}:refs/tags/${tag}"`

	validationIndex := strings.Index(workflowText, validation)
	if validationIndex == -1 {
		t.Fatal("manual release flow must validate the user-supplied tag before using it as a ref")
	}

	fetchIndex := strings.Index(workflowText, fetch)
	if fetchIndex == -1 {
		t.Fatal("manual release flow must fetch the resolved release tag")
	}
	if validationIndex > fetchIndex {
		t.Fatal("manual release flow must validate the user-supplied tag before fetching it")
	}
}

func TestReleaseWorkflowManualDispatchFallsBackToDraftReleaseLookup(t *testing.T) {
	t.Parallel()

	workflowText := readConfig(t, ".github/workflows/release.yml")
	if !strings.Contains(workflowText, `gh api --paginate --slurp "repos/${GITHUB_REPOSITORY}/releases"`) {
		t.Fatal("manual release flow must fall back to listing releases when the tag lookup misses a draft release")
	}
	if !strings.Contains(workflowText, `jq -c --arg tag "$tag" '.[].[] | select(.tag_name == $tag)'`) {
		t.Fatal("manual release flow must filter the release list by the requested tag")
	}
	if strings.Contains(workflowText, `head -n1 >"${release_json}" || true`) {
		t.Fatal("manual release draft lookup must not mask API or JSON failures as a missing release")
	}
}

func TestReleaseWorkflowMetadataCheckoutDoesNotPersistWriteToken(t *testing.T) {
	t.Parallel()

	var workflow struct {
		Jobs map[string]workflowJobConfig `yaml:"jobs"`
	}
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	step := workflowStepByName(t, workflow.Jobs, "prepare-release", "Checkout release metadata")
	if !strings.HasPrefix(step.Uses, "actions/checkout@") {
		t.Fatalf("release metadata checkout uses %q, want actions/checkout", step.Uses)
	}
	if step.With["token"] == "" {
		t.Fatal("release metadata checkout must explicitly receive its token")
	}
	if step.With["persist-credentials"] != "false" {
		t.Fatal("release metadata checkout must set persist-credentials: false so its write token is not stored in local git config")
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

func TestReleaseWorkflowPublishesMarketplaceFromTrustedIsolatedToolchain(t *testing.T) {
	t.Parallel()

	var workflow struct {
		Jobs map[string]workflowJobConfig `yaml:"jobs"`
	}
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	workflowText := readConfig(t, ".github/workflows/release.yml")
	if !strings.Contains(workflowText, "trusted_main_sha") {
		t.Fatal("release workflow must resolve and expose a trusted main revision for Marketplace manifests")
	}

	prepareJob := workflowJobRequired(t, workflow.Jobs, "prepare-marketplace-toolchain")
	if !slices.Equal(prepareJob.Needs, workflowNeeds{"prepare-release"}) {
		t.Fatalf("prepare-marketplace-toolchain needs = %#v, want only prepare-release", prepareJob.Needs)
	}
	if len(prepareJob.Env) != 0 {
		t.Fatalf("prepare-marketplace-toolchain env = %#v, want no job-scoped credentials", prepareJob.Env)
	}
	checkout := workflowStepByName(t, workflow.Jobs, "prepare-marketplace-toolchain", "Checkout trusted main Marketplace manifests")
	if checkout.With["ref"] != "${{ needs.prepare-release.outputs.trusted_main_sha }}" {
		t.Fatalf("trusted Marketplace manifest checkout ref = %q", checkout.With["ref"])
	}
	if checkout.With["persist-credentials"] != "false" {
		t.Fatalf("trusted Marketplace manifest checkout persist-credentials = %q, want false", checkout.With["persist-credentials"])
	}
	lockfileValidation := workflowStepByName(t, workflow.Jobs, "prepare-marketplace-toolchain", "Validate Marketplace tooling lockfile")
	if !strings.Contains(lockfileValidation.Run, `jq -er '.packages["node_modules/@vscode/vsce"].version'`) {
		t.Fatal("trusted Marketplace toolchain prep must validate the pinned @vscode/vsce lockfile version")
	}
	prepareToolchain := workflowStepByName(t, workflow.Jobs, "prepare-marketplace-toolchain", "Prepare integrity-bound Marketplace toolchain")
	if !strings.Contains(prepareToolchain.Run, "npm ci --ignore-scripts --include=dev --audit=false --fund=false") {
		t.Fatal("trusted Marketplace toolchain prep must install from the pinned lockfile without running package scripts")
	}
	uploadToolchain := workflowStepByName(t, workflow.Jobs, "prepare-marketplace-toolchain", "Upload Marketplace toolchain")
	if uploadToolchain.With["name"] != "marketplace-toolchain" {
		t.Fatalf("Marketplace toolchain artifact name = %q, want marketplace-toolchain", uploadToolchain.With["name"])
	}

	if strings.Contains(workflowText, "make vscode-extension-install") && workflowStepByName(t, workflow.Jobs, "publish", "Upload GitHub Release assets").Name != "" {
		if marketplaceStep, ok := workflowStepByNameIfPresent(workflow.Jobs, "publish", "Publish VS Code extension to Marketplace"); ok {
			if strings.Contains(marketplaceStep.Run, "make vscode-extension-install") {
				t.Fatal("publish job must not run repo-controlled Marketplace install steps")
			}
		}
	}
	if _, ok := workflowStepByNameIfPresent(workflow.Jobs, "publish", "Publish VS Code extension to Marketplace"); ok {
		t.Fatal("publish job must not publish to the VS Code Marketplace directly")
	}

	marketplaceJob := workflowJobRequired(t, workflow.Jobs, "publish-vscode-marketplace")
	if !slices.Equal(marketplaceJob.Needs, workflowNeeds{"prepare-release", "publish", "build-vscode-extension", "prepare-marketplace-toolchain"}) {
		t.Fatalf("publish-vscode-marketplace needs = %#v", marketplaceJob.Needs)
	}
	if len(marketplaceJob.Permissions) != 0 {
		t.Fatalf("publish-vscode-marketplace permissions = %#v, want permissions-empty job", marketplaceJob.Permissions)
	}
	for _, step := range marketplaceJob.Steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			t.Fatalf("publish-vscode-marketplace must not checkout repository content in step %q", step.Name)
		}
	}
	validateInputs := workflowStepByName(t, workflow.Jobs, "publish-vscode-marketplace", "Validate Marketplace publication inputs")
	if validateInputs.Env["VSIX_DIR"] != "${{ runner.temp }}/vscode-marketplace" {
		t.Fatalf("marketplace validation VSIX_DIR = %q", validateInputs.Env["VSIX_DIR"])
	}
	if validateInputs.Env["TOOLCHAIN_ARTIFACT_DIR"] != "${{ runner.temp }}/marketplace-toolchain-artifact" {
		t.Fatalf("marketplace validation TOOLCHAIN_ARTIFACT_DIR = %q", validateInputs.Env["TOOLCHAIN_ARTIFACT_DIR"])
	}
	for _, want := range []string{
		`sha256sum --check --strict SHA256SUMS`,
		`expected_vsix="${VSIX_DIR}/lopper-vscode-${RELEASE_VERSION}.vsix"`,
		`vsce-toolchain.tar.gz`,
	} {
		if !strings.Contains(validateInputs.Run, want) {
			t.Fatalf("marketplace validation must contain %q", want)
		}
	}
	publishMarketplace := workflowStepByName(t, workflow.Jobs, "publish-vscode-marketplace", "Publish VS Code extension to Marketplace")
	if publishMarketplace.Env["VSCE_PAT"] != "${{ secrets.VSCE_PUBLISH }}" {
		t.Fatalf("marketplace publish VSCE_PAT env = %q", publishMarketplace.Env["VSCE_PAT"])
	}
	if !strings.Contains(publishMarketplace.Run, `"${VSCE_BIN}" publish --packagePath "${VSIX_PATH}"`) {
		t.Fatal("marketplace publish step must publish only the validated VSIX with the trusted VSCE binary")
	}
	if workflowStepIndexByName(t, marketplaceJob, "Validate Marketplace publication inputs") > workflowStepIndexByName(t, marketplaceJob, "Publish VS Code extension to Marketplace") {
		t.Fatal("marketplace publication inputs must be validated before VSCE_PAT is exposed")
	}
	if workflowStepIndexByName(t, marketplaceJob, "Publish VS Code extension to Marketplace") != len(marketplaceJob.Steps)-1 {
		t.Fatal("marketplace publish step must be the terminal step in the isolated Marketplace job")
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

func workflowJobRequired(t *testing.T, jobs map[string]workflowJobConfig, jobName string) workflowJobConfig {
	t.Helper()

	job, ok := jobs[jobName]
	if !ok {
		t.Fatalf("workflow must define job %s", jobName)
	}
	return job
}

func workflowStepByNameIfPresent(jobs map[string]workflowJobConfig, jobName string, stepName string) (workflowStepConfig, bool) {
	job, ok := jobs[jobName]
	if !ok {
		return workflowStepConfig{}, false
	}
	for _, step := range job.Steps {
		if step.Name == stepName {
			return step, true
		}
	}
	return workflowStepConfig{}, false
}

func workflowStepIndexByName(t *testing.T, job workflowJobConfig, stepName string) int {
	t.Helper()

	for index, step := range job.Steps {
		if step.Name == stepName {
			return index
		}
	}
	t.Fatalf("workflow job must define step %q", stepName)
	return -1
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
