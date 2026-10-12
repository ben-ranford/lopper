package scripts

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type trustedProviderCallSpan struct {
	start int
	end   int
}

func workflowStepOrderError(job workflowJobConfig, stepNames ...string) error {
	previousIndex := -1
	for _, stepName := range stepNames {
		stepIndex := -1
	forIndex:
		for index := previousIndex + 1; index < len(job.Steps); index++ {
			if job.Steps[index].Name == stepName {
				stepIndex = index
				break forIndex
			}
		}
		if stepIndex < 0 {
			return fmt.Errorf("workflow job must define step occurrence %q after index %d", stepName, previousIndex)
		}
		previousIndex = stepIndex
	}
	return nil
}

func assertWorkflowStepOrderOccurrences(t *testing.T, job workflowJobConfig, stepNames ...string) {
	t.Helper()
	if err := workflowStepOrderError(job, stepNames...); err != nil {
		t.Fatal(err)
	}
}

func trustedProviderAuthCallSpans(script, call string) []trustedProviderCallSpan {
	pattern := regexp.MustCompile(`(?m)^[ \t]*` + regexp.QuoteMeta(call) + `[ \t]*\r?$`)
	matches := pattern.FindAllStringIndex(script, -1)
	spans := make([]trustedProviderCallSpan, 0, len(matches))
	for _, match := range matches {
		end := match[1]
		if end < len(script) && script[end] == '\n' {
			end++
		}
		spans = append(spans, trustedProviderCallSpan{start: match[0], end: end})
	}
	return spans
}

func trustedProviderAuthBracket(script, call, invocation string) (trustedProviderCallSpan, trustedProviderCallSpan, error) {
	if strings.Count(script, invocation) != 1 {
		return trustedProviderCallSpan{}, trustedProviderCallSpan{}, fmt.Errorf("expected one invocation %q", invocation)
	}
	invokeAt := strings.Index(script, invocation)
	tryAt := strings.LastIndex(script[:invokeAt], "try {")
	if tryAt < 0 {
		return trustedProviderCallSpan{}, trustedProviderCallSpan{}, fmt.Errorf("invocation %q is not inside try", invocation)
	}
	spans := trustedProviderAuthCallSpans(script, call)
	var pre, post trustedProviderCallSpan
	havePre, havePost := false, false
	for _, span := range spans {
		if span.end <= invokeAt && span.start > tryAt {
			pre, havePre = span, true
		} else if span.start > invokeAt && !havePost {
			post, havePost = span, true
		}
	}
	if !havePre {
		return pre, post, fmt.Errorf("invocation %q lacks an executable pre-auth call inside its try block", invocation)
	}
	if !havePost {
		return pre, post, fmt.Errorf("invocation %q lacks an executable post-auth call", invocation)
	}
	finallyAt := strings.LastIndex(script[:post.start], "finally {")
	if finallyAt <= invokeAt {
		return pre, post, fmt.Errorf("post-auth call for %q is not inside finally", invocation)
	}
	closingBrace := regexp.MustCompile(`(?m)^[ \t]*}[ \t]*\r?$`).FindStringIndex(script[finallyAt+len("finally {"):])
	if len(closingBrace) == 0 || post.start >= finallyAt+len("finally {")+closingBrace[0] {
		return pre, post, fmt.Errorf("post-auth call for %q is outside its finally block", invocation)
	}
	return pre, post, nil
}

func assertTrustedProviderAuthBracket(t *testing.T, script, call, invocation, label string) {
	t.Helper()
	if _, _, err := trustedProviderAuthBracket(script, call, invocation); err != nil {
		t.Fatalf("%s: %v", label, err)
	}
}

func removeTrustedProviderCall(script string, span trustedProviderCallSpan) string {
	return script[:span.start] + script[span.end:]
}

func TestCIRequiresNativeWindowsRegressionProof(t *testing.T) {
	t.Parallel()
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	job := workflowJobByName(t, workflow.Jobs, "regression-proof-windows")
	if job.RunsOn != "windows-latest" || job.If != "" || job.ContinueOnError {
		t.Fatal("Windows proof must execute on a required native Windows job")
	}
	assertWorkflowJobPermissions(t, job, "Windows regression proof", map[string]string{"contents": "read"})
	assertWorkflowStepOrder(t, job, "Setup Go", "Provision restricted regression proof runtime", "Test native Windows proof tools", "Prove Windows regression tests for fix PRs")
	native := workflowStepByName(t, workflow.Jobs, "regression-proof-windows", "Test native Windows proof tools")
	assertPinnedNodeConsumerEnvironment(t, native)
	if native.Env["REGRESSION_PROOF_GO_ROOT"] != "${{ steps.proof_runtime.outputs.go_root }}" {
		t.Fatal("Windows root must use authenticated compiler output")
	}
	toolchain := workflowStepByName(t, workflow.Jobs, "regression-proof-windows", "Provision restricted regression proof runtime")
	if toolchain.ID != "proof_runtime" || toolchain.Env["LOPPER_RUNTIME_MODE"] != "capture" {
		t.Fatal("Windows compiler root must come from the authenticated pre-execution capture")
	}
	checkout := workflowStepByName(t, workflow.Jobs, "regression-proof-windows", "Checkout")
	if checkout.With["ref"] != "${{ github.sha }}" || checkout.With["fetch-depth"] != "0" || checkout.With["persist-credentials"] != "false" {
		t.Fatal("Windows proof must use the same event source and full history without saved credentials")
	}
	linux := workflowStepByName(t, workflow.Jobs, "verify-checks", "Prove regression tests for fix PRs")
	windows := workflowStepByName(t, workflow.Jobs, "regression-proof-windows", "Prove Windows regression tests for fix PRs")
	if windows.If != linux.If || windows.ContinueOnError || windows.Shell != "pwsh" {
		t.Fatal("Windows proof must enforce the same PR scope and fail on errors")
	}
	assertWorkflowStepRunContainsAll(t, linux, "Linux proof partition", []string{"--target-os linux"})
	assertWorkflowStepRunContainsAll(t, windows, "Windows proof partition", []string{
		"& $env:LOPPER_PROOF_GO run ./tools/regressionproof", "--target-os windows", `--body-file "$env:PR_BODY_FILE"`, `--base-sha "$env:PR_BASE_SHA"`,
	})
	gate := workflowStepByName(t, workflow.Jobs, "verify", "Require every verification job")
	for _, result := range []string{"success", "failure", "cancelled", "skipped", ""} {
		output, err := runShellCommand(t.TempDir(), gate.Run, map[string]string{
			"CHECKS_RESULT": "success", "TESTS_RESULT": "success", "WINDOWS_PROOF_RESULT": result, "DARWIN_PROOF_RESULT": "success", "WINDOWS_ACTION_PROOF_RESULT": "success",
		})
		if (err == nil) != (result == "success") {
			t.Fatalf("Windows result %q: %v\n%s", result, err, output)
		}
	}
}

func TestCIRequiresTrustedNativeWindowsActionProviderProof(t *testing.T) {
	t.Parallel()
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	job := workflowJobByName(t, workflow.Jobs, "action-windows-provider-proof")
	assertTrustedWindowsProviderJob(t, workflow, job)
	assertTrustedWindowsProviderExecution(t, workflow.Jobs)
	assertTrustedWindowsProviderGate(t, workflow)
}

func assertTrustedWindowsProviderJob(t *testing.T, workflow workflowConfig, job workflowJobConfig) {
	t.Helper()
	if job.RunsOn != "windows-2025" || job.If != "" || job.ContinueOnError {
		t.Fatal("trusted action provider proof must run as a required native windows-2025 job")
	}
	if !strings.Contains(readRepoFile(t, ".github/workflows/ci.yml"), "action-windows-provider-proof:\n    runs-on: windows-2025\n    timeout-minutes: 20") {
		t.Fatal("trusted action provider proof must retain the 20-minute job budget")
	}
	assertWorkflowJobPermissions(t, job, "Windows action provider proof", map[string]string{"contents": "read"})
	assertWorkflowStepOrderOccurrences(t, job,
		"Checkout subject source", "Checkout trusted Python provider O", "Setup Go",
		"Setup regression proof Node", "Provision restricted regression proof runtime", "Verify restricted regression proof runtime",
		"Authenticate trusted provider and preflight Python cache", "Setup trusted cached Python",
		"Verify completed Python cache and provision provider", "Run fixed and floating native Windows installer proofs",
		"Verify restricted regression proof runtime", "Write PR body for native action proof",
		"Prove native Windows regression tests for fix PRs")
	bootstrapGo := workflowStepByName(t, workflow.Jobs, "action-windows-provider-proof", "Setup Go")
	if bootstrapGo.With["go-version"] != "1.27.2" || bootstrapGo.With["go-version-file"] != "" || bootstrapGo.With["check-latest"] != "false" {
		t.Fatal("trusted bootstrap Go must be fixed independently of candidate go.mod")
	}
	trustedCheckout := workflowStepByName(t, workflow.Jobs, "action-windows-provider-proof", "Checkout trusted Python provider O")
	if trustedCheckout.With["ref"] != "75bd830fbf0a3908269dd15c92af35e895e196e7" || trustedCheckout.With["persist-credentials"] != "false" {
		t.Fatal("provider checkout must bind the reviewed trusted O commit without persisted credentials")
	}
	setupPython := workflowStepByName(t, workflow.Jobs, "action-windows-provider-proof", "Setup trusted cached Python")
	if setupPython.Uses != "actions/setup-python@5fda3b95a4ea91299a34e894583c3862153e4b97" || setupPython.With["python-version"] != "3.13.16" || setupPython.With["architecture"] != "x64" || setupPython.With["check-latest"] != "false" || setupPython.With["update-environment"] != "false" {
		t.Fatal("setup-python must use the pinned CPython x64 cache without latest lookup or environment mutation")
	}
	preflight := workflowStepByName(t, workflow.Jobs, "action-windows-provider-proof", "Authenticate trusted provider and preflight Python cache")
	if !strings.Contains(preflight.Run, "rev-parse 'HEAD^{tree}'") || !strings.Contains(preflight.Run, "Completed official CPython cache entry is unavailable") || !strings.Contains(preflight.Run, "tools/proofpython") || !strings.Contains(preflight.Run, "go version go1.27.2 windows/amd64") {
		t.Fatal("trusted provider commit/tree and completed-cache preflight must precede setup-python")
	}
	checkout := "git -c core.autocrlf=false -C $trusted checkout-index --force -- scripts/proof-python/provision.ps1 scripts/proof-python/verify.ps1"
	functionStart := strings.Index(preflight.Run, "function Assert-TrustedProviderSource {")
	if strings.Count(preflight.Run, checkout) != 1 || functionStart < 0 || strings.Index(preflight.Run, checkout) > functionStart {
		t.Fatal("trusted provider scripts must be restored exactly once before read-only source authentication")
	}
	functionBody, err := trustedProviderFunctionBody(preflight.Run, "Assert-TrustedProviderSource")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(functionBody, "checkout-index") {
		t.Fatal("source authentication must not repair changed bytes after the initial checkout")
	}
	for _, digestCheck := range []string{
		"Get-FileHash -LiteralPath $bootstrap -Algorithm SHA256",
		"Get-FileHash -LiteralPath $verify -Algorithm SHA256",
	} {
		if !strings.Contains(functionBody, digestCheck) {
			t.Fatalf("read-only source authentication must retain %q", digestCheck)
		}
	}
	assertTrustedProviderAuthBracket(t, preflight.Run, "Assert-TrustedProviderSource", "& $verifier @cacheArguments", "cache verifier")
	bootstrap := workflowStepByName(t, workflow.Jobs, "action-windows-provider-proof", "Verify completed Python cache and provision provider")
	assertTrustedProviderAuthBracket(t, bootstrap.Run, "Assert-TrustedProviderSource", "$result = & $env:TRUSTED_BOOTSTRAP", "trusted bootstrap")
}

func trustedProviderFunctionBody(script, name string) (string, error) {
	declaration := "function " + name + " {"
	start := strings.Index(script, declaration)
	if start < 0 {
		return "", fmt.Errorf("function %q is missing", name)
	}
	bodyStart := start + len(declaration)
	depth := 1
	for index := bodyStart; index < len(script); index++ {
		switch script[index] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return script[bodyStart:index], nil
			}
		}
	}
	return "", fmt.Errorf("function %q has no closing brace", name)
}

func TestTrustedProviderCheckoutRestoresAuthenticatedBytesWithAutocrlf(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	trustedScript := "scripts/proof-python/provision.ps1"
	runGitCommand(t, repo, "init", "--quiet")
	runGitCommand(t, repo, "config", "core.autocrlf", "true")
	runGitCommand(t, repo, "config", "user.name", "Test")
	runGitCommand(t, repo, "config", "user.email", "test@example.invalid")

	want := []byte("Write-Output 'pinned LF bytes'\nexit 0\n")
	path := filepath.Join(repo, filepath.FromSlash(trustedScript))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, repo, "add", trustedScript)
	runGitCommand(t, repo, "commit", "--quiet", "-m", "pin provider fixture")

	crlf := bytes.ReplaceAll(want, []byte("\n"), []byte("\r\n"))
	if err := os.WriteFile(path, crlf, 0o600); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, repo, "-c", "core.autocrlf=false", "checkout-index", "--force", "--", trustedScript)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("byte-preserving checkout returned %q, want pinned LF bytes %q", got, want)
	}
	wantDigest := sha256.Sum256(want)
	if gotDigest := sha256.Sum256(got); gotDigest != wantDigest {
		t.Fatal("restored provider bytes do not match the pinned digest")
	}
	if err := os.WriteFile(path, append(append([]byte(nil), got...), []byte("# changed\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changedDigest := sha256.Sum256(changed)
	if changedDigest == wantDigest {
		t.Fatal("changed provider bytes unexpectedly retained the pinned digest")
	}
	stillChanged, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stillChanged, changed) {
		t.Fatal("a post-authentication check must detect tampering without restoring the file")
	}
}

func assertTrustedWindowsProviderExecution(t *testing.T, jobs map[string]workflowJobConfig) {
	t.Helper()
	installer := workflowStepByName(t, jobs, "action-windows-provider-proof", "Run fixed and floating native Windows installer proofs")
	if !strings.Contains(installer.Run, "TestInstallScriptWindows") || !strings.Contains(installer.Run, "-ProofArguments $proofArgs") {
		t.Fatal("native installer proof must execute its published-release cases through the joined verifier")
	}
	if installer.Shell != "pwsh" {
		t.Fatal("subject tests must execute only through the trusted joined-command verifier")
	}
	formal := workflowStepByName(t, jobs, "action-windows-provider-proof", "Prove native Windows regression tests for fix PRs")
	for _, step := range []struct {
		name string
		step workflowStepConfig
	}{
		{name: "installer", step: installer},
		{name: "formal regression", step: formal},
	} {
		assertTrustedProviderAuthBracket(t, step.step.Run, "Assert-TrustedProviderInvocation", "& $env:TRUSTED_VERIFY -Verifier", step.name+" joined command")
		for _, digest := range []string{
			"b74f65b784b648ddcd6dfa23836d77b6231a3c6272b0b972577d4d878b895391",
			"1eaa2b86f6e5c65ec49edfe320160b36b7777369c1fd5cc50257725121d2ca46",
		} {
			if !strings.Contains(step.step.Run, digest) {
				t.Fatalf("%s must inline both trusted O source digests", step.name)
			}
		}
	}
	if formal.If == "" || !strings.Contains(formal.Run, "'--target-os', 'windows'") || !strings.Contains(formal.Run, "-ProofArguments $proofArgs") {
		t.Fatal("formal candidate/base regression proof must run through the trusted verifier")
	}
}

func assertTrustedWindowsProviderGate(t *testing.T, workflow workflowConfig) {
	t.Helper()
	gate := workflowStepByName(t, workflow.Jobs, "verify", "Require every verification job")
	if !strings.Contains(gate.Env["WINDOWS_ACTION_PROOF_RESULT"], "action-windows-provider-proof.result") || !strings.Contains(gate.Run, "WINDOWS_ACTION_PROOF_RESULT") {
		t.Fatal("aggregate verification must require the native action provider proof result")
	}
}

func TestWorkflowStepOrderOccurrencesRequireEveryOccurrence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		steps     []string
		wantError bool
	}{
		{name: "ordered repeated name", steps: []string{"A", "B", "A"}},
		{name: "missing first occurrence", steps: []string{"B", "A"}, wantError: true},
		{name: "missing second occurrence", steps: []string{"A", "B"}, wantError: true},
		{name: "reordered required names", steps: []string{"A", "A", "B"}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job := workflowJobConfig{Steps: make([]workflowStepConfig, len(test.steps))}
			for index, name := range test.steps {
				job.Steps[index].Name = name
			}
			err := workflowStepOrderError(job, "A", "B", "A")
			if (err != nil) != test.wantError {
				t.Fatalf("workflowStepOrderError() = %v, wantError %t", err, test.wantError)
			}
		})
	}
}

func TestCIRejectsRemovedTrustedNativeWindowsProviderAuthCalls(t *testing.T) {
	t.Parallel()
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/ci.yml", &workflow)
	sites := []struct {
		name       string
		step       workflowStepConfig
		call       string
		invocation string
	}{
		{
			name:       "cache verifier",
			step:       workflowStepByName(t, workflow.Jobs, "action-windows-provider-proof", "Authenticate trusted provider and preflight Python cache"),
			call:       "Assert-TrustedProviderSource",
			invocation: "& $verifier @cacheArguments",
		},
		{
			name:       "provider bootstrap",
			step:       workflowStepByName(t, workflow.Jobs, "action-windows-provider-proof", "Verify completed Python cache and provision provider"),
			call:       "Assert-TrustedProviderSource",
			invocation: "$result = & $env:TRUSTED_BOOTSTRAP",
		},
		{
			name:       "installer proof",
			step:       workflowStepByName(t, workflow.Jobs, "action-windows-provider-proof", "Run fixed and floating native Windows installer proofs"),
			call:       "Assert-TrustedProviderInvocation",
			invocation: "& $env:TRUSTED_VERIFY -Verifier",
		},
		{
			name:       "regression proof",
			step:       workflowStepByName(t, workflow.Jobs, "action-windows-provider-proof", "Prove native Windows regression tests for fix PRs"),
			call:       "Assert-TrustedProviderInvocation",
			invocation: "& $env:TRUSTED_VERIFY -Verifier",
		},
	}
	for _, site := range sites {
		pre, post, err := trustedProviderAuthBracket(site.step.Run, site.call, site.invocation)
		if err != nil {
			t.Fatalf("%s setup: %v", site.name, err)
		}
		for _, mutation := range []struct {
			name string
			span trustedProviderCallSpan
		}{
			{name: "pre-auth", span: pre},
			{name: "post-auth", span: post},
		} {
			t.Run(site.name+"/"+mutation.name, func(t *testing.T) {
				mutated := removeTrustedProviderCall(site.step.Run, mutation.span)
				if _, _, err := trustedProviderAuthBracket(mutated, site.call, site.invocation); err == nil {
					t.Fatalf("removing the executable %s call while retaining comments must be rejected", mutation.name)
				}
			})
		}
	}
}
