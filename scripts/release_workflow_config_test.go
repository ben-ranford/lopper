package scripts

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var (
	workflowSecretsContextPattern          = regexp.MustCompile(`(?:^|[^a-z0-9_])secrets(?:$|[^a-z0-9_])`)
	workflowGitHubCredentialContextPattern = regexp.MustCompile(`(?:^|[^a-z0-9_])github(?:\.token|\['token'\]|\.\*|\['\*'\])(?:$|[^a-z0-9_])`)
	workflowBareGitHubContextPattern       = regexp.MustCompile(`(?:^|[^a-z0-9_])github(?:$|[^a-z0-9_.\[])`)
)

const workflowImplicitGitHubToken = "<implicit github.token>"

type workflowCredentialRole uint8

const (
	workflowCredentialGenericRole workflowCredentialRole = iota
	workflowCredentialRootRole
	workflowCredentialJobsRole
	workflowCredentialJobRole
	workflowCredentialStepsRole
	workflowCredentialStepRole
	workflowCredentialExpressionRole
	workflowCredentialInheritedSecretsRole
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
	ContinueOnError bool                 `yaml:"continue-on-error"`
	If              string               `yaml:"if"`
	Env             map[string]string    `yaml:"env"`
	Needs           workflowJobNeeds     `yaml:"needs"`
	Outputs         map[string]string    `yaml:"outputs"`
	Permissions     map[string]string    `yaml:"permissions"`
	RunsOn          string               `yaml:"runs-on"`
	Steps           []workflowStepConfig `yaml:"steps"`
	Strategy        workflowStrategy     `yaml:"strategy"`
}

type workflowStrategy struct {
	FailFast *bool          `yaml:"fail-fast"`
	Matrix   workflowMatrix `yaml:"matrix"`
}

type workflowMatrix struct {
	Include []workflowMatrixEntry `yaml:"include"`
}

type workflowMatrixEntry struct {
	Architecture string `yaml:"architecture"`
	Platform     string `yaml:"platform"`
	Runner       string `yaml:"runner"`
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

func TestReleaseWorkflowPinsTrustedMainToWorkflowRevision(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	preparation := workflowJobByName(t, workflow.Jobs, "prepare-release")
	if preparation.Outputs["trusted_main_sha"] != "${{ steps.trusted_main.outputs.trusted_main_sha }}" {
		t.Fatalf("prepare-release trusted_main_sha output = %q", preparation.Outputs["trusted_main_sha"])
	}
	if len(preparation.Steps) == 0 || preparation.Steps[0].Name != "Resolve trusted main workflow revision" {
		t.Fatal("trusted main workflow revision must be resolved before release side effects")
	}
	resolver := preparation.Steps[0]
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "trusted main resolver id", got: resolver.ID, want: "trusted_main"},
		{label: "trusted main resolver shell", got: resolver.Shell, want: "/usr/bin/env -u BASH_ENV -u ENV -u PROMPT_COMMAND -u PS4 -u SHELLOPTS -u BASHOPTS /usr/bin/bash --noprofile --norc -euo pipefail {0}"},
		{label: "trusted main resolver action", got: resolver.Uses, want: ""},
		{label: "trusted main resolver condition", got: resolver.If, want: ""},
		{label: "trusted main resolver working directory", got: resolver.WorkingDirectory, want: ""},
	})
	assertWorkflowStepEnv(t, resolver, "trusted main workflow resolver", map[string]string{
		"PATH":         "/usr/bin:/bin",
		"WORKFLOW_REF": "${{ github.workflow_ref }}",
		"WORKFLOW_SHA": "${{ github.workflow_sha }}",
	})
	assertWorkflowStepRunContainsAll(t, resolver, "trusted main workflow resolver", []string{
		`expected_workflow_ref="${GITHUB_REPOSITORY}/.github/workflows/release.yml@refs/heads/main"`,
		`if [ "${WORKFLOW_REF}" != "${expected_workflow_ref}" ]; then`,
		`echo "::error::Release workflow must come from ${expected_workflow_ref}; got ${WORKFLOW_REF}." >&2`,
		`if [[ ! "${WORKFLOW_SHA}" =~ ^[0-9a-f]{40}$ ]]; then`,
		`echo "::error::Release workflow SHA must be a lowercase full 40-character commit SHA; got ${WORKFLOW_SHA}." >&2`,
		`/usr/bin/printf 'trusted_main_sha=%s\n' "${WORKFLOW_SHA}" >> "$GITHUB_OUTPUT"`,
	})
	if strings.Count(resolver.Run, "exit 1") != 2 {
		t.Fatal("trusted main workflow resolver must fail closed on ref or SHA mismatch")
	}
	if strings.Count(resolver.Run, `>> "$GITHUB_OUTPUT"`) != 1 {
		t.Fatal("trusted main workflow resolver must output only the validated workflow SHA")
	}
	assertTextAppearsBefore(t, resolver.Run, `if [ "${WORKFLOW_REF}" != "${expected_workflow_ref}" ]; then`, `/usr/bin/printf 'trusted_main_sha=%s\n' "${WORKFLOW_SHA}" >> "$GITHUB_OUTPUT"`, "trusted main workflow ref must be validated before output")
	assertTextAppearsBefore(t, resolver.Run, `if [[ ! "${WORKFLOW_SHA}" =~ ^[0-9a-f]{40}$ ]]; then`, `/usr/bin/printf 'trusted_main_sha=%s\n' "${WORKFLOW_SHA}" >> "$GITHUB_OUTPUT"`, "trusted main workflow SHA must be validated before output")
	assertWorkflowStepRunOmitsAll(t, resolver, "trusted main workflow resolver", []string{"git ", "gh ", "curl ", "wget ", "secrets.", "token"})
	assertWorkflowStepOrder(t, preparation, "Resolve trusted main workflow revision", "Run release-please", "Checkout release metadata", "Prepare manual release")

	wantTrustedMain := "${{ needs.prepare-release.outputs.trusted_main_sha }}"
	marketplaceCheckout := workflowStepByName(t, workflow.Jobs, "prepare-marketplace-toolchain", "Checkout trusted main Marketplace manifests")
	featureHistoryCheckout := workflowStepByName(t, workflow.Jobs, "prepare-feature-release-history", "Checkout trusted main tooling")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "trusted Marketplace checkout ref", got: marketplaceCheckout.With["ref"], want: wantTrustedMain},
		{label: "trusted feature history checkout ref", got: featureHistoryCheckout.With["ref"], want: wantTrustedMain},
	})
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
	if !strings.Contains(workflowText, `resolved_sha="${existing_commit}"`) {
		t.Fatal("manual release flow must use the API-verified release tag commit as its source SHA")
	}
	if strings.Contains(workflowText, `resolved_sha="$(git rev-list -n 1 "refs/tags/${tag}")"`) {
		t.Fatal("manual release flow must not resolve its source through an unauthenticated tag fetch")
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

func TestReleaseWorkflowManualDispatchValidatesTagBeforeLookup(t *testing.T) {
	t.Parallel()

	workflowText := readConfig(t, ".github/workflows/release.yml")
	validation := `git check-ref-format --normalize "refs/tags/${tag}" >/dev/null 2>&1`
	lookup := `encoded_tag="$(python3 -c 'import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=""))' "$tag")"`

	validationIndex := strings.Index(workflowText, validation)
	if validationIndex == -1 {
		t.Fatal("manual release flow must validate the user-supplied tag before using it as a ref")
	}

	lookupIndex := strings.Index(workflowText, lookup)
	if lookupIndex == -1 {
		t.Fatal("manual release flow must encode the validated release tag")
	}
	if validationIndex > lookupIndex {
		t.Fatal("manual release flow must validate the user-supplied tag before looking it up")
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
	if strings.Contains(workflowText, `head -n1 >"${release_metadata_file}" || true`) {
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
	if step.With["token"] != "${{ secrets.GITHUB_TOKEN }}" {
		t.Fatalf("release metadata checkout token = %q, want GITHUB_TOKEN only", step.With["token"])
	}
	if step.With["persist-credentials"] != "false" {
		t.Fatal("release metadata checkout must set persist-credentials: false so its write token is not stored in local git config")
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
		`if grep -q "HTTP 404" "${release_lookup_error}"; then`,
		`if ! gh api "repos/${GITHUB_REPOSITORY}/git/ref/tags/${encoded_tag}" >"${release_ref_json}" 2>/dev/null; then`,
		`existing_ref_type="$(jq -r '.object.type // empty' "${release_ref_json}")"`,
		`case "${existing_ref_type}" in`,
		`if ! gh api "repos/${GITHUB_REPOSITORY}/git/tags/${existing_ref_sha}" >"${annotated_tag_json}" 2>/dev/null; then`,
		`if [ -z "${existing_commit}" ]; then`,
		`resolved_sha="${existing_commit}"`,
	})
	for _, forbidden := range []string{"target_commitish", `git fetch --force origin "refs/tags/${tag}:refs/tags/${tag}"`, `resolved_sha="$(git rev-parse HEAD)"`, `[ -n "${existing_commit}" ] &&`} {
		if strings.Contains(manualStep.Run, forbidden) {
			t.Fatalf("manual release flow must not contain stale validation path %q", forbidden)
		}
	}
}

func TestReleaseWorkflowManualReleaseRequiresExistingReleaseAfterExplicit404(t *testing.T) {
	t.Parallel()

	var workflow struct {
		Jobs map[string]workflowJobConfig `yaml:"jobs"`
	}
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	manualStep := workflowStepByName(t, workflow.Jobs, "prepare-release", "Prepare manual release")
	assertWorkflowStepRunContainsAll(t, manualStep, "manual release lookup failure contract", []string{
		`release_metadata_file="$(mktemp)"`,
		`release_lookup_error="$(mktemp)"`,
		`gh api "repos/${GITHUB_REPOSITORY}/releases/tags/${encoded_tag}" >"${release_metadata_file}" 2>"${release_lookup_error}"`,
		`release_lookup_status=$?`,
		`if grep -q "HTTP 404" "${release_lookup_error}"; then`,
		`gh api --paginate --slurp "repos/${GITHUB_REPOSITORY}/releases"`,
		`if ! [ -s "${release_metadata_file}" ]; then`,
		`Manual release promotion can only retry an existing GitHub release for ${tag}.`,
		`cat "${release_lookup_error}" >&2`,
		`exit "${release_lookup_status}"`,
	})
	if strings.Contains(manualStep.Run, `gh release create "${tag}"`) {
		t.Fatal("manual release retries must not create missing releases")
	}

	lookupIndex := strings.Index(manualStep.Run, `gh api "repos/${GITHUB_REPOSITORY}/releases/tags/${encoded_tag}" >"${release_metadata_file}" 2>"${release_lookup_error}"`)
	statusIndex := strings.Index(manualStep.Run, `release_lookup_status=$?`)
	notFoundIndex := strings.Index(manualStep.Run, `if grep -q "HTTP 404" "${release_lookup_error}"; then`)
	draftLookupIndex := strings.Index(manualStep.Run, `gh api --paginate --slurp "repos/${GITHUB_REPOSITORY}/releases"`)
	requireExistingIndex := strings.Index(manualStep.Run, `if ! [ -s "${release_metadata_file}" ]; then`)
	if lookupIndex < 0 || statusIndex < lookupIndex || notFoundIndex < statusIndex || draftLookupIndex < notFoundIndex || requireExistingIndex < draftLookupIndex {
		t.Fatal("manual release retry validation must follow the captured lookup status and explicit HTTP 404 evidence")
	}
}

func TestReleaseWorkflowConfinesMainSyncPATToReleasePleaseAndTrustedFeatureHistoryPush(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	releasePlease := workflowStepByName(t, workflow.Jobs, "prepare-release", "Run release-please")
	if got := releasePlease.With["token"]; got != "${{ secrets.RELEASE_PLEASE_TOKEN || secrets.MAIN_SYNC_PAT || secrets.GITHUB_TOKEN }}" {
		t.Fatalf("release-please token = %q, want RELEASE_PLEASE_TOKEN fallback to MAIN_SYNC_PAT then GITHUB_TOKEN", got)
	}

	manual := workflowStepByName(t, workflow.Jobs, "prepare-release", "Prepare manual release")
	if got := manual.Env["GH_TOKEN"]; got != "${{ secrets.RELEASE_PLEASE_TOKEN || secrets.GITHUB_TOKEN }}" {
		t.Fatalf("manual release GH_TOKEN = %q, want RELEASE_PLEASE_TOKEN fallback to GITHUB_TOKEN", got)
	}

	workflowText := readConfig(t, ".github/workflows/release.yml")
	if got := strings.Count(workflowText, "secrets.MAIN_SYNC_PAT"); got != 2 {
		t.Fatalf("release workflow MAIN_SYNC_PAT references = %d, want release-please and trusted feature history push fallbacks only", got)
	}
	if !strings.Contains(workflowText, "PUSH_TOKEN: ${{ secrets.MAIN_SYNC_PAT || secrets.GITHUB_TOKEN }}") {
		t.Fatal("trusted feature history push must retain MAIN_SYNC_PAT fallback to GITHUB_TOKEN")
	}
}

func TestReleaseWorkflowBuildsVSIXFromFreshArtifactStaging(t *testing.T) {
	t.Parallel()
	const hardenedShell = "/usr/bin/env -u BASH_ENV -u ENV -u PROMPT_COMMAND -u PS4 -u SHELLOPTS -u BASHOPTS /usr/bin/bash --noprofile --norc -euo pipefail {0}"

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	const jobName = "build-vscode-extension"
	build := workflowJobByName(t, workflow.Jobs, jobName)
	syncIndex := workflowStepIndexByName(t, workflow.Jobs, jobName, "Sync VS Code extension version")
	resetIndex := workflowStepIndexByName(t, workflow.Jobs, jobName, "Reset VS Code extension artifact staging")
	packageIndex := workflowStepIndexByName(t, workflow.Jobs, jobName, "Package VS Code extension")
	validateIndex := workflowStepIndexByName(t, workflow.Jobs, jobName, "Validate VS Code extension artifact")
	uploadIndex := workflowStepIndexByName(t, workflow.Jobs, jobName, "Upload VS Code extension artifact")
	if resetIndex != syncIndex+1 || packageIndex != resetIndex+1 || validateIndex != packageIndex+1 || uploadIndex != validateIndex+1 {
		t.Fatal("VS Code release packaging must sync, reset, package, validate, and upload in one contiguous sequence")
	}

	syncStep := build.Steps[syncIndex]
	assertWorkflowStepRunContainsAll(t, syncStep, "VS Code extension version sync", []string{
		`version="${RELEASE_TAG#v}"`,
		`make sync-version VERSION="$version"`,
	})

	resetStep := build.Steps[resetIndex]
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "VS Code extension artifact staging reset shell", got: resetStep.Shell, want: hardenedShell},
	})
	assertWorkflowStepEnv(t, resetStep, "VS Code extension artifact staging reset", map[string]string{"PATH": "/usr/bin:/bin"})
	assertWorkflowStepRunContainsAll(t, resetStep, "VS Code extension artifact staging reset", []string{
		`rm -rf -- dist`,
		`mkdir -- dist`,
	})
	assertWorkflowStepRunOmitsAll(t, resetStep, "VS Code extension artifact staging reset", []string{"mkdir -p"})
	assertTextAppearsBefore(t, resetStep.Run, `rm -rf -- dist`, `mkdir -- dist`, "VS Code artifact staging must remove checkout-provided files before recreating dist")

	packageStep := build.Steps[packageIndex]
	const packageCommand = `npx @vscode/vsce package --out "../../dist/lopper-vscode-${version}.vsix"`
	assertWorkflowStepRunContainsAll(t, packageStep, "VS Code extension package", []string{
		`version="${RELEASE_TAG#v}"`,
		packageCommand,
	})
	if strings.Count(packageStep.Run, "npx ") != 1 {
		t.Fatal("VS Code extension packaging must run exactly one npx command after resetting artifact staging")
	}

	validateStep := build.Steps[validateIndex]
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "VS Code extension artifact validation shell", got: validateStep.Shell, want: hardenedShell},
	})
	assertWorkflowStepEnv(t, validateStep, "VS Code extension artifact validation", map[string]string{
		"PATH":            "/usr/bin:/bin",
		"RELEASE_VERSION": "${{ needs.prepare-release.outputs.version }}",
	})
	assertWorkflowStepRunContainsAll(t, validateStep, "VS Code extension artifact validation", []string{
		`expected="dist/lopper-vscode-${RELEASE_VERSION}.vsix"`,
		`find -P dist -mindepth 1 -maxdepth 1 ! -type f -print -quit`,
		`[ ! -f "${expected}" ] || [ -L "${expected}" ]`,
		`[ ! -s "${expected}" ]`,
		`find -P dist -mindepth 1 -maxdepth 1 -type f | wc -l`,
	})

	for _, step := range build.Steps[resetIndex:] {
		for _, repositoryCommand := range []string{"go run ./", "make ", "npm ", "npx ", "scripts/", "./extensions/"} {
			if step.Name == packageStep.Name && repositoryCommand == "npx " && strings.Contains(step.Run, packageCommand) {
				continue
			}
			if strings.Contains(step.Run, repositoryCommand) {
				t.Fatalf("VS Code release step %q must not execute repository-controlled command %q after resetting artifact staging", step.Name, repositoryCommand)
			}
		}
	}

	uploadStep := build.Steps[uploadIndex]
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "VS Code release artifact name", got: uploadStep.With["name"], want: "release-vscode-extension"},
		{label: "VS Code release artifact path", got: uploadStep.With["path"], want: "dist/lopper-vscode-${{ needs.prepare-release.outputs.version }}.vsix"},
		{label: "VS Code release artifact missing-file behavior", got: uploadStep.With["if-no-files-found"], want: "error"},
	})
}

func TestReleaseWorkflowPublishesFromFreshValidatedInputs(t *testing.T) {
	t.Parallel()
	const hardenedShell = "/usr/bin/env -u BASH_ENV -u ENV -u PROMPT_COMMAND -u PS4 -u SHELLOPTS -u BASHOPTS /usr/bin/bash --noprofile --norc -euo pipefail {0}"

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	reportPreparation := workflowJobByName(t, workflow.Jobs, "prepare-release-feature-report")
	assertWorkflowJobNeeds(t, reportPreparation, "release feature report preparation", workflowJobNeeds{"prepare-release"})
	assertWorkflowJobPermissions(t, reportPreparation, "release feature report preparation", map[string]string{"contents": "read"})
	assertWorkflowJobEnvEmpty(t, reportPreparation, "release feature report preparation")
	assertWorkflowJobOmitsText(t, reportPreparation, "secrets.", "release feature report preparation must not receive secrets")
	assertWorkflowJobCheckoutsDisablePersistedCredentials(t, reportPreparation, "prepare-release-feature-report")
	assertWorkflowStepOrder(t, reportPreparation, "Checkout release source", "Verify release source checkout", "Setup Go", "Generate feature flag release report", "Validate feature flag release report", "Upload feature flag release report")
	verifySource := workflowStepByName(t, workflow.Jobs, "prepare-release-feature-report", "Verify release source checkout")
	assertWorkflowStepEnv(t, verifySource, "release source checkout verification", map[string]string{
		"EXPECTED_SOURCE_SHA": "${{ needs.prepare-release.outputs.sha }}",
	})
	assertWorkflowStepRunContainsAll(t, verifySource, "release source checkout verification", []string{
		`actual_source_sha="$(git rev-parse HEAD)"`,
		`[ "${actual_source_sha}" != "${EXPECTED_SOURCE_SHA}" ]`,
	})

	generateReport := workflowStepByName(t, workflow.Jobs, "prepare-release-feature-report", "Generate feature flag release report")
	assertWorkflowStepRunContainsAll(t, generateReport, "feature flag release report generation", []string{
		`report_dir="${RUNNER_TEMP}/release-feature-report"`,
		`rm -rf -- "${report_dir}"`,
		`mkdir -- "${report_dir}"`,
		`go run ./tools/featureflag "${args[@]}" > "${report_dir}/feature-flags.md"`,
	})
	validateReport := workflowStepByName(t, workflow.Jobs, "prepare-release-feature-report", "Validate feature flag release report")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature report validation shell", got: validateReport.Shell, want: hardenedShell},
	})
	assertWorkflowStepEnv(t, validateReport, "feature report validation", map[string]string{"PATH": "/usr/bin:/bin"})
	assertWorkflowStepRunContainsAll(t, validateReport, "feature report validation", []string{
		`find -P "${report_dir}" -mindepth 1 -maxdepth 1 ! -type f -print -quit`,
		`[ ! -f "${report}" ] || [ -L "${report}" ]`,
		`[ ! -s "${report}" ]`,
		`find -P "${report_dir}" -mindepth 1 -maxdepth 1 -type f | wc -l`,
	})
	reportUpload := workflowStepByName(t, workflow.Jobs, "prepare-release-feature-report", "Upload feature flag release report")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature report artifact name", got: reportUpload.With["name"], want: "stable-feature-report"},
		{label: "feature report artifact path", got: reportUpload.With["path"], want: "${{ runner.temp }}/release-feature-report/feature-flags.md"},
		{label: "feature report artifact missing-file behavior", got: reportUpload.With["if-no-files-found"], want: "error"},
	})

	preparation := workflowJobByName(t, workflow.Jobs, "prepare-release-publication")
	assertWorkflowJobNeeds(t, preparation, "release publication preparation", workflowJobNeeds{"prepare-release", "prepare-release-feature-report", "orchestrate-release", "build-vscode-extension", "build-darwin-amd64"})
	assertWorkflowJobHasExplicitEmptyPermissions(t, preparation, "release publication preparation")
	assertWorkflowJobEnvEmpty(t, preparation, "release publication preparation")
	assertWorkflowJobOmitsText(t, preparation, "GH_TOKEN", "release publication preparation must not receive GH_TOKEN")
	assertWorkflowJobOmitsText(t, preparation, "secrets.", "release publication preparation must not receive secrets")
	assertWorkflowJobOmitsCheckout(t, preparation, "prepare-release-publication")
	assertWorkflowJobStepRunsOmitAllFold(t, preparation, "release publication preparation", []string{"go run ./", "make ", "npm ", "npx ", "scripts/"})

	resetIndex := workflowStepIndexByName(t, workflow.Jobs, "prepare-release-publication", "Reset release publication assembly")
	reportDownloadIndex := workflowStepIndexByName(t, workflow.Jobs, "prepare-release-publication", "Download feature flag release report")
	linuxWindowsDownloadIndex := workflowStepIndexByName(t, workflow.Jobs, "prepare-release-publication", "Download Linux and Windows release artifacts")
	darwinArm64DownloadIndex := workflowStepIndexByName(t, workflow.Jobs, "prepare-release-publication", "Download Darwin arm64 release artifact")
	darwinAmd64DownloadIndex := workflowStepIndexByName(t, workflow.Jobs, "prepare-release-publication", "Download Darwin amd64 release artifact")
	vsixDownloadIndex := workflowStepIndexByName(t, workflow.Jobs, "prepare-release-publication", "Download VS Code extension release artifact")
	stageIndex := workflowStepIndexByName(t, workflow.Jobs, "prepare-release-publication", "Stage bounded release publication inputs")
	uploadIndex := workflowStepIndexByName(t, workflow.Jobs, "prepare-release-publication", "Upload release publication inputs")
	if reportDownloadIndex != resetIndex+1 || linuxWindowsDownloadIndex != reportDownloadIndex+1 ||
		darwinArm64DownloadIndex != linuxWindowsDownloadIndex+1 || darwinAmd64DownloadIndex != darwinArm64DownloadIndex+1 ||
		vsixDownloadIndex != darwinAmd64DownloadIndex+1 {
		t.Fatal("release inputs must be downloaded into fresh roots immediately after resetting publication assembly")
	}
	if stageIndex != vsixDownloadIndex+1 || uploadIndex != stageIndex+1 {
		t.Fatal("release artifacts must be downloaded, staged, and uploaded in one contiguous sequence")
	}

	resetStep := preparation.Steps[resetIndex]
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "release artifact staging reset shell", got: resetStep.Shell, want: hardenedShell},
	})
	assertWorkflowStepEnv(t, resetStep, "release artifact staging reset", map[string]string{"PATH": "/usr/bin:/bin"})
	assertWorkflowStepRunContainsAll(t, resetStep, "release artifact staging reset", []string{
		`assembly_root="${RUNNER_TEMP}/release-publication-assembly"`,
		`rm -rf -- "${assembly_root}"`,
		`rm -rf -- publication-inputs`,
		`mkdir -- "${assembly_root}"`,
	})
	assertTextAppearsBefore(t, resetStep.Run, `rm -rf -- "${assembly_root}"`, `mkdir -- "${assembly_root}"`, "release assembly must remove prior inputs before recreating its runner-temp root")
	downloadContracts := []workflowArtifactDownloadContract{
		{index: reportDownloadIndex, name: "stable-feature-report", path: "${{ runner.temp }}/release-publication-assembly/feature-report"},
		{index: linuxWindowsDownloadIndex, name: "release-linux-windows", path: "${{ runner.temp }}/release-publication-assembly/linux-windows"},
		{index: darwinArm64DownloadIndex, name: "release-darwin", path: "${{ runner.temp }}/release-publication-assembly/darwin-arm64"},
		{index: darwinAmd64DownloadIndex, name: "release-darwin-amd64", path: "${{ runner.temp }}/release-publication-assembly/darwin-amd64"},
		{index: vsixDownloadIndex, name: "release-vscode-extension", path: "${{ runner.temp }}/release-publication-assembly/vscode"},
	}
	assertExactArtifactDownloads(t, preparation.Steps, downloadContracts)
	for _, step := range preparation.Steps {
		if strings.Contains(step.Run, "${{ needs.") {
			t.Fatalf("fresh release assembly step %q must bind trusted values through env instead of interpolating expressions into shell source", step.Name)
		}
	}

	stageStep := workflowStepByName(t, workflow.Jobs, "prepare-release-publication", "Stage bounded release publication inputs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "release publication staging shell", got: stageStep.Shell, want: hardenedShell},
	})
	assertWorkflowStepEnv(t, stageStep, "release publication staging", map[string]string{
		"ASSEMBLY_ROOT": "${{ runner.temp }}/release-publication-assembly",
		"PATH":          "/usr/bin:/bin",
		"RELEASE_TAG":   "${{ needs.prepare-release.outputs.tag }}",
	})
	assertWorkflowStepRunContainsAll(t, stageStep, "release publication staging step", []string{
		`report="${ASSEMBLY_ROOT}/feature-report/feature-flags.md"`,
		`linux_windows_assets=(`,
		`${ASSEMBLY_ROOT}/linux-windows/lopper_${RELEASE_TAG}_linux_amd64.tar.gz`,
		`${ASSEMBLY_ROOT}/linux-windows/lopper_${RELEASE_TAG}_linux_arm64.tar.gz`,
		`${ASSEMBLY_ROOT}/linux-windows/lopper_${RELEASE_TAG}_windows_amd64.zip`,
		`${ASSEMBLY_ROOT}/linux-windows/lopper_${RELEASE_TAG}_windows_arm64.zip`,
		`darwin_arm64_assets=("${ASSEMBLY_ROOT}/darwin-arm64/lopper_${RELEASE_TAG}_darwin_arm64.tar.gz")`,
		`darwin_amd64_assets=("${ASSEMBLY_ROOT}/darwin-amd64/lopper_${RELEASE_TAG}_darwin_amd64.tar.gz")`,
		`vsix_assets=("${ASSEMBLY_ROOT}/vscode/lopper-vscode-${version}.vsix")`,
		`validate_input_root "${ASSEMBLY_ROOT}/feature-report" 1048576 "${report}"`,
		`validate_input_root "${ASSEMBLY_ROOT}/linux-windows" 1073741824 "${linux_windows_assets[@]}"`,
		`validate_input_root "${ASSEMBLY_ROOT}/darwin-arm64" 1073741824 "${darwin_arm64_assets[@]}"`,
		`validate_input_root "${ASSEMBLY_ROOT}/darwin-amd64" 1073741824 "${darwin_amd64_assets[@]}"`,
		`validate_input_root "${ASSEMBLY_ROOT}/vscode" 1073741824 "${vsix_assets[@]}"`,
		`expected_assets=(`,
		`dist/lopper_${RELEASE_TAG}_linux_amd64.tar.gz`,
		`dist/lopper_${RELEASE_TAG}_linux_arm64.tar.gz`,
		`dist/lopper_${RELEASE_TAG}_windows_amd64.zip`,
		`dist/lopper_${RELEASE_TAG}_windows_arm64.zip`,
		`dist/lopper_${RELEASE_TAG}_darwin_amd64.tar.gz`,
		`dist/lopper_${RELEASE_TAG}_darwin_arm64.tar.gz`,
		`dist/lopper-vscode-${version}.vsix`,
		`[ ! -f "${asset}" ] || [ -L "${asset}" ]`,
		`[ ! -s "${asset}" ]`,
		`rm -rf -- publication-inputs`,
		`mkdir -p publication-inputs/dist`,
		`cp -- "${report}" publication-inputs/feature-flags.md`,
		`manifest_inputs=("feature-flags.md" "${expected_assets[@]}")`,
		`printf '%s\0' "${manifest_inputs[@]}" | sort -z`,
		`sha256sum "${sorted_manifest_inputs[@]}" > SHA256SUMS`,
	})
	assertWorkflowStepRunOmitsAll(t, stageStep, "release publication staging step", []string{
		"Expected between 1 and 32 release assets",
		"pattern:",
		`-name '*.tar.gz'`,
		`-name 'lopper-vscode-*.vsix'`,
	})
	assertTextAppearsBefore(t, stageStep.Run, `rm -rf -- publication-inputs`, `mkdir -p publication-inputs/dist`, "release publication staging must remove prior inputs before recreating the directory")
	assertTextAppearsBefore(t, stageStep.Run, `mkdir -p publication-inputs/dist`, `cp -- "${assets[@]}" publication-inputs/dist/`, "release publication staging must recreate the directory before copying bounded assets")
	assertTextAppearsBefore(t, stageStep.Run, `mkdir -p publication-inputs/dist`, `cp -- "${report}" publication-inputs/feature-flags.md`, "release publication staging must recreate the directory before copying the feature report")
	uploadStep := workflowStepByName(t, workflow.Jobs, "prepare-release-publication", "Upload release publication inputs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "release publication input artifact name", got: uploadStep.With["name"], want: "publication-inputs"},
		{label: "release publication input artifact path", got: uploadStep.With["path"], want: "publication-inputs"},
		{label: "release publication input artifact missing-file behavior", got: uploadStep.With["if-no-files-found"], want: "error"},
	})

	publication := workflowJobByName(t, workflow.Jobs, "publish")
	assertWorkflowJobPermissions(t, publication, "fresh release publication", map[string]string{"contents": "write"})
	assertWorkflowJobEnvEmpty(t, publication, "fresh release publication")
	validateStep := workflowStepByName(t, workflow.Jobs, "publish", "Validate release publication inputs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "privileged release publication validation shell", got: validateStep.Shell, want: hardenedShell},
	})
	assertWorkflowStepEnv(t, validateStep, "privileged release publication validation", map[string]string{
		"PATH":        "/usr/bin:/bin",
		"RELEASE_TAG": "${{ needs.prepare-release.outputs.tag }}",
	})
	assertWorkflowStepRunContainsAll(t, validateStep, "privileged release publication validation", []string{
		`expected_assets=(`,
		`dist/lopper_${RELEASE_TAG}_linux_amd64.tar.gz`,
		`dist/lopper_${RELEASE_TAG}_linux_arm64.tar.gz`,
		`dist/lopper_${RELEASE_TAG}_windows_amd64.zip`,
		`dist/lopper_${RELEASE_TAG}_windows_arm64.zip`,
		`dist/lopper_${RELEASE_TAG}_darwin_amd64.tar.gz`,
		`dist/lopper_${RELEASE_TAG}_darwin_arm64.tar.gz`,
		`dist/lopper-vscode-${version}.vsix`,
		`[ ! -f SHA256SUMS ] || [ -L SHA256SUMS ] || [ ! -s SHA256SUMS ]`,
		`stat --format=%s SHA256SUMS`,
		`manifest_inputs=("feature-flags.md" "${expected_assets[@]}")`,
		`printf '%s\0' "${manifest_inputs[@]}" | sort -z`,
		`sha256sum "${sorted_manifest_inputs[@]}" > "${computed_checksums}"`,
		`cmp -s SHA256SUMS "${computed_checksums}"`,
		`[ ! -f "${asset}" ] || [ -L "${asset}" ]`,
		`[ ! -s "${asset}" ]`,
	})
	assertTextAppearsBefore(t, validateStep.Run, `stat --format=%s "${asset}"`, `sha256sum "${sorted_manifest_inputs[@]}"`, "privileged publication must bound every release asset before hashing it")
	assertTextAppearsBefore(t, validateStep.Run, `stat --format=%s feature-flags.md`, `sha256sum "${sorted_manifest_inputs[@]}"`, "privileged publication must bound the feature report before hashing it")
	assertTextAppearsBefore(t, validateStep.Run, `stat --format=%s SHA256SUMS`, `sha256sum "${sorted_manifest_inputs[@]}"`, "privileged publication must bound the checksum manifest before hashing release inputs")
	assertWorkflowStepRunOmitsAll(t, validateStep, "privileged release publication validation", []string{
		"Expected between 1 and 32 release assets",
		`-name '*.tar.gz'`,
		`-name 'lopper-vscode-*.vsix'`,
	})
	uploadAssets := workflowStepByName(t, workflow.Jobs, "publish", "Upload GitHub Release assets")
	assertWorkflowStepRunContainsAll(t, uploadAssets, "explicit GitHub release asset upload", []string{
		`publication-inputs/dist/lopper_${tag}_linux_amd64.tar.gz`,
		`publication-inputs/dist/lopper_${tag}_linux_arm64.tar.gz`,
		`publication-inputs/dist/lopper_${tag}_windows_amd64.zip`,
		`publication-inputs/dist/lopper_${tag}_windows_arm64.zip`,
		`publication-inputs/dist/lopper_${tag}_darwin_amd64.tar.gz`,
		`publication-inputs/dist/lopper_${tag}_darwin_arm64.tar.gz`,
		`publication-inputs/dist/lopper-vscode-${tag#v}.vsix`,
	})
	assertWorkflowStepRunOmitsAll(t, uploadAssets, "explicit GitHub release asset upload", []string{"find publication-inputs/dist", "*.tar.gz", "*.zip", "*.vsix"})
	assertReleasePublicationCredentialScope(t, publication)
	assertReleasePublicationOmitsCheckoutAndCommands(t, publication)
}

func TestReleaseWorkflowUsesCanonicalPublicationManifestPaths(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	assembler := workflowStepByName(t, workflow.Jobs, "prepare-release-publication", "Stage bounded release publication inputs")
	publisher := workflowStepByName(t, workflow.Jobs, "publish", "Validate release publication inputs")
	marketplace := workflowStepByName(t, workflow.Jobs, "publish-marketplace", "Validate Marketplace publication inputs")

	const canonicalInputs = `manifest_inputs=("feature-flags.md" "${expected_assets[@]}")`
	for name, step := range map[string]workflowStepConfig{
		"assembler": assembler,
		"publisher": publisher,
	} {
		if !strings.Contains(step.Run, canonicalInputs) {
			t.Fatalf("%s must hash canonical publication paths without a leading ./", name)
		}
	}

	assertWorkflowStepRunContainsAll(t, marketplace, "Marketplace canonical publication manifest paths", []string{
		`printf '%s\n' feature-flags.md`,
		`find dist -maxdepth 1 -type f -print`,
	})
	assertWorkflowStepRunOmitsAll(t, marketplace, "Marketplace canonical publication manifest paths", []string{
		`printf '%s\n' ./feature-flags.md`,
		`find ./dist -maxdepth 1 -type f -print`,
		`[.]\/`,
	})

	manifestPatternMatch := regexp.MustCompile(`grep -Ev '([^']+)' SHA256SUMS`).FindStringSubmatch(marketplace.Run)
	if len(manifestPatternMatch) != 2 {
		t.Fatal("Marketplace validation must expose one checksum-manifest path pattern")
	}
	manifestPattern, err := regexp.Compile(manifestPatternMatch[1])
	if err != nil {
		t.Fatalf("compile Marketplace checksum-manifest path pattern: %v", err)
	}
	for _, line := range []string{
		strings.Repeat("a", 64) + "  feature-flags.md",
		strings.Repeat("b", 64) + "  dist/lopper_v1.2.3_linux_amd64.tar.gz",
		strings.Repeat("c", 64) + "  dist/lopper-vscode-1.2.3.vsix",
	} {
		if !manifestPattern.MatchString(line) {
			t.Fatalf("Marketplace checksum-manifest path pattern rejects canonical producer line %q", line)
		}
	}
}

func TestRollingWorkflowPublishesFromFreshValidatedInputs(t *testing.T) {
	t.Parallel()
	const hardenedShell = "/usr/bin/env -u BASH_ENV -u ENV -u PROMPT_COMMAND -u PS4 -u SHELLOPTS -u BASHOPTS /usr/bin/bash --noprofile --norc -euo pipefail {0}"

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/rolling.yml", &workflow)

	darwinProducer := workflowJobByName(t, workflow.Jobs, "build-darwin-amd64-rolling")
	darwinCheckout := workflowStepByName(t, workflow.Jobs, "build-darwin-amd64-rolling", "Checkout rolling source")
	if darwinCheckout.With["persist-credentials"] != "false" {
		t.Fatal("rolling Darwin amd64 checkout must disable persisted credentials")
	}
	assertWorkflowJobPermissions(t, darwinProducer, "rolling Darwin amd64 producer", map[string]string{"contents": "read"})

	notesPreparation := workflowJobByName(t, workflow.Jobs, "prepare-rolling-release-notes")
	assertWorkflowJobNeeds(t, notesPreparation, "rolling release note preparation", workflowJobNeeds{"prepare-rolling"})
	assertWorkflowJobPermissions(t, notesPreparation, "rolling release note preparation", map[string]string{"contents": "read"})
	assertWorkflowJobEnvEmpty(t, notesPreparation, "rolling release note preparation")
	assertWorkflowJobOmitsText(t, notesPreparation, "secrets.", "rolling release note preparation must not receive secrets")
	assertWorkflowJobCheckoutsDisablePersistedCredentials(t, notesPreparation, "prepare-rolling-release-notes")
	assertWorkflowStepOrder(t, notesPreparation, "Checkout rolling source", "Verify rolling source checkout", "Setup Go", "Generate rolling release notes", "Validate rolling release notes", "Upload rolling release notes")
	generateNotes := workflowStepByName(t, workflow.Jobs, "prepare-rolling-release-notes", "Generate rolling release notes")
	assertWorkflowStepRunContainsAll(t, generateNotes, "rolling release note generation", []string{
		`notes_root="${RUNNER_TEMP}/rolling-release-notes"`,
		`rm -rf -- "${notes_root}"`,
		`mkdir -- "${notes_root}"`,
		`go run ./tools/featureflag "${args[@]}" > "${feature_report}"`,
		`> "${notes_root}/rolling-changelog.md"`,
	})
	validateNotes := workflowStepByName(t, workflow.Jobs, "prepare-rolling-release-notes", "Validate rolling release notes")
	assertWorkflowStringValues(t, []workflowStringValue{{
		label: "rolling release note validation shell",
		got:   validateNotes.Shell,
		want:  hardenedShell,
	}})
	assertWorkflowStepEnv(t, validateNotes, "rolling release note validation", map[string]string{
		"NOTES_ROOT": "${{ runner.temp }}/rolling-release-notes",
		"PATH":       "/usr/bin:/bin",
	})
	assertWorkflowStepRunContainsAll(t, validateNotes, "rolling release note validation", []string{
		`changelog="${NOTES_ROOT}/rolling-changelog.md"`,
		`find -P "${NOTES_ROOT}" -mindepth 1 -maxdepth 1 ! -type f -print -quit`,
		`[ "${file_count}" -ne 1 ]`,
		`[ ! -f "${changelog}" ] || [ -L "${changelog}" ] || [ ! -s "${changelog}" ]`,
		`[ "$(stat --format=%s "${changelog}")" -gt 1048576 ]`,
	})
	notesUpload := workflowStepByName(t, workflow.Jobs, "prepare-rolling-release-notes", "Upload rolling release notes")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "rolling release note artifact name", got: notesUpload.With["name"], want: "rolling-release-notes"},
		{label: "rolling release note artifact path", got: notesUpload.With["path"], want: "${{ runner.temp }}/rolling-release-notes/rolling-changelog.md"},
		{label: "rolling release note missing-file behavior", got: notesUpload.With["if-no-files-found"], want: "error"},
	})

	archivePreparation := workflowJobByName(t, workflow.Jobs, "prepare-rolling-source-archive")
	assertWorkflowJobNeeds(t, archivePreparation, "rolling source archive preparation", workflowJobNeeds{"prepare-rolling"})
	assertWorkflowJobPermissions(t, archivePreparation, "rolling source archive preparation", map[string]string{"contents": "read"})
	assertWorkflowJobEnvEmpty(t, archivePreparation, "rolling source archive preparation")
	assertWorkflowJobOmitsText(t, archivePreparation, "secrets.", "rolling source archive preparation must not receive secrets")
	assertWorkflowJobCheckoutsDisablePersistedCredentials(t, archivePreparation, "prepare-rolling-source-archive")
	assertWorkflowJobStepRunsOmitAllFold(t, archivePreparation, "rolling source archive preparation", []string{"go run ./", "make ", "npm ", "npx ", "scripts/", "./extensions/"})
	assertWorkflowStepOrder(t, archivePreparation, "Checkout rolling source", "Verify rolling source checkout", "Build rolling source archive", "Validate rolling source archive", "Upload rolling source archive")
	verifySource := workflowStepByName(t, workflow.Jobs, "prepare-rolling-source-archive", "Verify rolling source checkout")
	assertWorkflowStepEnv(t, verifySource, "rolling source archive checkout verification", map[string]string{
		"EXPECTED_SOURCE_SHA": "${{ needs.prepare-rolling.outputs.source_sha }}",
	})
	assertWorkflowStepRunContainsAll(t, verifySource, "rolling source archive checkout verification", []string{
		`actual_source_sha="$(git rev-parse HEAD)"`,
		`[ "${actual_source_sha}" != "${EXPECTED_SOURCE_SHA}" ]`,
	})
	buildSource := workflowStepByName(t, workflow.Jobs, "prepare-rolling-source-archive", "Build rolling source archive")
	assertWorkflowStringValues(t, []workflowStringValue{{
		label: "rolling source archive build shell",
		got:   buildSource.Shell,
		want:  hardenedShell,
	}})
	assertWorkflowStepEnv(t, buildSource, "rolling source archive", map[string]string{
		"PATH":        "/usr/bin:/bin",
		"ROLLING_TAG": "${{ needs.prepare-rolling.outputs.tag }}",
		"SOURCE_SHA":  "${{ needs.prepare-rolling.outputs.source_sha }}",
	})
	assertWorkflowStepRunContainsAll(t, buildSource, "rolling source archive", []string{
		`archive_root="${RUNNER_TEMP}/rolling-source-archive"`,
		`rm -rf -- "${archive_root}"`,
		`mkdir -- "${archive_root}"`,
		`archive_path="${archive_root}/lopper_${ROLLING_TAG}_source.tar.gz"`,
		`git archive --format=tar.gz --prefix="${bundle}/" -o "${archive_path}" "${SOURCE_SHA}"`,
	})
	validateSource := workflowStepByName(t, workflow.Jobs, "prepare-rolling-source-archive", "Validate rolling source archive")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "rolling source archive validation shell", got: validateSource.Shell, want: hardenedShell},
	})
	assertWorkflowStepEnv(t, validateSource, "rolling source archive validation", map[string]string{
		"ARCHIVE_ROOT": "${{ runner.temp }}/rolling-source-archive",
		"PATH":         "/usr/bin:/bin",
		"ROLLING_TAG":  "${{ needs.prepare-rolling.outputs.tag }}",
	})
	assertWorkflowStepRunContainsAll(t, validateSource, "rolling source archive validation", []string{
		`archive="${ARCHIVE_ROOT}/lopper_${ROLLING_TAG}_source.tar.gz"`,
		`find -P "${ARCHIVE_ROOT}" -mindepth 1 -maxdepth 1 ! -type f -print -quit`,
		`[ "${file_count}" -ne 1 ]`,
		`[ ! -f "${archive}" ] || [ -L "${archive}" ] || [ ! -s "${archive}" ]`,
		`[ "$(stat --format=%s "${archive}")" -gt 1073741824 ]`,
	})
	sourceUpload := workflowStepByName(t, workflow.Jobs, "prepare-rolling-source-archive", "Upload rolling source archive")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "rolling source archive artifact name", got: sourceUpload.With["name"], want: "rolling-source-archive"},
		{label: "rolling source archive artifact path", got: sourceUpload.With["path"], want: "${{ runner.temp }}/rolling-source-archive/lopper_${{ needs.prepare-rolling.outputs.tag }}_source.tar.gz"},
		{label: "rolling source archive missing-file behavior", got: sourceUpload.With["if-no-files-found"], want: "error"},
	})
	for _, step := range archivePreparation.Steps[workflowStepIndexByName(t, workflow.Jobs, "prepare-rolling-source-archive", "Build rolling source archive")+1:] {
		for _, repositoryCommand := range []string{"go run ./", "make ", "npm ", "npx ", "scripts/", "./extensions/"} {
			if strings.Contains(step.Run, repositoryCommand) {
				t.Fatalf("rolling source archive step %q must not execute repository-controlled command %q after archive creation", step.Name, repositoryCommand)
			}
		}
	}

	preparation := workflowJobByName(t, workflow.Jobs, "prepare-rolling-publication")
	assertWorkflowJobNeeds(t, preparation, "rolling publication preparation", workflowJobNeeds{"prepare-rolling", "prepare-rolling-release-notes", "prepare-rolling-source-archive", "orchestrate-rolling", "build-darwin-amd64-rolling"})
	assertWorkflowJobHasExplicitEmptyPermissions(t, preparation, "rolling publication preparation")
	assertWorkflowJobEnvEmpty(t, preparation, "rolling publication preparation")
	assertWorkflowJobOmitsText(t, preparation, "secrets.", "rolling publication preparation must not receive secrets")
	assertWorkflowJobOmitsCheckout(t, preparation, "prepare-rolling-publication")
	assertWorkflowJobStepRunsOmitAllFold(t, preparation, "rolling publication preparation", []string{"go run ./", "make ", "npm ", "npx ", "scripts/", "git "})

	resetIndex := workflowStepIndexByName(t, workflow.Jobs, "prepare-rolling-publication", "Reset rolling publication assembly")
	sourceDownloadIndex := workflowStepIndexByName(t, workflow.Jobs, "prepare-rolling-publication", "Download rolling source archive")
	notesDownloadIndex := workflowStepIndexByName(t, workflow.Jobs, "prepare-rolling-publication", "Download rolling release notes")
	linuxWindowsDownloadIndex := workflowStepIndexByName(t, workflow.Jobs, "prepare-rolling-publication", "Download rolling Linux and Windows artifacts")
	darwinArm64DownloadIndex := workflowStepIndexByName(t, workflow.Jobs, "prepare-rolling-publication", "Download rolling Darwin arm64 artifact")
	darwinAmd64DownloadIndex := workflowStepIndexByName(t, workflow.Jobs, "prepare-rolling-publication", "Download rolling Darwin amd64 artifact")
	stageIndex := workflowStepIndexByName(t, workflow.Jobs, "prepare-rolling-publication", "Stage bounded rolling publication inputs")
	uploadIndex := workflowStepIndexByName(t, workflow.Jobs, "prepare-rolling-publication", "Upload rolling publication inputs")
	if sourceDownloadIndex != resetIndex+1 || notesDownloadIndex != sourceDownloadIndex+1 || linuxWindowsDownloadIndex != notesDownloadIndex+1 ||
		darwinArm64DownloadIndex != linuxWindowsDownloadIndex+1 || darwinAmd64DownloadIndex != darwinArm64DownloadIndex+1 ||
		stageIndex != darwinAmd64DownloadIndex+1 || uploadIndex != stageIndex+1 {
		t.Fatal("rolling publication inputs must reset, download exact producers, stage, and upload contiguously")
	}
	resetStep := preparation.Steps[resetIndex]
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "rolling publication assembly reset shell", got: resetStep.Shell, want: hardenedShell},
	})
	assertWorkflowStepEnv(t, resetStep, "rolling publication assembly reset", map[string]string{"PATH": "/usr/bin:/bin"})
	assertWorkflowStepRunContainsAll(t, resetStep, "rolling publication assembly reset", []string{
		`assembly_root="${RUNNER_TEMP}/rolling-publication-assembly"`,
		`rm -rf -- "${assembly_root}"`,
		`rm -rf -- rolling-publication-inputs`,
		`mkdir -- "${assembly_root}"`,
	})
	downloadContracts := []workflowArtifactDownloadContract{
		{index: sourceDownloadIndex, name: "rolling-source-archive", path: "${{ runner.temp }}/rolling-publication-assembly/source-archive"},
		{index: notesDownloadIndex, name: "rolling-release-notes", path: "${{ runner.temp }}/rolling-publication-assembly/release-notes"},
		{index: linuxWindowsDownloadIndex, name: "rolling-linux-windows", path: "${{ runner.temp }}/rolling-publication-assembly/linux-windows"},
		{index: darwinArm64DownloadIndex, name: "rolling-darwin", path: "${{ runner.temp }}/rolling-publication-assembly/darwin-arm64"},
		{index: darwinAmd64DownloadIndex, name: "rolling-darwin-amd64", path: "${{ runner.temp }}/rolling-publication-assembly/darwin-amd64"},
	}
	assertExactArtifactDownloads(t, preparation.Steps, downloadContracts)
	for _, step := range preparation.Steps {
		if strings.Contains(step.Run, "${{ needs.") {
			t.Fatalf("fresh rolling assembly step %q must bind trusted values through env", step.Name)
		}
	}
	stageStep := preparation.Steps[stageIndex]
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "rolling publication staging shell", got: stageStep.Shell, want: hardenedShell},
	})
	assertWorkflowStepEnv(t, stageStep, "rolling publication staging", map[string]string{
		"ASSEMBLY_ROOT": "${{ runner.temp }}/rolling-publication-assembly",
		"PATH":          "/usr/bin:/bin",
		"ROLLING_TAG":   "${{ needs.prepare-rolling.outputs.tag }}",
	})
	assertWorkflowStepRunContainsAll(t, stageStep, "rolling publication staging", []string{
		`source_archive="${ASSEMBLY_ROOT}/source-archive/lopper_${ROLLING_TAG}_source.tar.gz"`,
		`changelog="${ASSEMBLY_ROOT}/release-notes/rolling-changelog.md"`,
		`${ASSEMBLY_ROOT}/linux-windows/lopper_${ROLLING_TAG}_linux_amd64.tar.gz`,
		`${ASSEMBLY_ROOT}/linux-windows/lopper_${ROLLING_TAG}_linux_arm64.tar.gz`,
		`${ASSEMBLY_ROOT}/linux-windows/lopper_${ROLLING_TAG}_windows_amd64.zip`,
		`${ASSEMBLY_ROOT}/linux-windows/lopper_${ROLLING_TAG}_windows_arm64.zip`,
		`${ASSEMBLY_ROOT}/darwin-arm64/lopper_${ROLLING_TAG}_darwin_arm64.tar.gz`,
		`${ASSEMBLY_ROOT}/darwin-amd64/lopper_${ROLLING_TAG}_darwin_amd64.tar.gz`,
		`validate_input_root "${ASSEMBLY_ROOT}/source-archive" 1073741824 "${source_archive}"`,
		`validate_input_root "${ASSEMBLY_ROOT}/release-notes" 1048576 "${changelog}"`,
		`validate_input_root "${ASSEMBLY_ROOT}/linux-windows" 1073741824 "${linux_windows_assets[@]}"`,
		`validate_input_root "${ASSEMBLY_ROOT}/darwin-arm64" 1073741824 "${darwin_arm64_assets[@]}"`,
		`validate_input_root "${ASSEMBLY_ROOT}/darwin-amd64" 1073741824 "${darwin_amd64_assets[@]}"`,
		`[ "$(stat --format=%s "${changelog}")" -gt 1048576 ]`,
		`cp -- "${binary_assets[@]}" rolling-publication-inputs/dist/`,
		`cp -- "${source_archive}" "rolling-publication-inputs/dist/${source_basename}"`,
		`cp -- "${changelog}" rolling-publication-inputs/rolling-changelog.md`,
		`sha256sum "${source_basename}" > "${source_basename}.sha256"`,
		`expected_assets=(`,
		`dist/lopper_${ROLLING_TAG}_linux_amd64.tar.gz`,
		`dist/lopper_${ROLLING_TAG}_linux_arm64.tar.gz`,
		`dist/lopper_${ROLLING_TAG}_windows_amd64.zip`,
		`dist/lopper_${ROLLING_TAG}_windows_arm64.zip`,
		`dist/lopper_${ROLLING_TAG}_darwin_amd64.tar.gz`,
		`dist/lopper_${ROLLING_TAG}_darwin_arm64.tar.gz`,
		`dist/lopper_${ROLLING_TAG}_source.tar.gz`,
		`dist/lopper_${ROLLING_TAG}_source.tar.gz.sha256`,
		`stat --format=%s "rolling-publication-inputs/dist/${source_basename}.sha256"`,
		`manifest_inputs=("rolling-changelog.md" "${expected_assets[@]}")`,
		`sha256sum "${sorted_manifest_inputs[@]}" > SHA256SUMS`,
	})
	assertWorkflowStepRunOmitsAll(t, stageStep, "rolling publication staging", []string{"*.tar.gz", "*.zip", "find dist", "pattern:"})
	uploadInputs := preparation.Steps[uploadIndex]
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "rolling publication input artifact name", got: uploadInputs.With["name"], want: "rolling-publication-inputs"},
		{label: "rolling publication input artifact path", got: uploadInputs.With["path"], want: "rolling-publication-inputs"},
		{label: "rolling publication input missing-file behavior", got: uploadInputs.With["if-no-files-found"], want: "error"},
	})

	publication := workflowJobByName(t, workflow.Jobs, "publish-rolling")
	assertWorkflowJobNeeds(t, publication, "rolling publication", workflowJobNeeds{"prepare-rolling", "prepare-rolling-publication"})
	assertWorkflowJobPermissions(t, publication, "rolling publication", map[string]string{"contents": "write"})
	assertWorkflowJobEnvEmpty(t, publication, "rolling publication")
	assertWorkflowStringValues(t, []workflowStringValue{{
		label: "rolling publication tag output",
		got:   publication.Outputs["tag"],
		want:  "${{ needs.prepare-rolling.outputs.tag }}",
	}})
	assertWorkflowJobOmitsCheckout(t, publication, "publish-rolling")
	assertWorkflowJobStepRunsOmitAllFold(t, publication, "rolling publication", []string{"go run ./", "make ", "npm ", "npx ", "scripts/", "git "})
	assertWorkflowStepOrder(t, publication, "Download rolling publication inputs", "Validate rolling publication inputs", "Publish rolling prerelease")
	downloadInputs := workflowStepByName(t, workflow.Jobs, "publish-rolling", "Download rolling publication inputs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "privileged rolling publication download name", got: downloadInputs.With["name"], want: "rolling-publication-inputs"},
		{label: "privileged rolling publication download path", got: downloadInputs.With["path"], want: "rolling-publication-inputs"},
	})
	validateInputs := workflowStepByName(t, workflow.Jobs, "publish-rolling", "Validate rolling publication inputs")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "privileged rolling publication validation shell", got: validateInputs.Shell, want: hardenedShell},
	})
	assertWorkflowStepEnv(t, validateInputs, "privileged rolling publication validation", map[string]string{
		"PATH":        "/usr/bin:/bin",
		"ROLLING_TAG": "${{ needs.prepare-rolling.outputs.tag }}",
	})
	assertWorkflowStepRunContainsAll(t, validateInputs, "privileged rolling publication validation", []string{
		`expected_assets=(`,
		`dist/lopper_${ROLLING_TAG}_linux_amd64.tar.gz`,
		`dist/lopper_${ROLLING_TAG}_linux_arm64.tar.gz`,
		`dist/lopper_${ROLLING_TAG}_windows_amd64.zip`,
		`dist/lopper_${ROLLING_TAG}_windows_arm64.zip`,
		`dist/lopper_${ROLLING_TAG}_darwin_amd64.tar.gz`,
		`dist/lopper_${ROLLING_TAG}_darwin_arm64.tar.gz`,
		`dist/lopper_${ROLLING_TAG}_source.tar.gz`,
		`dist/lopper_${ROLLING_TAG}_source.tar.gz.sha256`,
		`[ "$(find . -type f | wc -l | tr -d '[:space:]')" -ne 10 ]`,
		`[ ! -f SHA256SUMS ] || [ -L SHA256SUMS ] || [ ! -s SHA256SUMS ]`,
		`stat --format=%s SHA256SUMS`,
		`[ ! -f rolling-changelog.md ] || [ -L rolling-changelog.md ] || [ ! -s rolling-changelog.md ]`,
		`[ "$(stat --format=%s rolling-changelog.md)" -gt 1048576 ]`,
		`[ ! -f "${asset}" ] || [ -L "${asset}" ] || [ ! -s "${asset}" ]`,
		`stat --format=%s "dist/${source_basename}.sha256"`,
		`sha256sum --check --strict "${source_basename}.sha256"`,
		`manifest_inputs=("rolling-changelog.md" "${expected_assets[@]}")`,
		`sha256sum "${sorted_manifest_inputs[@]}" > "${computed_checksums}"`,
		`cmp -s SHA256SUMS "${computed_checksums}"`,
		`sha256sum --check --strict SHA256SUMS`,
	})
	assertTextAppearsBefore(t, validateInputs.Run, `stat --format=%s "${asset}"`, `sha256sum --check --strict "${source_basename}.sha256"`, "rolling publisher must bound every asset before verifying checksums")
	assertTextAppearsBefore(t, validateInputs.Run, `stat --format=%s rolling-changelog.md`, `sha256sum "${sorted_manifest_inputs[@]}"`, "rolling publisher must bound the changelog before hashing inputs")
	publishStep := workflowStepByName(t, workflow.Jobs, "publish-rolling", "Publish rolling prerelease")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "rolling release action", got: publishStep.Uses, want: "softprops/action-gh-release@c12583777ecdfd3be55c69cf75464299dc01057e"},
		{label: "rolling release tag", got: publishStep.With["tag_name"], want: "${{ needs.prepare-rolling.outputs.tag }}"},
		{label: "rolling release target", got: publishStep.With["target_commitish"], want: "${{ needs.prepare-rolling.outputs.source_sha }}"},
		{label: "rolling release body", got: publishStep.With["body_path"], want: "rolling-publication-inputs/rolling-changelog.md"},
		{label: "rolling unmatched-file behavior", got: publishStep.With["fail_on_unmatched_files"], want: "true"},
	})
	wantPublishedAssets := []string{
		"rolling-publication-inputs/dist/lopper_${{ needs.prepare-rolling.outputs.tag }}_linux_amd64.tar.gz",
		"rolling-publication-inputs/dist/lopper_${{ needs.prepare-rolling.outputs.tag }}_linux_arm64.tar.gz",
		"rolling-publication-inputs/dist/lopper_${{ needs.prepare-rolling.outputs.tag }}_windows_amd64.zip",
		"rolling-publication-inputs/dist/lopper_${{ needs.prepare-rolling.outputs.tag }}_windows_arm64.zip",
		"rolling-publication-inputs/dist/lopper_${{ needs.prepare-rolling.outputs.tag }}_darwin_amd64.tar.gz",
		"rolling-publication-inputs/dist/lopper_${{ needs.prepare-rolling.outputs.tag }}_darwin_arm64.tar.gz",
		"rolling-publication-inputs/dist/lopper_${{ needs.prepare-rolling.outputs.tag }}_source.tar.gz",
		"rolling-publication-inputs/dist/lopper_${{ needs.prepare-rolling.outputs.tag }}_source.tar.gz.sha256",
	}
	if got := strings.Split(strings.TrimSpace(publishStep.With["files"]), "\n"); !slices.Equal(got, wantPublishedAssets) {
		t.Fatalf("rolling published assets = %#v", got)
	}
	if strings.Contains(publishStep.With["files"], "*") {
		t.Fatal("rolling publication must not upload asset globs")
	}
}

func TestReleaseWorkflowScopesPublicationSecretsToNamedSteps(t *testing.T) {
	t.Parallel()

	var workflow yaml.Node
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	want := []string{
		"jobs.prepare-release.steps.Run release-please#1.with.token=${{ secrets.RELEASE_PLEASE_TOKEN || secrets.MAIN_SYNC_PAT || secrets.GITHUB_TOKEN }}",
		"jobs.prepare-release.steps.Checkout release metadata#1.with.token=${{ secrets.GITHUB_TOKEN }}",
		"jobs.prepare-release.steps.Prepare manual release#1.env.GH_TOKEN=${{ secrets.RELEASE_PLEASE_TOKEN || secrets.GITHUB_TOKEN }}",
		"jobs.prepare-marketplace-toolchain.steps.Detect Marketplace token#1.env.VSCE_PUBLISH=${{ secrets.VSCE_PUBLISH }}",
		"jobs.publish-marketplace.steps.Publish VS Code extension to Marketplace#1.env.VSCE_PAT=${{ secrets.VSCE_PUBLISH }}",
		"jobs.publish.steps.Update GitHub Release notes#1.env.GH_TOKEN=${{ secrets.GITHUB_TOKEN }}",
		"jobs.publish.steps.Upload GitHub Release assets#1.env.GH_TOKEN=${{ secrets.GITHUB_TOKEN }}",
		"jobs.finalize-release.steps.Publish GitHub Release#1.env.GH_TOKEN=${{ secrets.GITHUB_TOKEN }}",
		"jobs.finalize-release.steps.Push GitHub Action floating tags#1.env.PUSH_TOKEN=${{ secrets.GITHUB_TOKEN }}",
		"jobs.homebrew-tap-token-gate.steps.Detect tap token#1.env.HOMEBREW_TAP_TOKEN=${{ secrets.HOMEBREW_TAP_TOKEN }}",
		"jobs.update-homebrew-tap.steps.Regenerate and push formula changes#1.env.HOMEBREW_TAP_TOKEN=${{ secrets.HOMEBREW_TAP_TOKEN }}",
		"jobs.push-feature-release-history.steps.Push feature history commit#1.env.PUSH_TOKEN=${{ secrets.MAIN_SYNC_PAT || secrets.GITHUB_TOKEN }}",
	}
	wantImplicitGitHubTokenPaths := []string{
		"jobs.build-darwin-amd64.steps.Checkout release source#1.uses",
		"jobs.build-darwin-amd64.steps.Setup Go#1.uses",
		"jobs.build-darwin-amd64.steps.Upload Darwin amd64 artifacts#1.uses",
		"jobs.build-vscode-extension.steps.Checkout release source#1.uses",
		"jobs.build-vscode-extension.steps.Setup Go#1.uses",
		"jobs.build-vscode-extension.steps.Setup Node#1.uses",
		"jobs.build-vscode-extension.steps.Upload VS Code extension artifact#1.uses",
		"jobs.orchestrate-release.uses",
		"jobs.prepare-feature-release-history-push.steps.Download feature history patch#1.uses",
		"jobs.prepare-feature-release-history-push.steps.Upload prepared trusted feature history worktree#1.uses",
		"jobs.prepare-feature-release-history.steps.Checkout trusted main tooling#1.uses",
		"jobs.prepare-feature-release-history.steps.Checkout validated release data#1.uses",
		"jobs.prepare-feature-release-history.steps.Setup Go#1.uses",
		"jobs.prepare-feature-release-history.steps.Upload feature history patch#1.uses",
		"jobs.prepare-marketplace-toolchain.steps.Checkout trusted main Marketplace manifests#1.uses",
		"jobs.prepare-marketplace-toolchain.steps.Setup Node for Marketplace tooling#1.uses",
		"jobs.prepare-marketplace-toolchain.steps.Upload Marketplace toolchain#1.uses",
		"jobs.prepare-release-feature-report.steps.Checkout release source#1.uses",
		"jobs.prepare-release-feature-report.steps.Setup Go#1.uses",
		"jobs.prepare-release-feature-report.steps.Upload feature flag release report#1.uses",
		"jobs.prepare-release-publication.steps.Download Darwin amd64 release artifact#1.uses",
		"jobs.prepare-release-publication.steps.Download Darwin arm64 release artifact#1.uses",
		"jobs.prepare-release-publication.steps.Download Linux and Windows release artifacts#1.uses",
		"jobs.prepare-release-publication.steps.Download VS Code extension release artifact#1.uses",
		"jobs.prepare-release-publication.steps.Download feature flag release report#1.uses",
		"jobs.prepare-release-publication.steps.Upload release publication inputs#1.uses",
		"jobs.prepare-release.steps.Checkout release metadata#1.uses",
		"jobs.prepare-release.steps.Run release-please#1.uses",
		"jobs.publish-marketplace.steps.Download Marketplace toolchain#1.uses",
		"jobs.publish-marketplace.steps.Download release publication inputs#1.uses",
		"jobs.publish-marketplace.steps.Setup Node for Marketplace tooling#1.uses",
		"jobs.publish.steps.Download release publication inputs#1.uses",
		"jobs.push-feature-release-history.steps.Download prepared trusted feature history worktree#1.uses",
		"jobs.validate-homebrew-tap.steps.Set up Homebrew#1.uses",
	}
	for _, path := range wantImplicitGitHubTokenPaths {
		want = append(want, path+"="+workflowImplicitGitHubToken)
	}
	slices.Sort(want)
	got := workflowCredentialBindings(t, &workflow)

	if !slices.Equal(got, want) {
		t.Fatalf("release publication secret bindings = %#v, want %#v", got, want)
	}
}

func TestWorkflowCredentialBindingsAuditRawWorkflowFieldsAliasesAndDuplicateSteps(t *testing.T) {
	t.Parallel()

	const config = `env:
  GH_TOKEN: ${{ secrets.WORKFLOW_TOKEN }}
jobs:
  publisher:
    env:
      ANCHORED_TOKEN: &anchored-token ${{ secrets.ALIAS_TOKEN }}
    container:
      credentials:
        password: ${{ secrets['CONTAINER_TOKEN'] }}
    steps:
      - name: Publish
        env:
          TOKEN: ${{ github["token"] }}
      - name: Publish
        env:
          TOKEN: ${{ secrets.SECOND_TOKEN }}
      - name: Publish all
        env:
          ALL_SECRETS: ${{ toJson(secrets) }}
          ANCHORED_TOKEN: *anchored-token
          GITHUB_CONTEXT: ${{ toJson(github) }}
          GITHUB_WILDCARD_CONTEXT: ${{ toJson(github.*) }}
          EVENT_NAME: ${{ github.event_name }}
      - name: Explain configuration
        run: echo "no secrets configured"
      - name: Checkout implicitly
        uses: actions/checkout@v4
        with:
          if: secrets.NOT_AN_EXPRESSION
          uses: documentation-only-input
  future-publisher:
    if: github.token != '' && secrets.IF_TOKEN != ''
    services:
      registry:
        credentials:
          password: ${{ secrets.SERVICE_TOKEN }}
  inherited-publisher:
    uses: ./.github/workflows/publish.yml
    secrets: inherit
`
	var workflow yaml.Node
	if err := yaml.Unmarshal([]byte(config), &workflow); err != nil {
		t.Fatalf("parse workflow fixture: %v", err)
	}
	want := []string{
		"env.GH_TOKEN=${{ secrets.WORKFLOW_TOKEN }}",
		"jobs.future-publisher.services.registry.credentials.password=${{ secrets.SERVICE_TOKEN }}",
		"jobs.future-publisher.if=github.token != '' && secrets.IF_TOKEN != ''",
		"jobs.inherited-publisher.secrets=inherit",
		"jobs.inherited-publisher.uses=" + workflowImplicitGitHubToken,
		"jobs.publisher.container.credentials.password=${{ secrets['CONTAINER_TOKEN'] }}",
		"jobs.publisher.env.ANCHORED_TOKEN=${{ secrets.ALIAS_TOKEN }}",
		"jobs.publisher.steps.Publish#1.env.TOKEN=${{ github[\"token\"] }}",
		"jobs.publisher.steps.Publish#2.env.TOKEN=${{ secrets.SECOND_TOKEN }}",
		"jobs.publisher.steps.Publish all#1.env.ALL_SECRETS=${{ toJson(secrets) }}",
		"jobs.publisher.steps.Publish all#1.env.ANCHORED_TOKEN=${{ secrets.ALIAS_TOKEN }}",
		"jobs.publisher.steps.Publish all#1.env.GITHUB_CONTEXT=${{ toJson(github) }}",
		"jobs.publisher.steps.Publish all#1.env.GITHUB_WILDCARD_CONTEXT=${{ toJson(github.*) }}",
		"jobs.publisher.steps.Checkout implicitly#1.uses=" + workflowImplicitGitHubToken,
	}
	slices.Sort(want)

	got := workflowCredentialBindings(t, &workflow)
	if !slices.Equal(got, want) {
		t.Fatalf("raw workflow credential bindings = %#v, want %#v", got, want)
	}
}

func workflowCredentialBindings(t *testing.T, workflow *yaml.Node) []string {
	t.Helper()

	if workflow == nil || workflow.Kind != yaml.DocumentNode || len(workflow.Content) != 1 {
		t.Fatal("workflow must define one YAML document")
	}
	bindings := make([]string, 0)
	collectWorkflowCredentialBindings(t, workflow.Content[0], "", &bindings, make(map[*yaml.Node]bool), workflowCredentialRootRole)
	slices.Sort(bindings)
	return bindings
}

func collectWorkflowCredentialBindings(t *testing.T, node *yaml.Node, path string, bindings *[]string, resolvingAliases map[*yaml.Node]bool, role workflowCredentialRole) {
	switch node.Kind {
	case yaml.ScalarNode:
		collectWorkflowCredentialScalarBindings(node, path, bindings, role)
	case yaml.AliasNode:
		collectWorkflowCredentialAliasBindings(t, node, path, bindings, resolvingAliases, role)
	case yaml.MappingNode:
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index].Value
			value := node.Content[index+1]
			childPath := workflowCredentialChildPath(path, key)
			if workflowRoleReceivesImplicitGitHubToken(role, key) {
				*bindings = append(*bindings, childPath+"="+workflowImplicitGitHubToken)
			}
			childRole := workflowCredentialChildRole(role, key)
			collectWorkflowCredentialBindings(t, value, childPath, bindings, resolvingAliases, childRole)
		}
	case yaml.SequenceNode:
		if role == workflowCredentialStepsRole {
			collectWorkflowCredentialStepBindings(t, node, path, bindings, resolvingAliases)
			return
		}
		for index, child := range node.Content {
			collectWorkflowCredentialBindings(t, child, path+"["+strconv.Itoa(index)+"]", bindings, resolvingAliases, workflowCredentialGenericRole)
		}
	}
}

func collectWorkflowCredentialScalarBindings(node *yaml.Node, path string, bindings *[]string, role workflowCredentialRole) {
	if workflowValueReferencesCredential(role, node.Value) {
		*bindings = append(*bindings, path+"="+node.Value)
	}
}

func collectWorkflowCredentialAliasBindings(t *testing.T, alias *yaml.Node, path string, bindings *[]string, resolvingAliases map[*yaml.Node]bool, role workflowCredentialRole) {
	if alias.Alias == nil {
		return
	}
	if resolvingAliases[alias.Alias] {
		t.Fatalf("workflow credential alias cycle at %s", path)
	}
	resolvingAliases[alias.Alias] = true
	collectWorkflowCredentialBindings(t, alias.Alias, path, bindings, resolvingAliases, role)
	delete(resolvingAliases, alias.Alias)
}

func workflowCredentialChildRole(parent workflowCredentialRole, key string) workflowCredentialRole {
	switch parent {
	case workflowCredentialRootRole:
		if key == "jobs" {
			return workflowCredentialJobsRole
		}
	case workflowCredentialJobsRole:
		return workflowCredentialJobRole
	case workflowCredentialJobRole:
		if key == "steps" {
			return workflowCredentialStepsRole
		}
		if key == "if" {
			return workflowCredentialExpressionRole
		}
		if key == "secrets" {
			return workflowCredentialInheritedSecretsRole
		}
	case workflowCredentialStepRole:
		if key == "if" {
			return workflowCredentialExpressionRole
		}
	}
	return workflowCredentialGenericRole
}

func workflowRoleReceivesImplicitGitHubToken(role workflowCredentialRole, key string) bool {
	return key == "uses" && (role == workflowCredentialJobRole || role == workflowCredentialStepRole)
}

func workflowCredentialChildPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func collectWorkflowCredentialStepBindings(t *testing.T, steps *yaml.Node, path string, bindings *[]string, resolvingAliases map[*yaml.Node]bool) {
	stepNameOccurrences := make(map[string]int)
	for _, step := range steps.Content {
		nameNode := workflowYAMLMappingValue(step, "name")
		stepName := "<unnamed>"
		if nameNode != nil && nameNode.Value != "" {
			stepName = nameNode.Value
		}
		stepNameOccurrences[stepName]++
		stepPath := path + "." + stepName + "#" + strconv.Itoa(stepNameOccurrences[stepName])
		collectWorkflowCredentialBindings(t, step, stepPath, bindings, resolvingAliases, workflowCredentialStepRole)
	}
}

func workflowYAMLMappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil
		}
		node = node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}
	return nil
}

func workflowValueReferencesCredential(role workflowCredentialRole, value string) bool {
	if role == workflowCredentialInheritedSecretsRole && strings.EqualFold(strings.TrimSpace(value), "inherit") {
		return true
	}
	if role == workflowCredentialExpressionRole && workflowExpressionReferencesCredential(value) {
		return true
	}
	for remainder := value; ; {
		start := strings.Index(remainder, "${{")
		if start < 0 {
			return false
		}
		remainder = remainder[start+3:]
		end := strings.Index(remainder, "}}")
		if end < 0 {
			return false
		}
		if workflowExpressionReferencesCredential(remainder[:end]) {
			return true
		}
		remainder = remainder[end+2:]
	}
}

func workflowExpressionReferencesCredential(expression string) bool {
	normalized := strings.Join(strings.Fields(strings.ToLower(expression)), "")
	normalized = strings.ReplaceAll(normalized, `"`, `'`)
	return workflowSecretsContextPattern.MatchString(normalized) ||
		workflowGitHubCredentialContextPattern.MatchString(normalized) ||
		workflowBareGitHubContextPattern.MatchString(normalized)
}

func TestReleaseWorkflowDownloadsReleaseArtifactsByExactName(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	expectedArtifacts := map[string]string{
		"Download feature flag release report":         "stable-feature-report",
		"Download Linux and Windows release artifacts": "release-linux-windows",
		"Download Darwin arm64 release artifact":       "release-darwin",
		"Download Darwin amd64 release artifact":       "release-darwin-amd64",
		"Download VS Code extension release artifact":  "release-vscode-extension",
	}
	for stepName, artifactName := range expectedArtifacts {
		downloadStep := workflowStepByName(t, workflow.Jobs, "prepare-release-publication", stepName)
		if downloadStep.With["name"] != artifactName {
			t.Fatalf("%s artifact name = %q", stepName, downloadStep.With["name"])
		}
		for _, forbidden := range []string{"pattern", "merge-multiple"} {
			if _, ok := downloadStep.With[forbidden]; ok {
				t.Fatalf("%s must not use with.%s", stepName, forbidden)
			}
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

	preparation := workflowJobByName(t, workflow.Jobs, "prepare-marketplace-toolchain")
	assertWorkflowJobNeeds(t, preparation, "Marketplace tooling preparation", workflowJobNeeds{"prepare-release"})
	assertWorkflowJobPermissions(t, preparation, "Marketplace tooling preparation", map[string]string{"contents": "read"})
	assertWorkflowJobEnvEmpty(t, preparation, "Marketplace tooling preparation")
	if len(preparation.Outputs) != 1 || preparation.Outputs["configured"] != "${{ steps.gate.outputs.configured }}" {
		t.Fatalf("Marketplace tooling preparation outputs = %#v, want only the token configuration boolean", preparation.Outputs)
	}
	assertWorkflowStringValues(t, []workflowStringValue{
		{
			label: "Marketplace tooling preparation if",
			got:   preparation.If,
			want:  "${{ needs.prepare-release.outputs.release_created == 'true' }}",
		},
	})
	assertWorkflowJobOmitsText(t, preparation, "VSCE_PAT", "Marketplace tooling preparation must not receive VSCE_PAT")
	assertMarketplacePreparationGate(t, preparation)
	assertWorkflowJobCheckoutsDisablePersistedCredentials(t, preparation, "prepare-marketplace-toolchain")

	checkoutStep := workflowStepByName(t, workflow.Jobs, "prepare-marketplace-toolchain", "Checkout trusted main Marketplace manifests")
	setupStep := workflowStepByName(t, workflow.Jobs, "prepare-marketplace-toolchain", "Setup Node for Marketplace tooling")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "trusted Marketplace checkout ref", got: checkoutStep.With["ref"], want: "${{ needs.prepare-release.outputs.trusted_main_sha }}"},
		{label: "trusted Marketplace checkout path", got: checkoutStep.With["path"], want: ""},
		{label: "Marketplace tooling Node version", got: setupStep.With["node-version"], want: "24"},
	})

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
		`trusted_vsce_link="${scratch_dir}/node_modules/.bin/vsce"`,
		`find "${scratch_dir}/node_modules" -type l ! -path "${trusted_vsce_link}" -delete`,
		`find "${scratch_dir}/node_modules" -type l ! -path "${trusted_vsce_link}" -print -quit | grep -q .`,
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
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "Marketplace toolchain artifact name", got: uploadStep.With["name"], want: "marketplace-toolchain"},
		{label: "Marketplace toolchain artifact missing-file behavior", got: uploadStep.With["if-no-files-found"], want: "error"},
	})
	assertTextContainsAll(t, uploadStep.With["path"], "Marketplace toolchain artifact path", []string{
		".artifacts/marketplace-toolchain/vsce-toolchain.tar.gz",
		".artifacts/marketplace-toolchain/SHA256SUMS",
	})
}

func assertMarketplacePreparationGate(t *testing.T, preparation workflowJobConfig) {
	t.Helper()

	if len(preparation.Steps) == 0 || preparation.Steps[0].Name != "Detect Marketplace token" {
		t.Fatal("Marketplace token detection must be the first fresh-runner step")
	}
	gate := preparation.Steps[0]
	if gate.ID != "gate" {
		t.Fatalf("Marketplace token detector id = %q, want gate", gate.ID)
	}
	if len(gate.Env) != 2 || gate.Env["VSCE_PUBLISH"] != "${{ secrets.VSCE_PUBLISH }}" || gate.Env["PATH"] != "/usr/bin:/bin" {
		t.Fatalf("Marketplace token detector env = %#v, want only the scoped token and trusted PATH", gate.Env)
	}
	wantShell := "/usr/bin/env -u BASH_ENV -u ENV -u PROMPT_COMMAND -u PS4 -u SHELLOPTS -u BASHOPTS /usr/bin/bash --noprofile --norc -euo pipefail {0}"
	if gate.Shell != wantShell {
		t.Fatalf("Marketplace token detector shell = %q, want sanitized shell", gate.Shell)
	}
	assertWorkflowStepRunContainsAll(t, gate, "Marketplace token detector", []string{
		`if [ -n "${VSCE_PUBLISH:-}" ]; then`,
		`/usr/bin/printf '%s\n' 'configured=true' >> "$GITHUB_OUTPUT"`,
		`/usr/bin/printf '%s\n' 'configured=false' >> "$GITHUB_OUTPUT"`,
	})
	if strings.Count(gate.Run, `>> "$GITHUB_OUTPUT"`) != 2 {
		t.Fatal("Marketplace token detector must write only configured=true|false to GITHUB_OUTPUT")
	}

	skip := workflowStepByName(t, map[string]workflowJobConfig{"prepare-marketplace-toolchain": preparation}, "prepare-marketplace-toolchain", "Skip Marketplace toolchain prep when token is missing")
	if skip.If != "${{ steps.gate.outputs.configured != 'true' }}" || !strings.Contains(skip.Run, "VSCE publish token not configured; skipping Marketplace toolchain preparation.") {
		t.Fatal("Marketplace tooling preparation must preserve an explicit no-token skip message")
	}
	assertWorkflowStepOrder(t, preparation, "Detect Marketplace token", "Skip Marketplace toolchain prep when token is missing", "Checkout trusted main Marketplace manifests")

	configuredCondition := "${{ steps.gate.outputs.configured == 'true' }}"
	for _, stepName := range []string{
		"Checkout trusted main Marketplace manifests",
		"Setup Node for Marketplace tooling",
		"Validate Marketplace tooling lockfile",
		"Prepare integrity-bound Marketplace toolchain",
		"Archive Marketplace toolchain",
		"Upload Marketplace toolchain",
	} {
		step := workflowStepByName(t, map[string]workflowJobConfig{"prepare-marketplace-toolchain": preparation}, "prepare-marketplace-toolchain", stepName)
		if step.If != configuredCondition {
			t.Fatalf("Marketplace preparation step %q condition = %q, want %q", stepName, step.If, configuredCondition)
		}
	}
}

func TestReleaseWorkflowPublishesMarketplaceFromValidatedArtifacts(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	marketplace := workflowJobByName(t, workflow.Jobs, "publish-marketplace")
	assertWorkflowJobPermissions(t, marketplace, "Marketplace publication", nil)
	assertWorkflowJobEnvEmpty(t, marketplace, "Marketplace publication")
	assertWorkflowStringValues(t, []workflowStringValue{
		{
			label: "Marketplace publication if",
			got:   marketplace.If,
			want:  "${{ needs.prepare-release.outputs.release_created == 'true' }}",
		},
	})
	assertWorkflowEnvKeyOnlyOnStep(t, workflow.Jobs, "VSCE_PAT", "publish-marketplace", "Publish VS Code extension to Marketplace")
	assertMarketplacePublicationGate(t, workflow.Jobs)
	assertMarketplaceSecretBoundary(t, workflow.Jobs)
	assertWorkflowJobOmitsCheckout(t, marketplace, "Marketplace publication")
	assertWorkflowJobStepRunsOmitAllFold(t, marketplace, "Marketplace publication", []string{"npm ", "npx ", "git ", "go run ./", "make ", "scripts/", "./extensions/"})

	downloadStep := workflowStepByName(t, workflow.Jobs, "publish-marketplace", "Download release publication inputs")
	toolchainDownload := workflowStepByName(t, workflow.Jobs, "publish-marketplace", "Download Marketplace toolchain")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "Marketplace release input artifact name", got: downloadStep.With["name"], want: "publication-inputs"},
		{label: "Marketplace release input artifact path", got: downloadStep.With["path"], want: "publication-inputs"},
		{label: "Marketplace toolchain artifact name", got: toolchainDownload.With["name"], want: "marketplace-toolchain"},
		{label: "Marketplace toolchain artifact path", got: toolchainDownload.With["path"], want: "marketplace-toolchain-input"},
	})

	validateStep := workflowStepByName(t, workflow.Jobs, "publish-marketplace", "Validate Marketplace publication inputs")
	assertWorkflowStringValues(t, []workflowStringValue{{
		label: "Marketplace input validation shell",
		got:   validateStep.Shell,
		want:  "/usr/bin/env -u BASH_ENV -u ENV -u PROMPT_COMMAND -u PS4 -u SHELLOPTS -u BASHOPTS /usr/bin/bash --noprofile --norc -euo pipefail {0}",
	}})
	assertWorkflowStepEnv(t, validateStep, "Marketplace input validation", map[string]string{
		"RELEASE_VERSION": "${{ needs.prepare-release.outputs.version }}",
	})
	assertWorkflowStepRunContainsAll(t, validateStep, "Marketplace input validation step", []string{
		`publication_dir="${GITHUB_WORKSPACE}/publication-inputs"`,
		`artifact_dir="${GITHUB_WORKSPACE}/marketplace-toolchain-input"`,
		`archive_file="${artifact_dir}/vsce-toolchain.tar.gz"`,
		`if find "${publication_dir}" -mindepth 1 ! -type f ! -type d -print -quit | grep -q .; then`,
		`if find "${artifact_dir}" -mindepth 1 -maxdepth 1 ! -type f -print -quit | grep -q .; then`,
		`if [ "$(find "${artifact_dir}" -maxdepth 1 -type f | wc -l)" -ne 2 ]; then`,
		`if [ "$(stat --format=%s "${archive_file}")" -gt 268435456 ]; then`,
		`expected_vsix="lopper-vscode-${RELEASE_VERSION}.vsix"`,
		`vsix_path="${publication_dir}/dist/${expected_vsix}"`,
		`python3 - "${vsix_path}" "${RELEASE_VERSION}" <<'PY'`,
		`import posixpath`,
		`import stat`,
		`infos = archive.infolist()`,
		`if len(infos) == 0 or len(infos) > 65536:`,
		`total_size = 0`,
		`seen_names = set()`,
		`raw_name = info.filename`,
		`normalized_name = posixpath.normpath(raw_name)`,
		`if raw_name.startswith("/") or not normalized_name or normalized_name == "." or ".." in normalized_name.split("/"):`,
		`mode = (info.external_attr >> 16) & 0o170000`,
		`if stat.S_ISLNK(mode):`,
		`if normalized_name in seen_names:`,
		`total_size += info.file_size`,
		`if total_size > 1073741824:`,
		`manifest_path = "extension/package.json"`,
		`if len(manifest_entries) != 1:`,
		`if manifest.get("publisher") != "BenRanford":`,
		`if manifest.get("name") != "vscode-lopper":`,
		`if manifest.get("version") != expected_version:`,
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
	assertWorkflowStepEnv(t, marketplaceStep, "Marketplace publication", map[string]string{
		"RELEASE_VERSION": "${{ needs.prepare-release.outputs.version }}",
		"VSCE_PAT":        "${{ secrets.VSCE_PUBLISH }}",
	})
	if marketplaceStep.Uses != "" {
		t.Fatalf("Marketplace publication must invoke the prepared toolchain directly, found action %q", marketplaceStep.Uses)
	}
	assertWorkflowStepRunContainsAll(t, marketplaceStep, "Marketplace publication step", []string{
		`if [ -z "${VSCE_PAT:-}" ]; then`,
		`vsce_bin="${RUNNER_TEMP}/vsce-toolchain/node_modules/.bin/vsce"`,
		`vsix_path="${GITHUB_WORKSPACE}/publication-inputs/dist/lopper-vscode-${RELEASE_VERSION}.vsix"`,
		`"${vsce_bin}" publish --packagePath "${vsix_path}"`,
	})
	assertWorkflowStepRunOmitsAll(t, marketplaceStep, "Marketplace publication", []string{
		"${{ needs.prepare-release.outputs.version }}",
	})
	assertWorkflowStepRunOmitsAllFold(t, marketplaceStep, "Marketplace publication", []string{
		"npx ", "npm ", "find ", "test -x", "sha256sum ", "tar ", "python", "node ",
		"curl ", "wget ", "git ", "go ", "make ", "scripts/", "./extensions/",
	})

	workflowText := readConfig(t, ".github/workflows/release.yml")
	if count := strings.Count(workflowText, "${{ secrets.VSCE_PUBLISH }}"); count != 2 {
		t.Fatalf("Marketplace secret references = %d, want isolated detection and final publication only", count)
	}
}

func TestMarketplaceToolchainValidatorAcceptsTrustedArchiveShape(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)
	validate := workflowStepByName(t, workflow.Jobs, "publish-marketplace", "Validate Marketplace publication inputs")
	validator := embeddedPythonScript(t, validate.Run, `python3 - "${archive_file}" <<'PY'`)
	archivePath := writeTarFixture(t, []tarFixtureMember{
		regularTarMember("package.json", 0o644),
		regularTarMember("package-lock.json", 0o644),
		directoryTarMember("node_modules/"),
		directoryTarMember("node_modules/.bin/"),
		directoryTarMember("node_modules/@vscode/"),
		directoryTarMember("node_modules/@vscode/vsce/"),
		regularTarMember("node_modules/@vscode/vsce/vsce", 0o755),
		{
			name:     "node_modules/.bin/vsce",
			mode:     0o777,
			typeflag: tar.TypeSymlink,
			linkname: "../@vscode/vsce/vsce",
		},
	})

	assertTarValidatorAccepts(t, validator, archivePath)
}

func TestMarketplaceToolchainValidatorRejectsManifestRootShapes(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)
	validate := workflowStepByName(t, workflow.Jobs, "publish-marketplace", "Validate Marketplace publication inputs")
	validator := embeddedPythonScript(t, validate.Run, `python3 - "${archive_file}" <<'PY'`)
	fixtures := []tarValidatorFixture{
		{
			name: "manifest descendant",
			members: []tarFixtureMember{
				regularTarMember("package.json/child", 0o644),
				regularTarMember("package-lock.json", 0o644),
				regularTarMember("node_modules/tool", 0o644),
			},
			want: "unexpected path",
		},
		{
			name: "manifest directory",
			members: []tarFixtureMember{
				directoryTarMember("package.json/"),
				regularTarMember("package-lock.json", 0o644),
				regularTarMember("node_modules/tool", 0o644),
			},
			want: "unexpected entry type",
		},
	}

	assertTarValidatorFixtures(t, validator, nil, fixtures)
}

func TestFeatureHistoryWorktreeValidatorAcceptsHiddenGitArchiveShape(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)
	validate := workflowStepByName(t, workflow.Jobs, "push-feature-release-history", "Validate prepared trusted feature history worktree")
	validator := embeddedPythonScript(t, validate.Run, `python3 - "${archive_file}" <<'PY'`)
	archivePath := writeTarFixture(t, []tarFixtureMember{
		directoryTarMember("feature-history-push/"),
		directoryTarMember("feature-history-push/.git/"),
		regularTarMember("feature-history-push/.git/HEAD", 0o644),
		directoryTarMember("feature-history-push/internal/"),
		directoryTarMember("feature-history-push/internal/featureflags/"),
		regularTarMember("feature-history-push/internal/featureflags/features.json", 0o644),
	})

	assertTarValidatorAccepts(t, validator, archivePath)
}

func TestTarValidatorsRejectNoncanonicalMemberForms(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)
	marketplaceStep := workflowStepByName(t, workflow.Jobs, "publish-marketplace", "Validate Marketplace publication inputs")
	featureHistoryStep := workflowStepByName(t, workflow.Jobs, "push-feature-release-history", "Validate prepared trusted feature history worktree")

	marketplaceBase := []tarFixtureMember{
		regularTarMember("package.json", 0o644),
		regularTarMember("package-lock.json", 0o644),
	}
	marketplaceCases := []tarValidatorFixture{
		{name: "absolute", members: []tarFixtureMember{regularTarMember("/node_modules/tool", 0o644)}, want: "unsafe path"},
		{name: "parent traversal", members: []tarFixtureMember{regularTarMember("../node_modules/tool", 0o644)}, want: "unsafe path"},
		{name: "unexpected dot root", members: []tarFixtureMember{regularTarMember(".root/tool", 0o644)}, want: "unexpected path"},
		{name: "leading dot segment", members: []tarFixtureMember{regularTarMember("./node_modules/tool", 0o644)}, want: "unsafe path"},
		{name: "internal dot segment", members: []tarFixtureMember{regularTarMember("node_modules/./tool", 0o644)}, want: "unsafe path"},
		{name: "empty segment", members: []tarFixtureMember{regularTarMember("node_modules//tool", 0o644)}, want: "unsafe path"},
		{name: "internal parent traversal", members: []tarFixtureMember{regularTarMember("node_modules/tool/../other", 0o644)}, want: "unsafe path"},
		{name: "canonical alias", members: []tarFixtureMember{regularTarMember("node_modules/tool", 0o644), regularTarMember("node_modules/./tool", 0o644)}, want: "unsafe path"},
		{name: "trusted symlink path alias", members: []tarFixtureMember{{name: "node_modules/./.bin/vsce", mode: 0o777, typeflag: tar.TypeSymlink, linkname: "../@vscode/vsce/vsce"}}, want: "unsafe path"},
		{name: "trusted symlink target alias", members: []tarFixtureMember{{name: "node_modules/.bin/vsce", mode: 0o777, typeflag: tar.TypeSymlink, linkname: "../@vscode/vsce/./vsce"}}, want: "symbolic link"},
	}
	assertTarValidatorFixtures(t, embeddedPythonScript(t, marketplaceStep.Run, `python3 - "${archive_file}" <<'PY'`), marketplaceBase, marketplaceCases)

	featureRoot := "feature-history-push"
	featureCases := []tarValidatorFixture{
		{name: "absolute", members: []tarFixtureMember{regularTarMember("/"+featureRoot+"/.git/HEAD", 0o644)}, want: "unsafe path"},
		{name: "parent traversal", members: []tarFixtureMember{regularTarMember("../"+featureRoot+"/.git/HEAD", 0o644)}, want: "unsafe path"},
		{name: "unexpected dot root", members: []tarFixtureMember{regularTarMember("."+featureRoot+"/.git/HEAD", 0o644)}, want: "unexpected path"},
		{name: "leading dot segment", members: []tarFixtureMember{regularTarMember("./"+featureRoot+"/.git/HEAD", 0o644)}, want: "unsafe path"},
		{name: "internal dot segment", members: []tarFixtureMember{regularTarMember(featureRoot+"/./.git/HEAD", 0o644)}, want: "unsafe path"},
		{name: "empty segment", members: []tarFixtureMember{regularTarMember(featureRoot+"//.git/HEAD", 0o644)}, want: "unsafe path"},
		{name: "internal parent traversal", members: []tarFixtureMember{regularTarMember(featureRoot+"/work/../.git/HEAD", 0o644)}, want: "unsafe path"},
		{name: "canonical alias", members: []tarFixtureMember{regularTarMember(featureRoot+"/.git/HEAD", 0o644), regularTarMember(featureRoot+"/./.git/HEAD", 0o644)}, want: "unsafe path"},
	}
	assertTarValidatorFixtures(t, embeddedPythonScript(t, featureHistoryStep.Run, `python3 - "${archive_file}" <<'PY'`), nil, featureCases)
}

func TestReleaseWorkflowFailsClosedWhenConfiguredMarketplaceTokenDisappears(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	publishStep := workflowStepByName(t, workflow.Jobs, "publish-marketplace", "Publish VS Code extension to Marketplace")
	cmd := exec.Command("bash", "-c", publishStep.Run)
	cmd.Env = append(os.Environ(), "RELEASE_VERSION=1.8.0", "VSCE_PAT=")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("configured Marketplace publication succeeded after its token disappeared: %s", output)
	}
	if !strings.Contains(string(output), "::error::VSCE publish token became unavailable after Marketplace publication was enabled.") {
		t.Fatalf("configured Marketplace missing-token error = %q", output)
	}
}

func assertMarketplacePublicationGate(t *testing.T, jobs map[string]workflowJobConfig) {
	t.Helper()

	marketplace := workflowJobByName(t, jobs, "publish-marketplace")
	assertWorkflowJobEnvEmpty(t, marketplace, "Marketplace publication")
	skip := workflowStepByName(t, jobs, "publish-marketplace", "Skip Marketplace publish when token is missing")
	if skip.If != "${{ needs.prepare-marketplace-toolchain.outputs.configured != 'true' }}" || !strings.Contains(skip.Run, "VSCE publish token not configured; skipping Marketplace publish.") {
		t.Fatal("Marketplace publication must preserve an explicit no-token skip message")
	}
	assertWorkflowStepOrder(t, marketplace, "Skip Marketplace publish when token is missing", "Setup Node for Marketplace tooling")

	configuredCondition := "${{ needs.prepare-marketplace-toolchain.outputs.configured == 'true' }}"
	for _, stepName := range []string{
		"Setup Node for Marketplace tooling",
		"Download release publication inputs",
		"Download Marketplace toolchain",
		"Validate Marketplace publication inputs",
		"Publish VS Code extension to Marketplace",
	} {
		step := workflowStepByName(t, jobs, "publish-marketplace", stepName)
		if step.If != configuredCondition {
			t.Fatalf("Marketplace publication step %q condition = %q, want %q", stepName, step.If, configuredCondition)
		}
	}
}

func assertMarketplaceSecretBoundary(t *testing.T, jobs map[string]workflowJobConfig) {
	t.Helper()

	credentialedSteps := 0
	for jobName, job := range jobs {
		credentialedSteps += marketplaceTokenBindingsInJob(t, jobName, job)
	}
	if credentialedSteps != 2 {
		t.Fatalf("Marketplace token-bearing steps = %d, want isolated detection and final publication only", credentialedSteps)
	}
}

func marketplaceTokenBindingsInJob(t *testing.T, jobName string, job workflowJobConfig) int {
	t.Helper()

	const secretReference = "secrets.VSCE_PUBLISH"
	if strings.Contains(job.If, secretReference) {
		t.Fatalf("Marketplace token must not reach job %q through its condition", jobName)
	}
	for key, value := range job.Env {
		if strings.Contains(value, secretReference) {
			t.Fatalf("Marketplace token must not reach job %q through env.%s", jobName, key)
		}
	}
	for key, value := range job.Outputs {
		if strings.Contains(value, secretReference) {
			t.Fatalf("Marketplace token must not reach job %q through outputs.%s", jobName, key)
		}
	}

	credentialedSteps := 0
	for _, step := range job.Steps {
		credentialedSteps += marketplaceTokenBindingsInStep(t, jobName, step)
	}
	return credentialedSteps
}

func marketplaceTokenBindingsInStep(t *testing.T, jobName string, step workflowStepConfig) int {
	t.Helper()

	const secretReference = "secrets.VSCE_PUBLISH"
	for _, value := range []string{step.If, step.Run, step.Shell, step.Uses, step.WorkingDirectory} {
		if strings.Contains(value, secretReference) {
			t.Fatalf("Marketplace token reaches unexpected %s step %q", jobName, step.Name)
		}
	}
	for key, value := range step.With {
		if strings.Contains(value, secretReference) {
			t.Fatalf("Marketplace token reaches unexpected %s step %q with.%s", jobName, step.Name, key)
		}
	}

	credentialedSteps := 0
	for key, value := range step.Env {
		if !strings.Contains(value, secretReference) {
			continue
		}
		credentialedSteps++
		detectorBinding := jobName == "prepare-marketplace-toolchain" && step.Name == "Detect Marketplace token" && key == "VSCE_PUBLISH" && value == "${{ secrets.VSCE_PUBLISH }}"
		publishBinding := jobName == "publish-marketplace" && step.Name == "Publish VS Code extension to Marketplace" && key == "VSCE_PAT" && value == "${{ secrets.VSCE_PUBLISH }}"
		if !detectorBinding && !publishBinding {
			t.Fatalf("Marketplace token reaches unexpected %s step %q env.%s", jobName, step.Name, key)
		}
	}
	return credentialedSteps
}

func TestReleaseWorkflowPublishesMarketplaceAfterGitHubReleaseBoundary(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	marketplace := workflowJobByName(t, workflow.Jobs, "publish-marketplace")
	assertWorkflowJobNeeds(t, marketplace, "Marketplace publication", workflowJobNeeds{"prepare-release", "publish", "prepare-release-publication", "prepare-marketplace-toolchain"})

	publication := workflowJobByName(t, workflow.Jobs, "publish")
	assertWorkflowJobNeeds(t, publication, "fresh release publication", workflowJobNeeds{"prepare-release", "prepare-release-publication"})
	if slices.Contains(publication.Needs, "publish-marketplace") {
		t.Fatalf("GitHub release publication must not wait for Marketplace publication: %v", publication.Needs)
	}
}

func TestReleaseWorkflowFinalizesStableReleaseAfterMarketplace(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	publish := workflowJobByName(t, workflow.Jobs, "publish")
	workflowJobByName(t, workflow.Jobs, "publish-marketplace")
	finalizer := workflowJobByName(t, workflow.Jobs, "finalize-release")
	assertWorkflowJobNeeds(t, finalizer, "release finalizer", workflowJobNeeds{"prepare-release", "publish", "publish-marketplace"})
	assertWorkflowJobPermissions(t, finalizer, "release finalizer", map[string]string{"contents": "write"})
	publishRelease := workflowStepByName(t, workflow.Jobs, "finalize-release", "Publish GitHub Release")
	assertWorkflowStepEnv(t, publishRelease, "release finalization", map[string]string{
		"GH_TOKEN":    "${{ secrets.GITHUB_TOKEN }}",
		"RELEASE_TAG": "${{ needs.prepare-release.outputs.tag }}",
	})
	assertWorkflowStepRunContainsAll(t, publishRelease, "release finalization", []string{"-F draft=false", "-f make_latest=true"})

	for _, step := range publish.Steps {
		if step.Name == "Publish GitHub Release" {
			t.Fatal("publish job must not make the GitHub release public before Marketplace publication")
		}
		if strings.Contains(step.Run, "draft=false") || strings.Contains(step.Run, "make_latest=true") {
			t.Fatalf("publish job must not finalize the GitHub release in step %q", step.Name)
		}
	}

	for _, step := range finalizer.Steps {
		if step.Name == "Prepare GitHub Action floating tags" {
			if step.Env["RELEASE_TAG"] != "${{ needs.prepare-release.outputs.tag }}" {
				t.Fatalf("finalize floating tag RELEASE_TAG env = %q", step.Env["RELEASE_TAG"])
			}
			return
		}
	}
	t.Fatal("finalize-release must own stable floating tag mutation after Marketplace publication")
}

func TestReleaseWorkflowPublishesActionFloatingTags(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	job := workflowJobByName(t, workflow.Jobs, "finalize-release")
	assertWorkflowJobNeeds(t, job, "action floating tag job", workflowJobNeeds{"prepare-release", "publish", "publish-marketplace"})
	assertWorkflowJobPermissions(t, job, "action floating tag job", map[string]string{"contents": "write"})
	assertWorkflowJobEnvEmpty(t, job, "action floating tag job")
	assertWorkflowJobOmitsCheckout(t, job, "action floating tag job")

	prepareStep := workflowStepByName(t, workflow.Jobs, "finalize-release", "Prepare GitHub Action floating tags")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "action floating tag preparation id", got: prepareStep.ID, want: "prepare_tags"},
		{label: "action floating tag preparation shell", got: prepareStep.Shell, want: "/usr/bin/env -u BASH_ENV -u ENV -u PROMPT_COMMAND -u PS4 -u SHELLOPTS -u BASHOPTS /usr/bin/bash --noprofile --norc -euo pipefail {0}"},
	})
	assertWorkflowStepEnv(t, prepareStep, "action floating tag preparation", map[string]string{
		"RELEASE_TAG": "${{ needs.prepare-release.outputs.tag }}",
		"RELEASE_SHA": "${{ needs.prepare-release.outputs.sha }}",
	})
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
	assertWorkflowStepRunOmitsAll(t, prepareStep, "action floating tag preparation", []string{"PUSH_TOKEN", "secrets.", "AUTHORIZATION: basic", "extraheader"})

	pushStep := workflowStepByName(t, workflow.Jobs, "finalize-release", "Push GitHub Action floating tags")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "action floating tag push if", got: pushStep.If, want: "${{ steps.prepare_tags.outputs.push == 'true' }}"},
		{label: "action floating tag push shell", got: pushStep.Shell, want: prepareStep.Shell},
	})
	assertWorkflowStepEnv(t, pushStep, "action floating tag push", map[string]string{
		"RELEASE_TAG": "${{ needs.prepare-release.outputs.tag }}",
		"RELEASE_SHA": "${{ needs.prepare-release.outputs.sha }}",
		"PUSH_TOKEN":  "${{ secrets.GITHUB_TOKEN }}",
	})
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
	assertTextAppearsBefore(t, pushStep.Run, `if [ "${resolved_sha}" != "${RELEASE_SHA}" ]`, `push_token="${PUSH_TOKEN}"`, "action floating tag push must validate origin, SHA, and tag targets before reading the push token")
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

	gate := workflowJobByName(t, workflow.Jobs, "homebrew-tap-token-gate")
	assertWorkflowJobNeeds(t, gate, "tap token gate", workflowJobNeeds{"prepare-release", "finalize-release"})
	assertWorkflowJobHasExplicitEmptyPermissions(t, gate, "tap token gate")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "tap token gate if", got: gate.If, want: "${{ needs.prepare-release.outputs.release_created == 'true' }}"},
		{label: "tap token gate configured output", got: gate.Outputs["configured"], want: "${{ steps.gate.outputs.configured }}"},
	})
	gateStep := workflowStepByName(t, workflow.Jobs, "homebrew-tap-token-gate", "Detect tap token")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "tap token gate step id", got: gateStep.ID, want: "gate"},
		{label: "tap token gate secret", got: gateStep.Env["HOMEBREW_TAP_TOKEN"], want: "${{ secrets.HOMEBREW_TAP_TOKEN }}"},
	})
	assertWorkflowStepRunContainsAll(t, gateStep, "tap token gate step", []string{
		`if [ -n "${HOMEBREW_TAP_TOKEN:-}" ]; then`,
		`echo "configured=true" >> "$GITHUB_OUTPUT"`,
		`echo "configured=false" >> "$GITHUB_OUTPUT"`,
	})

	validation := workflowJobByName(t, workflow.Jobs, "validate-homebrew-tap")
	assertWorkflowJobNeeds(t, validation, "tap validation", workflowJobNeeds{"prepare-release", "publish", "homebrew-tap-token-gate"})
	assertWorkflowJobPermissions(t, validation, "tap validation", map[string]string{"contents": "read"})
	publication := workflowJobByName(t, workflow.Jobs, "update-homebrew-tap")
	assertWorkflowJobNeeds(t, publication, "tap publication", workflowJobNeeds{"prepare-release", "publish", "validate-homebrew-tap"})
	assertWorkflowJobPermissions(t, publication, "tap publication", map[string]string{"contents": "read"})
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "tap validation if", got: validation.If, want: "${{ needs.prepare-release.outputs.release_created == 'true' && needs.homebrew-tap-token-gate.outputs.configured == 'true' }}"},
		{label: "tap publication if", got: publication.If, want: "${{ needs.prepare-release.outputs.release_created == 'true' }}"},
	})
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

	preparation := workflowJobByName(t, workflow.Jobs, "prepare-feature-release-history")
	assertWorkflowJobNeeds(t, preparation, "feature history preparation", workflowJobNeeds{"prepare-release", "finalize-release"})
	assertWorkflowJobPermissions(t, preparation, "feature history preparation", map[string]string{"contents": "read"})
	assertWorkflowStringValues(t, []workflowStringValue{{
		label: "feature history preparation changed output",
		got:   preparation.Outputs["changed"],
		want:  "${{ steps.stamp_history.outputs.changed }}",
	}})
	assertWorkflowJobOmitsText(t, preparation, "PUSH_TOKEN", "feature history preparation must not receive a push token")
	assertWorkflowJobOmitsText(t, preparation, "secrets.", "feature history preparation must not receive secrets")
	assertWorkflowJobCheckoutsDisablePersistedCredentials(t, preparation, "prepare-feature-release-history")

	trustedCheckout := workflowStepByName(t, workflow.Jobs, "prepare-feature-release-history", "Checkout trusted main tooling")
	releaseCheckout := workflowStepByName(t, workflow.Jobs, "prepare-feature-release-history", "Checkout validated release data")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "trusted tooling checkout ref", got: trustedCheckout.With["ref"], want: "${{ needs.prepare-release.outputs.trusted_main_sha }}"},
		{label: "trusted tooling checkout path", got: trustedCheckout.With["path"], want: ""},
		{label: "validated release data checkout ref", got: releaseCheckout.With["ref"], want: "${{ needs.prepare-release.outputs.sha }}"},
		{label: "validated release data checkout path", got: releaseCheckout.With["path"], want: "release-source"},
	})

	validateStep := workflowStepByName(t, workflow.Jobs, "prepare-feature-release-history", "Validate release data against trusted main")
	assertWorkflowStepRunContainsAll(t, validateStep, "release data validation step", []string{
		`release_commit="$(git -C release-source rev-parse HEAD)"`,
		`if [ "${release_commit}" != "${RELEASE_SHA}" ]; then`,
		`if ! git merge-base --is-ancestor "${release_commit}" HEAD; then`,
	})
	buildStep := workflowStepByName(t, workflow.Jobs, "prepare-feature-release-history", "Build trusted feature flag tool")
	assertWorkflowStringValues(t, []workflowStringValue{{label: "trusted feature flag build working directory", got: buildStep.WorkingDirectory, want: ""}})
	assertTextContainsAll(t, buildStep.Run, "trusted feature flag build step", []string{`go build -o "${RUNNER_TEMP}/featureflag" ./tools/featureflag`})

	stampStep := workflowStepByName(t, workflow.Jobs, "prepare-feature-release-history", "Stamp first stable release history")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "stamp history step id", got: stampStep.ID, want: "stamp_history"},
		{label: "stamp history working directory", got: stampStep.WorkingDirectory, want: "release-source"},
	})
	assertWorkflowStepEnvMissing(t, stampStep, "PUSH_TOKEN", "stamp history step must not expose PUSH_TOKEN to repository-controlled featureflag tooling")
	assertWorkflowStepRunContainsAll(t, stampStep, "stamp history step", []string{
		`"${RUNNER_TEMP}/featureflag" stamp-release --release "${RELEASE_TAG}"`,
		`"${RUNNER_TEMP}/featureflag" validate`,
		`echo "changed=false" >> "$GITHUB_OUTPUT"`,
		`echo "changed=true" >> "$GITHUB_OUTPUT"`,
	})
	assertWorkflowStepRunOmitsAll(t, stampStep, "stamp history step", []string{"git commit", "git push", "PUSH_TOKEN"})

	patchStep := workflowStepByName(t, workflow.Jobs, "prepare-feature-release-history", "Validate and stage feature history patch")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature history patch step if", got: patchStep.If, want: "${{ steps.stamp_history.outputs.changed == 'true' }}"},
		{label: "feature history patch working directory", got: patchStep.WorkingDirectory, want: "release-source"},
	})
	assertWorkflowStepRunContainsAll(t, patchStep, "feature history patch step", []string{
		`mapfile -t changed_files < <(git diff --name-only)`,
		`if [ "${#changed_files[@]}" -ne 1 ] || [ "${changed_files[0]}" != "internal/featureflags/features.json" ]; then`,
		`git diff --binary --full-index -- internal/featureflags/features.json > "${patch_file}"`,
		`git apply --check --reverse "${patch_file}"`,
		`sha256sum feature-history.patch > SHA256SUMS`,
	})
	assertWorkflowStepEnvMissing(t, patchStep, "PUSH_TOKEN", "feature history patch staging must be tokenless")

	uploadStep := workflowStepByName(t, workflow.Jobs, "prepare-feature-release-history", "Upload feature history patch")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature history patch upload if", got: uploadStep.If, want: "${{ steps.stamp_history.outputs.changed == 'true' }}"},
		{label: "feature history patch artifact name", got: uploadStep.With["name"], want: "feature-history-patch"},
		{label: "feature history patch artifact missing-file behavior", got: uploadStep.With["if-no-files-found"], want: "error"},
	})
	assertTextContainsAll(t, uploadStep.With["path"], "feature history patch artifact path", []string{".artifacts/feature-history.patch", ".artifacts/SHA256SUMS"})
	assertFeatureHistoryPreparationDoesNotPush(t, preparation)

	pushPreparation := workflowJobByName(t, workflow.Jobs, "prepare-feature-release-history-push")
	assertWorkflowJobNeeds(t, pushPreparation, "feature history push preparation", workflowJobNeeds{"prepare-release", "prepare-feature-release-history"})
	assertWorkflowJobPermissions(t, pushPreparation, "feature history push preparation", map[string]string{"contents": "read"})
	assertWorkflowJobEnvEmpty(t, pushPreparation, "feature history push preparation")
	assertWorkflowStringValues(t, []workflowStringValue{{
		label: "feature history push preparation if",
		got:   pushPreparation.If,
		want:  "${{ needs.prepare-release.outputs.release_created == 'true' && needs.prepare-feature-release-history.outputs.changed == 'true' }}",
	}})
	assertFeatureHistoryPublicationUsesFreshInputs(t, pushPreparation)

	downloadStep := workflowStepByName(t, workflow.Jobs, "prepare-feature-release-history-push", "Download feature history patch")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature history patch download artifact name", got: downloadStep.With["name"], want: "feature-history-patch"},
		{label: "feature history patch download artifact path", got: downloadStep.With["path"], want: "${{ runner.temp }}/feature-history-input"},
	})

	publication := workflowJobByName(t, workflow.Jobs, "push-feature-release-history")
	assertWorkflowJobNeeds(t, publication, "feature history publication", workflowJobNeeds{"prepare-feature-release-history-push"})
	assertWorkflowJobPermissions(t, publication, "feature history publication", map[string]string{"contents": "write"})
	assertWorkflowJobEnvEmpty(t, publication, "feature history publication")
	assertWorkflowStringValues(t, []workflowStringValue{{
		label: "feature history publication if",
		got:   publication.If,
		want:  "${{ needs.prepare-feature-release-history-push.result == 'success' }}",
	}})
	assertFeatureHistoryPublicationUsesFreshInputs(t, publication)
}

func TestReleaseWorkflowSkipsPrereleaseFeatureHistoryBeforeStamping(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	stampStep := workflowStepByName(t, workflow.Jobs, "prepare-feature-release-history", "Stamp first stable release history")
	runnerTemp := t.TempDir()
	invocationPath := filepath.Join(runnerTemp, "featureflag-invoked")
	featureFlagStub := "#!/bin/sh\n" +
		"printf 'featureflag invoked for %s\\n' \"$*\" >&2\n" +
		"printf '%s\\n' \"$*\" > \"$FEATUREFLAG_INVOCATION\"\n" +
		"exit 99\n"
	if err := os.WriteFile(filepath.Join(runnerTemp, "featureflag"), []byte(featureFlagStub), 0o755); err != nil {
		t.Fatalf("write featureflag stub: %v", err)
	}

	githubOutput := filepath.Join(runnerTemp, "github-output")
	cmd := exec.Command("bash", "-c", stampStep.Run)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "FEATUREFLAG_INVOCATION="+invocationPath, "GITHUB_OUTPUT="+githubOutput, "RELEASE_TAG=v1.8.2-rc.1", "RUNNER_TEMP="+runnerTemp)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run prerelease feature history step: %v\n%s", err, output)
	}
	if _, err := os.Stat(invocationPath); !os.IsNotExist(err) {
		t.Fatalf("prerelease feature history invoked featureflag: %v", err)
	}
	if !strings.Contains(string(output), "::notice::Skipping feature release history for non-stable semver release v1.8.2-rc.1.") {
		t.Fatalf("prerelease feature history notice = %q", output)
	}
	changedOutput, err := os.ReadFile(githubOutput)
	if err != nil {
		t.Fatalf("read prerelease feature history output: %v", err)
	}
	if string(changedOutput) != "changed=false\n" {
		t.Fatalf("prerelease feature history output = %q, want changed=false", changedOutput)
	}

	const stableSemverGate = `if [[ ! "${RELEASE_TAG}" =~ ^v([0-9]+)[.]([0-9]+)[.]([0-9]+)$ ]]; then`
	floatingTagStep := workflowStepByName(t, workflow.Jobs, "finalize-release", "Prepare GitHub Action floating tags")
	if !strings.Contains(floatingTagStep.Run, stableSemverGate) {
		t.Fatal("floating tag preparation must retain the stable semver gate")
	}
	gateIndex := strings.Index(stampStep.Run, stableSemverGate)
	stampIndex := strings.Index(stampStep.Run, `"${RUNNER_TEMP}/featureflag" stamp-release --release "${RELEASE_TAG}"`)
	if gateIndex < 0 || stampIndex < 0 || gateIndex >= stampIndex {
		t.Fatal("feature history preparation must apply the floating-tag stable semver gate before stamp-release")
	}

	patchStep := workflowStepByName(t, workflow.Jobs, "prepare-feature-release-history", "Validate and stage feature history patch")
	uploadStep := workflowStepByName(t, workflow.Jobs, "prepare-feature-release-history", "Upload feature history patch")
	pushPreparation := workflowJobByName(t, workflow.Jobs, "prepare-feature-release-history-push")
	publication := workflowJobByName(t, workflow.Jobs, "push-feature-release-history")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature history patch step prerelease guard", got: patchStep.If, want: "${{ steps.stamp_history.outputs.changed == 'true' }}"},
		{label: "feature history patch upload prerelease guard", got: uploadStep.If, want: "${{ steps.stamp_history.outputs.changed == 'true' }}"},
		{label: "feature history push preparation prerelease guard", got: pushPreparation.If, want: "${{ needs.prepare-release.outputs.release_created == 'true' && needs.prepare-feature-release-history.outputs.changed == 'true' }}"},
		{label: "feature history push dependency guard", got: publication.If, want: "${{ needs.prepare-feature-release-history-push.result == 'success' }}"},
	})
}

func TestReleaseWorkflowPushesFeatureHistoryFromFreshValidatedCommit(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	prepareStep := workflowStepByName(t, workflow.Jobs, "prepare-feature-release-history-push", "Prepare trusted feature history commit")
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
		`input_dir="${RUNNER_TEMP}/feature-history-input"`,
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
	if len(pushStep.Env) != 3 || pushStep.Env["RELEASE_TAG"] != "${{ needs.prepare-feature-release-history-push.outputs.release_tag }}" || pushStep.Env["RELEASE_SHA"] != "${{ needs.prepare-feature-release-history-push.outputs.release_sha }}" || pushStep.Env["PUSH_TOKEN"] != "${{ secrets.MAIN_SYNC_PAT || secrets.GITHUB_TOKEN }}" {
		t.Fatalf("feature history push env = %#v", pushStep.Env)
	}
	assertWorkflowStepRunContainsAll(t, pushStep, "feature history push", []string{
		"git_bin=/usr/bin/git",
		"env_bin=/usr/bin/env",
		`git_home="$("${mktemp_bin}" -d)"`,
		`repo_dir="${RUNNER_TEMP}/prepared-feature-release-history/feature-history-push"`,
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

func TestReleaseWorkflowSplitsFeatureHistoryPreparationFromTokenedPush(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	preparation := workflowJobByName(t, workflow.Jobs, "prepare-feature-release-history-push")
	assertWorkflowJobNeeds(t, preparation, "feature history push preparation", workflowJobNeeds{"prepare-release", "prepare-feature-release-history"})
	assertWorkflowJobPermissions(t, preparation, "feature history push preparation", map[string]string{"contents": "read"})
	assertWorkflowJobOmitsText(t, preparation, "PUSH_TOKEN", "feature history push preparation must not receive PUSH_TOKEN")
	assertWorkflowJobOmitsText(t, preparation, "secrets.MAIN_SYNC_PAT", "feature history push preparation must not receive MAIN_SYNC_PAT")
	workflowStepByName(t, workflow.Jobs, "prepare-feature-release-history-push", "Prepare trusted feature history commit")
	archive := workflowStepByName(t, workflow.Jobs, "prepare-feature-release-history-push", "Archive prepared trusted feature history worktree")
	assertWorkflowStepRunContainsAll(t, archive, "prepared feature history worktree archive", []string{
		`archive_file="${archive_root}/prepared-feature-release-history-worktree.tar.gz"`,
		`tar --create --gzip --file "${archive_file}" --directory "${RUNNER_TEMP}" feature-history-push`,
		`sha256sum "$(basename "${archive_file}")" > "${checksum_file}"`,
	})
	upload := workflowStepByName(t, workflow.Jobs, "prepare-feature-release-history-push", "Upload prepared trusted feature history worktree")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "prepared feature history artifact name", got: upload.With["name"], want: "prepared-feature-release-history-worktree"},
		{label: "prepared feature history artifact path", got: upload.With["path"], want: "${{ runner.temp }}/prepared-feature-release-history"},
	})

	publication := workflowJobByName(t, workflow.Jobs, "push-feature-release-history")
	assertWorkflowJobNeeds(t, publication, "feature history push", workflowJobNeeds{"prepare-feature-release-history-push"})
	assertWorkflowJobPermissions(t, publication, "feature history push", map[string]string{"contents": "write"})
	download := workflowStepByName(t, workflow.Jobs, "push-feature-release-history", "Download prepared trusted feature history worktree")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "prepared feature history download name", got: download.With["name"], want: "prepared-feature-release-history-worktree"},
		{label: "prepared feature history download path", got: download.With["path"], want: "${{ runner.temp }}/prepared-feature-release-history-input"},
	})
	validate := workflowStepByName(t, workflow.Jobs, "push-feature-release-history", "Validate prepared trusted feature history worktree")
	assertWorkflowStepRunContainsAll(t, validate, "prepared feature history worktree validation", []string{
		`sha256sum --check --strict "${checksum_file}"`,
		`python3 - "${archive_file}" <<'PY'`,
		`if member.issym() or member.islnk():`,
		`tar --extract --gzip --no-same-owner --no-same-permissions --file "${archive_file}" --directory "${worktree_parent}"`,
		`if [ ! -d "${worktree_dir}/.git" ] || [ -L "${worktree_dir}" ] || [ -L "${worktree_dir}/.git" ]; then`,
	})
	assertWorkflowStepEnvMissing(t, validate, "PUSH_TOKEN", "prepared worktree validation must be tokenless")
	push := workflowStepByName(t, workflow.Jobs, "push-feature-release-history", "Push feature history commit")
	if push.Env["PUSH_TOKEN"] != "${{ secrets.MAIN_SYNC_PAT || secrets.GITHUB_TOKEN }}" {
		t.Fatalf("feature history PUSH_TOKEN = %q", push.Env["PUSH_TOKEN"])
	}
	if _, ok := push.Env["READ_TOKEN"]; ok {
		t.Fatal("feature history push must not receive READ_TOKEN")
	}
}

func TestReleaseWorkflowManualCheckoutUsesReadOnlyToken(t *testing.T) {
	t.Parallel()

	var workflow struct {
		Jobs map[string]workflowJobConfig `yaml:"jobs"`
	}
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	step := workflowStepByName(t, workflow.Jobs, "prepare-release", "Checkout release metadata")
	if got := workflowStepWithString(t, step, "token"); got != "${{ secrets.GITHUB_TOKEN }}" {
		t.Fatalf("manual release checkout token = %q, want GITHUB_TOKEN-only checkout", got)
	}
	if got := workflowStepWithString(t, step, "persist-credentials"); got != "false" {
		t.Fatalf("manual release checkout persist-credentials = %q, want false", got)
	}
}

func TestReleaseWorkflowManualReleaseVerifiesExistingTagsViaGitHubAPI(t *testing.T) {
	t.Parallel()

	var workflow struct {
		Jobs map[string]workflowJobConfig `yaml:"jobs"`
	}
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	step := workflowStepByName(t, workflow.Jobs, "prepare-release", "Prepare manual release")
	assertWorkflowStepRunContainsAll(t, step, "manual release existing-tag verification step", []string{
		`if ! gh api "repos/${GITHUB_REPOSITORY}/git/ref/tags/${encoded_tag}" >"${release_ref_json}" 2>/dev/null; then`,
		`existing_ref_type="$(jq -r '.object.type // empty' "${release_ref_json}")"`,
		`case "${existing_ref_type}" in`,
		`if ! gh api "repos/${GITHUB_REPOSITORY}/git/tags/${existing_ref_sha}" >"${annotated_tag_json}" 2>/dev/null; then`,
		`echo "::error::Existing release ${tag} target commit could not be verified." >&2`,
		`resolved_sha="${existing_commit}"`,
	})
	if strings.Contains(step.Run, `git fetch --force origin "refs/tags/${tag}:refs/tags/${tag}"`) {
		t.Fatal("manual release existing-tag verification must not rely on an unauthenticated tag fetch")
	}
	if strings.Contains(step.Run, `target_commitish`) {
		t.Fatal("manual release existing-tag verification must fail closed instead of trusting release target_commitish metadata")
	}
	if strings.Contains(step.Run, `release_json`) {
		t.Fatal("manual release existing-release probe must not persist an unused response body")
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

func TestReleaseArchiveProducersUseFreshExactArtifactStaging(t *testing.T) {
	t.Parallel()
	const hardenedShell = "/usr/bin/env -u BASH_ENV -u ENV -u PROMPT_COMMAND -u PS4 -u SHELLOPTS -u BASHOPTS /usr/bin/bash --noprofile --norc -euo pipefail {0}"

	testCases := []struct {
		name             string
		path             string
		jobName          string
		resetStepName    string
		buildStepName    string
		validateStepName string
		uploadStepName   string
		releaseTag       string
		expectedRuntime  []string
		expectedUploads  []string
	}{
		{
			name:             "stable Darwin amd64",
			path:             ".github/workflows/release.yml",
			jobName:          "build-darwin-amd64",
			resetStepName:    "Reset Darwin amd64 artifact staging",
			buildStepName:    "Build Darwin amd64 release artifact",
			validateStepName: "Validate Darwin amd64 release artifact",
			uploadStepName:   "Upload Darwin amd64 artifacts",
			releaseTag:       "${{ needs.prepare-release.outputs.tag }}",
			expectedRuntime: []string{
				`dist/lopper_${RELEASE_TAG}_darwin_amd64.tar.gz`,
			},
			expectedUploads: []string{
				"dist/lopper_${{ needs.prepare-release.outputs.tag }}_darwin_amd64.tar.gz",
			},
		},
		{
			name:             "orchestrated Linux and Windows",
			path:             ".github/workflows/release-orchestration.yml",
			jobName:          "build-linux-windows",
			resetStepName:    "Reset Linux/Windows artifact staging",
			buildStepName:    "Build Linux/Windows release artifacts",
			validateStepName: "Validate Linux/Windows release artifacts",
			uploadStepName:   "Upload Linux/Windows artifacts",
			releaseTag:       "${{ inputs.tag }}",
			expectedRuntime: []string{
				`dist/lopper_${RELEASE_TAG}_linux_amd64.tar.gz`,
				`dist/lopper_${RELEASE_TAG}_linux_arm64.tar.gz`,
				`dist/lopper_${RELEASE_TAG}_windows_amd64.zip`,
				`dist/lopper_${RELEASE_TAG}_windows_arm64.zip`,
			},
			expectedUploads: []string{
				"dist/lopper_${{ inputs.tag }}_linux_amd64.tar.gz",
				"dist/lopper_${{ inputs.tag }}_linux_arm64.tar.gz",
				"dist/lopper_${{ inputs.tag }}_windows_amd64.zip",
				"dist/lopper_${{ inputs.tag }}_windows_arm64.zip",
			},
		},
		{
			name:             "orchestrated Darwin arm64",
			path:             ".github/workflows/release-orchestration.yml",
			jobName:          "build-darwin",
			resetStepName:    "Reset Darwin arm64 artifact staging",
			buildStepName:    "Build Darwin release artifact",
			validateStepName: "Validate Darwin arm64 release artifact",
			uploadStepName:   "Upload Darwin artifacts",
			releaseTag:       "${{ inputs.tag }}",
			expectedRuntime: []string{
				`dist/lopper_${RELEASE_TAG}_darwin_arm64.tar.gz`,
			},
			expectedUploads: []string{
				"dist/lopper_${{ inputs.tag }}_darwin_arm64.tar.gz",
			},
		},
		{
			name:             "rolling Darwin amd64",
			path:             ".github/workflows/rolling.yml",
			jobName:          "build-darwin-amd64-rolling",
			resetStepName:    "Reset Darwin amd64 rolling artifact staging",
			buildStepName:    "Build Darwin amd64 rolling artifact",
			validateStepName: "Validate Darwin amd64 rolling artifact",
			uploadStepName:   "Upload Darwin amd64 rolling artifacts",
			releaseTag:       "${{ needs.prepare-rolling.outputs.tag }}",
			expectedRuntime: []string{
				`dist/lopper_${RELEASE_TAG}_darwin_amd64.tar.gz`,
			},
			expectedUploads: []string{
				"dist/lopper_${{ needs.prepare-rolling.outputs.tag }}_darwin_amd64.tar.gz",
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var workflow workflowConfig
			readYAMLConfig(t, tc.path, &workflow)
			job := workflowJobByName(t, workflow.Jobs, tc.jobName)
			resetIndex := workflowStepIndexByName(t, workflow.Jobs, tc.jobName, tc.resetStepName)
			buildIndex := workflowStepIndexByName(t, workflow.Jobs, tc.jobName, tc.buildStepName)
			validateIndex := workflowStepIndexByName(t, workflow.Jobs, tc.jobName, tc.validateStepName)
			uploadIndex := workflowStepIndexByName(t, workflow.Jobs, tc.jobName, tc.uploadStepName)
			if buildIndex != resetIndex+1 || validateIndex != buildIndex+1 || uploadIndex != validateIndex+1 {
				t.Fatal("archive release producer must reset, build, validate, and upload in one contiguous sequence")
			}

			resetStep := job.Steps[resetIndex]
			assertWorkflowStringValues(t, []workflowStringValue{
				{label: tc.resetStepName + " shell", got: resetStep.Shell, want: hardenedShell},
			})
			assertWorkflowStepEnv(t, resetStep, tc.resetStepName, map[string]string{"PATH": "/usr/bin:/bin"})
			assertWorkflowStepRunContainsAll(t, resetStep, tc.resetStepName, []string{`rm -rf -- dist`, `mkdir -- dist`})
			assertTextAppearsBefore(t, resetStep.Run, `rm -rf -- dist`, `mkdir -- dist`, "archive staging must remove checkout-provided files before recreating dist")

			validateStep := job.Steps[validateIndex]
			assertWorkflowStringValues(t, []workflowStringValue{
				{label: tc.validateStepName + " shell", got: validateStep.Shell, want: hardenedShell},
			})
			assertWorkflowStepEnv(t, validateStep, tc.validateStepName, map[string]string{
				"PATH":        "/usr/bin:/bin",
				"RELEASE_TAG": tc.releaseTag,
			})
			validationContract := []string{
				`find -P dist -mindepth 1 -maxdepth 1 ! -type f -print -quit`,
				`find -P dist -mindepth 1 -maxdepth 1 -type f | wc -l`,
				`[ ! -f "${artifact}" ] || [ -L "${artifact}" ]`,
				`[ ! -s "${artifact}" ]`,
			}
			validationContract = append(validationContract, tc.expectedRuntime...)
			assertWorkflowStepRunContainsAll(t, validateStep, tc.validateStepName, validationContract)

			uploadStep := job.Steps[uploadIndex]
			gotUploads := strings.Split(strings.TrimSpace(uploadStep.With["path"]), "\n")
			if !slices.Equal(gotUploads, tc.expectedUploads) {
				t.Fatalf("%s upload paths = %#v, want %#v", tc.uploadStepName, gotUploads, tc.expectedUploads)
			}
			if strings.Contains(uploadStep.With["path"], "*") {
				t.Fatalf("%s must not upload archive globs", tc.uploadStepName)
			}
			if uploadStep.With["if-no-files-found"] != "error" {
				t.Fatalf("%s must fail when an expected artifact is missing", tc.uploadStepName)
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

	step := workflowStepByName(t, workflow.Jobs, "prepare-ghcr", "Compute image tags")
	assertArchImageTagStep(t, step, step.Name, "-${{ matrix.architecture }}")

	t.Run("manifest", func(t *testing.T) {
		manifestStep := workflowStepByName(t, workflow.Jobs, "prepare-ghcr-manifest", "Compute manifest image tags")
		assertManifestImageTagStep(t, manifestStep)
	})
}

func TestReleaseOrchestrationUsesStaticGHCRPreparationMatrix(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release-orchestration.yml", &workflow)

	prepare, ok := workflow.Jobs["prepare-ghcr"]
	if !ok {
		t.Fatal("release orchestration must consolidate GHCR preparation into prepare-ghcr")
	}
	for _, legacyJob := range []string{"prepare-ghcr-amd64", "prepare-ghcr-arm64"} {
		if _, ok := workflow.Jobs[legacyJob]; ok {
			t.Fatalf("release orchestration must remove duplicate job %q", legacyJob)
		}
	}

	wantMatrix := []workflowMatrixEntry{
		{Architecture: "amd64", Platform: "linux/amd64", Runner: "ubuntu-latest"},
		{Architecture: "arm64", Platform: "linux/arm64", Runner: "ubuntu-24.04-arm"},
	}
	if !slices.Equal(prepare.Strategy.Matrix.Include, wantMatrix) {
		t.Fatalf("prepare-ghcr matrix = %#v, want %#v", prepare.Strategy.Matrix.Include, wantMatrix)
	}
	if prepare.RunsOn != "${{ matrix.runner }}" {
		t.Fatalf("prepare-ghcr runs-on = %q, want matrix runner", prepare.RunsOn)
	}
	if prepare.Strategy.FailFast == nil || *prepare.Strategy.FailFast {
		t.Fatal("prepare-ghcr matrix must keep architecture preparation legs independent")
	}
	if prepare.ContinueOnError {
		t.Fatal("prepare-ghcr must fail when an architecture preparation leg fails")
	}

	imageTags := workflowStepByName(t, workflow.Jobs, "prepare-ghcr", "Compute image tags")
	if imageTags.ID != "image_tags" {
		t.Fatalf("prepare-ghcr image tag step ID = %q, want image_tags", imageTags.ID)
	}
	build := workflowStepByName(t, workflow.Jobs, "prepare-ghcr", "Build OCI publication payload")
	if build.ID != "" {
		t.Fatalf("prepare-ghcr build step ID = %q, want no unused ID", build.ID)
	}
	if build.With["tags"] != "${{ steps.image_tags.outputs.tags }}" {
		t.Fatalf("prepare-ghcr build tags = %q, want image_tags output", build.With["tags"])
	}
	publishImages := workflow.Jobs["publish-ghcr-images"]
	assertWorkflowJobNeeds(t, publishImages, "publish-ghcr-images", workflowJobNeeds{"prepare-ghcr"})
	if publishImages.If != "" {
		t.Fatalf("publish-ghcr-images if = %q, want no override of failed-needs handling", publishImages.If)
	}
}

func TestReleaseOrchestrationUsesFreshTrustedGHCRPublicationJobs(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release-orchestration.yml", &workflow)

	architectures := []ghcrArchitecture{
		{name: "amd64", platform: "linux/amd64"},
		{name: "arm64", platform: "linux/arm64"},
	}
	assertTrustedGHCRPreparation(t, workflow)
	assertTrustedGHCRImagePublisher(t, workflow, architectures)

	t.Run("manifest", func(t *testing.T) {
		assertTrustedGHCRManifestPublisher(t, workflow)
	})
}

type ghcrArchitecture struct {
	name     string
	platform string
}

func assertTrustedGHCRPreparation(t *testing.T, workflow workflowConfig) {
	t.Helper()

	const prepareJobName = "prepare-ghcr"
	prepare := workflowJobByName(t, workflow.Jobs, prepareJobName)
	assertWorkflowJobPermissions(t, prepare, prepareJobName, map[string]string{"contents": "read"})
	for _, credentialReference := range []string{"secrets.", "github.token"} {
		assertGHCRJobDoesNotReference(t, prepare, credentialReference, prepareJobName)
	}
	checkout := workflowStepByName(t, workflow.Jobs, prepareJobName, "Checkout")
	if checkout.With["persist-credentials"] != "false" {
		t.Fatalf("%s checkout persist-credentials = %q, want false", prepareJobName, checkout.With["persist-credentials"])
	}

	build := workflowStepByName(t, workflow.Jobs, prepareJobName, "Build OCI publication payload")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: prepareJobName + " registry push", got: build.With["push"], want: "false"},
		{label: prepareJobName + " deferred provenance", got: build.With["provenance"], want: "false"},
		{label: prepareJobName + " deferred SBOM", got: build.With["sbom"], want: "false"},
		{label: prepareJobName + " platform", got: build.With["platforms"], want: "${{ matrix.platform }}"},
	})
	assertTextContainsAll(t, build.With["outputs"], prepareJobName+" OCI output", []string{
		"type=oci",
		"tar=false",
		"${{ runner.temp }}/ghcr-${{ matrix.architecture }}-publication/layout",
	})
	metadata := workflowStepByName(t, workflow.Jobs, prepareJobName, "Prepare publication metadata")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: prepareJobName + " metadata architecture", got: metadata.Env["ARCHITECTURE"], want: "${{ matrix.architecture }}"},
		{label: prepareJobName + " metadata platform", got: metadata.Env["PLATFORM"], want: "${{ matrix.platform }}"},
	})
	assertWorkflowStepRunContainsAll(t, metadata, prepareJobName+" metadata", []string{
		`payload_root="${RUNNER_TEMP}/ghcr-${ARCHITECTURE}-publication"`,
		`--arg platform "${PLATFORM}"`,
		`find -P . -type f ! -path './SHA256SUMS'`,
		`> "${payload_root}/SHA256SUMS"`,
	})
	upload := workflowStepByName(t, workflow.Jobs, prepareJobName, "Upload OCI publication payload")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: prepareJobName + " artifact name", got: upload.With["name"], want: "ghcr-${{ matrix.architecture }}-publication-payload"},
		{label: prepareJobName + " artifact path", got: upload.With["path"], want: "${{ runner.temp }}/ghcr-${{ matrix.architecture }}-publication"},
	})
}

func assertTrustedGHCRImagePublisher(t *testing.T, workflow workflowConfig, architectures []ghcrArchitecture) {
	t.Helper()

	publishImages := workflowJobByName(t, workflow.Jobs, "publish-ghcr-images")
	assertWorkflowJobNeeds(t, publishImages, "publish-ghcr-images", workflowJobNeeds{"prepare-ghcr"})
	assertWorkflowJobPermissions(t, publishImages, "publish-ghcr-images", map[string]string{"packages": "write"})
	assertFreshGHCRPublisher(t, publishImages, "Log in to GHCR")
	validation := workflowStepByName(t, workflow.Jobs, "publish-ghcr-images", "Validate OCI publication payloads")
	assertWorkflowStepRunContainsAll(t, validation, "publish-ghcr-images validation", []string{
		`expected_source_sha="${EXPECTED_SOURCE_SHA}"`,
		`expected_image_name="ghcr.io/${owner}/lopper"`,
		`find -P "${payload_root}" ! -type d ! -type f`,
		`SHA256SUMS`,
		`sha256sum --check --strict SHA256SUMS`,
		`org.opencontainers.image.revision`,
		`OnBuild`,
		`artifact payload exceeds`,
		`unexpected image reference`,
		`validate_payload amd64 linux/amd64`,
		`validate_payload arm64 linux/arm64`,
	})
	workflowStepByName(t, workflow.Jobs, "publish-ghcr-images", "Prepare trusted image promotion Dockerfiles")
	for _, architecture := range architectures {
		assertTrustedGHCRArchitecturePublication(t, workflow, architecture)
	}
}

func assertTrustedGHCRArchitecturePublication(t *testing.T, workflow workflowConfig, architecture ghcrArchitecture) {
	t.Helper()

	download := workflowStepByName(t, workflow.Jobs, "publish-ghcr-images", "Download "+architecture.name+" OCI publication payload")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "publish-ghcr-images artifact name", got: download.With["name"], want: "ghcr-" + architecture.name + "-publication-payload"},
		{label: "publish-ghcr-images artifact path", got: download.With["path"], want: "${{ runner.temp }}/ghcr-" + architecture.name + "-publication"},
	})

	publishImage := workflowStepByName(t, workflow.Jobs, "publish-ghcr-images", "Publish "+architecture.name+" release image")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "publish-ghcr-images trusted context", got: publishImage.With["context"], want: "${{ runner.temp }}/ghcr-" + architecture.name + "-promotion"},
		{label: "publish-ghcr-images trusted Dockerfile", got: publishImage.With["file"], want: "${{ runner.temp }}/ghcr-" + architecture.name + "-promotion/Dockerfile"},
		{label: "publish-ghcr-images validated tags", got: publishImage.With["tags"], want: "${{ steps.validate.outputs." + architecture.name + "_tags }}"},
		{label: "publish-ghcr-images push", got: publishImage.With["push"], want: "true"},
		{label: "publish-ghcr-images provenance", got: publishImage.With["provenance"], want: "false"},
		{label: "publish-ghcr-images SBOM", got: publishImage.With["sbom"], want: "true"},
	})
	wantBuildContext := "prepared=oci-layout://${{ runner.temp }}/ghcr-" + architecture.name + "-publication/layout@${{ steps.validate.outputs." + architecture.name + "_digest }}"
	if !strings.Contains(publishImage.With["build-contexts"], wantBuildContext) {
		t.Fatalf("publish-ghcr-images %s build contexts = %q, want validated OCI digest", architecture.name, publishImage.With["build-contexts"])
	}
}

func assertTrustedGHCRManifestPublisher(t *testing.T, workflow workflowConfig) {
	t.Helper()

	prepareManifest := workflowJobByName(t, workflow.Jobs, "prepare-ghcr-manifest")
	assertWorkflowJobPermissions(t, prepareManifest, "prepare-ghcr-manifest", map[string]string{"contents": "read"})
	assertGHCRJobDoesNotReference(t, prepareManifest, "secrets.GITHUB_TOKEN", "prepare-ghcr-manifest")
	for _, step := range prepareManifest.Steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") || strings.HasPrefix(step.Uses, "./") {
			t.Fatalf("prepare-ghcr-manifest must not make repository code available; step %q uses %q", step.Name, step.Uses)
		}
		if strings.Contains(step.Run, "scripts/") {
			t.Fatalf("prepare-ghcr-manifest must not execute repository scripts; step %q run = %q", step.Name, step.Run)
		}
	}
	manifestTags := workflowStepByName(t, workflow.Jobs, "prepare-ghcr-manifest", "Compute manifest image tags")
	if manifestTags.ID != "image_tags" {
		t.Fatalf("manifest image tag step ID = %q, want stable file output source", manifestTags.ID)
	}
	manifestPayload := workflowStepByName(t, workflow.Jobs, "prepare-ghcr-manifest", "Prepare manifest publication payload")
	if got := manifestPayload.Env["IMAGE_TAGS_FILE"]; got != "${{ steps.image_tags.outputs.file }}" {
		t.Fatalf("manifest payload IMAGE_TAGS_FILE = %q, want trusted image tag output", got)
	}
	assertWorkflowStepRunContainsAll(t, manifestPayload, "manifest payload tag binding", []string{
		`install -m 0600 "${IMAGE_TAGS_FILE}" "${payload_root}/image-tags.txt"`,
	})
	manifestUpload := workflowStepByName(t, workflow.Jobs, "prepare-ghcr-manifest", "Upload manifest publication payload")
	if manifestUpload.With["name"] != "ghcr-manifest-publication-payload" {
		t.Fatalf("manifest artifact name = %q", manifestUpload.With["name"])
	}

	publishManifest := workflowJobByName(t, workflow.Jobs, "publish-ghcr-manifest")
	assertWorkflowJobNeeds(t, publishManifest, "publish-ghcr-manifest", workflowJobNeeds{"publish-ghcr-images", "prepare-ghcr-manifest"})
	assertWorkflowJobPermissions(t, publishManifest, "publish-ghcr-manifest", map[string]string{"packages": "write"})
	assertFreshGHCRPublisher(t, publishManifest, "Log in to GHCR")
	manifestValidation := workflowStepByName(t, workflow.Jobs, "publish-ghcr-manifest", "Validate manifest publication payload")
	assertWorkflowStepRunContainsAll(t, manifestValidation, "manifest validation", []string{
		`expected_source_sha="${EXPECTED_SOURCE_SHA}"`,
		`expected_image_name="ghcr.io/${owner}/lopper"`,
		`find -P "${payload_root}" ! -type d ! -type f`,
		`directory_count`,
		`artifact payload exceeds`,
		`unexpected image reference`,
	})
	manifestPublish := workflowStepByName(t, workflow.Jobs, "publish-ghcr-manifest", "Publish multi-arch manifests")
	if strings.Contains(manifestPublish.Run, "scripts/") {
		t.Fatal("trusted manifest publisher must not run repository scripts")
	}
	assertWorkflowStepRunContainsAll(t, manifestPublish, "trusted manifest publication", []string{
		`done < "${IMAGE_TAGS_FILE}"`,
		`docker buildx imagetools create`,
		`docker buildx imagetools inspect`,
	})
}

func TestReleaseOrchestrationRequiresIntegrityBoundManifestPayload(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release-orchestration.yml", &workflow)

	manifestPayload := workflowStepByName(t, workflow.Jobs, "prepare-ghcr-manifest", "Prepare manifest publication payload")
	assertWorkflowStepRunContainsAll(t, manifestPayload, "manifest payload preparation", []string{
		`find -P . -type f ! -path './SHA256SUMS'`,
		`> "${payload_root}/SHA256SUMS"`,
	})

	publishManifest := workflowJobByName(t, workflow.Jobs, "publish-ghcr-manifest")
	manifestValidation := workflowStepByName(t, workflow.Jobs, "publish-ghcr-manifest", "Validate manifest publication payload")
	if manifestValidation.Env["EXPECTED_IMAGE_TAGS"] != "${{ inputs.image_tags }}" {
		t.Fatalf("manifest validation EXPECTED_IMAGE_TAGS = %q, want immutable workflow input", manifestValidation.Env["EXPECTED_IMAGE_TAGS"])
	}
	validationIndex := workflowStepIndexByName(t, workflow.Jobs, "publish-ghcr-manifest", "Validate manifest publication payload")
	loginIndex := workflowStepIndexByName(t, workflow.Jobs, "publish-ghcr-manifest", "Log in to GHCR")
	if validationIndex >= loginIndex {
		t.Fatal("manifest publication inputs must be validated before GHCR login")
	}
	assertWorkflowStepRunContainsAll(t, manifestValidation, "integrity-bound manifest validation", []string{
		`expected_source_sha="${EXPECTED_SOURCE_SHA}"`,
		`expected_image_tags="${EXPECTED_IMAGE_TAGS}"`,
		`expected_image_name="ghcr.io/${owner}/lopper"`,
		`checksums_file="${payload_root}/SHA256SUMS"`,
		`find -P "${payload_root}" ! -type d ! -type f`,
		`directory_count`,
		`[ "${file_count}" -ne 3 ]`,
		`SHA256SUMS|publication.json|image-tags.txt`,
		`computed_checksums=`,
		`cmp -s "${checksums_file}" "${computed_checksums}"`,
		`sha256sum --check --strict SHA256SUMS`,
		`while IFS= read -r raw_tag || [[ -n "${raw_tag}" ]]; do`,
		`tag="${tag%$'\r'}"`,
		`printf '%s:%s\n' "${expected_image_name}" "${tag}" >> "${expected_tags_file}"`,
		`cmp -s "${tags_file}" "${expected_tags_file}"`,
		`unexpected image reference`,
	})

	if len(publishManifest.Permissions) != 1 || publishManifest.Permissions["packages"] != "write" {
		t.Fatalf("publish-ghcr-manifest permissions = %#v, want packages: write", publishManifest.Permissions)
	}
}

func TestReleaseOrchestrationRequiresDigestPinnedGHCRManifests(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release-orchestration.yml", &workflow)

	publishImages := workflowJobByName(t, workflow.Jobs, "publish-ghcr-images")
	architectures := []struct {
		name     string
		platform string
	}{
		{name: "amd64", platform: "linux/amd64"},
		{name: "arm64", platform: "linux/arm64"},
	}
	for _, architecture := range architectures {
		publishImage := workflowStepByName(t, workflow.Jobs, "publish-ghcr-images", "Publish "+architecture.name+" release image")
		if publishImage.ID != "publish_"+architecture.name {
			t.Fatalf("publish-ghcr-images %s step ID = %q, want stable digest source", architecture.name, publishImage.ID)
		}
		if publishImage.With["platforms"] != architecture.platform {
			t.Fatalf("publish-ghcr-images %s platform = %q, want %q", architecture.name, publishImage.With["platforms"], architecture.platform)
		}
	}
	for _, output := range []struct {
		name string
		want string
	}{
		{name: "amd64_digest", want: "${{ steps.publish_amd64.outputs.digest }}"},
		{name: "arm64_digest", want: "${{ steps.publish_arm64.outputs.digest }}"},
	} {
		if publishImages.Outputs[output.name] != output.want {
			t.Fatalf("publish-ghcr-images output %s = %q, want %q", output.name, publishImages.Outputs[output.name], output.want)
		}
	}

	manifestValidation := workflowStepByName(t, workflow.Jobs, "publish-ghcr-manifest", "Validate manifest publication payload")
	for _, digest := range []struct {
		name string
		want string
	}{
		{name: "EXPECTED_AMD64_DIGEST", want: "${{ needs.publish-ghcr-images.outputs.amd64_digest }}"},
		{name: "EXPECTED_ARM64_DIGEST", want: "${{ needs.publish-ghcr-images.outputs.arm64_digest }}"},
	} {
		if manifestValidation.Env[digest.name] != digest.want {
			t.Fatalf("manifest validation %s = %q, want %q", digest.name, manifestValidation.Env[digest.name], digest.want)
		}
	}
	assertWorkflowStepRunContainsAll(t, manifestValidation, "manifest digest validation", []string{
		`amd64_digest="${EXPECTED_AMD64_DIGEST}"`,
		`arm64_digest="${EXPECTED_ARM64_DIGEST}"`,
		`[[ ! "${amd64_digest}" =~ ^sha256:[0-9a-f]{64}$ ]]`,
		`[[ ! "${arm64_digest}" =~ ^sha256:[0-9a-f]{64}$ ]]`,
	})

	manifestPublish := workflowStepByName(t, workflow.Jobs, "publish-ghcr-manifest", "Publish multi-arch manifests")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "manifest publication AMD64_DIGEST", got: manifestPublish.Env["AMD64_DIGEST"], want: "${{ needs.publish-ghcr-images.outputs.amd64_digest }}"},
		{label: "manifest publication ARM64_DIGEST", got: manifestPublish.Env["ARM64_DIGEST"], want: "${{ needs.publish-ghcr-images.outputs.arm64_digest }}"},
	})
	for _, mutableSource := range []string{`"${image_ref}-amd64"`, `"${image_ref}-arm64"`} {
		if strings.Contains(manifestPublish.Run, mutableSource) {
			t.Fatalf("trusted manifest publication must not use mutable source %s", mutableSource)
		}
	}
	assertWorkflowStepRunContainsAll(t, manifestPublish, "digest-pinned manifest publication", []string{
		`"${expected_image_name}@${amd64_digest}"`,
		`"${expected_image_name}@${arm64_digest}"`,
	})
}

func TestReleaseOrchestrationDedupesPlatformOCIManifestDescriptorsByDigest(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release-orchestration.yml", &workflow)

	validation := workflowStepByName(t, workflow.Jobs, "publish-ghcr-images", "Validate OCI publication payloads")
	assertWorkflowStepRunContainsAll(t, validation, "unique OCI image digest validation", []string{
		`| .digest' "${layout_root}/index.json" | sort -u)`,
		`OCI layout must contain exactly one unique ${platform} image digest.`,
	})
}

func TestReleaseOrchestrationRequiresExactTrustedArchitectureTags(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release-orchestration.yml", &workflow)

	publishImages := workflowJobByName(t, workflow.Jobs, "publish-ghcr-images")
	validation := workflowStepByName(t, workflow.Jobs, "publish-ghcr-images", "Validate OCI publication payloads")
	if validation.Env["EXPECTED_IMAGE_TAGS"] != "${{ inputs.image_tags }}" {
		t.Fatalf("architecture tag validation EXPECTED_IMAGE_TAGS = %q, want immutable workflow input", validation.Env["EXPECTED_IMAGE_TAGS"])
	}
	if strings.Contains(validation.Run, "scripts/") {
		t.Fatal("trusted architecture tag validation must not execute repository scripts")
	}
	validationIndex := workflowStepIndexByName(t, workflow.Jobs, "publish-ghcr-images", "Validate OCI publication payloads")
	loginIndex := workflowStepIndexByName(t, workflow.Jobs, "publish-ghcr-images", "Log in to GHCR")
	if validationIndex >= loginIndex {
		t.Fatal("architecture publication tags must be validated before GHCR login")
	}
	assertWorkflowStepRunContainsAll(t, validation, "exact trusted architecture tag validation", []string{
		`expected_image_tags="${EXPECTED_IMAGE_TAGS}"`,
		`local expected_tags_file="${RUNNER_TEMP}/ghcr-${architecture}-expected-image-tags.txt"`,
		`: > "${expected_tags_file}"`,
		`local expected_tag_count=0`,
		`local -A expected_seen_tags=()`,
		`while IFS= read -r raw_tag || [[ -n "${raw_tag}" ]]; do`,
		`tag="${tag%$'\r'}"`,
		`tag="${tag#"${tag%%[![:space:]]*}"}"`,
		`tag="${tag%"${tag##*[![:space:]]}"}"`,
		`[[ "${tag}" == *-amd64 ]]`,
		`[[ "${tag}" == *-arm64 ]]`,
		`expected_image_ref="${expected_image_name}:${tag}-${architecture}"`,
		`expected_seen_tags["${expected_image_ref}"]=1`,
		`printf '%s\n' "${expected_image_ref}" >> "${expected_tags_file}"`,
		`done <<< "${expected_image_tags}"`,
		`[ "${expected_tag_count}" -lt 1 ]`,
		`[ "${expected_tag_count}" -gt 16 ]`,
		`cmp -s "${tags_file}" "${expected_tags_file}"`,
		`rm -f "${expected_tags_file}"`,
	})

	shapeValidation := strings.Index(validation.Run, `if [ "${tag_count}" -lt 1 ] || [ "${tag_count}" -gt 16 ]; then`)
	expectedDerivation := strings.Index(validation.Run, `local expected_tags_file="${RUNNER_TEMP}/ghcr-${architecture}-expected-image-tags.txt"`)
	exactComparison := strings.Index(validation.Run, `cmp -s "${tags_file}" "${expected_tags_file}"`)
	outputPublication := strings.Index(validation.Run, `cat "${tags_file}"`)
	if shapeValidation < 0 || expectedDerivation < shapeValidation || exactComparison < expectedDerivation || outputPublication < exactComparison {
		t.Fatal("architecture tags must pass existing shape checks, exact immutable-input comparison, then publication")
	}
	for _, insufficientValidation := range []string{
		`sort -u "${tags_file}"`,
		`comm -3`,
		`diff -q`,
	} {
		if strings.Contains(validation.Run, insufficientValidation) {
			t.Fatalf("trusted architecture tags must use deterministic byte equality, not %q", insufficientValidation)
		}
	}
	if len(publishImages.Permissions) != 1 || publishImages.Permissions["packages"] != "write" {
		t.Fatalf("publish-ghcr-images permissions = %#v, want packages: write", publishImages.Permissions)
	}
}

func assertGHCRJobDoesNotReference(t *testing.T, job workflowJobConfig, needle string, jobLabel string) {
	t.Helper()

	assertStringMapDoesNotContain(t, job.Env, needle, jobLabel+" job env")
	for _, step := range job.Steps {
		stepLabel := jobLabel + " step " + step.Name
		assertStringsDoNotContain(t, []string{step.Uses, step.Run, step.Shell, step.WorkingDirectory}, needle, stepLabel)
		assertStringMapDoesNotContain(t, step.Env, needle, stepLabel+" env")
		assertStringMapDoesNotContain(t, step.With, needle, stepLabel+" inputs")
	}
}

func assertFreshGHCRPublisher(t *testing.T, job workflowJobConfig, loginStepName string) {
	t.Helper()

	if len(job.Env) != 0 {
		t.Fatalf("fresh GHCR publisher job env = %#v, want empty", job.Env)
	}
	loginIndex := -1
	for index, step := range job.Steps {
		if step.Name == loginStepName {
			loginIndex = index
			if step.With["password"] != "${{ secrets.GITHUB_TOKEN }}" {
				t.Fatalf("%s password = %q, want step-local GITHUB_TOKEN", step.Name, step.With["password"])
			}
			continue
		}
		assertFreshGHCRPublisherStep(t, step)
	}
	if loginIndex < 0 {
		t.Fatalf("fresh GHCR publisher is missing %q", loginStepName)
	}
	for _, step := range job.Steps[loginIndex+1:] {
		assertStringsDoNotContain(t, []string{step.Run}, "./Dockerfile", "fresh GHCR publisher step "+step.Name)
	}
}

func assertFreshGHCRPublisherStep(t *testing.T, step workflowStepConfig) {
	t.Helper()

	if strings.Contains(step.Uses, "actions/checkout@") || strings.HasPrefix(step.Uses, "./") {
		t.Fatalf("fresh GHCR publisher must not execute repository action %q", step.Uses)
	}
	assertStringsDoNotContain(t, []string{step.Run}, "scripts/", "fresh GHCR publisher step "+step.Name)
	assertStringsDoNotContain(t, []string{step.Run}, "make ", "fresh GHCR publisher step "+step.Name)
	if step.WorkingDirectory != "" {
		t.Fatalf("fresh GHCR publisher step %q must not set a repository working directory", step.Name)
	}
	if step.With["context"] == "." || strings.HasPrefix(step.With["file"], "./") {
		t.Fatalf("fresh GHCR publisher step %q must not use repository build context", step.Name)
	}
	credentialValues := append([]string{step.Run, step.Shell, step.Uses}, mapValues(step.Env)...)
	credentialValues = append(credentialValues, mapValues(step.With)...)
	for _, credentialMarker := range []string{"secrets.GITHUB_TOKEN", "github.token"} {
		assertStringsDoNotContain(t, credentialValues, credentialMarker, "fresh GHCR publisher step "+step.Name)
	}
}

func assertStringMapDoesNotContain(t *testing.T, values map[string]string, needle string, label string) {
	t.Helper()

	for key, value := range values {
		assertStringsDoNotContain(t, []string{key, value}, needle, label)
	}
}

func assertStringsDoNotContain(t *testing.T, values []string, needle string, label string) {
	t.Helper()

	for _, value := range values {
		if strings.Contains(value, needle) {
			t.Fatalf("%s must not reference %q", label, needle)
		}
	}
}

func mapValues(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
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
		t.Fatalf("%s must generate tokenless build tags through scripts/release-image-tags.sh", stepName)
	}
	if strings.Contains(step.Run, "while IFS= read -r tag") {
		t.Fatalf("%s must not use the stale unsanitized tag loop", stepName)
	}
}

func assertManifestImageTagStep(t *testing.T, manifestStep workflowStepConfig) {
	t.Helper()

	for _, want := range []string{
		"valid_image_tag_pattern='^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$'",
		"declare -a sanitized_tags=()",
		`while IFS= read -r raw_tag || [[ -n "$raw_tag" ]]`,
		`if [[ ! "$tag" =~ $valid_image_tag_pattern ]]`,
		`sanitized_tags+=("$tag")`,
		`for tag in "${sanitized_tags[@]}"; do`,
		`printf '%s:%s\n' "$IMAGE_NAME" "$tag"`,
		`echo "file=$image_tags_file" >> "$GITHUB_OUTPUT"`,
	} {
		if !strings.Contains(manifestStep.Run, want) {
			t.Fatalf("manifest preparation must contain trusted inline tag sanitizer snippet %q", want)
		}
	}
	if strings.Contains(manifestStep.Run, "scripts/release-image-tags.sh") {
		t.Fatal("manifest preparation must not execute repository-controlled tag sanitizer scripts")
	}
	if strings.Contains(manifestStep.Run, "docker ") {
		t.Fatal("manifest preparation step must not publish Docker manifests")
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

func TestHomebrewTapWorkflowsSkipAllTapJobsWithoutToken(t *testing.T) {
	t.Parallel()

	for _, tc := range homebrewTapWorkflowCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assertHomebrewTapWorkflowSkipsAllJobsWithoutToken(t, tc)
		})
	}
}

func TestHomebrewTapWorkflowsUseFreshPrivilegedTapUpdateJobs(t *testing.T) {
	t.Parallel()

	for _, tc := range homebrewTapWorkflowCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assertHomebrewTapWorkflowUsesFreshPrivilegedJob(t, tc)
		})
	}
}

func TestReleaseWorkflowOmitsDeadTapTokenConfiguredEnv(t *testing.T) {
	t.Parallel()

	workflowText := readConfig(t, ".github/workflows/release.yml")
	if strings.Contains(workflowText, "HOMEBREW_TAP_TOKEN_CONFIGURED") {
		t.Fatal("release workflow must not retain the dead HOMEBREW_TAP_TOKEN_CONFIGURED env")
	}
}

func TestHomebrewTapWorkflowsUseImmutablePreparedSourceBindings(t *testing.T) {
	t.Parallel()

	for _, tc := range homebrewTapWorkflowCases() {
		tc := tc
		t.Run(tc.workflowPath, func(t *testing.T) {
			t.Parallel()
			assertImmutablePreparedSourceBindings(t, tc)
		})
	}
}

func assertImmutablePreparedSourceBindings(t *testing.T, tc homebrewTapWorkflowCase) {
	t.Helper()

	var workflow struct {
		Jobs map[string]workflowJobConfig `yaml:"jobs"`
	}
	readYAMLConfig(t, tc.workflowPath, &workflow)

	validationJob := workflow.Jobs[tc.validationJobName]
	if validationJob.Env["SOURCE_SHA"] != tc.sourceSHAExpr {
		t.Fatalf("%s validation job SOURCE_SHA env = %q", tc.workflowPath, validationJob.Env["SOURCE_SHA"])
	}
	if validationJob.Env["FORMULA_VERSION"] != tc.formulaVersionExpr {
		t.Fatalf("%s validation job FORMULA_VERSION env = %q", tc.workflowPath, validationJob.Env["FORMULA_VERSION"])
	}
	if validationJob.Env["SOURCE_REPO"] != "${{ github.repository }}" {
		t.Fatalf("%s validation job SOURCE_REPO env = %q", tc.workflowPath, validationJob.Env["SOURCE_REPO"])
	}
	for _, deadEnv := range []string{"RELEASE_TAG", "ROLLING_TAG"} {
		if _, ok := validationJob.Env[deadEnv]; ok {
			t.Fatalf("%s validation job must not retain unused %s env", tc.workflowPath, deadEnv)
		}
	}
	updateJob := workflow.Jobs[tc.updateJobName]
	if updateJob.Env["SOURCE_SHA"] != tc.sourceSHAExpr {
		t.Fatalf("%s update job SOURCE_SHA env = %q", tc.workflowPath, updateJob.Env["SOURCE_SHA"])
	}
	if updateJob.Env["FORMULA_VERSION"] != tc.formulaVersionExpr {
		t.Fatalf("%s update job FORMULA_VERSION env = %q", tc.workflowPath, updateJob.Env["FORMULA_VERSION"])
	}

	assertImmutableSourceBinding(t, tc.workflowPath, workflowStepByName(t, workflow.Jobs, tc.validationJobName, tc.regenerateStepName).Run, tc)
	assertImmutableSourceBinding(t, tc.workflowPath, workflowStepByName(t, workflow.Jobs, tc.updateJobName, tc.pushStepName).Run, tc)
}

type homebrewTapWorkflowCase struct {
	name               string
	workflowPath       string
	gateJobName        string
	validationJobName  string
	updateJobName      string
	regenerateStepName string
	pushStepName       string
	formulaPath        string
	sourceURLLine      string
	formulaVersionExpr string
	commitMessageLine  string
	updateNeedsLine    string
	requiredIfFragment string
	sourceSHAExpr      string
}

func homebrewTapWorkflowCases() []homebrewTapWorkflowCase {
	return []homebrewTapWorkflowCase{
		{
			name:               "release",
			workflowPath:       ".github/workflows/release.yml",
			gateJobName:        "homebrew-tap-token-gate",
			validationJobName:  "validate-homebrew-tap",
			updateJobName:      "update-homebrew-tap",
			regenerateStepName: "Regenerate lopper formula",
			pushStepName:       "Regenerate and push formula changes",
			formulaPath:        "Formula/lopper.rb",
			sourceURLLine:      `source_url="https://github.com/${SOURCE_REPO}/archive/${SOURCE_SHA}.tar.gz"`,
			formulaVersionExpr: "${{ needs.prepare-release.outputs.version }}",
			commitMessageLine:  `git_safe -C "${tap_repo}" commit --no-verify -m "lopper ${RELEASE_TAG}"`,
			updateNeedsLine:    "  update-homebrew-tap:\n    needs:\n      - prepare-release\n      - publish\n      - validate-homebrew-tap",
			requiredIfFragment: "needs.prepare-release.outputs.release_created == 'true'",
			sourceSHAExpr:      "${{ needs.prepare-release.outputs.sha }}",
		},
		{
			name:               "rolling",
			workflowPath:       ".github/workflows/rolling.yml",
			gateJobName:        "homebrew-tap-token-gate",
			validationJobName:  "validate-homebrew-tap-rolling",
			updateJobName:      "update-homebrew-tap-rolling",
			regenerateStepName: "Regenerate lopper-rolling formula",
			pushStepName:       "Regenerate and push rolling formula changes",
			formulaPath:        "Formula/lopper-rolling.rb",
			sourceURLLine:      `source_url="https://github.com/${SOURCE_REPO}/archive/${SOURCE_SHA}.tar.gz"`,
			formulaVersionExpr: "${{ needs.prepare-rolling.outputs.tag }}",
			commitMessageLine:  `git_safe -C "${tap_repo}" commit --no-verify -m "lopper-rolling ${ROLLING_TAG}"`,
			updateNeedsLine:    "  update-homebrew-tap-rolling:\n    needs:\n      - prepare-rolling\n      - publish-rolling\n      - validate-homebrew-tap-rolling",
			sourceSHAExpr:      "${{ needs.prepare-rolling.outputs.source_sha }}",
		},
	}
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

func assertHomebrewTapWorkflowSkipsAllJobsWithoutToken(t *testing.T, tc homebrewTapWorkflowCase) {
	t.Helper()

	var workflow workflowConfig
	readYAMLConfig(t, tc.workflowPath, &workflow)
	workflowText := readConfig(t, tc.workflowPath)

	assertTapTokenGateJob(t, workflow.Jobs, workflowText, tc)

	validationJob, ok := workflow.Jobs[tc.validationJobName]
	if !ok {
		t.Fatalf("%s must define job %s", tc.workflowPath, tc.validationJobName)
	}
	if !strings.Contains(validationJob.If, "needs.homebrew-tap-token-gate.outputs.configured == 'true'") {
		t.Fatalf("%s job %s must skip entirely when HOMEBREW_TAP_TOKEN is absent", tc.workflowPath, tc.validationJobName)
	}
	if tc.requiredIfFragment != "" && !strings.Contains(validationJob.If, tc.requiredIfFragment) {
		t.Fatalf("%s job %s must preserve %q", tc.workflowPath, tc.validationJobName, tc.requiredIfFragment)
	}

	updateJob, ok := workflow.Jobs[tc.updateJobName]
	if !ok {
		t.Fatalf("%s must define job %s", tc.workflowPath, tc.updateJobName)
	}
	if !slices.Contains(updateJob.Needs, tc.validationJobName) {
		t.Fatalf("%s job %s must depend on token-gated job %s", tc.workflowPath, tc.updateJobName, tc.validationJobName)
	}
	if strings.Contains(updateJob.If, "always()") {
		t.Fatalf("%s job %s must not bypass a skipped token-gated dependency", tc.workflowPath, tc.updateJobName)
	}
}

func assertTapTokenGateJob(t *testing.T, jobs map[string]workflowJobConfig, workflowText string, tc homebrewTapWorkflowCase) {
	t.Helper()

	gateJob, ok := jobs[tc.gateJobName]
	if !ok {
		t.Fatalf("%s must define job %s", tc.workflowPath, tc.gateJobName)
	}
	if !strings.Contains(workflowText, "configured: ${{ steps.gate.outputs.configured }}") {
		t.Fatalf("%s must expose the tap-token gate output for downstream job gating", tc.workflowPath)
	}
	if gateJob.Permissions == nil || len(gateJob.Permissions) != 0 {
		t.Fatalf("%s gate job permissions = %#v, want an explicit empty permission set", tc.workflowPath, gateJob.Permissions)
	}

	gateStep := workflowStepByName(t, jobs, tc.gateJobName, "Detect tap token")
	if gateStep.Env["HOMEBREW_TAP_TOKEN"] != "${{ secrets.HOMEBREW_TAP_TOKEN }}" {
		t.Fatalf("%s gate step must read HOMEBREW_TAP_TOKEN only inside the gate job", tc.workflowPath)
	}
	for _, want := range []string{
		`if [ -n "${HOMEBREW_TAP_TOKEN:-}" ]; then`,
		`echo "configured=true" >> "$GITHUB_OUTPUT"`,
		`echo "configured=false" >> "$GITHUB_OUTPUT"`,
	} {
		if !strings.Contains(gateStep.Run, want) {
			t.Fatalf("%s gate step must contain %q", tc.workflowPath, want)
		}
	}
	if tc.requiredIfFragment != "" && !strings.Contains(gateJob.If, tc.requiredIfFragment) {
		t.Fatalf("%s gate job must preserve %q", tc.workflowPath, tc.requiredIfFragment)
	}
}

func assertTokenlessValidationJob(t *testing.T, jobs map[string]workflowJobConfig, tc homebrewTapWorkflowCase) {
	t.Helper()

	validationJob, ok := jobs[tc.validationJobName]
	if !ok {
		t.Fatalf("workflow must define job %s", tc.validationJobName)
	}
	assertAnonymousValidationCloneStep(t, validationJob, tc)
	assertJobDoesNotReceiveTapToken(t, validationJob, tc)
	assertValidationRegenerationStep(t, jobs, tc)
}

func assertAnonymousValidationCloneStep(t *testing.T, job workflowJobConfig, tc homebrewTapWorkflowCase) {
	t.Helper()

	for _, step := range job.Steps {
		if step.Name == "Checkout tap repository" || strings.Contains(step.Uses, "actions/checkout") {
			t.Fatalf("%s validation job must not use actions/checkout for tap validation", tc.workflowPath)
		}
	}

	cloneStep := workflowStepByName(t, map[string]workflowJobConfig{tc.validationJobName: job}, tc.validationJobName, "Clone tap repository anonymously")
	if cloneStep.Uses != "" {
		t.Fatalf("%s validation clone step must be a run step, got uses %q", tc.workflowPath, cloneStep.Uses)
	}
	if cloneStep.Shell != "/usr/bin/env -u BASH_ENV -u ENV -u PROMPT_COMMAND -u PS4 -u SHELLOPTS -u BASHOPTS /usr/bin/bash --noprofile --norc -euo pipefail {0}" {
		t.Fatalf("%s validation clone step shell = %q", tc.workflowPath, cloneStep.Shell)
	}

	for _, want := range []string{
		"git_bin=/usr/bin/git",
		"mktemp_bin=/usr/bin/mktemp",
		`export PATH=/usr/bin:/bin`,
		`git_home="$("${mktemp_bin}" -d)"`,
		`trap 'rm -rf "${git_home}"' EXIT`,
		"env -i",
		`HOME="${git_home}"`,
		`PATH="${PATH}"`,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		`-c core.hooksPath=/dev/null`,
		`-c protocol.file.allow=never`,
		`-c protocol.ext.allow=never`,
		`clone --origin origin --branch main --single-branch`,
		`https://github.com/ben-ranford/homebrew-tap.git homebrew-tap`,
	} {
		if !strings.Contains(cloneStep.Run, want) {
			t.Fatalf("%s validation clone step must contain %q", tc.workflowPath, want)
		}
	}

	for _, forbidden := range []string{
		"actions/checkout",
		"github.token",
		"GITHUB_TOKEN",
		"HOMEBREW_TAP_TOKEN",
		"persist-credentials",
		"credential.helper",
		"http.https://github.com/.extraheader",
		"x-access-token:",
		"AUTHORIZATION: basic",
	} {
		if strings.Contains(cloneStep.Run, forbidden) {
			t.Fatalf("%s validation clone step must not contain %q", tc.workflowPath, forbidden)
		}
	}
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
		`version "${FORMULA_VERSION}"`,
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
		`version "${FORMULA_VERSION}"`,
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
		"awk_bin=",
		`if [ -z "${HOMEBREW_TAP_TOKEN:-}" ]; then`,
		"HOMEBREW_TAP_TOKEN not configured; skipping",
	} {
		if strings.Contains(pushStep.Run, forbidden) {
			t.Fatalf("%s privileged tap update step must not contain %q", tc.workflowPath, forbidden)
		}
	}
}

func assertImmutableSourceBinding(t *testing.T, workflowPath string, run string, tc homebrewTapWorkflowCase) {
	t.Helper()

	for _, want := range []string{
		tc.sourceURLLine,
		`read -r source_sha _ < <`,
		`version "${FORMULA_VERSION}"`,
	} {
		if !strings.Contains(run, want) {
			t.Fatalf("%s step must contain %q", workflowPath, want)
		}
	}

	for _, forbidden := range []string{
		"resolve_source_tag_commit() {",
		"ls-remote",
		"codeload.github.com",
		"archive/refs/tags/",
		"refs/tags/",
		"Source tag ",
	} {
		if strings.Contains(run, forbidden) {
			t.Fatalf("%s step must not contain %q", workflowPath, forbidden)
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

	return jobs[jobName].Steps[workflowStepIndexByName(t, jobs, jobName, stepName)]
}

func workflowJobByName(t *testing.T, jobs map[string]workflowJobConfig, jobName string) workflowJobConfig {
	t.Helper()

	job, ok := jobs[jobName]
	if !ok {
		t.Fatalf("workflow must define job %s", jobName)
	}
	return job
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

type workflowStringValue struct {
	label string
	got   string
	want  string
}

type workflowArtifactDownloadContract struct {
	index int
	name  string
	path  string
}

func assertWorkflowStringValues(t *testing.T, values []workflowStringValue) {
	t.Helper()

	for _, value := range values {
		if value.got != value.want {
			t.Fatalf("%s = %q", value.label, value.got)
		}
	}
}

func assertExactArtifactDownloads(t *testing.T, steps []workflowStepConfig, contracts []workflowArtifactDownloadContract) {
	t.Helper()

	for _, contract := range contracts {
		download := steps[contract.index]
		assertWorkflowStringValues(t, []workflowStringValue{
			{label: download.Name + " artifact name", got: download.With["name"], want: contract.name},
			{label: download.Name + " path", got: download.With["path"], want: contract.path},
		})
		for _, forbidden := range []string{"pattern", "merge-multiple"} {
			if _, ok := download.With[forbidden]; ok {
				t.Fatalf("%s must not use with.%s", download.Name, forbidden)
			}
		}
	}
}

func assertWorkflowJobNeeds(t *testing.T, job workflowJobConfig, jobLabel string, want workflowJobNeeds) {
	t.Helper()

	if !slices.Equal(job.Needs, want) {
		t.Fatalf("%s needs = %v", jobLabel, job.Needs)
	}
}

func assertWorkflowJobPermissions(t *testing.T, job workflowJobConfig, jobLabel string, want map[string]string) {
	t.Helper()

	if !maps.Equal(job.Permissions, want) {
		t.Fatalf("%s permissions = %#v", jobLabel, job.Permissions)
	}
}

func assertWorkflowJobHasExplicitEmptyPermissions(t *testing.T, job workflowJobConfig, jobLabel string) {
	t.Helper()

	if job.Permissions == nil || len(job.Permissions) != 0 {
		t.Fatalf("%s permissions = %#v, want explicit empty permissions", jobLabel, job.Permissions)
	}
}

func assertWorkflowJobEnvEmpty(t *testing.T, job workflowJobConfig, jobLabel string) {
	t.Helper()

	if len(job.Env) != 0 {
		t.Fatalf("%s env = %#v, want no job-scoped credentials", jobLabel, job.Env)
	}
}

func assertReleasePublicationCredentialScope(t *testing.T, publication workflowJobConfig) {
	t.Helper()

	apiSteps := map[string]bool{
		"Update GitHub Release notes":  false,
		"Upload GitHub Release assets": false,
	}
	for _, step := range publication.Steps {
		if _, isAPIStep := apiSteps[step.Name]; isAPIStep {
			apiSteps[step.Name] = true
			stepLabel := "GitHub API step " + step.Name
			assertWorkflowStepEnv(t, step, stepLabel, map[string]string{
				"GH_TOKEN":    "${{ secrets.GITHUB_TOKEN }}",
				"RELEASE_TAG": "${{ needs.prepare-release.outputs.tag }}",
			})
			assertWorkflowStepRunContainsAll(t, step, stepLabel, []string{`tag="${RELEASE_TAG}"`})
			assertWorkflowStepRunOmitsAll(t, step, stepLabel, []string{"${{ needs.prepare-release.outputs.tag }}"})
		} else if _, ok := step.Env["GH_TOKEN"]; ok {
			t.Fatalf("non-API publication step %q must not receive GH_TOKEN", step.Name)
		}
	}
	for stepName, found := range apiSteps {
		if !found {
			t.Fatalf("fresh release publication must define GitHub API step %q", stepName)
		}
	}
}

func assertReleasePublicationOmitsCheckoutAndCommands(t *testing.T, publication workflowJobConfig) {
	t.Helper()

	for _, step := range publication.Steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			t.Fatalf("fresh release publication must not checkout repository code: %q", step.Name)
		}
		for _, repositoryCommand := range []string{"go run ./", "make ", "./extensions/", "scripts/"} {
			if strings.Contains(step.Run, repositoryCommand) {
				t.Fatalf("fresh release publication step %q must not execute repository-controlled command %q", step.Name, repositoryCommand)
			}
		}
	}
}

func assertFeatureHistoryPreparationDoesNotPush(t *testing.T, preparation workflowJobConfig) {
	t.Helper()

	for _, step := range preparation.Steps {
		if strings.Contains(step.Name, "Push") || strings.Contains(step.Run, "git push") {
			t.Fatalf("feature history preparation must not push from release-source: %q", step.Name)
		}
	}
}

func assertFeatureHistoryPublicationUsesFreshInputs(t *testing.T, publication workflowJobConfig) {
	t.Helper()

	for _, step := range publication.Steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			t.Fatalf("feature history publication must use a fresh host-Git clone, found checkout step %q", step.Name)
		}
		if step.WorkingDirectory == "release-source" || strings.Contains(step.Run, "release-source") {
			t.Fatalf("feature history publication must not reuse release-source: %q", step.Name)
		}
	}
}

func assertWorkflowStepEnv(t *testing.T, step workflowStepConfig, stepLabel string, want map[string]string) {
	t.Helper()

	if !maps.Equal(step.Env, want) {
		t.Fatalf("%s env = %#v", stepLabel, step.Env)
	}
}

func assertWorkflowJobOmitsCheckout(t *testing.T, job workflowJobConfig, jobLabel string) {
	t.Helper()

	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			t.Fatalf("%s must not checkout repository code: %q", jobLabel, step.Name)
		}
	}
}

func assertWorkflowJobStepRunsOmitAllFold(t *testing.T, job workflowJobConfig, jobLabel string, forbiddenValues []string) {
	t.Helper()

	for _, step := range job.Steps {
		for _, forbidden := range forbiddenValues {
			if strings.Contains(strings.ToLower(step.Run), forbidden) {
				t.Fatalf("%s step %q must not execute %q", jobLabel, step.Name, forbidden)
			}
		}
	}
}

func assertWorkflowStepRunOmitsAllFold(t *testing.T, step workflowStepConfig, stepLabel string, forbiddenValues []string) {
	t.Helper()

	for _, forbidden := range forbiddenValues {
		if strings.Contains(strings.ToLower(step.Run), forbidden) {
			t.Fatalf("%s must not execute %q while credential is in scope", stepLabel, forbidden)
		}
	}
}

func assertWorkflowStepRunOmitsAll(t *testing.T, step workflowStepConfig, stepLabel string, forbiddenValues []string) {
	t.Helper()

	for _, forbidden := range forbiddenValues {
		if strings.Contains(step.Run, forbidden) {
			t.Fatalf("%s must not contain %q", stepLabel, forbidden)
		}
	}
}

func assertTextAppearsBefore(t *testing.T, text string, earlier string, later string, message string) {
	t.Helper()

	earlierIndex := strings.Index(text, earlier)
	laterIndex := strings.Index(text, later)
	if earlierIndex < 0 || laterIndex < 0 || earlierIndex >= laterIndex {
		t.Fatal(message)
	}
}

func assertWorkflowStepOrder(t *testing.T, job workflowJobConfig, stepNames ...string) {
	t.Helper()

	previousIndex := -1
	for _, stepName := range stepNames {
		stepIndex := -1
		for index, step := range job.Steps {
			if step.Name == stepName {
				stepIndex = index
				break
			}
		}
		if stepIndex < 0 {
			t.Fatalf("workflow job must define step %q", stepName)
		}
		if stepIndex <= previousIndex {
			t.Fatalf("workflow step %q is out of order", stepName)
		}
		previousIndex = stepIndex
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

func assertTextContainsAll(t *testing.T, text string, textLabel string, wants []string) {
	t.Helper()

	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Fatalf("%s must contain %q", textLabel, want)
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

func workflowStepWithString(t *testing.T, step workflowStepConfig, key string) string {
	t.Helper()

	value, ok := step.With[key]
	if !ok {
		t.Fatalf("workflow step %q must define with.%s", step.Name, key)
	}
	return value
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

func embeddedPythonScript(t *testing.T, run string, marker string) string {
	t.Helper()

	_, after, ok := strings.Cut(run, marker+"\n")
	if !ok {
		t.Fatalf("workflow step does not contain Python validator marker %q", marker)
	}
	script, _, ok := strings.Cut(after, "\nPY\n")
	if !ok {
		t.Fatal("workflow step Python validator is missing its heredoc terminator")
	}
	return script + "\n"
}

type tarFixtureMember struct {
	name     string
	mode     int64
	typeflag byte
	linkname string
	contents []byte
}

type tarValidatorFixture struct {
	name    string
	members []tarFixtureMember
	want    string
}

func regularTarMember(name string, mode int64) tarFixtureMember {
	return tarFixtureMember{name: name, mode: mode, typeflag: tar.TypeReg, contents: []byte("fixture\n")}
}

func directoryTarMember(name string) tarFixtureMember {
	return tarFixtureMember{name: name, mode: 0o755, typeflag: tar.TypeDir}
}

func writeTarFixture(t *testing.T, members []tarFixtureMember) string {
	t.Helper()

	archivePath := filepath.Join(t.TempDir(), "fixture.tar.gz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create tar fixture: %v", err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, member := range members {
		if err := tarWriter.WriteHeader(&tar.Header{
			Name:     member.name,
			Mode:     member.mode,
			Size:     int64(len(member.contents)),
			Typeflag: member.typeflag,
			Linkname: member.linkname,
		}); err != nil {
			t.Fatalf("write tar header %q: %v", member.name, err)
		}
		if len(member.contents) > 0 {
			if _, err := tarWriter.Write(member.contents); err != nil {
				t.Fatalf("write tar contents %q: %v", member.name, err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar fixture: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip fixture: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close tar fixture file: %v", err)
	}
	return archivePath
}

func assertTarValidatorRejects(t *testing.T, validator string, archivePath string, want string) {
	t.Helper()

	cmd := exec.Command("python3", "-", archivePath)
	cmd.Stdin = strings.NewReader(validator)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("tar validator accepted unsafe archive %s", archivePath)
	}
	if !strings.Contains(string(output), want) {
		t.Fatalf("tar validator error = %q, want substring %q", output, want)
	}
}

func assertTarValidatorAccepts(t *testing.T, validator string, archivePath string) {
	t.Helper()

	cmd := exec.Command("python3", "-", archivePath)
	cmd.Stdin = strings.NewReader(validator)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tar validator rejected trusted archive %s: %v\n%s", archivePath, err, output)
	}
}

func assertTarValidatorFixtures(t *testing.T, validator string, base []tarFixtureMember, fixtures []tarValidatorFixture) {
	t.Helper()

	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			members := append([]tarFixtureMember{}, base...)
			members = append(members, fixture.members...)
			assertTarValidatorRejects(t, validator, writeTarFixture(t, members), fixture.want)
		})
	}
}
