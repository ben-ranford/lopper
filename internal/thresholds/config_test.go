package thresholds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	writeConfig(t, filepath.Join(repo, lopperYMLName), cfg)

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
	writeConfig(t, filepath.Join(repo, lopperJSONName), cfg)

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
	writeConfig(t, filepath.Join(repo, lopperYMLName), "thresholds:\n  unknown: 1\n")

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
	writeConfig(t, filepath.Join(repo, lopperYMLName), cfg)

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
	writeConfig(t, filepath.Join(repo, customConfigName), "thresholds:\n  low_confidence_warning_percent: 11\n")

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
	writeConfig(t, externalPath, "thresholds:\n  low_confidence_warning_percent: 17\n")

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
	writeConfig(t, filepath.Join(repo, lopperJSONName), cfg)

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
	writeConfig(t, filepath.Join(repo, lopperYAMLName), "thresholds: [\n")
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
	writeConfig(t, filepath.Join(repo, lopperYAMLName), "thresholds:\n  fail_on_increase_percent: 7\n")
	writeConfig(t, filepath.Join(repo, lopperJSONName), `{"thresholds":{"fail_on_increase_percent":2}}`)

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
	writeConfig(t, fileRepo, "x")
	_, _, err := resolveConfigPath(fileRepo, "child.yml")
	if err == nil {
		t.Fatalf("expected explicit stat error when repo path is not a directory")
	}
}

func TestLoadWithPolicyPackPrecedenceAndSources(t *testing.T) {
	repo := t.TempDir()
	base := strings.Join([]string{
		"thresholds:",
		"  low_confidence_warning_percent: 21",
		"  removal_candidate_weight_usage: 0.1",
		"  removal_candidate_weight_impact: 0.7",
		"  removal_candidate_weight_confidence: 0.2",
		"",
	}, "\n")
	overlay := strings.Join([]string{
		"policy:",
		"  packs:",
		"    - base.yml",
		"thresholds:",
		"  low_confidence_warning_percent: 33",
		"",
	}, "\n")
	root := strings.Join([]string{
		"policy:",
		"  packs:",
		"    - packs/overlay.yml",
		"thresholds:",
		"  fail_on_increase_percent: 4",
		"",
	}, "\n")
	writeConfig(t, filepath.Join(repo, "packs", "base.yml"), base)
	writeConfig(t, filepath.Join(repo, "packs", "overlay.yml"), overlay)
	writeConfig(t, filepath.Join(repo, lopperYMLName), root)

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
	writeConfig(t, filepath.Join(repo, "packs", "a.yml"), "policy:\n  packs:\n    - b.yml\n")
	writeConfig(t, filepath.Join(repo, "packs", "b.yml"), "policy:\n  packs:\n    - a.yml\n")
	writeConfig(t, filepath.Join(repo, lopperYMLName), "policy:\n  packs:\n    - packs/a.yml\n")

	_, err := LoadWithPolicy(repo, "")
	if err == nil {
		t.Fatalf("expected cycle error")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected cycle error text, got %v", err)
	}
}

func TestLoadWithPolicyRejectsRemotePack(t *testing.T) {
	repo := t.TempDir()
	writeConfig(t, filepath.Join(repo, lopperYMLName), "policy:\n  packs:\n    - https://example.com/policy.yml\n")
	_, err := LoadWithPolicy(repo, "")
	if err == nil {
		t.Fatalf("expected remote pack rejection")
	}
	if !strings.Contains(err.Error(), "remote policy packs are not supported") {
		t.Fatalf("unexpected remote rejection error: %v", err)
	}
}

func writeConfig(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertLoadConfigErrorContains(t *testing.T, config string, expectedText string) {
	t.Helper()
	repo := t.TempDir()
	writeConfig(t, filepath.Join(repo, lopperYMLName), config)
	_, _, err := Load(repo, "")
	if err == nil {
		t.Fatalf("expected config validation error")
	}
	if !strings.Contains(err.Error(), expectedText) {
		t.Fatalf(unexpectedErrFmt, err)
	}
}
