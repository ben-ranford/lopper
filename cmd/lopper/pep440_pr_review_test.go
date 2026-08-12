package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/app"
	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

type pep440PRReviewAnalyzer struct {
	baseVersion string
	headVersion string
}

func (p *pep440PRReviewAnalyzer) Analyse(_ context.Context, req analysis.Request) (report.Report, error) {
	version := p.headVersion
	if filepath.Base(req.RepoPath) == "base" {
		version = p.baseVersion
	}
	return report.Report{Dependencies: []report.DependencyReport{{
		Name:     "example-lib",
		Language: "python",
		Identity: &report.DependencyIdentity{
			Ecosystem:  "pypi",
			Name:       "example-lib",
			Version:    version,
			PURL:       "pkg:pypi/example-lib@" + version,
			Confidence: "high",
			Evidence:   []string{"package lock"},
		},
	}}}, nil
}

func TestExecutePRReviewGatesPEP440Downgrade(t *testing.T) {
	repoPath, baseSHA, headSHA := createPEP440PRReviewGitRepo(t)
	features, err := featureflags.DefaultRegistry().Resolve(featureflags.ResolveOptions{
		Channel: featureflags.ChannelDev,
		Enable:  []string{report.DependencySurfacePRReviewPreviewFeature},
	})
	if err != nil {
		t.Fatalf("resolve pr-review feature: %v", err)
	}

	req := app.DefaultRequest()
	req.Mode = app.ModePRReview
	req.RepoPath = repoPath
	req.PRReview.BaseSHA = baseSHA
	req.PRReview.HeadSHA = headSHA
	req.PRReview.Features = features
	req.PRReview.FailOnRegression = true

	for _, test := range []struct {
		name           string
		baseVersion    string
		headVersion    string
		wantRegression bool
	}{
		{name: "epoch downgrade", baseVersion: "1!2.2.0", headVersion: "1!2.1.0", wantRegression: true},
		{name: "prerelease downgrade", baseVersion: "2.0b1", headVersion: "2.0a1", wantRegression: true},
		{name: "post release downgrade", baseVersion: "2.0.post1", headVersion: "2.0", wantRegression: true},
		{name: "development release downgrade", baseVersion: "2.0", headVersion: "2.0.dev1", wantRegression: true},
		{name: "local version downgrade", baseVersion: "2.0+local.1", headVersion: "2.0", wantRegression: true},
		{name: "numeric local segment downgrade", baseVersion: "2.0+1", headVersion: "2.0+local", wantRegression: true},
		{name: "invalid version stays unordered", baseVersion: "release-a", headVersion: "1.0", wantRegression: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := (&app.App{Analyzer: &pep440PRReviewAnalyzer{baseVersion: test.baseVersion, headVersion: test.headVersion}}).Execute(context.Background(), req)
			if got := errors.Is(err, app.ErrPRReviewRegressions); got != test.wantRegression {
				t.Fatalf("regression status = %t, want %t (error: %v)", got, test.wantRegression, err)
			}
		})
	}
}

func createPEP440PRReviewGitRepo(t *testing.T) (string, string, string) {
	t.Helper()

	repoPath := t.TempDir()
	testutil.RunGit(t, repoPath, "init")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "src", "app.txt"), "base\n")
	testutil.RunGit(t, repoPath, "add", ".")
	testutil.RunGit(t, repoPath, "-c", "user.email=pep440-test@example.com", "-c", "user.name=PEP 440 Test", "commit", "-m", "base")
	baseSHA := testutil.GitOutput(t, repoPath, "rev-parse", "HEAD")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "src", "app.txt"), "head\n")
	testutil.RunGit(t, repoPath, "add", ".")
	testutil.RunGit(t, repoPath, "-c", "user.email=pep440-test@example.com", "-c", "user.name=PEP 440 Test", "commit", "-m", "head")
	return repoPath, baseSHA, testutil.GitOutput(t, repoPath, "rev-parse", "HEAD")
}
