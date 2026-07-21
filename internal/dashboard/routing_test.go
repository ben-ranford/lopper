package dashboard

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func assertRoutingAssignment(t *testing.T, item RemediationItem, wantOwner, wantTeam, wantDue, wantStatus, wantSource string) {
	t.Helper()

	if item.Owner != wantOwner || item.Team != wantTeam || item.Due != wantDue || item.Status != wantStatus || item.RoutingSource != wantSource {
		t.Fatalf("unexpected routed item: %#v", item)
	}
}

func TestApplyRoutingUsesConfigRulesBeforeDefaults(t *testing.T) {
	items := []RemediationItem{{
		ID:         "item-1",
		Repo:       "api",
		RepoPath:   "services/api",
		Dependency: "lib",
		Category:   remediationCategoryVulnerability,
		Priority:   "high",
	}}
	routed := ApplyRouting(items, RoutingOptions{
		DefaultOwner:  "default-owner",
		DefaultTeam:   "default-team",
		DefaultStatus: "open",
		Rules: []RoutingRule{{
			Repo:       "api",
			Dependency: "lib",
			Owner:      "security",
			Team:       "appsec",
			Due:        "2026-08-01",
			Status:     "triage",
		}},
	})

	assertRoutingAssignment(t, routed[0], "security", "appsec", "2026-08-01", "triage", "config")
	if items[0].Owner != "" {
		t.Fatalf("expected ApplyRouting not to mutate input, got %#v", items[0])
	}
}

func TestFormatTeamSummaryIncludesRouting(t *testing.T) {
	reportData := Report{
		GeneratedAt: time.Date(2026, time.July, 13, 0, 0, 0, 0, time.UTC),
		Summary:     Summary{TotalRepos: 1, ReachableVulnerabilities: 1},
		RemediationItems: []RemediationItem{{
			Repo:            "api",
			Category:        remediationCategoryVulnerability,
			Dependency:      "lib",
			Priority:        "critical",
			Owner:           "security",
			Team:            "appsec",
			Due:             "2026-08-01",
			SuggestedAction: "Upgrade lib.",
		}},
	}

	output := formatTeamSummary(reportData, "slack")
	for _, want := range []string{"appsec", "owner=security", "due=2026-08-01", "Upgrade lib."} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected summary to contain %q, got %q", want, output)
		}
	}
}

func TestApplyRoutingUsesCodeownersAndDefaults(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, ".github", "CODEOWNERS"), `
# owned by app platform
/services/api/ @team/api @user
*.md @docs
`)
	rules := LoadCodeowners(repoPath)
	if len(rules) != 2 {
		t.Fatalf("expected codeowner rules, got %#v", rules)
	}

	routingItems := []RemediationItem{{
		Repo:     "api",
		RepoPath: "services/api/go.mod",
		Category: remediationCategoryVulnerability,
	}}
	routingOptions := RoutingOptions{
		DefaultDue:    "2026-08-01",
		DefaultStatus: "triage",
		Codeowners:    rules,
	}
	assertRoutingAssignment(t, ApplyRouting(routingItems, routingOptions)[0], "@team/api,@user", "", "2026-08-01", "triage", ".github/CODEOWNERS")

	markdown := ApplyRouting([]RemediationItem{{Repo: "docs", RepoPath: "README.md"}}, RoutingOptions{Codeowners: rules})
	if markdown[0].Owner != "@docs" {
		t.Fatalf("expected basename codeowner routing for README.md, got %#v", markdown[0])
	}
}

func TestApplyRoutingUsesEvidencePathsForCodeowners(t *testing.T) {
	rules := []CodeownerRule{{
		Pattern: "/services/api/",
		Owners:  []string{"@team/api", "@user"},
		Source:  ".github/CODEOWNERS",
	}}
	items := []RemediationItem{{
		Repo:     "api",
		RepoPath: "api",
		Category: remediationCategoryVulnerability,
		Evidence: []string{"static_location: services/api/go.mod:1"},
	}}
	routed := ApplyRouting(items, RoutingOptions{
		DefaultDue:    "2026-08-01",
		DefaultStatus: "triage",
		Codeowners:    rules,
	})

	assertRoutingAssignment(t, routed[0], "@team/api,@user", "", "2026-08-01", "triage", ".github/CODEOWNERS")
}

func TestApplyRoutingUsesRootEvidencePathsForCodeowners(t *testing.T) {
	items := []RemediationItem{{
		Repo:     "api",
		RepoPath: "/tmp/api-checkout",
		Evidence: []string{"static_location: main.go:12"},
	}}
	options := RoutingOptions{
		Codeowners: []CodeownerRule{{
			Pattern: "/main.go",
			Owners:  []string{"@team/api"},
			Source:  ".github/CODEOWNERS",
		}},
	}
	routed := ApplyRouting(items, options)

	assertRoutingAssignment(t, routed[0], "@team/api", "", "", "open", ".github/CODEOWNERS")
}

func TestCodeownerEvidenceTargetsNormalizesFiltersAndDeduplicates(t *testing.T) {
	got := codeownerEvidenceTargets([]string{
		"",
		"static_location: ./services/api/go.mod:12",
		"services/api/go.mod",
		"static_location: services/web/main.go:not-a-line",
		"static_location: services/empty.go:",
		"static_location: main.go:12",
		"static_location: src\\pkg\\main.go:12",
		"static_location: src/../main.go:12",
		"static_location: C:/repo/main.go:12",
		"static_location: C:\\repo\\main.go:12",
		"static_location: \\\\server\\share\\main.go:12",
		".",
		"..",
		"../outside/file.go",
		"/absolute/file.go",
		"README.md",
		"docs/read me.md",
	})
	want := []string{"services/api/go.mod", "services/web/main.go:not-a-line", "services/empty.go:", "main.go", "src/pkg/main.go"}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected CODEOWNERS evidence targets: got %q, want %q", got, want)
	}

	fallback := codeownerRuleTargets(RemediationItem{
		Repo:     "services/api",
		RepoPath: "services/api",
		Evidence: []string{"README.md"},
	})
	if len(fallback) != 1 || fallback[0] != "services/api" {
		t.Fatalf("expected duplicate fallback targets to collapse, got %q", fallback)
	}
}

func TestLoadCodeownersPrefersGitHubPrecedenceAndFallsBack(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, ".github", "CODEOWNERS"), "/services/api/ @github\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "CODEOWNERS"), "/services/api/ @root\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "docs", "CODEOWNERS"), "/services/api/ @docs\n")

	rules := LoadCodeowners(repoPath)
	if len(rules) != 1 || strings.Join(rules[0].Owners, ",") != "@github" || rules[0].Source != ".github/CODEOWNERS" {
		t.Fatalf("expected .github/CODEOWNERS to win precedence, got %#v", rules)
	}

	if err := os.Remove(filepath.Join(repoPath, ".github", "CODEOWNERS")); err != nil {
		t.Fatalf("remove github codeowners: %v", err)
	}
	rules = LoadCodeowners(repoPath)
	if len(rules) != 1 || strings.Join(rules[0].Owners, ",") != "@root" || rules[0].Source != "CODEOWNERS" {
		t.Fatalf("expected root CODEOWNERS fallback, got %#v", rules)
	}

	if err := os.Remove(filepath.Join(repoPath, "CODEOWNERS")); err != nil {
		t.Fatalf("remove root codeowners: %v", err)
	}
	rules = LoadCodeowners(repoPath)
	if len(rules) != 1 || strings.Join(rules[0].Owners, ",") != "@docs" || rules[0].Source != "docs/CODEOWNERS" {
		t.Fatalf("expected docs/CODEOWNERS fallback, got %#v", rules)
	}
}

func TestApplyRoutingFallsBackToUnassignedDefaults(t *testing.T) {
	defaulted := ApplyRouting([]RemediationItem{{Repo: "unknown"}}, RoutingOptions{})
	assertRoutingAssignment(t, defaulted[0], "unassigned", "", "", "open", "unassigned")
}

func TestCodeownerRuleMatchesRejectsBlankPatternAndTarget(t *testing.T) {
	if codeownerRuleMatches(CodeownerRule{}, RemediationItem{Repo: "repo"}) {
		t.Fatalf("expected empty codeowner pattern not to match")
	}
	if codeownerRuleMatches(CodeownerRule{Pattern: "bad["}, RemediationItem{Repo: ""}) {
		t.Fatalf("expected empty target not to match")
	}
	for _, pattern := range []string{"!docs", `\#docs`, "docs[ab]"} {
		if codeownerRuleMatches(CodeownerRule{Pattern: pattern}, RemediationItem{RepoPath: "docs/readme.md"}) {
			t.Fatalf("expected unsupported pattern %q not to match", pattern)
		}
	}
}

func TestApplyRoutingUsesLastMatchingCodeownerRule(t *testing.T) {
	items := []RemediationItem{{
		RepoPath: "services/api/main.go",
	}}
	routed := ApplyRouting(items, RoutingOptions{
		Codeowners: []CodeownerRule{
			{Pattern: "*", Owners: []string{"@catchall"}, Source: "CODEOWNERS"},
			{Pattern: "*.go", Owners: []string{"@golang"}, Source: "CODEOWNERS"},
		},
	})
	if routed[0].Owner != "@golang" {
		t.Fatalf("expected last matching CODEOWNERS rule to win, got %#v", routed[0])
	}
}

func TestApplyRoutingMatchesOfficialCodeownerPathSemantics(t *testing.T) {
	for _, tc := range []struct {
		name     string
		patterns []CodeownerRule
		path     string
		want     string
	}{
		{
			name: "slash-patterns-are-root-relative",
			patterns: []CodeownerRule{
				{Pattern: "/docs/*.md", Owners: []string{"@root"}, Source: "CODEOWNERS"},
				{Pattern: "docs/*.md", Owners: []string{"@nested"}, Source: "CODEOWNERS"},
			},
			path: "pkg/docs/readme.md",
			want: "unassigned",
		},
		{
			name: "root-relative-pattern-hit",
			patterns: []CodeownerRule{
				{Pattern: "docs/*.md", Owners: []string{"@docs"}, Source: "CODEOWNERS"},
			},
			path: "docs/readme.md",
			want: "@docs",
		},
		{
			name: "anchored-root-hit",
			patterns: []CodeownerRule{
				{Pattern: "/docs/*.md", Owners: []string{"@root"}, Source: "CODEOWNERS"},
			},
			path: "docs/readme.md",
			want: "@root",
		},
		{
			name: "unanchored-directory-multiple-depths",
			patterns: []CodeownerRule{
				{Pattern: "apps/", Owners: []string{"@apps"}, Source: "CODEOWNERS"},
			},
			path: "src/platform/apps/service/main.go",
			want: "@apps",
		},
		{
			name: "segment-boundary-near-miss",
			patterns: []CodeownerRule{
				{Pattern: "docs/", Owners: []string{"@docs"}, Source: "CODEOWNERS"},
			},
			path: "src/docs-old/readme.md",
			want: "unassigned",
		},
		{
			name: "bare-directory-pattern-matches-descendants",
			patterns: []CodeownerRule{
				{Pattern: "docs", Owners: []string{"@docs"}, Source: "CODEOWNERS"},
			},
			path: "nested/docs/readme.md",
			want: "@docs",
		},
		{
			name: "bare-directory-pattern-respects-segment-boundaries",
			patterns: []CodeownerRule{
				{Pattern: "docs", Owners: []string{"@docs"}, Source: "CODEOWNERS"},
			},
			path: "nested/docs-old/readme.md",
			want: "unassigned",
		},
		{
			name: "globstar-literal-terminal-segment-matches-descendants",
			patterns: []CodeownerRule{
				{Pattern: "**/logs", Owners: []string{"@logs"}, Source: "CODEOWNERS"},
			},
			path: "nested/logs/app/output.txt",
			want: "@logs",
		},
		{
			name: "anchored-literal-terminal-segment-matches-root-descendants",
			patterns: []CodeownerRule{
				{Pattern: "/apps/github", Owners: []string{"@github"}, Source: "CODEOWNERS"},
			},
			path: "apps/github/workflows/ci.yml",
			want: "@github",
		},
		{
			name: "terminal-wildcard-segment-does-not-gain-descendants",
			patterns: []CodeownerRule{
				{Pattern: "docs/*", Owners: []string{"@docs"}, Source: "CODEOWNERS"},
			},
			path: "docs/build-app/troubleshooting.md",
			want: "unassigned",
		},
		{
			name: "globstar-full-path",
			patterns: []CodeownerRule{
				{Pattern: "*.go", Owners: []string{"@basename"}, Source: "CODEOWNERS"},
				{Pattern: "/services/**/main.go", Owners: []string{"@fullpath"}, Source: "CODEOWNERS"},
			},
			path: "services/api/cmd/server/main.go",
			want: "@fullpath",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := ApplyRouting([]RemediationItem{{RepoPath: tc.path}}, RoutingOptions{Codeowners: tc.patterns})[0].Owner
			if owner != tc.want {
				t.Fatalf("ApplyRouting(%s) owner = %q, want %q", tc.path, owner, tc.want)
			}
		})
	}
}

func TestRoutingRuleMismatchBranches(t *testing.T) {
	if routed := ApplyRouting(nil, RoutingOptions{}); len(routed) != 0 {
		t.Fatalf("expected nil routing input to remain nil, got %#v", routed)
	}
	item := RemediationItem{Repo: "api", RepoPath: "services/api/go.mod", Dependency: "lib", Category: remediationCategoryVulnerability}
	for _, rule := range []RoutingRule{
		{Repo: "web"},
		{Category: remediationCategoryLicense},
		{Dependency: "other"},
		{PathPrefix: "services/web"},
	} {
		if routingRuleMatches(rule, item) {
			t.Fatalf("expected routing rule not to match: %#v", rule)
		}
	}
	if !routingRuleMatches(RoutingRule{Repo: "api", Category: remediationCategoryVulnerability, Dependency: "lib", PathPrefix: "services/api"}, item) {
		t.Fatalf("expected full routing rule to match")
	}
	if codeownerRuleMatches(CodeownerRule{Pattern: "bad["}, RemediationItem{RepoPath: "README.md"}) {
		t.Fatalf("expected invalid glob pattern not to match")
	}
	if got := LoadCodeowners(t.TempDir()); len(got) != 0 {
		t.Fatalf("expected no CODEOWNERS rules, got %#v", got)
	}
}

func TestRoutingRuleMatchesPathPrefixOnSegmentBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		prefix   string
		repoPath string
		want     bool
	}{
		{name: "exact", prefix: "services/api", repoPath: "services/api", want: true},
		{name: "descendant", prefix: "services/api", repoPath: "services/api/cmd/main.go", want: true},
		{name: "sibling-with-shared-prefix", prefix: "services/api", repoPath: "services/api-old/cmd/main.go", want: false},
		{name: "different-child", prefix: "services/api", repoPath: "services/application/cmd/main.go", want: false},
		{name: "trailing-slash-descendant", prefix: "services/api/", repoPath: "services/api/cmd/main.go", want: true},
		{name: "trailing-slash-sibling-with-shared-prefix", prefix: "services/api/", repoPath: "services/api-old/cmd/main.go", want: false},
		{name: "root-prefix-leading-slash-path", prefix: "/", repoPath: "/services/api/cmd/main.go", want: true},
		{name: "root-prefix-relative-path", prefix: "/", repoPath: "services/api/cmd/main.go", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := routingRuleMatches(RoutingRule{PathPrefix: tc.prefix}, RemediationItem{RepoPath: tc.repoPath})
			if got != tc.want {
				t.Fatalf("routingRuleMatches(%q, %q) = %t, want %t", tc.prefix, tc.repoPath, got, tc.want)
			}
		})
	}
}

func TestCodeownerPathMatchSupportsRecursivePatterns(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pattern string
		target  string
		want    bool
	}{
		{name: "recursive-zero-segments", pattern: "docs/**/README.md", target: "docs/README.md", want: true},
		{name: "recursive-nested-segments", pattern: "docs/**/README.md", target: "docs/guides/setup/README.md", want: true},
		{name: "recursive", pattern: "services/**/main.go", target: "services/api/cmd/main.go", want: true},
		{name: "question", pattern: "services/api/file?.go", target: "services/api/file1.go", want: true},
		{name: "root-relative-slash-pattern", pattern: "docs/*.md", target: "pkg/docs/readme.md", want: false},
		{name: "root-relative-slash-pattern-hit", pattern: "docs/*.md", target: "docs/readme.md", want: true},
		{name: "anchored-root-pattern", pattern: "/docs/*.md", target: "pkg/docs/readme.md", want: false},
		{name: "directory-pattern-any-depth", pattern: "apps/", target: "src/platform/apps/service/main.go", want: true},
		{name: "bare-directory-pattern-descendants", pattern: "docs", target: "nested/docs/readme.md", want: true},
		{name: "bare-directory-pattern-boundary", pattern: "docs", target: "nested/docs-old/readme.md", want: false},
		{name: "globstar-literal-terminal-segment-descendants", pattern: "**/logs", target: "nested/logs/app/output.txt", want: true},
		{name: "anchored-literal-terminal-segment-descendants", pattern: "/apps/github", target: "apps/github/workflows/ci.yml", want: true},
		{name: "terminal-wildcard-segment-no-descendants", pattern: "docs/*", target: "docs/build-app/troubleshooting.md", want: false},
		{name: "segment-boundary", pattern: "docs/", target: "src/docs-old/readme.md", want: false},
		{name: "no-match", pattern: "services/**/worker.go", target: "services/api/main.go", want: false},
	} {
		got, err := codeownerPathMatch(tc.pattern, tc.target)
		if err != nil {
			t.Fatalf("codeownerPathMatch(%s) returned error: %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("codeownerPathMatch(%s) = %t, want %t", tc.name, got, tc.want)
		}
	}
}

func TestCodeownerPathMatchGracefullyRejectsInvalidPatterns(t *testing.T) {
	for _, pattern := range []string{
		"services/[",
		"!services/api",
		`\#literal`,
		"docs[ab]/",
		"***/*.rb",
		"services/**foo/main.go",
		"services/foo**/main.go",
		"services/***/main.go",
	} {
		got, err := codeownerPathMatch(pattern, "services/api/main.go")
		if err == nil || got {
			t.Fatalf("expected invalid pattern %q to be rejected without a match, got match=%t err=%v", pattern, got, err)
		}
	}
}

func TestParseCodeownersSkipsUnsupportedSyntaxAndInlineComments(t *testing.T) {
	content := `/services/[ab]pi/ @bad
!negated @bad
\#literal @bad
/services/api/ @good # owned by api
/services/web/ # clear ownership # trailing comment`
	rules := parseCodeowners(content, ".github/CODEOWNERS")
	if len(rules) != 2 {
		t.Fatalf("expected only supported CODEOWNERS rules to parse, got %#v", rules)
	}
	if rules[0].Pattern != "/services/api/" || strings.Join(rules[0].Owners, ",") != "@good" {
		t.Fatalf("expected supported owner rule with inline comment trimmed, got %#v", rules[0])
	}
	if rules[1].Pattern != "/services/web/" || len(rules[1].Owners) != 0 {
		t.Fatalf("expected owner-clearing rule preserved after inline comment trim, got %#v", rules[1])
	}
}

func TestParseCodeownersSkipsInvalidPatternsAndOwnersWithoutOverriding(t *testing.T) {
	content := `*.rb @fallback
***/*.rb @override
*.txt @fallback
*.txt docs@
*.go @fallback
*.go @valid docs@
services/**foo/main.go @bad
services/api/ # clear ownership
`
	rules := parseCodeowners(content, "CODEOWNERS")
	if len(rules) != 4 {
		t.Fatalf("expected only valid CODEOWNERS rules to parse, got %#v", rules)
	}

	rb := ApplyRouting([]RemediationItem{{RepoPath: "lib/a.rb"}}, RoutingOptions{Codeowners: rules})
	assertRoutingAssignment(t, rb[0], "@fallback", "", "", "open", "CODEOWNERS")

	txt := ApplyRouting([]RemediationItem{{RepoPath: "notes/readme.txt"}}, RoutingOptions{Codeowners: rules})
	assertRoutingAssignment(t, txt[0], "@fallback", "", "", "open", "CODEOWNERS")

	goItem := ApplyRouting([]RemediationItem{{RepoPath: "services/api/main.go"}}, RoutingOptions{Codeowners: rules})
	assertRoutingAssignment(t, goItem[0], "unassigned", "", "", "open", "CODEOWNERS")
}

func TestCodeownerOwnerValidationMatrix(t *testing.T) {
	for _, tc := range []struct {
		owner string
		want  bool
	}{
		{owner: "@octocat", want: true},
		{owner: "@octo-cat", want: true},
		{owner: "@octocat_fabrikam", want: true},
		{owner: "@platform/core-team", want: true},
		{owner: "@platform/docs.team", want: true},
		{owner: "octocat@example.com", want: true},
		{owner: "octo.cat+alerts@example.co.uk", want: true},
		{owner: "@bad--owner", want: false},
		{owner: "@-bad", want: false},
		{owner: "@bad-", want: false},
		{owner: "@platform/bad--team", want: false},
		{owner: "@platform/-bad", want: false},
		{owner: "@platform/bad-", want: false},
		{owner: "docs@", want: false},
		{owner: "user@-example.com", want: false},
		{owner: "user@example-.com", want: false},
		{owner: "user@example..com", want: false},
		{owner: "@platform/core/team", want: false},
	} {
		if got := codeownerOwnerValid(tc.owner); got != tc.want {
			t.Fatalf("codeownerOwnerValid(%q) = %t, want %t", tc.owner, got, tc.want)
		}
	}
}

func TestRouteRemediationItemTreatsOwnerlessCodeownerRulesAsUnassigned(t *testing.T) {
	routed := ApplyRouting([]RemediationItem{{RepoPath: "services/api/main.go"}}, RoutingOptions{
		DefaultDue:    "2026-08-02",
		DefaultStatus: "triage",
		Codeowners:    []CodeownerRule{{Pattern: "/services/api/**", Source: "docs/CODEOWNERS"}},
	})
	assertRoutingAssignment(t, routed[0], "unassigned", "", "2026-08-02", "triage", "docs/CODEOWNERS")
}

func TestParseCodeownersSortsOwners(t *testing.T) {
	rules := parseCodeowners("/services/api/ @user @team\ninvalid\n", "CODEOWNERS")
	if len(rules) != 2 {
		t.Fatalf("expected parsed CODEOWNERS rules including ownerless entries, got %#v", rules)
	}
	if got := strings.Join(rules[0].Owners, ","); got != "@team,@user" {
		t.Fatalf("expected CODEOWNERS owners to be sorted, got %q", got)
	}
	if rules[1].Pattern != "invalid" || len(rules[1].Owners) != 0 {
		t.Fatalf("expected ownerless CODEOWNERS rule to be preserved, got %#v", rules[1])
	}
}

func TestParseCodeownersPreservesOwnerlessClearingRules(t *testing.T) {
	rules := parseCodeowners("/services/ @team\n/services/api/** # clear ownership", "docs/CODEOWNERS")
	if len(rules) != 2 {
		t.Fatalf("expected ownerless clearing rule to parse, got %#v", rules)
	}

	routed := ApplyRouting([]RemediationItem{{RepoPath: "services/api/main.go"}}, RoutingOptions{
		DefaultDue:    "2026-08-03",
		DefaultStatus: "triage",
		Codeowners:    rules,
	})
	assertRoutingAssignment(t, routed[0], "unassigned", "", "2026-08-03", "triage", "docs/CODEOWNERS")
}

func TestApplyRoutingExplicitOwnerlessRuleClearsLaterByLastMatch(t *testing.T) {
	content := `apps/ @apps
apps/generated/ @generated
apps/generated/tmp/ # clear ownership`
	rules := parseCodeowners(content, "CODEOWNERS")
	routed := ApplyRouting([]RemediationItem{{RepoPath: "apps/generated/tmp/file.go"}}, RoutingOptions{
		DefaultDue:    "2026-08-03",
		DefaultStatus: "triage",
		Codeowners:    rules,
	})
	assertRoutingAssignment(t, routed[0], "unassigned", "", "2026-08-03", "triage", "CODEOWNERS")
}

func TestParseCodeownersInvalidOwnerLineCannotOverrideEarlierValidRule(t *testing.T) {
	content := `*.go @fallback
*.go @bad--owner
*.md @docs
*.md user@-example.com`
	rules := parseCodeowners(content, "CODEOWNERS")
	if len(rules) != 2 {
		t.Fatalf("expected invalid owner lines to be skipped entirely, got %#v", rules)
	}

	goItem := ApplyRouting([]RemediationItem{{RepoPath: "services/api/main.go"}}, RoutingOptions{Codeowners: rules})
	assertRoutingAssignment(t, goItem[0], "@fallback", "", "", "open", "CODEOWNERS")

	mdItem := ApplyRouting([]RemediationItem{{RepoPath: "README.md"}}, RoutingOptions{Codeowners: rules})
	assertRoutingAssignment(t, mdItem[0], "@docs", "", "", "open", "CODEOWNERS")
}

func TestRelativeRoutingSourceFallsBackForMixedPaths(t *testing.T) {
	if got := relativeRoutingSource("/repo", "relative/CODEOWNERS"); got != "relative/CODEOWNERS" {
		t.Fatalf("expected mixed absolute/relative source to fall back to path, got %q", got)
	}
}

func TestCodeownerGlobRegexpBuildsMatchersForRecursivePatterns(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pattern string
		target  string
		want    bool
	}{
		{name: "double-star", pattern: "services/**/main.go", target: "services/api/cmd/main.go", want: true},
		{name: "single-star", pattern: "services/*.go", target: "services/main.go", want: true},
		{name: "question", pattern: "services/file?.go", target: "services/file1.go", want: true},
		{name: "mismatch", pattern: "services/worker?.go", target: "services/beta.go", want: false},
	} {
		expr, err := codeownerGlobRegexp(tc.pattern)
		if err != nil {
			t.Fatalf("codeownerGlobRegexp(%s) returned error: %v", tc.name, err)
		}
		if got := expr.MatchString(tc.target); got != tc.want {
			t.Fatalf("codeownerGlobRegexp(%s) matched %t, want %t", tc.name, got, tc.want)
		}
	}
}

func TestRemediationHelpersDeduplicateAndSortBySeverity(t *testing.T) {
	items := []RemediationItem{
		{ID: "b", Repo: "api", Dependency: "lib", Category: remediationCategoryLicense, Severity: "medium", Evidence: []string{"older"}},
		{ID: "a", Repo: "api", Dependency: "lib", Category: remediationCategoryVulnerability, Priority: "critical", Evidence: []string{"newer"}},
		{ID: "a", Repo: "api", Dependency: "lib", Category: remediationCategoryVulnerability, Priority: "low", Evidence: []string{"older"}},
		{Repo: "ignored"},
	}
	byID := remediationItemsByID(items)
	if len(byID) != 2 {
		t.Fatalf("expected remediationItemsByID to drop blank IDs and dedupe, got %#v", byID)
	}
	if got := byID["a"]; got.Priority != "critical" || strings.Join(got.Evidence, "|") != "newer|older" {
		t.Fatalf("expected higher-priority remediation item to win while merging evidence, got %#v", got)
	}
	ordered := dedupeAndSortRemediationItems(items)
	if len(ordered) != 2 || ordered[0].ID != "a" || ordered[1].ID != "b" {
		t.Fatalf("expected remediation items to sort by severity and ID, got %#v", ordered)
	}
}

func TestVulnerabilityRemediationItemsUseSeverityWhenPriorityIsBlank(t *testing.T) {
	if got := vulnerabilityRemediationItems("repo-id", "api", "services/api", "lib", []report.VulnerabilityFinding{{Package: "pkg", Priority: "", Severity: "high"}}); len(got) != 1 || got[0].Priority != "high" || got[0].Severity != "high" {
		t.Fatalf("expected vulnerability remediation priority fallback, got %#v", got)
	}
}

func TestSortRemediationItemDeltasOrdersByRankKindAndIdentity(t *testing.T) {
	deltas := []RemediationItemDelta{
		{ID: "b", Kind: RemediationItemExisting, Repo: "web", Dependency: "dep", Priority: "low"},
		{ID: "c", Kind: RemediationItemNew, Repo: "api", Dependency: "dep", Severity: "critical"},
		{ID: "a", Kind: RemediationItemExisting, Repo: "api", Dependency: "dep", Priority: "critical"},
	}
	sortRemediationItemDeltas(deltas)
	got := []string{deltas[0].ID, deltas[1].ID, deltas[2].ID}
	if strings.Join(got, ",") != "a,c,b" {
		t.Fatalf("expected remediation item deltas to sort by rank, kind, repo, and ID, got %#v", deltas)
	}
}
