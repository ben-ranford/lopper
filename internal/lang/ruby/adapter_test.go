package ruby

import (
"context"
"path/filepath"
"slices"
"strings"
"testing"

"github.com/ben-ranford/lopper/internal/language"
"github.com/ben-ranford/lopper/internal/testutil"
)

func TestRubyAdapterDetectBundlerProject(t *testing.T) {
repo := t.TempDir()
testutil.MustWriteFile(t, filepath.Join(repo, gemfileName), "source 'https://rubygems.org'\ngem 'httparty'\n")
testutil.MustWriteFile(t, filepath.Join(repo, "main.rb"), "require 'httparty'\n")

detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
if err != nil {
t.Fatalf("detect: %v", err)
}
if !detection.Matched {
t.Fatalf("expected ruby detection to match")
}
if detection.Confidence <= 0 {
t.Fatalf("expected positive confidence, got %d", detection.Confidence)
}
}

func TestRubyAdapterAnalyseDependencyAndTopN(t *testing.T) {
repo := t.TempDir()
testutil.MustWriteFile(t, filepath.Join(repo, gemfileName), strings.Join([]string{
"source 'https://rubygems.org'",
"gem 'httparty'",
"gem 'nokogiri'",
"",
}, "\n"))
testutil.MustWriteFile(t, filepath.Join(repo, "Gemfile.lock"), strings.Join([]string{
"GEM",
"  specs:",
"    httparty (0.22.0)",
"    nokogiri (1.16.2)",
"",
}, "\n"))
testutil.MustWriteFile(t, filepath.Join(repo, "app.rb"), "require 'httparty'\nHTTParty.get('https://example.test')\n")

adapter := NewAdapter()
depReport, err := adapter.Analyse(context.Background(), language.Request{RepoPath: repo, Dependency: "httparty"})
if err != nil {
t.Fatalf("analyse dependency: %v", err)
}
if len(depReport.Dependencies) != 1 {
t.Fatalf("expected one dependency report, got %d", len(depReport.Dependencies))
}
if depReport.Dependencies[0].Language != "ruby" {
t.Fatalf("expected ruby language, got %q", depReport.Dependencies[0].Language)
}
if depReport.Dependencies[0].TotalExportsCount == 0 {
t.Fatalf("expected require signals to be counted")
}

topReport, err := adapter.Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 5})
if err != nil {
t.Fatalf("analyse topN: %v", err)
}
names := make([]string, 0, len(topReport.Dependencies))
for _, dep := range topReport.Dependencies {
names = append(names, dep.Name)
}
if !slices.Contains(names, "nokogiri") {
t.Fatalf("expected Bundler gem from Gemfile in topN output, got %#v", names)
}
}
