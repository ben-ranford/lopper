package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAnalysisPreparationParity(t *testing.T) {
	repo := t.TempDir()
	config := filepath.Join(repo, "policy.json")
	if err := os.WriteFile(config, []byte(`{"thresholds":{"low_confidence_warning_percent":35,"license_fail_on_deny":true,"license_include_registry_provenance":true,"license_deny":["GPL-3.0-ONLY"]},"scope":{"include":["src/**"],"exclude":["vendor/**"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	server := NewServer(Options{})
	for _, tc := range []struct {
		name         string
		args         mutationAnalysisArguments
		readKind     analysisToolKind
		mutationKind mutationAnalysisKind
	}{
		{"defaults", mutationAnalysisArguments{}, analysisToolKindTop, mutationAnalysisKindTopOrDependency},
		{"top", mutationAnalysisArguments{TopN: intPtr(3)}, analysisToolKindTop, mutationAnalysisKindTopOrDependency},
		{"dependency ignores top", mutationAnalysisArguments{Dependency: " dep ", TopN: intPtr(0)}, analysisToolKindDependency, mutationAnalysisKindDependency},
		{"config", mutationAnalysisArguments{ConfigPath: config}, analysisToolKindTop, mutationAnalysisKindTopOrDependency},
		{"overrides", mutationAnalysisArguments{ConfigPath: config, LowConfidenceWarningPercent: intPtr(0), LicenseFailOnDeny: boolPtr(false), LicenseProvenanceRegistry: boolPtr(false), Include: []string{"lib/**"}, Exclude: []string{"tmp/**"}, Language: " go ", RuntimeProfile: " node ", RuntimeTracePath: " trace.json "}, analysisToolKindTop, mutationAnalysisKindTopOrDependency},
		{"empty options", mutationAnalysisArguments{ConfigPath: config, Include: []string{}, Exclude: []string{}, LicenseDeny: []string{}}, analysisToolKindTop, mutationAnalysisKindTopOrDependency},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.args.RepoPath = repo
			read, err := server.resolveAnalysisRequest(context.Background(), analysisArgsFromMutation(tc.args), tc.readKind)
			if err != nil {
				t.Fatal(err)
			}
			mutation, err := server.resolveAnalysisMutationRequest(context.Background(), tc.args, tc.mutationKind)
			if err != nil {
				t.Fatal(err)
			}
			assertAnalysisPreparationParity(t, tc.name, tc.args, read, mutation)
		})
	}
}

func assertAnalysisPreparationParity(t *testing.T, name string, args mutationAnalysisArguments, read resolvedToolRequest, mutation AnalysisMutationRequest) {
	t.Helper()
	r := read.analysisRequest
	got := []any{r.RepoPath, r.Dependency, r.TopN, r.ScopeMode, r.Language, r.ConfigPath, r.RuntimeProfile, r.RuntimeTracePath, r.IncludePatterns, r.ExcludePatterns, r.Features, read.thresholds, read.policySources, read.policyTrace}
	want := []any{mutation.RepoPath, mutation.Dependency, mutation.TopN, mutation.ScopeMode, mutation.Language, mutation.ConfigPath, mutation.RuntimeProfile, mutation.RuntimeTracePath, mutation.IncludePatterns, mutation.ExcludePatterns, mutation.Features, mutation.Thresholds, mutation.PolicySources, mutation.PolicyTrace}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("read/mutation preparation differs:\n%#v\n%#v", got, want)
	}
	if name == "overrides" && (mutation.Thresholds.LowConfidenceWarningPercent != 0 || mutation.Thresholds.LicenseFailOnDeny || mutation.Thresholds.LicenseIncludeRegistryProvenance || !reflect.DeepEqual(r.IncludePatterns, args.Include)) {
		t.Fatal("explicit overrides lost")
	}
	if name == "empty options" && (!reflect.DeepEqual(r.IncludePatterns, []string{"src/**"}) || !reflect.DeepEqual(r.LicenseDenyList, []string{"GPL-3.0-ONLY"})) {
		t.Fatal("empty options must retain configured scope")
	}
}

func TestAnalysisPreparationValidationOrder(t *testing.T) {
	repo := t.TempDir()
	server := NewServer(Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name string
		args mutationAnalysisArguments
		want string
	}{
		{"target before scope", mutationAnalysisArguments{ScopeMode: "bad", EnableFeatures: []string{"missing"}}, "dependency is required"},
		{"scope before threshold", mutationAnalysisArguments{Dependency: "dep", ScopeMode: "bad", LowConfidenceWarningPercent: intPtr(101)}, "scopeMode"},
		{"threshold before feature", mutationAnalysisArguments{Dependency: "dep", LowConfidenceWarningPercent: intPtr(101), EnableFeatures: []string{"missing"}}, "low_confidence"},
		{"feature before cancellation", mutationAnalysisArguments{Dependency: "dep", EnableFeatures: []string{"missing"}}, "unknown feature"},
		{"disabled feature validation", mutationAnalysisArguments{Dependency: "dep", DisableFeatures: []string{"missing"}}, "unknown feature"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.args.RepoPath = repo
			_, readErr := server.resolveAnalysisRequest(ctx, analysisArgsFromMutation(tc.args), analysisToolKindDependency)
			_, mutationErr := server.resolveAnalysisMutationRequest(ctx, tc.args, mutationAnalysisKindDependency)
			if readErr == nil || mutationErr == nil || readErr.Error() != mutationErr.Error() || !strings.Contains(readErr.Error(), tc.want) {
				t.Fatalf("read=%v mutation=%v want=%s", readErr, mutationErr, tc.want)
			}
		})
	}
	args := mutationAnalysisArguments{RepoPath: repo, Dependency: "dep"}
	read, err := server.resolveAnalysisRequest(ctx, analysisArgsFromMutation(args), analysisToolKindDependency)
	if !errors.Is(err, context.Canceled) || read.repoPath == "" {
		t.Fatalf("read cancellation contract: %+v %v", read, err)
	}
	mutation, err := server.resolveAnalysisMutationRequest(ctx, args, mutationAnalysisKindDependency)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(mutation, AnalysisMutationRequest{}) {
		t.Fatalf("mutation cancellation contract: %+v %v", mutation, err)
	}
}

func TestAnalysisPreparationEndpointDifferences(t *testing.T) {
	repo := t.TempDir()
	server := NewServer(Options{})
	args := mutationAnalysisArguments{RepoPath: repo, Dependency: "dep", TopN: intPtr(2), CachePath: " missing-cache "}
	readArgs := analysisArgsFromMutation(args)
	readArgs.BaselinePath = " baseline.json "
	read, err := server.resolveAnalysisRequest(context.Background(), readArgs, analysisToolKindCompare)
	if err != nil {
		t.Fatal(err)
	}
	if read.baselinePath != "baseline.json" || read.analysisRequest.TopN != 0 || read.analysisRequest.Cache.Enabled || !read.analysisRequest.Cache.ReadOnly {
		t.Fatalf("read boundary changed: %+v", read)
	}
	_, err = server.resolveAnalysisMutationRequest(context.Background(), args, mutationAnalysisKindTopOrDependency)
	if err == nil || err.Error() != "topN cannot be combined with dependency" {
		t.Fatalf("mutation target: %v", err)
	}
	mutation, err := server.resolveAnalysisMutationRequest(context.Background(), args, mutationAnalysisKindDependency)
	if err != nil || !mutation.CacheEnabled || mutation.CacheReadOnly || mutation.CachePath != "missing-cache" || mutation.BaselineStorePath != "" {
		t.Fatalf("mutation boundary: %+v %v", mutation, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	readArgs.BaselineStorePath = "store"
	_, err = server.resolveAnalysisRequest(ctx, readArgs, analysisToolKindCompare)
	if err == nil || err.Error() != "baselinePath and baselineStorePath cannot both be provided" {
		t.Fatalf("baseline before cancellation: %v", err)
	}
}
