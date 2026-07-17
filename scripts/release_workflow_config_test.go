package scripts

import (
	"encoding/json"
	"maps"
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
	assertWorkflowStepOrder(t, preparation, "Resolve trusted main workflow revision", "Run release-please", "Checkout manual release source", "Prepare manual release")

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

	preparation := workflowJobByName(t, workflow.Jobs, "prepare-release-publication")
	assertWorkflowJobNeeds(t, preparation, "release publication preparation", workflowJobNeeds{"prepare-release", "orchestrate-release", "build-vscode-extension", "build-darwin-amd64"})
	assertWorkflowJobPermissions(t, preparation, "release publication preparation", map[string]string{"contents": "read"})
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
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "release publication input artifact name", got: uploadStep.With["name"], want: "publication-inputs"},
		{label: "release publication input artifact path", got: uploadStep.With["path"], want: "publication-inputs"},
		{label: "release publication input artifact missing-file behavior", got: uploadStep.With["if-no-files-found"], want: "error"},
	})

	publication := workflowJobByName(t, workflow.Jobs, "publish")
	assertWorkflowJobPermissions(t, publication, "fresh release publication", map[string]string{"contents": "write"})
	assertWorkflowJobEnvEmpty(t, publication, "fresh release publication")
	assertReleasePublicationCredentialScope(t, publication)
	assertReleasePublicationOmitsCheckoutAndCommands(t, publication)
}

func TestReleaseWorkflowPublicationArtifactNameAvoidsReleaseArtifactWildcard(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	downloadStep := workflowStepByName(t, workflow.Jobs, "prepare-release-publication", "Download release artifacts")
	uploadStep := workflowStepByName(t, workflow.Jobs, "prepare-release-publication", "Upload release publication inputs")

	pattern := downloadStep.With["pattern"]
	artifactName := uploadStep.With["name"]
	matched, err := filepath.Match(pattern, artifactName)
	if err != nil {
		t.Fatalf("release artifact download pattern %q is invalid: %v", pattern, err)
	}
	if matched {
		t.Fatalf("release publication input artifact %q must not match release artifact download pattern %q", artifactName, pattern)
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

func TestReleaseWorkflowPublishesActionFloatingTags(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)

	job := workflowJobByName(t, workflow.Jobs, "update-floating-tags")
	assertWorkflowJobNeeds(t, job, "action floating tag job", workflowJobNeeds{"prepare-release", "publish"})
	assertWorkflowJobPermissions(t, job, "action floating tag job", map[string]string{"contents": "write"})
	assertWorkflowJobEnvEmpty(t, job, "action floating tag job")
	assertWorkflowJobOmitsCheckout(t, job, "action floating tag job")

	prepareStep := workflowStepByName(t, workflow.Jobs, "update-floating-tags", "Prepare GitHub Action floating tags")
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

	pushStep := workflowStepByName(t, workflow.Jobs, "update-floating-tags", "Push GitHub Action floating tags")
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
	assertWorkflowJobNeeds(t, gate, "tap token gate", workflowJobNeeds{"prepare-release", "publish", "update-floating-tags"})
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
	assertWorkflowJobNeeds(t, preparation, "feature history preparation", workflowJobNeeds{"prepare-release", "publish", "update-floating-tags"})
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

	publication := workflowJobByName(t, workflow.Jobs, "push-feature-release-history")
	assertWorkflowJobNeeds(t, publication, "feature history publication", workflowJobNeeds{"prepare-release", "prepare-feature-release-history"})
	assertWorkflowJobPermissions(t, publication, "feature history publication", map[string]string{"contents": "write"})
	assertWorkflowJobEnvEmpty(t, publication, "feature history publication")
	assertWorkflowStringValues(t, []workflowStringValue{{
		label: "feature history publication if",
		got:   publication.If,
		want:  "${{ needs.prepare-release.outputs.release_created == 'true' && needs.prepare-feature-release-history.outputs.changed == 'true' }}",
	}})
	assertFeatureHistoryPublicationUsesFreshInputs(t, publication)

	downloadStep := workflowStepByName(t, workflow.Jobs, "push-feature-release-history", "Download feature history patch")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature history patch download artifact name", got: downloadStep.With["name"], want: "feature-history-patch"},
		{label: "feature history patch download artifact path", got: downloadStep.With["path"], want: "feature-history-input"},
	})
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
	floatingTagStep := workflowStepByName(t, workflow.Jobs, "update-floating-tags", "Prepare GitHub Action floating tags")
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
	publication := workflowJobByName(t, workflow.Jobs, "push-feature-release-history")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: "feature history patch step prerelease guard", got: patchStep.If, want: "${{ steps.stamp_history.outputs.changed == 'true' }}"},
		{label: "feature history patch upload prerelease guard", got: uploadStep.If, want: "${{ steps.stamp_history.outputs.changed == 'true' }}"},
		{label: "feature history push prerelease guard", got: publication.If, want: "${{ needs.prepare-release.outputs.release_created == 'true' && needs.prepare-feature-release-history.outputs.changed == 'true' }}"},
	})
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
			jobName:  "prepare-ghcr-amd64",
			stepName: "Compute amd64 image tags",
			suffix:   "-amd64",
		},
		{
			jobName:  "prepare-ghcr-arm64",
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

	t.Run("manifest", func(t *testing.T) {
		manifestStep := workflowStepByName(t, workflow.Jobs, "prepare-ghcr-manifest", "Compute manifest image tags")
		assertManifestImageTagStep(t, manifestStep)
	})
}

func TestReleaseOrchestrationUsesFreshTrustedGHCRPublicationJobs(t *testing.T) {
	t.Parallel()

	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release-orchestration.yml", &workflow)

	architectures := []ghcrArchitecture{
		{name: "amd64", platform: "linux/amd64"},
		{name: "arm64", platform: "linux/arm64"},
	}
	for _, architecture := range architectures {
		architecture := architecture
		t.Run(architecture.name, func(t *testing.T) {
			t.Parallel()
			assertTrustedGHCRPreparation(t, workflow, architecture)
		})
	}
	assertTrustedGHCRImagePublisher(t, workflow, architectures)

	t.Run("manifest", func(t *testing.T) {
		assertTrustedGHCRManifestPublisher(t, workflow)
	})
}

type ghcrArchitecture struct {
	name     string
	platform string
}

func assertTrustedGHCRPreparation(t *testing.T, workflow workflowConfig, architecture ghcrArchitecture) {
	t.Helper()

	prepareJobName := "prepare-ghcr-" + architecture.name
	prepare := workflowJobByName(t, workflow.Jobs, prepareJobName)
	assertWorkflowJobPermissions(t, prepare, prepareJobName, map[string]string{"contents": "read"})
	assertGHCRJobDoesNotReference(t, prepare, "secrets.GITHUB_TOKEN", prepareJobName)
	checkout := workflowStepByName(t, workflow.Jobs, prepareJobName, "Checkout")
	if checkout.With["persist-credentials"] != "false" {
		t.Fatalf("%s checkout persist-credentials = %q, want false", prepareJobName, checkout.With["persist-credentials"])
	}

	build := workflowStepByName(t, workflow.Jobs, prepareJobName, "Build "+architecture.name+" OCI publication payload")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: prepareJobName + " registry push", got: build.With["push"], want: "false"},
		{label: prepareJobName + " deferred provenance", got: build.With["provenance"], want: "false"},
		{label: prepareJobName + " deferred SBOM", got: build.With["sbom"], want: "false"},
		{label: prepareJobName + " platform", got: build.With["platforms"], want: architecture.platform},
	})
	assertTextContainsAll(t, build.With["outputs"], prepareJobName+" OCI output", []string{
		"type=oci",
		"tar=false",
		"${{ runner.temp }}/ghcr-" + architecture.name + "-publication/layout",
	})
	metadata := workflowStepByName(t, workflow.Jobs, prepareJobName, "Prepare "+architecture.name+" publication metadata")
	assertWorkflowStepRunContainsAll(t, metadata, prepareJobName+" metadata", []string{
		`find -P . -type f ! -path './SHA256SUMS'`,
		`> "${payload_root}/SHA256SUMS"`,
	})
	upload := workflowStepByName(t, workflow.Jobs, prepareJobName, "Upload "+architecture.name+" OCI publication payload")
	assertWorkflowStringValues(t, []workflowStringValue{
		{label: prepareJobName + " artifact name", got: upload.With["name"], want: "ghcr-" + architecture.name + "-publication-payload"},
		{label: prepareJobName + " artifact path", got: upload.With["path"], want: "${{ runner.temp }}/ghcr-" + architecture.name + "-publication"},
	})
}

func assertTrustedGHCRImagePublisher(t *testing.T, workflow workflowConfig, architectures []ghcrArchitecture) {
	t.Helper()

	publishImages := workflowJobByName(t, workflow.Jobs, "publish-ghcr-images")
	assertWorkflowJobNeeds(t, publishImages, "publish-ghcr-images", workflowJobNeeds{"prepare-ghcr-amd64", "prepare-ghcr-arm64"})
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
	manifestCheckout := workflowStepByName(t, workflow.Jobs, "prepare-ghcr-manifest", "Checkout")
	if manifestCheckout.With["persist-credentials"] != "false" {
		t.Fatalf("prepare-ghcr-manifest checkout persist-credentials = %q, want false", manifestCheckout.With["persist-credentials"])
	}
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
		t.Fatalf("%s must generate tags through scripts/release-image-tags.sh", stepName)
	}
	if strings.Contains(step.Run, "while IFS= read -r tag") {
		t.Fatalf("%s must not use the stale unsanitized tag loop", stepName)
	}
}

func assertManifestImageTagStep(t *testing.T, manifestStep workflowStepConfig) {
	t.Helper()

	if !strings.Contains(manifestStep.Run, "bash scripts/release-image-tags.sh > image-tags.txt") {
		t.Fatal("manifest preparation step must sanitize image tags before publication")
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

func assertWorkflowStringValues(t *testing.T, values []workflowStringValue) {
	t.Helper()

	for _, value := range values {
		if value.got != value.want {
			t.Fatalf("%s = %q", value.label, value.got)
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
		"Publish GitHub Release":       false,
	}
	for _, step := range publication.Steps {
		if _, isAPIStep := apiSteps[step.Name]; isAPIStep {
			apiSteps[step.Name] = true
			assertWorkflowStepEnv(t, step, "GitHub API step "+step.Name, map[string]string{"GH_TOKEN": "${{ secrets.GITHUB_TOKEN }}"})
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
