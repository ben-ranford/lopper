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
	If          string               `yaml:"if"`
	Env         map[string]string    `yaml:"env"`
	Needs       workflowJobNeeds     `yaml:"needs"`
	Outputs     map[string]string    `yaml:"outputs"`
	Permissions map[string]string    `yaml:"permissions"`
	Steps       []workflowStepConfig `yaml:"steps"`
}

type workflowJobNeeds []string

func (n *workflowJobNeeds) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var need string
		if err := node.Decode(&need); err != nil {
			return err
		}
		*n = []string{need}
		return nil
	}

	var decoded []string
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*n = decoded
	return nil
}

type workflowStepConfig struct {
	Name             string            `yaml:"name"`
	ID               string            `yaml:"id"`
	If               string            `yaml:"if"`
	Uses             string            `yaml:"uses"`
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

func TestReleaseWorkflowPublishesFromFreshValidatedInputs(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	preparation, ok := workflow.Jobs["prepare-release-publication"]
	if !ok {
		t.Fatal("release workflow must prepare bounded publication inputs in a separate job")
	}
	if !slices.Equal(preparation.Needs, workflowJobNeeds{"prepare-release", "orchestrate-release", "build-vscode-extension", "build-darwin-amd64"}) {
		t.Fatalf("release publication preparation needs = %v", preparation.Needs)
	}
	if len(preparation.Permissions) != 1 || preparation.Permissions["contents"] != "read" {
		t.Fatalf("release publication preparation permissions = %#v", preparation.Permissions)
	}
	assertWorkflowJobOmitsText(t, preparation, "GH_TOKEN", "release publication preparation must not receive GH_TOKEN")
	assertWorkflowJobOmitsText(t, preparation, "secrets.", "release publication preparation must not receive secrets")
	assertWorkflowJobCheckoutsDisablePersistedCredentials(t, preparation, "prepare-release-publication")

	stageStep := workflowStepByName(t, workflow.Jobs, "prepare-release-publication", "Stage bounded release publication inputs")
	assertWorkflowStepRunContainsAll(t, stageStep, "release publication staging step", []string{
		`find dist -maxdepth 1 -type f`,
		`lopper-vscode-${version}.vsix`,
		`publication-inputs/feature-flags.md`,
		`mapfile -d '' checksum_files`,
		`sha256sum "${checksum_files[@]}" > SHA256SUMS`,
	})
	uploadStep := workflowStepByName(t, workflow.Jobs, "prepare-release-publication", "Upload release publication inputs")
	if uploadStep.With["name"] != "release-publication-inputs" {
		t.Fatalf("release publication input artifact name = %q", uploadStep.With["name"])
	}
	if uploadStep.With["path"] != "publication-inputs" {
		t.Fatalf("release publication input artifact path = %q", uploadStep.With["path"])
	}
	if uploadStep.With["if-no-files-found"] != "error" {
		t.Fatalf("release publication input artifact missing-file behavior = %q", uploadStep.With["if-no-files-found"])
	}

	publication, ok := workflow.Jobs["publish"]
	if !ok {
		t.Fatal("release workflow must define the fresh publication job")
	}
	if !slices.Equal(publication.Needs, workflowJobNeeds{"prepare-release", "prepare-release-publication", "publish-marketplace"}) {
		t.Fatalf("fresh release publication needs = %v", publication.Needs)
	}
	if len(publication.Permissions) != 1 || publication.Permissions["contents"] != "write" {
		t.Fatalf("fresh release publication permissions = %#v", publication.Permissions)
	}
	if len(publication.Env) != 0 {
		t.Fatalf("fresh release publication job env = %#v, want no job-scoped credentials", publication.Env)
	}

	apiSteps := map[string]bool{
		"Update GitHub Release notes":  false,
		"Upload GitHub Release assets": false,
		"Publish GitHub Release":       false,
	}
	for _, step := range publication.Steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			t.Fatalf("fresh release publication must not checkout repository code: %q", step.Name)
		}
		if _, isAPIStep := apiSteps[step.Name]; isAPIStep {
			apiSteps[step.Name] = true
			if len(step.Env) != 1 || step.Env["GH_TOKEN"] != "${{ secrets.GITHUB_TOKEN }}" {
				t.Fatalf("GitHub API step %q env = %#v", step.Name, step.Env)
			}
		} else if _, ok := step.Env["GH_TOKEN"]; ok {
			t.Fatalf("non-API publication step %q must not receive GH_TOKEN", step.Name)
		}

		for _, repositoryCommand := range []string{"go run ./", "make ", "./extensions/", "scripts/"} {
			if strings.Contains(step.Run, repositoryCommand) {
				t.Fatalf("fresh release publication step %q must not execute repository-controlled command %q", step.Name, repositoryCommand)
			}
		}
	}
	for stepName, found := range apiSteps {
		if !found {
			t.Fatalf("fresh release publication must define GitHub API step %q", stepName)
		}
	}
}

func TestReleaseWorkflowPreparesIntegrityBoundMarketplaceTooling(t *testing.T) {
	t.Parallel()

	var lockfile struct {
		Packages map[string]struct {
			Version   string `json:"version"`
			Integrity string `json:"integrity"`
		} `json:"packages"`
	}
	readJSONConfig(t, "extensions/vscode-lopper/package-lock.json", &lockfile)
	vsce, ok := lockfile.Packages["node_modules/@vscode/vsce"]
	if !ok {
		t.Fatal("VS Code extension lockfile must contain node_modules/@vscode/vsce")
	}
	if vsce.Version != "3.9.2" || !strings.HasPrefix(vsce.Integrity, "sha512-") {
		t.Fatalf("locked Marketplace tool = version %q, integrity %q", vsce.Version, vsce.Integrity)
	}

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	preparation, ok := workflow.Jobs["prepare-marketplace-toolchain"]
	if !ok {
		t.Fatal("release workflow must prepare Marketplace tooling in a separate trusted-main job")
	}
	if !slices.Equal(preparation.Needs, workflowJobNeeds{"prepare-release"}) {
		t.Fatalf("Marketplace tooling preparation needs = %v", preparation.Needs)
	}
	if preparation.If != "${{ needs.prepare-release.outputs.release_created == 'true' }}" {
		t.Fatalf("Marketplace tooling preparation if = %q", preparation.If)
	}
	if len(preparation.Permissions) != 1 || preparation.Permissions["contents"] != "read" {
		t.Fatalf("Marketplace tooling preparation permissions = %#v", preparation.Permissions)
	}
	if len(preparation.Env) != 0 {
		t.Fatalf("Marketplace tooling preparation env = %#v, want no job-scoped credentials", preparation.Env)
	}
	assertWorkflowJobOmitsText(t, preparation, "VSCE_PAT", "Marketplace tooling preparation must not receive VSCE_PAT")
	assertWorkflowJobOmitsText(t, preparation, "secrets.", "Marketplace tooling preparation must not receive secrets")
	assertWorkflowJobCheckoutsDisablePersistedCredentials(t, preparation, "prepare-marketplace-toolchain")

	checkoutStep := workflowStepByName(t, workflow.Jobs, "prepare-marketplace-toolchain", "Checkout trusted main Marketplace manifests")
	if checkoutStep.With["ref"] != "main" || checkoutStep.With["path"] != "" {
		t.Fatalf("trusted Marketplace checkout config = %#v", checkoutStep.With)
	}
	setupStep := workflowStepByName(t, workflow.Jobs, "prepare-marketplace-toolchain", "Setup Node for Marketplace tooling")
	if setupStep.With["node-version"] != "24" {
		t.Fatalf("Marketplace tooling Node version = %q", setupStep.With["node-version"])
	}

	lockStep := workflowStepByName(t, workflow.Jobs, "prepare-marketplace-toolchain", "Validate Marketplace tooling lockfile")
	assertWorkflowStepRunContainsAll(t, lockStep, "Marketplace lockfile validation step", []string{
		`lockfile="extensions/vscode-lopper/package-lock.json"`,
		`vsce_version="$(jq -er '.packages["node_modules/@vscode/vsce"].version' "${lockfile}")"`,
		`vsce_integrity="$(jq -er '.packages["node_modules/@vscode/vsce"].integrity' "${lockfile}")"`,
		`if [ "${vsce_version}" != "3.9.2" ]; then`,
		`case "${vsce_integrity}" in`,
		`sha512-?*)`,
	})

	prepareStep := workflowStepByName(t, workflow.Jobs, "prepare-marketplace-toolchain", "Prepare integrity-bound Marketplace toolchain")
	assertWorkflowStepRunContainsAll(t, prepareStep, "Marketplace tooling preparation step", []string{
		`source_dir="extensions/vscode-lopper"`,
		`scratch_dir="${RUNNER_TEMP}/marketplace-toolchain"`,
		`cp -- "${source_dir}/package.json" "${source_dir}/package-lock.json" "${scratch_dir}/"`,
		`if [ "$(find "${scratch_dir}" -mindepth 1 -maxdepth 1 -type f | wc -l)" -ne 2 ]; then`,
		`npm ci --ignore-scripts --include=dev --audit=false --fund=false`,
		`test -x "${scratch_dir}/node_modules/.bin/vsce"`,
	})
	for _, forbidden := range []string{"npm install", "npm exec", "npx "} {
		if strings.Contains(prepareStep.Run, forbidden) {
			t.Fatalf("Marketplace tooling preparation must not contain %q", forbidden)
		}
	}

	archiveStep := workflowStepByName(t, workflow.Jobs, "prepare-marketplace-toolchain", "Archive Marketplace toolchain")
	assertWorkflowStepRunContainsAll(t, archiveStep, "Marketplace toolchain archive step", []string{
		`archive_dir="${GITHUB_WORKSPACE}/.artifacts/marketplace-toolchain"`,
		`archive_file="${archive_dir}/vsce-toolchain.tar.gz"`,
		`tar --create --gzip --file "${archive_file}" --directory "${scratch_dir}" package.json package-lock.json node_modules`,
		`sha256sum vsce-toolchain.tar.gz > SHA256SUMS`,
	})

	uploadStep := workflowStepByName(t, workflow.Jobs, "prepare-marketplace-toolchain", "Upload Marketplace toolchain")
	if uploadStep.With["name"] != "marketplace-toolchain" ||
		!strings.Contains(uploadStep.With["path"], ".artifacts/marketplace-toolchain/vsce-toolchain.tar.gz") ||
		!strings.Contains(uploadStep.With["path"], ".artifacts/marketplace-toolchain/SHA256SUMS") {
		t.Fatalf("Marketplace toolchain artifact config = %#v", uploadStep.With)
	}
	if uploadStep.With["if-no-files-found"] != "error" {
		t.Fatalf("Marketplace toolchain artifact missing-file behavior = %q", uploadStep.With["if-no-files-found"])
	}
}

func TestReleaseWorkflowPublishesMarketplaceFromValidatedArtifacts(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	marketplace, ok := workflow.Jobs["publish-marketplace"]
	if !ok {
		t.Fatal("release workflow must publish Marketplace inputs from a fresh job")
	}
	if !slices.Equal(marketplace.Needs, workflowJobNeeds{"prepare-release", "prepare-release-publication", "prepare-marketplace-toolchain"}) {
		t.Fatalf("Marketplace publication needs = %v", marketplace.Needs)
	}
	if marketplace.If != "${{ needs.prepare-release.outputs.release_created == 'true' }}" {
		t.Fatalf("Marketplace publication if = %q", marketplace.If)
	}
	if len(marketplace.Permissions) != 0 {
		t.Fatalf("Marketplace publication permissions = %#v, want none", marketplace.Permissions)
	}
	if len(marketplace.Env) != 0 {
		t.Fatalf("Marketplace publication job env = %#v, want no job-scoped credentials", marketplace.Env)
	}
	assertWorkflowEnvKeyOnlyOnStep(t, workflow.Jobs, "VSCE_PAT", "publish-marketplace", "Publish VS Code extension to Marketplace")

	for _, step := range marketplace.Steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			t.Fatalf("Marketplace publication must not checkout repository code: %q", step.Name)
		}
		for _, forbidden := range []string{"npm ", "npx ", "git ", "go run ./", "make ", "scripts/", "./extensions/"} {
			if strings.Contains(strings.ToLower(step.Run), forbidden) {
				t.Fatalf("Marketplace publication step %q must not execute %q", step.Name, forbidden)
			}
		}
	}

	downloadStep := workflowStepByName(t, workflow.Jobs, "publish-marketplace", "Download release publication inputs")
	if downloadStep.With["name"] != "release-publication-inputs" || downloadStep.With["path"] != "publication-inputs" {
		t.Fatalf("Marketplace release input download config = %#v", downloadStep.With)
	}
	toolchainDownload := workflowStepByName(t, workflow.Jobs, "publish-marketplace", "Download Marketplace toolchain")
	if toolchainDownload.With["name"] != "marketplace-toolchain" || toolchainDownload.With["path"] != "marketplace-toolchain-input" {
		t.Fatalf("Marketplace toolchain download config = %#v", toolchainDownload.With)
	}

	validateStep := workflowStepByName(t, workflow.Jobs, "publish-marketplace", "Validate Marketplace publication inputs")
	if validateStep.Shell != "/usr/bin/env -u BASH_ENV -u ENV -u PROMPT_COMMAND -u PS4 -u SHELLOPTS -u BASHOPTS /usr/bin/bash --noprofile --norc -euo pipefail {0}" {
		t.Fatalf("Marketplace input validation shell = %q", validateStep.Shell)
	}
	if len(validateStep.Env) != 1 || validateStep.Env["RELEASE_VERSION"] != "${{ needs.prepare-release.outputs.version }}" {
		t.Fatalf("Marketplace input validation env = %#v", validateStep.Env)
	}
	assertWorkflowStepRunContainsAll(t, validateStep, "Marketplace input validation step", []string{
		`publication_dir="${GITHUB_WORKSPACE}/publication-inputs"`,
		`artifact_dir="${GITHUB_WORKSPACE}/marketplace-toolchain-input"`,
		`archive_file="${artifact_dir}/vsce-toolchain.tar.gz"`,
		`if find "${publication_dir}" -mindepth 1 ! -type f ! -type d -print -quit | grep -q .; then`,
		`if find "${artifact_dir}" -mindepth 1 -maxdepth 1 ! -type f -print -quit | grep -q .; then`,
		`if [ "$(find "${artifact_dir}" -maxdepth 1 -type f | wc -l)" -ne 2 ]; then`,
		`if [ "$(stat --format=%s "${archive_file}")" -gt 268435456 ]; then`,
		`expected_vsix="lopper-vscode-${RELEASE_VERSION}.vsix"`,
		`if [ "${#assets[@]}" -eq 0 ] || [ "${#assets[@]}" -gt 32 ]; then`,
		`if [ "$(stat --format=%s "${asset}")" -gt 1073741824 ]; then`,
		`allowed_roots = {"package.json", "package-lock.json", "node_modules"}`,
		`if member.issym():`,
		`if member.islnk() or not (member.isfile() or member.isdir()):`,
		`if member_count < 3 or member_count > 65536:`,
		`if total_size > 1073741824:`,
		`tar --extract --gzip --file "${archive_file}" --directory "${toolchain_dir}"`,
		`test -x "${toolchain_dir}/node_modules/.bin/vsce"`,
		`node_bin="$(command -v node)"`,
	})
	if count := strings.Count(validateStep.Run, "sha256sum --check --strict SHA256SUMS"); count != 2 {
		t.Fatalf("Marketplace input checksum validations = %d, want publication and toolchain artifacts", count)
	}
	assertWorkflowStepEnvMissing(t, validateStep, "VSCE_PAT", "Marketplace validation must be tokenless")

	publishIndex := workflowStepIndexByName(t, workflow.Jobs, "publish-marketplace", "Publish VS Code extension to Marketplace")
	if publishIndex != len(marketplace.Steps)-1 {
		t.Fatalf("Marketplace credentialed step index = %d, want final step", publishIndex)
	}
	marketplaceStep := marketplace.Steps[publishIndex]
	if len(marketplaceStep.Env) != 1 || marketplaceStep.Env["VSCE_PAT"] != "${{ secrets.VSCE_PUBLISH }}" {
		t.Fatalf("Marketplace publication env = %#v", marketplaceStep.Env)
	}
	if marketplaceStep.Uses != "" {
		t.Fatalf("Marketplace publication must invoke the prepared toolchain directly, found action %q", marketplaceStep.Uses)
	}
	assertWorkflowStepRunContainsAll(t, marketplaceStep, "Marketplace publication step", []string{
		`if [ -z "${VSCE_PAT:-}" ]; then`,
		`vsce_bin="${RUNNER_TEMP}/vsce-toolchain/node_modules/.bin/vsce"`,
		`vsix_path="${GITHUB_WORKSPACE}/publication-inputs/dist/lopper-vscode-${{ needs.prepare-release.outputs.version }}.vsix"`,
		`"${vsce_bin}" publish --packagePath "${vsix_path}"`,
	})
	for _, forbidden := range []string{
		"npx ", "npm ", "find ", "test -x", "sha256sum ", "tar ", "python", "node ",
		"curl ", "wget ", "git ", "go ", "make ", "scripts/", "./extensions/",
	} {
		if strings.Contains(strings.ToLower(marketplaceStep.Run), forbidden) {
			t.Fatalf("Marketplace publication must not execute %q while VSCE_PAT is in scope", forbidden)
		}
	}

	workflowText := readConfig(t, ".github/workflows/release.yml")
	if count := strings.Count(workflowText, "${{ secrets.VSCE_PUBLISH }}"); count != 1 {
		t.Fatalf("Marketplace secret references = %d, want final publish step only", count)
	}
}

func TestReleaseWorkflowPublishesActionFloatingTags(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	job := workflow.Jobs["update-floating-tags"]
	if !slices.Equal(job.Needs, workflowJobNeeds{"prepare-release", "publish"}) {
		t.Fatalf("action floating tag job needs = %v", job.Needs)
	}
	if len(job.Permissions) != 1 || job.Permissions["contents"] != "write" {
		t.Fatalf("action floating tag job permissions = %#v", job.Permissions)
	}
	if len(job.Env) != 0 {
		t.Fatalf("action floating tag job env = %#v, want no job-scoped credentials", job.Env)
	}
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			t.Fatalf("action floating tag job must use a fresh host-Git worktree, found checkout step %q", step.Name)
		}
	}

	prepareStep := workflowStepByName(t, workflow.Jobs, "update-floating-tags", "Prepare GitHub Action floating tags")
	if prepareStep.ID != "prepare_tags" {
		t.Fatalf("action floating tag preparation id = %q", prepareStep.ID)
	}
	if prepareStep.Shell != "/usr/bin/env -u BASH_ENV -u ENV -u PROMPT_COMMAND -u PS4 -u SHELLOPTS -u BASHOPTS /usr/bin/bash --noprofile --norc -euo pipefail {0}" {
		t.Fatalf("action floating tag preparation shell = %q", prepareStep.Shell)
	}
	if len(prepareStep.Env) != 2 || prepareStep.Env["RELEASE_TAG"] != "${{ needs.prepare-release.outputs.tag }}" || prepareStep.Env["RELEASE_SHA"] != "${{ needs.prepare-release.outputs.sha }}" {
		t.Fatalf("action floating tag preparation env = %#v", prepareStep.Env)
	}
	assertWorkflowStepEnvMissing(t, prepareStep, "PUSH_TOKEN", "action floating tag preparation must be tokenless")
	assertWorkflowStepRunContainsAll(t, prepareStep, "action floating tag preparation", []string{
		"git_bin=/usr/bin/git",
		"env_bin=/usr/bin/env",
		`git_home="$("${mktemp_bin}" -d)"`,
		`repo_dir="${RUNNER_TEMP}/floating-tags"`,
		`"${env_bin}" -i`,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		`^v([0-9]+)[.]([0-9]+)[.]([0-9]+)$`,
		`echo "push=false" >> "$GITHUB_OUTPUT"`,
		`if [[ ! "${RELEASE_SHA}" =~ ^[0-9a-f]{40}$ ]]; then`,
		`expected_origin="https://github.com/${GITHUB_REPOSITORY}"`,
		`git_safe init "${repo_dir}"`,
		`git_safe -C "${repo_dir}" remote add origin "${expected_origin}"`,
		`origin_fetch_urls="$(git_safe -C "${repo_dir}" remote get-url --all origin)"`,
		`origin_push_urls="$(git_safe -C "${repo_dir}" remote get-url --push --all origin)"`,
		`if [ "${origin_fetch_urls}" != "${expected_origin}" ] || [ "${origin_push_urls}" != "${expected_origin}" ]; then`,
		`git_safe -C "${repo_dir}" fetch --no-tags --depth=1 origin "${RELEASE_SHA}"`,
		`git_safe -C "${repo_dir}" checkout --detach FETCH_HEAD`,
		`resolved_sha="$(git_safe -C "${repo_dir}" rev-parse HEAD)"`,
		`if [ "${resolved_sha}" != "${RELEASE_SHA}" ]; then`,
		`major_tag="v${BASH_REMATCH[1]}"`,
		`minor_tag="v${BASH_REMATCH[1]}.${BASH_REMATCH[2]}"`,
		`git_safe -C "${repo_dir}" tag --force "${major_tag}" "${RELEASE_SHA}"`,
		`git_safe -C "${repo_dir}" tag --force "${minor_tag}" "${RELEASE_SHA}"`,
		`echo "push=true" >> "$GITHUB_OUTPUT"`,
	})
	for _, forbidden := range []string{"PUSH_TOKEN", "secrets.", "AUTHORIZATION: basic", "extraheader"} {
		if strings.Contains(prepareStep.Run, forbidden) {
			t.Fatalf("action floating tag preparation must not contain %q", forbidden)
		}
	}

	pushStep := workflowStepByName(t, workflow.Jobs, "update-floating-tags", "Push GitHub Action floating tags")
	if pushStep.If != "${{ steps.prepare_tags.outputs.push == 'true' }}" {
		t.Fatalf("action floating tag push if = %q", pushStep.If)
	}
	if pushStep.Shell != prepareStep.Shell {
		t.Fatalf("action floating tag push shell = %q", pushStep.Shell)
	}
	if len(pushStep.Env) != 3 || pushStep.Env["RELEASE_TAG"] != "${{ needs.prepare-release.outputs.tag }}" || pushStep.Env["RELEASE_SHA"] != "${{ needs.prepare-release.outputs.sha }}" || pushStep.Env["PUSH_TOKEN"] != "${{ secrets.GITHUB_TOKEN }}" {
		t.Fatalf("action floating tag push env = %#v", pushStep.Env)
	}
	assertWorkflowStepRunContainsAll(t, pushStep, "action floating tag push", []string{
		"git_bin=/usr/bin/git",
		"env_bin=/usr/bin/env",
		`repo_dir="${RUNNER_TEMP}/floating-tags"`,
		`expected_origin="https://github.com/${GITHUB_REPOSITORY}"`,
		`origin_fetch_urls="$(git_safe -C "${repo_dir}" remote get-url --all origin)"`,
		`origin_push_urls="$(git_safe -C "${repo_dir}" remote get-url --push --all origin)"`,
		`resolved_sha="$(git_safe -C "${repo_dir}" rev-parse HEAD)"`,
		`major_target="$(git_safe -C "${repo_dir}" rev-parse "refs/tags/${major_tag}^{commit}")"`,
		`minor_target="$(git_safe -C "${repo_dir}" rev-parse "refs/tags/${minor_tag}^{commit}")"`,
		`if [ "${resolved_sha}" != "${RELEASE_SHA}" ] || [ "${major_target}" != "${RELEASE_SHA}" ] || [ "${minor_target}" != "${RELEASE_SHA}" ]; then`,
		`push_token="${PUSH_TOKEN}"`,
		"unset PUSH_TOKEN",
		`auth_header="$(printf 'x-access-token:%s' "${push_token}" | "${base64_bin}" | "${tr_bin}" -d '\n')"`,
		"unset push_token",
		"GIT_CONFIG_KEY_1=credential.helper",
		"GIT_CONFIG_VALUE_1=",
		"GIT_CONFIG_KEY_4=http.https://github.com/.extraheader",
		`GIT_CONFIG_VALUE_4="AUTHORIZATION: basic ${auth_header}"`,
		`git_network -C "${repo_dir}" push --force origin "refs/tags/${major_tag}" "refs/tags/${minor_tag}"`,
	})
	validationIndex := strings.Index(pushStep.Run, `if [ "${resolved_sha}" != "${RELEASE_SHA}" ]`)
	tokenIndex := strings.Index(pushStep.Run, `push_token="${PUSH_TOKEN}"`)
	if validationIndex < 0 || tokenIndex < 0 || validationIndex >= tokenIndex {
		t.Fatal("action floating tag push must validate origin, SHA, and tag targets before reading the push token")
	}
	if strings.Count(pushStep.Run, "git_network ") != 1 {
		t.Fatal("action floating tag authentication must be exposed only to the single network push command")
	}
	assertWorkflowStepKeepsGitCredentialsCommandScoped(t, pushStep, "action floating tag push")

	workflowText := readConfig(t, ".github/workflows/release.yml")
	if !strings.Contains(workflowText, "- GitHub Action: \\`${GITHUB_REPOSITORY}@${tag}\\`") {
		t.Fatal("release notes must include the concrete GitHub Action ref")
	}
}

func TestReleaseWorkflowHomebrewUsesGatedThreeJobGraph(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)
	workflowText := readConfig(t, ".github/workflows/release.yml")

	gate := workflow.Jobs["homebrew-tap-token-gate"]
	if !slices.Equal(gate.Needs, workflowJobNeeds{"prepare-release", "publish", "update-floating-tags"}) {
		t.Fatalf("tap token gate needs = %v", gate.Needs)
	}
	if gate.If != "${{ needs.prepare-release.outputs.release_created == 'true' }}" {
		t.Fatalf("tap token gate if = %q", gate.If)
	}
	if gate.Permissions == nil || len(gate.Permissions) != 0 {
		t.Fatalf("tap token gate permissions = %#v, want explicit empty permissions", gate.Permissions)
	}
	if gate.Outputs["configured"] != "${{ steps.gate.outputs.configured }}" {
		t.Fatalf("tap token gate configured output = %q", gate.Outputs["configured"])
	}
	gateStep := workflowStepByName(t, workflow.Jobs, "homebrew-tap-token-gate", "Detect tap token")
	if gateStep.ID != "gate" {
		t.Fatalf("tap token gate step id = %q", gateStep.ID)
	}
	if gateStep.Env["HOMEBREW_TAP_TOKEN"] != "${{ secrets.HOMEBREW_TAP_TOKEN }}" {
		t.Fatalf("tap token gate secret = %q", gateStep.Env["HOMEBREW_TAP_TOKEN"])
	}
	assertWorkflowStepRunContainsAll(t, gateStep, "tap token gate step", []string{
		`if [ -n "${HOMEBREW_TAP_TOKEN:-}" ]; then`,
		`echo "configured=true" >> "$GITHUB_OUTPUT"`,
		`echo "configured=false" >> "$GITHUB_OUTPUT"`,
	})

	validation := workflow.Jobs["validate-homebrew-tap"]
	if !slices.Equal(validation.Needs, workflowJobNeeds{"prepare-release", "publish", "homebrew-tap-token-gate"}) {
		t.Fatalf("tap validation needs = %v", validation.Needs)
	}
	if validation.If != "${{ needs.prepare-release.outputs.release_created == 'true' && needs.homebrew-tap-token-gate.outputs.configured == 'true' }}" {
		t.Fatalf("tap validation if = %q", validation.If)
	}
	if len(validation.Permissions) != 1 || validation.Permissions["contents"] != "read" {
		t.Fatalf("tap validation permissions = %#v", validation.Permissions)
	}

	publication := workflow.Jobs["update-homebrew-tap"]
	if !slices.Equal(publication.Needs, workflowJobNeeds{"prepare-release", "publish", "validate-homebrew-tap"}) {
		t.Fatalf("tap publication needs = %v", publication.Needs)
	}
	if publication.If != "${{ needs.prepare-release.outputs.release_created == 'true' }}" {
		t.Fatalf("tap publication if = %q", publication.If)
	}
	if len(publication.Permissions) != 1 || publication.Permissions["contents"] != "read" {
		t.Fatalf("tap publication permissions = %#v", publication.Permissions)
	}
	if count := strings.Count(workflowText, "${{ secrets.HOMEBREW_TAP_TOKEN }}"); count != 2 {
		t.Fatalf("release workflow tap secret references = %d, want gate and publication only", count)
	}
}

func TestReleaseWorkflowHomebrewValidationIsTokenlessAndImmutable(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)
	validation := workflow.Jobs["validate-homebrew-tap"]
	assertWorkflowJobOmitsText(t, validation, "HOMEBREW_TAP_TOKEN", "tap validation job must not receive the tap token")
	assertWorkflowJobOmitsText(t, validation, "secrets.", "tap validation job must use only public read-only inputs")

	if validation.Env["SOURCE_SHA"] != "${{ needs.prepare-release.outputs.sha }}" {
		t.Fatalf("tap validation SOURCE_SHA = %q", validation.Env["SOURCE_SHA"])
	}
	if validation.Env["FORMULA_VERSION"] != "${{ needs.prepare-release.outputs.version }}" {
		t.Fatalf("tap validation FORMULA_VERSION = %q", validation.Env["FORMULA_VERSION"])
	}
	if validation.Env["SOURCE_REPO"] != "${{ github.repository }}" {
		t.Fatalf("tap validation SOURCE_REPO = %q", validation.Env["SOURCE_REPO"])
	}

	cloneStep := workflowStepByName(t, workflow.Jobs, "validate-homebrew-tap", "Clone tap repository anonymously")
	if cloneStep.Uses != "" {
		t.Fatalf("anonymous tap clone must be a run step, got uses %q", cloneStep.Uses)
	}
	assertWorkflowStepRunContainsAll(t, cloneStep, "anonymous tap clone step", []string{
		"git_bin=/usr/bin/git",
		"mktemp_bin=/usr/bin/mktemp",
		"env -i",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		`-c core.hooksPath=/dev/null`,
		`-c protocol.file.allow=never`,
		`-c protocol.ext.allow=never`,
		`clone --origin origin --branch main --single-branch`,
		`https://github.com/ben-ranford/homebrew-tap.git homebrew-tap`,
	})
	for _, forbidden := range []string{"actions/checkout", "persist-credentials", "credential.helper", "extraheader", "x-access-token:", "AUTHORIZATION: basic"} {
		if strings.Contains(cloneStep.Run, forbidden) {
			t.Fatalf("anonymous tap clone must not contain %q", forbidden)
		}
	}

	regenerateStep := workflowStepByName(t, workflow.Jobs, "validate-homebrew-tap", "Regenerate lopper formula")
	assertWorkflowStepRunContainsAll(t, regenerateStep, "tokenless formula regeneration step", []string{
		`source_url="https://github.com/${SOURCE_REPO}/archive/${SOURCE_SHA}.tar.gz"`,
		`read -r source_sha _ < <(sha256sum /tmp/lopper-source.tar.gz)`,
		`version "${FORMULA_VERSION}"`,
	})
	for _, forbidden := range []string{"archive/refs/tags/", "RELEASE_TAG", "target_commitish"} {
		if strings.Contains(regenerateStep.Run, forbidden) {
			t.Fatalf("tokenless formula regeneration must not contain %q", forbidden)
		}
	}

	validateStep := workflowStepByName(t, workflow.Jobs, "validate-homebrew-tap", "Validate formula")
	assertWorkflowStepRunContainsAll(t, validateStep, "tokenless formula validation step", []string{
		"brew audit --strict --online ben-ranford/tap/lopper",
		"brew install --build-from-source ben-ranford/tap/lopper",
		"brew test ben-ranford/tap/lopper",
	})
}

func TestReleaseWorkflowHomebrewPublicationUsesFreshCredentialScopedClone(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)
	publication := workflow.Jobs["update-homebrew-tap"]
	if publication.Env["SOURCE_SHA"] != "${{ needs.prepare-release.outputs.sha }}" {
		t.Fatalf("tap publication SOURCE_SHA = %q", publication.Env["SOURCE_SHA"])
	}
	if publication.Env["FORMULA_VERSION"] != "${{ needs.prepare-release.outputs.version }}" {
		t.Fatalf("tap publication FORMULA_VERSION = %q", publication.Env["FORMULA_VERSION"])
	}
	if len(publication.Steps) != 1 {
		t.Fatalf("privileged tap publication steps = %d, want one isolated host-tool step", len(publication.Steps))
	}

	step := workflowStepByName(t, workflow.Jobs, "update-homebrew-tap", "Regenerate and push formula changes")
	if len(step.Env) != 1 || step.Env["HOMEBREW_TAP_TOKEN"] != "${{ secrets.HOMEBREW_TAP_TOKEN }}" {
		t.Fatalf("privileged tap step env = %#v", step.Env)
	}
	if step.Shell != "/usr/bin/env -u BASH_ENV -u ENV -u PROMPT_COMMAND -u PS4 -u SHELLOPTS -u BASHOPTS /usr/bin/bash --noprofile --norc -euo pipefail {0}" {
		t.Fatalf("privileged tap step shell = %q", step.Shell)
	}
	assertWorkflowStepRunContainsAll(t, step, "privileged tap publication step", []string{
		"git_bin=/usr/bin/git",
		"curl_bin=/usr/bin/curl",
		"sha256sum_bin=/usr/bin/sha256sum",
		"base64_bin=/usr/bin/base64",
		"tr_bin=/usr/bin/tr",
		"mktemp_bin=/usr/bin/mktemp",
		`git_home="$("${mktemp_bin}" -d)"`,
		`work_root="$("${mktemp_bin}" -d)"`,
		`tap_repo="${work_root}/homebrew-tap"`,
		`git_safe clone --origin origin --branch main --single-branch https://github.com/ben-ranford/homebrew-tap.git "${tap_repo}"`,
		`source_url="https://github.com/${SOURCE_REPO}/archive/${SOURCE_SHA}.tar.gz"`,
		`version "${FORMULA_VERSION}"`,
		`git_safe -C "${tap_repo}" diff --cached --quiet`,
		`git_safe -C "${tap_repo}" commit --no-verify -m "lopper ${RELEASE_TAG}"`,
		`auth_header="$(printf 'x-access-token:%s' "${HOMEBREW_TAP_TOKEN}" | "${base64_bin}" | "${tr_bin}" -d '\n')"`,
		"unset HOMEBREW_TAP_TOKEN",
		"GIT_CONFIG_KEY_4=http.https://github.com/.extraheader",
		`GIT_CONFIG_VALUE_4="AUTHORIZATION: basic ${auth_header}"`,
		`git_network -C "${tap_repo}" fetch origin main:refs/remotes/origin/main`,
		`git_safe -C "${tap_repo}" rebase origin/main`,
		`git_network -C "${tap_repo}" push origin HEAD:main`,
		"Failed to push Homebrew formula after retries",
	})

	commitIndex := strings.Index(step.Run, `git_safe -C "${tap_repo}" commit`)
	authIndex := strings.Index(step.Run, `auth_header="$(printf`)
	fetchIndex := strings.Index(step.Run, `git_network -C "${tap_repo}" fetch`)
	if commitIndex < 0 || authIndex < 0 || fetchIndex < 0 || commitIndex >= authIndex || authIndex >= fetchIndex {
		t.Fatal("tap publication must finish regeneration and commit before constructing command-scoped GitHub authentication")
	}
	for _, forbidden := range []string{
		"brew audit --strict --online",
		"brew install --build-from-source",
		"brew test ben-ranford/tap/",
		"actions/checkout",
		"git remote set-url",
		"push_url=",
		"https://x-access-token:",
		`x-access-token:${HOMEBREW_TAP_TOKEN}@`,
		`-c "http.https://github.com/.extraheader=`,
		"credential.helper store",
	} {
		if strings.Contains(step.Run, forbidden) {
			t.Fatalf("privileged tap publication must not contain %q", forbidden)
		}
	}
}

func assertWorkflowJobOmitsText(t *testing.T, job workflowJobConfig, forbidden string, message string) {
	t.Helper()

	data, err := yaml.Marshal(job)
	if err != nil {
		t.Fatalf("marshal workflow job: %v", err)
	}
	if strings.Contains(string(data), forbidden) {
		t.Fatal(message)
	}
}

func TestReleaseWorkflowTransportsFeatureHistoryPatchAcrossJobs(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	preparation, ok := workflow.Jobs["prepare-feature-release-history"]
	if !ok {
		t.Fatal("release workflow must prepare feature history changes in a separate job")
	}
	if !slices.Equal(preparation.Needs, workflowJobNeeds{"prepare-release", "publish", "update-floating-tags"}) {
		t.Fatalf("feature history preparation needs = %v", preparation.Needs)
	}
	if len(preparation.Permissions) != 1 || preparation.Permissions["contents"] != "read" {
		t.Fatalf("feature history preparation permissions = %#v", preparation.Permissions)
	}
	if preparation.Outputs["changed"] != "${{ steps.stamp_history.outputs.changed }}" {
		t.Fatalf("feature history preparation changed output = %q", preparation.Outputs["changed"])
	}
	assertWorkflowJobOmitsText(t, preparation, "PUSH_TOKEN", "feature history preparation must not receive a push token")
	assertWorkflowJobOmitsText(t, preparation, "secrets.", "feature history preparation must not receive secrets")
	assertWorkflowJobCheckoutsDisablePersistedCredentials(t, preparation, "prepare-feature-release-history")

	trustedCheckout := workflowStepByName(t, workflow.Jobs, "prepare-feature-release-history", "Checkout trusted main tooling")
	if trustedCheckout.With["ref"] != "main" || trustedCheckout.With["path"] != "" {
		t.Fatalf("trusted tooling checkout config = %#v", trustedCheckout.With)
	}
	releaseCheckout := workflowStepByName(t, workflow.Jobs, "prepare-feature-release-history", "Checkout validated release data")
	if releaseCheckout.With["ref"] != "${{ needs.prepare-release.outputs.sha }}" || releaseCheckout.With["path"] != "release-source" {
		t.Fatalf("validated release data checkout config = %#v", releaseCheckout.With)
	}

	validateStep := workflowStepByName(t, workflow.Jobs, "prepare-feature-release-history", "Validate release data against trusted main")
	assertWorkflowStepRunContainsAll(t, validateStep, "release data validation step", []string{
		`release_commit="$(git -C release-source rev-parse HEAD)"`,
		`if [ "${release_commit}" != "${RELEASE_SHA}" ]; then`,
		`if ! git merge-base --is-ancestor "${release_commit}" HEAD; then`,
	})
	buildStep := workflowStepByName(t, workflow.Jobs, "prepare-feature-release-history", "Build trusted feature flag tool")
	if buildStep.WorkingDirectory != "" || !strings.Contains(buildStep.Run, `go build -o "${RUNNER_TEMP}/featureflag" ./tools/featureflag`) {
		t.Fatalf("trusted feature flag build config = %#v", buildStep)
	}

	stampStep := workflowStepByName(t, workflow.Jobs, "prepare-feature-release-history", "Stamp first stable release history")
	if stampStep.ID != "stamp_history" {
		t.Fatalf("stamp history step id = %q, want stamp_history", stampStep.ID)
	}
	if stampStep.WorkingDirectory != "release-source" {
		t.Fatalf("stamp history working directory = %q, want release-source", stampStep.WorkingDirectory)
	}
	assertWorkflowStepEnvMissing(t, stampStep, "PUSH_TOKEN", "stamp history step must not expose PUSH_TOKEN to repository-controlled featureflag tooling")
	assertWorkflowStepRunContainsAll(t, stampStep, "stamp history step", []string{
		`"${RUNNER_TEMP}/featureflag" stamp-release --release "${RELEASE_TAG}"`,
		`"${RUNNER_TEMP}/featureflag" validate`,
		`echo "changed=false" >> "$GITHUB_OUTPUT"`,
		`echo "changed=true" >> "$GITHUB_OUTPUT"`,
	})
	for _, forbidden := range []string{"git commit", "git push", "PUSH_TOKEN"} {
		if strings.Contains(stampStep.Run, forbidden) {
			t.Fatalf("stamp history step must not contain %q", forbidden)
		}
	}

	patchStep := workflowStepByName(t, workflow.Jobs, "prepare-feature-release-history", "Validate and stage feature history patch")
	if patchStep.If != "${{ steps.stamp_history.outputs.changed == 'true' }}" {
		t.Fatalf("feature history patch step if = %q", patchStep.If)
	}
	if patchStep.WorkingDirectory != "release-source" {
		t.Fatalf("feature history patch working directory = %q, want release-source", patchStep.WorkingDirectory)
	}
	assertWorkflowStepRunContainsAll(t, patchStep, "feature history patch step", []string{
		`mapfile -t changed_files < <(git diff --name-only)`,
		`if [ "${#changed_files[@]}" -ne 1 ] || [ "${changed_files[0]}" != "internal/featureflags/features.json" ]; then`,
		`git diff --binary --full-index -- internal/featureflags/features.json > "${patch_file}"`,
		`git apply --check --reverse "${patch_file}"`,
		`sha256sum feature-history.patch > SHA256SUMS`,
	})
	assertWorkflowStepEnvMissing(t, patchStep, "PUSH_TOKEN", "feature history patch staging must be tokenless")

	uploadStep := workflowStepByName(t, workflow.Jobs, "prepare-feature-release-history", "Upload feature history patch")
	if uploadStep.If != "${{ steps.stamp_history.outputs.changed == 'true' }}" {
		t.Fatalf("feature history patch upload if = %q", uploadStep.If)
	}
	if uploadStep.With["name"] != "feature-history-patch" || !strings.Contains(uploadStep.With["path"], ".artifacts/feature-history.patch") || !strings.Contains(uploadStep.With["path"], ".artifacts/SHA256SUMS") {
		t.Fatalf("feature history patch artifact config = %#v", uploadStep.With)
	}
	if uploadStep.With["if-no-files-found"] != "error" {
		t.Fatalf("feature history patch artifact missing-file behavior = %q", uploadStep.With["if-no-files-found"])
	}

	for _, step := range preparation.Steps {
		if strings.Contains(step.Name, "Push") || strings.Contains(step.Run, "git push") {
			t.Fatalf("feature history preparation must not push from release-source: %q", step.Name)
		}
	}

	publication, ok := workflow.Jobs["push-feature-release-history"]
	if !ok {
		t.Fatal("release workflow must push feature history from a separate fresh job")
	}
	if !slices.Equal(publication.Needs, workflowJobNeeds{"prepare-release", "prepare-feature-release-history"}) {
		t.Fatalf("feature history publication needs = %v", publication.Needs)
	}
	if publication.If != "${{ needs.prepare-release.outputs.release_created == 'true' && needs.prepare-feature-release-history.outputs.changed == 'true' }}" {
		t.Fatalf("feature history publication if = %q", publication.If)
	}
	if len(publication.Permissions) != 1 || publication.Permissions["contents"] != "write" {
		t.Fatalf("feature history publication permissions = %#v", publication.Permissions)
	}
	if len(publication.Env) != 0 {
		t.Fatalf("feature history publication job env = %#v, want no job-scoped credentials", publication.Env)
	}
	for _, step := range publication.Steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			t.Fatalf("feature history publication must use a fresh host-Git clone, found checkout step %q", step.Name)
		}
		if step.WorkingDirectory == "release-source" || strings.Contains(step.Run, "release-source") {
			t.Fatalf("feature history publication must not reuse release-source: %q", step.Name)
		}
	}

	downloadStep := workflowStepByName(t, workflow.Jobs, "push-feature-release-history", "Download feature history patch")
	if downloadStep.With["name"] != "feature-history-patch" || downloadStep.With["path"] != "feature-history-input" {
		t.Fatalf("feature history patch download config = %#v", downloadStep.With)
	}
}

func TestReleaseWorkflowPushesFeatureHistoryFromFreshValidatedCommit(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	prepareStep := workflowStepByName(t, workflow.Jobs, "push-feature-release-history", "Prepare trusted feature history commit")
	if prepareStep.Shell != "/usr/bin/env -u BASH_ENV -u ENV -u PROMPT_COMMAND -u PS4 -u SHELLOPTS -u BASHOPTS /usr/bin/bash --noprofile --norc -euo pipefail {0}" {
		t.Fatalf("feature history commit preparation shell = %q", prepareStep.Shell)
	}
	if len(prepareStep.Env) != 2 || prepareStep.Env["RELEASE_TAG"] != "${{ needs.prepare-release.outputs.tag }}" || prepareStep.Env["RELEASE_SHA"] != "${{ needs.prepare-release.outputs.sha }}" {
		t.Fatalf("feature history commit preparation env = %#v", prepareStep.Env)
	}
	assertWorkflowStepEnvMissing(t, prepareStep, "PUSH_TOKEN", "feature history commit preparation must be tokenless")
	assertWorkflowStepRunContainsAll(t, prepareStep, "feature history commit preparation", []string{
		"git_bin=/usr/bin/git",
		"env_bin=/usr/bin/env",
		`repo_dir="${RUNNER_TEMP}/feature-history-push"`,
		`input_dir="${GITHUB_WORKSPACE}/feature-history-input"`,
		`sha256sum --check --strict SHA256SUMS`,
		`if [ "$(find "${input_dir}" -maxdepth 1 -type f | wc -l)" -ne 2 ]; then`,
		`expected_origin="https://github.com/${GITHUB_REPOSITORY}"`,
		`git_safe init "${repo_dir}"`,
		`git_safe -C "${repo_dir}" remote add origin "${expected_origin}"`,
		`git_safe -C "${repo_dir}" fetch --no-tags --depth=1 origin "${RELEASE_SHA}"`,
		`git_safe -C "${repo_dir}" checkout -b feature-history FETCH_HEAD`,
		`git_safe -C "${repo_dir}" apply --index --whitespace=error-all "${patch_file}"`,
		`mapfile -t staged_files < <(git_safe -C "${repo_dir}" diff --cached --name-only)`,
		`if [ "${#staged_files[@]}" -ne 1 ] || [ "${staged_files[0]}" != "internal/featureflags/features.json" ]; then`,
		`expected_subject="chore(flags): stamp ${RELEASE_TAG} feature release history"`,
		`git_safe -C "${repo_dir}" commit -m "${expected_subject}"`,
		`commit_count="$(git_safe -C "${repo_dir}" rev-list --count "${RELEASE_SHA}..HEAD")"`,
		`parent_sha="$(git_safe -C "${repo_dir}" rev-parse HEAD^)"`,
		`commit_subject="$(git_safe -C "${repo_dir}" log -1 --format=%s)"`,
		`mapfile -t committed_files < <(git_safe -C "${repo_dir}" diff-tree --no-commit-id --name-only -r HEAD)`,
		`if [ "${commit_count}" -ne 1 ] || [ "${parent_sha}" != "${RELEASE_SHA}" ] || [ "${commit_subject}" != "${expected_subject}" ]; then`,
		`if [ "${#committed_files[@]}" -ne 1 ] || [ "${committed_files[0]}" != "internal/featureflags/features.json" ]; then`,
	})
	for _, forbidden := range []string{"PUSH_TOKEN", "secrets.", "AUTHORIZATION: basic", "extraheader"} {
		if strings.Contains(prepareStep.Run, forbidden) {
			t.Fatalf("feature history commit preparation must not contain %q", forbidden)
		}
	}

	pushStep := workflowStepByName(t, workflow.Jobs, "push-feature-release-history", "Push feature history commit")
	if pushStep.Shell != prepareStep.Shell {
		t.Fatalf("feature history push shell = %q", pushStep.Shell)
	}
	if len(pushStep.Env) != 3 || pushStep.Env["RELEASE_TAG"] != "${{ needs.prepare-release.outputs.tag }}" || pushStep.Env["RELEASE_SHA"] != "${{ needs.prepare-release.outputs.sha }}" || pushStep.Env["PUSH_TOKEN"] != "${{ secrets.MAIN_SYNC_PAT || secrets.GITHUB_TOKEN }}" {
		t.Fatalf("feature history push env = %#v", pushStep.Env)
	}
	assertWorkflowStepRunContainsAll(t, pushStep, "feature history push", []string{
		"git_bin=/usr/bin/git",
		"env_bin=/usr/bin/env",
		`git_home="$("${mktemp_bin}" -d)"`,
		`repo_dir="${RUNNER_TEMP}/feature-history-push"`,
		`"${env_bin}" -i`,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		`expected_origin="https://github.com/${GITHUB_REPOSITORY}"`,
		`origin_fetch_urls="$(git_safe -C "${repo_dir}" remote get-url --all origin)"`,
		`origin_push_urls="$(git_safe -C "${repo_dir}" remote get-url --push --all origin)"`,
		`prepared_commit_count="$(git_safe -C "${repo_dir}" rev-list --count "${RELEASE_SHA}..HEAD")"`,
		`prepared_parent="$(git_safe -C "${repo_dir}" rev-parse HEAD^)"`,
		`mapfile -t prepared_files < <(git_safe -C "${repo_dir}" diff-tree --no-commit-id --name-only -r HEAD)`,
		`if [ "${prepared_commit_count}" -ne 1 ] || [ "${prepared_parent}" != "${RELEASE_SHA}" ] || [ "${prepared_subject}" != "${expected_subject}" ]; then`,
		`if [ "${#prepared_files[@]}" -ne 1 ] || [ "${prepared_files[0]}" != "internal/featureflags/features.json" ]; then`,
		`push_token="${PUSH_TOKEN}"`,
		"unset PUSH_TOKEN",
		`auth_header="$(printf 'x-access-token:%s' "${push_token}" | "${base64_bin}" | "${tr_bin}" -d '\n')"`,
		"unset push_token",
		"GIT_CONFIG_KEY_1=credential.helper",
		"GIT_CONFIG_VALUE_1=",
		"GIT_CONFIG_KEY_4=http.https://github.com/.extraheader",
		`GIT_CONFIG_VALUE_4="AUTHORIZATION: basic ${auth_header}"`,
		`for attempt in 1 2 3; do`,
		`git_network -C "${repo_dir}" fetch origin main:refs/remotes/origin/main`,
		`git_safe -C "${repo_dir}" diff --quiet origin/main HEAD -- internal/featureflags/features.json`,
		`echo "No feature release history changes to push"`,
		`git_safe -C "${repo_dir}" rebase origin/main`,
		`ahead_count="$(git_safe -C "${repo_dir}" rev-list --count origin/main..HEAD)"`,
		`mapfile -t pushed_files < <(git_safe -C "${repo_dir}" diff-tree --no-commit-id --name-only -r HEAD)`,
		`if [ "${ahead_count}" -ne 1 ] || [ "${push_parent}" != "${main_sha}" ] || [ "${push_subject}" != "${expected_subject}" ]; then`,
		`if [ "${#pushed_files[@]}" -ne 1 ] || [ "${pushed_files[0]}" != "internal/featureflags/features.json" ]; then`,
		`git_network -C "${repo_dir}" push origin HEAD:main`,
		"Failed to push feature release history after retries",
	})
	validationIndex := strings.Index(pushStep.Run, `if [ "${prepared_commit_count}" -ne 1 ]`)
	tokenIndex := strings.Index(pushStep.Run, `push_token="${PUSH_TOKEN}"`)
	if validationIndex < 0 || tokenIndex < 0 || validationIndex >= tokenIndex {
		t.Fatal("feature history publication must validate the exact commit and file set before reading the push token")
	}
	if strings.Count(pushStep.Run, "git_network ") != 2 {
		t.Fatal("feature history authentication must be exposed only to fetch and push network commands")
	}
	assertWorkflowStepKeepsGitCredentialsCommandScoped(t, pushStep, "feature history push")
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

	return jobs[jobName].Steps[workflowStepIndexByName(t, jobs, jobName, stepName)]
}

func workflowStepIndexByName(t *testing.T, jobs map[string]workflowJobConfig, jobName string, stepName string) int {
	t.Helper()

	job, ok := jobs[jobName]
	if !ok {
		t.Fatalf("workflow must define job %s", jobName)
	}
	for index, step := range job.Steps {
		if step.Name == stepName {
			return index
		}
	}
	t.Fatalf("%s must define step %q", jobName, stepName)
	return -1
}

func assertWorkflowEnvKeyOnlyOnStep(t *testing.T, jobs map[string]workflowJobConfig, key string, wantJob string, wantStep string) {
	t.Helper()

	credentialedSteps := 0
	for jobName, job := range jobs {
		if _, present := job.Env[key]; present {
			t.Fatalf("%s must not be scoped to job %q", key, jobName)
		}
		for _, step := range job.Steps {
			if _, present := step.Env[key]; !present {
				continue
			}
			credentialedSteps++
			if jobName != wantJob || step.Name != wantStep {
				t.Fatalf("%s is exposed to unexpected step %q in job %q", key, step.Name, jobName)
			}
		}
	}
	if credentialedSteps != 1 {
		t.Fatalf("%s-bearing steps = %d, want exactly one", key, credentialedSteps)
	}
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

func assertWorkflowStepKeepsGitCredentialsCommandScoped(t *testing.T, step workflowStepConfig, stepLabel string) {
	t.Helper()

	for _, forbidden := range []string{
		"push_url=",
		"https://x-access-token:",
		"@github.com",
		"git remote set-url",
		"credential.helper store",
		"persist-credentials: true",
	} {
		if strings.Contains(step.Run, forbidden) {
			t.Fatalf("%s must not contain %q", stepLabel, forbidden)
		}
	}

	for _, line := range strings.Split(step.Run, "\n") {
		trimmed := strings.TrimSpace(line)
		isGitCommand := strings.HasPrefix(trimmed, "git ") ||
			strings.Contains(trimmed, " git ") ||
			strings.Contains(line, `"${git_bin}"`) ||
			strings.Contains(line, "git_safe ") ||
			strings.Contains(line, "git_network ")
		if isGitCommand && (strings.Contains(line, "PUSH_TOKEN") ||
			strings.Contains(line, "push_token") ||
			strings.Contains(line, "auth_header")) {
			t.Fatalf("%s must not place secret text in git argv: %q", stepLabel, trimmed)
		}
	}
}

func assertWorkflowJobCheckoutsDisablePersistedCredentials(t *testing.T, job workflowJobConfig, jobLabel string) {
	t.Helper()

	checkoutCount := 0
	for _, step := range job.Steps {
		if !strings.HasPrefix(step.Uses, "actions/checkout@") {
			continue
		}
		checkoutCount++
		if step.With["persist-credentials"] != "false" {
			t.Fatalf("%s checkout %q must set persist-credentials: false", jobLabel, step.Name)
		}
	}
	if checkoutCount == 0 {
		t.Fatalf("%s must contain a checkout step", jobLabel)
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
