package thresholds

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	loadConfigErrFmt = "load config: %v"
	unexpectedErrFmt = "unexpected error: %v"
	lopperYMLName    = ".lopper.yml"
	lopperYAMLName   = ".lopper.yaml"
	lopperJSONName   = "lopper.json"
	customConfigName = "custom.yml"
)

func TestLoadNoConfigFile(t *testing.T) {
	repo := t.TempDir()
	overrides, path, err := Load(repo, "")
	if err != nil {
		t.Fatalf(loadConfigErrFmt, err)
	}
	if path != "" {
		t.Fatalf("expected no config path, got %q", path)
	}
	resolved := overrides.Apply(Defaults())
	if resolved != Defaults() {
		t.Fatalf("expected defaults when no config file, got %+v", resolved)
	}
}

func TestLoadYAMLConfig(t *testing.T) {
	repo := t.TempDir()
	cfg := strings.Join([]string{"thresholds:", " fail_on_increase_percent: 3", " low_confidence_warning_percent: 25", " min_usage_percent_for_recommendations: 55", " removal_candidate_weight_usage: 0.6", " removal_candidate_weight_impact: 0.2", " removal_candidate_weight_confidence: 0.2", ""}, "\n")
	testutil.MustWriteFile(t, filepath.Join(repo, lopperYMLName), cfg)

	overrides, path, err := Load(repo, "")
	if err != nil {
		t.Fatalf(loadConfigErrFmt, err)
	}
	if !strings.HasSuffix(path, lopperYMLName) {
		t.Fatalf("expected %s path, got %q", lopperYMLName, path)
	}
	resolved := overrides.Apply(Defaults())
	if resolved.FailOnIncreasePercent != 3 {
		t.Fatalf("expected fail_on_increase_percent=3, got %d", resolved.FailOnIncreasePercent)
	}
	if resolved.LowConfidenceWarningPercent != 25 {
		t.Fatalf("expected low_confidence_warning_percent=25, got %d", resolved.LowConfidenceWarningPercent)
	}
	if resolved.MinUsagePercentForRecommendations != 55 {
		t.Fatalf("expected min_usage_percent_for_recommendations=55, got %d", resolved.MinUsagePercentForRecommendations)
	}
	if resolved.RemovalCandidateWeightUsage != 0.6 || resolved.RemovalCandidateWeightImpact != 0.2 || resolved.RemovalCandidateWeightConfidence != 0.2 {
		t.Fatalf("unexpected score weights: %+v", resolved)
	}
}

func TestLoadJSONConfig(t *testing.T) {
	repo := t.TempDir()
	cfg := `{
  "fail_on_increase_percent": 5,
  "low_confidence_warning_percent": 31,
  "min_usage_percent_for_recommendations": 48,
  "removal_candidate_weight_usage": 0.1,
  "removal_candidate_weight_impact": 0.2,
  "removal_candidate_weight_confidence": 0.7
}`
	testutil.MustWriteFile(t, filepath.Join(repo, lopperJSONName), cfg)

	overrides, _, err := Load(repo, "")
	if err != nil {
		t.Fatalf(loadConfigErrFmt, err)
	}
	resolved := overrides.Apply(Defaults())
	if resolved.FailOnIncreasePercent != 5 || resolved.LowConfidenceWarningPercent != 31 || resolved.MinUsagePercentForRecommendations != 48 {
		t.Fatalf("unexpected resolved thresholds: %+v", resolved)
	}
	if resolved.RemovalCandidateWeightUsage != 0.1 || resolved.RemovalCandidateWeightImpact != 0.2 || resolved.RemovalCandidateWeightConfidence != 0.7 {
		t.Fatalf("unexpected resolved score weights: %+v", resolved)
	}
}

func TestLoadConfigRejectsUnknownFields(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, lopperYMLName), "thresholds:\n  unknown: 1\n")

	_, _, err := Load(repo, "")
	if err == nil {
		t.Fatalf("expected error for unknown field")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestLoadConfigRejectsDuplicateFields(t *testing.T) {
	repo := t.TempDir()
	cfg := strings.Join([]string{"fail_on_increase_percent: 1", "thresholds:", " fail_on_increase_percent: 2", ""}, "\n")
	testutil.MustWriteFile(t, filepath.Join(repo, lopperYMLName), cfg)

	_, _, err := Load(repo, "")
	if err == nil {
		t.Fatalf("expected duplicate-key error")
	}
	if !strings.Contains(err.Error(), "defined more than once") {
		t.Fatalf("expected duplicate-key error, got %v", err)
	}
}

func TestLoadConfigFromExplicitPath(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, customConfigName), "thresholds:\n  low_confidence_warning_percent: 11\n")

	overrides, path, err := Load(repo, customConfigName)
	if err != nil {
		t.Fatalf("load explicit config: %v", err)
	}
	if !strings.HasSuffix(path, customConfigName) {
		t.Fatalf("expected explicit path %s, got %q", customConfigName, path)
	}
	resolved := overrides.Apply(Defaults())
	if resolved.LowConfidenceWarningPercent != 11 {
		t.Fatalf("expected low_confidence_warning_percent=11, got %d", resolved.LowConfidenceWarningPercent)
	}
}

func TestLoadConfigFromExplicitPathOutsideRepo(t *testing.T) {
	repo := t.TempDir()
	externalDir := t.TempDir()
	externalPath := filepath.Join(externalDir, customConfigName)
	testutil.MustWriteFile(t, externalPath, "thresholds:\n  low_confidence_warning_percent: 17\n")

	overrides, path, err := Load(repo, externalPath)
	if err != nil {
		t.Fatalf("load explicit external config: %v", err)
	}
	if path != externalPath {
		t.Fatalf("expected explicit external path %q, got %q", externalPath, path)
	}
	resolved := overrides.Apply(Defaults())
	if resolved.LowConfidenceWarningPercent != 17 {
		t.Fatalf("expected low_confidence_warning_percent=17, got %d", resolved.LowConfidenceWarningPercent)
	}
}

func TestLoadConfigExplicitPathMissing(t *testing.T) {
	repo := t.TempDir()
	_, _, err := Load(repo, "missing.yml")
	if err == nil {
		t.Fatalf("expected error for missing explicit config path")
	}
	if !strings.Contains(err.Error(), "config file not found") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestLoadConfigInvalidJSONMultipleValues(t *testing.T) {
	repo := t.TempDir()
	cfg := `{"thresholds":{"fail_on_increase_percent":1}} {"thresholds":{"fail_on_increase_percent":2}}`
	testutil.MustWriteFile(t, filepath.Join(repo, lopperJSONName), cfg)

	_, _, err := Load(repo, "")
	if err == nil {
		t.Fatalf("expected invalid JSON error")
	}
	if !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestLoadConfigInvalidYAML(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, lopperYAMLName), "thresholds: [\n")
	_, _, err := Load(repo, "")
	if err == nil {
		t.Fatalf("expected invalid YAML error")
	}
	if !strings.Contains(err.Error(), "invalid YAML") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestLoadConfigInvalidThresholdValue(t *testing.T) {
	assertLoadConfigErrorContains(t, "thresholds:\n  low_confidence_warning_percent: 101\n", "between 0 and 100")
}

func TestLoadConfigInvalidScoreWeightValue(t *testing.T) {
	assertLoadConfigErrorContains(t, "thresholds:\n  removal_candidate_weight_usage: -1\n", "removal_candidate_weight_usage")
}

func TestLoadConfigDiscoveryPriority(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, lopperYAMLName), "thresholds:\n  fail_on_increase_percent: 7\n")
	testutil.MustWriteFile(t, filepath.Join(repo, lopperJSONName), `{"thresholds":{"fail_on_increase_percent":2}}`)

	overrides, path, err := Load(repo, "")
	if err != nil {
		t.Fatalf(loadConfigErrFmt, err)
	}
	if !strings.HasSuffix(path, lopperYAMLName) {
		t.Fatalf("expected %s to be selected before %s, got %q", lopperYAMLName, lopperJSONName, path)
	}
	resolved := overrides.Apply(Defaults())
	if resolved.FailOnIncreasePercent != 7 {
		t.Fatalf("expected fail_on_increase_percent=7, got %d", resolved.FailOnIncreasePercent)
	}
}

func TestLoadConfigRepoPathResolutionError(t *testing.T) {
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf("restore wd %s: %v", originalWD, err)
		}
	})

	deadDir := filepath.Join(t.TempDir(), "dead-repo")
	if err := os.MkdirAll(deadDir, 0o755); err != nil {
		t.Fatalf("mkdir deadDir: %v", err)
	}
	if err := os.Chdir(deadDir); err != nil {
		t.Fatalf("chdir deadDir: %v", err)
	}
	if err := os.RemoveAll(deadDir); err != nil {
		t.Fatalf("remove deadDir: %v", err)
	}

	_, _, err = Load(".", "")
	if err != nil && !strings.Contains(err.Error(), "resolve repo path") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestLoadConfigExplicitPathDirectoryReadError(t *testing.T) {
	repo := t.TempDir()
	dirPath := filepath.Join(repo, "configs")
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatalf("mkdir dirPath: %v", err)
	}
	_, _, err := Load(repo, "configs")
	if err == nil {
		t.Fatalf("expected read error when explicit config path is a directory")
	}
	if !strings.Contains(err.Error(), "read config file") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestResolveConfigPathStatErrorForAutoDiscovery(t *testing.T) {
	repo := t.TempDir()
	fileRepo := filepath.Join(repo, "not-a-dir")
	if err := os.WriteFile(fileRepo, []byte("x"), 0o600); err != nil {
		t.Fatalf("write fileRepo: %v", err)
	}
	_, _, err := resolveConfigPath(fileRepo, "")
	if err == nil {
		t.Fatalf("expected stat error for invalid repo root path")
	}
}

func TestRawConfigToOverridesDuplicateNestedLowAndMinUsage(t *testing.T) {
	lowRoot := 10
	lowNested := 20
	cfg := rawConfig{
		LowConfidenceWarningPercent: &lowRoot,
		Thresholds: rawThresholds{
			LowConfidenceWarningPercent: &lowNested,
		},
	}
	if _, err := cfg.toOverrides(); err == nil {
		t.Fatalf("expected duplicate nested low confidence threshold error")
	}

	minRoot := 10
	minNested := 20
	cfg = rawConfig{
		MinUsagePercentForRecommendations: &minRoot,
		Thresholds: rawThresholds{
			MinUsagePercentForRecommendations: &minNested,
		},
	}
	if _, err := cfg.toOverrides(); err == nil {
		t.Fatalf("expected duplicate nested min usage threshold error")
	}
}

func TestRawConfigToOverridesDuplicateNestedScoreWeights(t *testing.T) {
	root := 0.1
	nested := 0.2
	cfg := rawConfig{
		RemovalCandidateWeightUsage: &root,
		Thresholds: rawThresholds{
			RemovalCandidateWeightUsage: &nested,
		},
	}
	if _, err := cfg.toOverrides(); err == nil {
		t.Fatalf("expected duplicate nested score usage weight error")
	}

	cfg = rawConfig{
		RemovalCandidateWeightImpact: &root,
		Thresholds: rawThresholds{
			RemovalCandidateWeightImpact: &nested,
		},
	}
	if _, err := cfg.toOverrides(); err == nil {
		t.Fatalf("expected duplicate nested score impact weight error")
	}

	cfg = rawConfig{
		RemovalCandidateWeightConfidence: &root,
		Thresholds: rawThresholds{
			RemovalCandidateWeightConfidence: &nested,
		},
	}
	if _, err := cfg.toOverrides(); err == nil {
		t.Fatalf("expected duplicate nested score confidence weight error")
	}
}

func TestParseConfigInvalidJSONDecodeError(t *testing.T) {
	if _, err := parseConfig(lopperJSONName, []byte("{")); err == nil {
		t.Fatalf("expected invalid JSON decode error")
	}
}

func TestResolveConfigPathExplicitStatError(t *testing.T) {
	repo := t.TempDir()
	fileRepo := filepath.Join(repo, "repo-file")
	testutil.MustWriteFile(t, fileRepo, "x")
	_, _, err := resolveConfigPath(fileRepo, "child.yml")
	if err == nil {
		t.Fatalf("expected explicit stat error when repo path is not a directory")
	}
}

func TestLoadWithPolicyPackPrecedenceAndSources(t *testing.T) {
	repo := t.TempDir()
	basePolicy := `thresholds:
  low_confidence_warning_percent: 21
  removal_candidate_weight_usage: 0.1
  removal_candidate_weight_impact: 0.7
  removal_candidate_weight_confidence: 0.2
`
	overlayPolicy := `policy:
  packs:
    - base.yml
thresholds:
  low_confidence_warning_percent: 33
`
	rootPolicy := `policy:
  packs:
    - packs/overlay.yml
thresholds:
  fail_on_increase_percent: 4
`
	testutil.MustWriteFile(t, filepath.Join(repo, "packs", "base.yml"), basePolicy)
	testutil.MustWriteFile(t, filepath.Join(repo, "packs", "overlay.yml"), overlayPolicy)
	testutil.MustWriteFile(t, filepath.Join(repo, lopperYMLName), rootPolicy)

	result, err := LoadWithPolicy(repo, "")
	if err != nil {
		t.Fatalf("load with policy packs: %v", err)
	}

	if result.Resolved.FailOnIncreasePercent != 4 {
		t.Fatalf("expected repo override fail_on_increase_percent=4, got %d", result.Resolved.FailOnIncreasePercent)
	}
	if result.Resolved.LowConfidenceWarningPercent != 33 {
		t.Fatalf("expected imported overlay low confidence=33, got %d", result.Resolved.LowConfidenceWarningPercent)
	}
	if result.Resolved.RemovalCandidateWeightImpact != 0.7 {
		t.Fatalf("expected inherited pack weight impact=0.7, got %f", result.Resolved.RemovalCandidateWeightImpact)
	}

	if len(result.PolicySources) != 4 {
		t.Fatalf("expected 4 policy sources, got %#v", result.PolicySources)
	}
	if !strings.HasSuffix(result.PolicySources[0], lopperYMLName) {
		t.Fatalf("expected highest-precedence source to be repo config, got %#v", result.PolicySources)
	}
	if !strings.HasSuffix(result.PolicySources[1], filepath.Join("packs", "overlay.yml")) {
		t.Fatalf("expected overlay pack source, got %#v", result.PolicySources)
	}
	if !strings.HasSuffix(result.PolicySources[2], filepath.Join("packs", "base.yml")) {
		t.Fatalf("expected base pack source, got %#v", result.PolicySources)
	}
	if result.PolicySources[3] != defaultPolicySource {
		t.Fatalf("expected defaults source, got %#v", result.PolicySources)
	}
}

func TestLoadWithPolicyPackCycle(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "packs", "a.yml"), "policy:\n  packs:\n    - b.yml\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "packs", "b.yml"), "policy:\n  packs:\n    - a.yml\n")
	testutil.MustWriteFile(t, filepath.Join(repo, lopperYMLName), "policy:\n  packs:\n    - packs/a.yml\n")

	_, err := LoadWithPolicy(repo, "")
	if err == nil {
		t.Fatalf("expected cycle error")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected cycle error text, got %v", err)
	}
}

func TestLoadWithPolicyRejectsRemotePackWithoutPin(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, lopperYMLName), "policy:\n  packs:\n    - https://example.com/policy.yml\n")
	_, err := LoadWithPolicy(repo, "")
	if err == nil {
		t.Fatalf("expected remote pack rejection")
	}
	if !strings.Contains(err.Error(), "must include a sha256 pin") {
		t.Fatalf("unexpected remote rejection error: %v", err)
	}
}

func TestLoadWithPolicyRemotePackWithPin(t *testing.T) {
	repo := t.TempDir()
	packBody := `thresholds:
  low_confidence_warning_percent: 19
  removal_candidate_weight_usage: 0.6
  removal_candidate_weight_impact: 0.2
  removal_candidate_weight_confidence: 0.2
`
	sum := sha256.Sum256([]byte(packBody))
	pin := hex.EncodeToString(sum[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/org.yml" {
			http.NotFound(w, r)
			return
		}
		if _, err := w.Write([]byte(packBody)); err != nil {
			t.Fatalf("write remote pack response: %v", err)
		}
	}))
	defer server.Close()

	policy := fmt.Sprintf("policy:\n  packs:\n    - %s/org.yml#sha256=%s\nthresholds:\n  fail_on_increase_percent: 3\n", server.URL, pin)
	testutil.MustWriteFile(t, filepath.Join(repo, lopperYMLName), policy)
	result, err := LoadWithPolicy(repo, "")
	if err != nil {
		t.Fatalf("load with pinned remote pack: %v", err)
	}
	if result.Resolved.FailOnIncreasePercent != 3 {
		t.Fatalf("expected repo override, got %d", result.Resolved.FailOnIncreasePercent)
	}
	if result.Resolved.LowConfidenceWarningPercent != 19 {
		t.Fatalf("expected remote pack threshold low_confidence_warning_percent=19, got %d", result.Resolved.LowConfidenceWarningPercent)
	}
	if len(result.PolicySources) < 3 || !strings.Contains(result.PolicySources[1], server.URL+"/org.yml#sha256="+pin) {
		t.Fatalf("expected remote policy source in precedence output, got %#v", result.PolicySources)
	}
}

func TestLoadWithPolicyRemotePackPinMismatch(t *testing.T) {
	repo := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte("thresholds:\n  low_confidence_warning_percent: 12\n")); err != nil {
			t.Fatalf("write mismatch response: %v", err)
		}
	}))
	defer server.Close()
	testutil.MustWriteFile(t, filepath.Join(repo, lopperYMLName), fmt.Sprintf("policy:\n  packs:\n    - %s/org.yml#sha256=%s\n", server.URL, strings.Repeat("a", 64)))

	_, err := LoadWithPolicy(repo, "")
	if err == nil {
		t.Fatalf("expected remote pin mismatch error")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("expected sha256 mismatch error, got %v", err)
	}
}

func TestResolvePackRefVariants(t *testing.T) {
	t.Run("empty_ref_rejected", func(t *testing.T) {
		if _, err := resolvePackRef("/repo/.lopper.yml", "   "); err == nil {
			t.Fatalf("expected empty ref rejection")
		}
	})

	t.Run("remote_parent_bad_relative_ref", func(t *testing.T) {
		current := "https://example.com/policies/root.yml#sha256=" + strings.Repeat("a", 64)
		_, err := resolvePackRef(current, "http://%zz")
		if err == nil || !strings.Contains(err.Error(), "invalid remote pack reference") {
			t.Fatalf("expected invalid remote pack reference error, got %v", err)
		}
	})

	t.Run("absolute_and_relative_local_paths", func(t *testing.T) {
		gotAbs, err := resolvePackRef("/repo/policies/root.yml", "/tmp/packs/base.yml")
		if err != nil {
			t.Fatalf("resolve abs ref: %v", err)
		}
		if gotAbs != filepath.Clean("/tmp/packs/base.yml") {
			t.Fatalf("unexpected abs ref: %q", gotAbs)
		}

		gotRel, err := resolvePackRef("/repo/policies/root.yml", "../shared/base.yml")
		if err != nil {
			t.Fatalf("resolve rel ref: %v", err)
		}
		want := filepath.Clean("/repo/shared/base.yml")
		if gotRel != want {
			t.Fatalf("unexpected rel ref: got %q want %q", gotRel, want)
		}
	})

	t.Run("remote_ref_is_canonicalized", func(t *testing.T) {
		remote := "https://example.com/org.yml#SHA256=" + strings.Repeat("B", 64)
		got, err := resolvePackRef("/repo/.lopper.yml", remote)
		if err != nil {
			t.Fatalf("resolve remote ref: %v", err)
		}
		want := "https://example.com/org.yml#sha256=" + strings.Repeat("b", 64)
		if got != want {
			t.Fatalf("unexpected canonical remote ref: got %q want %q", got, want)
		}
	})
}

func TestCanonicalPolicyLocationVariants(t *testing.T) {
	t.Run("remote_location", func(t *testing.T) {
		raw := "https://example.com/policy.yml#SHA256=" + strings.Repeat("C", 64)
		got, remote, err := canonicalPolicyLocation(raw)
		if err != nil {
			t.Fatalf("canonical remote: %v", err)
		}
		if !remote {
			t.Fatalf("expected remote=true")
		}
		want := "https://example.com/policy.yml#sha256=" + strings.Repeat("c", 64)
		if got != want {
			t.Fatalf("unexpected canonical remote location: got %q want %q", got, want)
		}
	})

	t.Run("relative_local_location", func(t *testing.T) {
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatalf("getwd: %v", err)
		}
		t.Cleanup(func() {
			if chdirErr := os.Chdir(cwd); chdirErr != nil {
				t.Fatalf("restore wd: %v", chdirErr)
			}
		})
		repo := t.TempDir()
		if err := os.Chdir(repo); err != nil {
			t.Fatalf("chdir repo: %v", err)
		}

		got, remote, err := canonicalPolicyLocation("policies/./base.yml")
		if err != nil {
			t.Fatalf("canonical local: %v", err)
		}
		if remote {
			t.Fatalf("expected remote=false")
		}
		want, err := filepath.Abs("policies/./base.yml")
		if err != nil {
			t.Fatalf("abs expected path: %v", err)
		}
		want = filepath.Clean(want)
		if got != want {
			t.Fatalf("unexpected canonical local location: got %q want %q", got, want)
		}
	})
}

func TestExtractRemotePolicyPinValidation(t *testing.T) {
	valid := strings.Repeat("d", 64)
	got, err := extractRemotePolicyPin("SHA256=" + strings.ToUpper(valid))
	if err != nil {
		t.Fatalf("extract valid pin: %v", err)
	}
	if got != valid {
		t.Fatalf("expected normalized pin %q, got %q", valid, got)
	}

	cases := []string{"", "sha256", "sha512=" + valid, "sha256=abc", "sha256=" + strings.Repeat("z", 64)}
	for _, tc := range cases {
		if _, err := extractRemotePolicyPin(tc); err == nil {
			t.Fatalf("expected validation failure for %q", tc)
		}
	}
}

func TestPackResolverPopAndPathRootHelpers(t *testing.T) {
	resolver := &packResolver{}

	resolver.pop("missing")

	resolver.stack = []string{"a", "b", "c"}
	resolver.pop("c")
	if got := strings.Join(resolver.stack, ","); got != "a,b" {
		t.Fatalf("unexpected stack after top pop: %s", got)
	}

	resolver.stack = []string{"a", "b", "c"}
	resolver.pop("b")
	if got := strings.Join(resolver.stack, ","); got != "a,c" {
		t.Fatalf("unexpected stack after middle pop: %s", got)
	}

	root := filepath.Clean(filepath.Join(string(os.PathSeparator), "repo"))
	if !isPathUnderRoot(root, filepath.Join(root, "policies", "base.yml")) {
		t.Fatalf("expected nested path to be under root")
	}
	if isPathUnderRoot(root, filepath.Clean(filepath.Join(root, "..", "outside.yml"))) {
		t.Fatalf("expected outside path to be rejected")
	}
}

func assertLoadConfigErrorContains(t *testing.T, config string, expectedText string) {
	t.Helper()
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, lopperYMLName), config)
	_, _, err := Load(repo, "")
	if err == nil {
		t.Fatalf("expected config validation error")
	}
	if !strings.Contains(err.Error(), expectedText) {
		t.Fatalf(unexpectedErrFmt, err)
	}
}
