package dashboard

import (
	"strings"
	"testing"
)

func TestPortfolioRefsExcludeForeignAbsolutePaths(t *testing.T) {
	for _, value := range []string{"/home/user/repo", `C:\repo\services\api`, "d:/repo/services/api", `\\server\share\api`, "//server/share/api", `\\?\C:\repo\api`, " /home/user/repo "} {
		t.Run(value, func(t *testing.T) {
			if got := stablePortfolioRefPath(value); got != "" {
				t.Fatalf("absolute path leaked into stable identity: %q", got)
			}
			repo := RepoResult{Name: "api", Path: value}
			if got, want := portfolioRepoRef(repo), portfolioRepoRef(RepoResult{Name: "api"}); got != want {
				t.Fatalf("repository ref includes machine path: got %q want %q", got, want)
			}
			dep := PortfolioComponent{Repo: "api", RepoPath: value, Language: "go", Name: "example.com/lib"}
			got := portfolioDependencyRef(dep)
			dep.RepoPath = ""
			if want := portfolioDependencyRef(dep); got != want {
				t.Fatalf("dependency ref includes machine path: got %q want %q", got, want)
			}
		})
	}
}

func TestPortfolioRefsNormalizeRelativePathsAcrossPlatforms(t *testing.T) {
	for _, value := range []string{"services/api", "./services/api", `services\api`, `.\services\api`, " services/api ", ".//services/api", ".\\\\services\\api", "././services/api"} {
		if got := stablePortfolioRefPath(value); got != "services/api" {
			t.Fatalf("relative path %q normalized to %q", value, got)
		}
	}
}

func TestPortfolioCycloneDXNormalizesRelativeRepositoryLabels(t *testing.T) {
	for _, name := range []string{"api", ""} {
		t.Run("name="+name, func(t *testing.T) {
			var expected string
			for _, paths := range [][]string{
				{"services/api", "platform/api"},
				{`services\api`, `platform\api`},
				{`.\services\api`, "./platform//api"},
			} {
				data := relativeRepositoryLabelsReport(name, paths)
				output, bom := formatPortfolioCycloneDXForTest(t, data)
				if bom.Components[0].BOMRef == bom.Components[1].BOMRef {
					t.Fatal("relative repository labels lost their distinct identities")
				}
				if expected == "" {
					expected = output
				} else if output != expected {
					t.Fatalf("equivalent relative paths changed CycloneDX refs or properties: %q\nwant:\n%s\ngot:\n%s", paths, expected, output)
				}
			}
		})
	}
}

func relativeRepositoryLabelsReport(name string, paths []string) Report {
	data := Report{}
	for _, repoPath := range paths {
		label := crossRepoRepositoryLabel(RepoInput{Name: name, Path: repoPath}, map[string]int{name: 2})
		data.PortfolioComponents = append(data.PortfolioComponents, PortfolioComponent{
			Repo: label, RepoPath: repoPath, Language: "go", Name: "example.com/lib", Version: "v1",
		})
	}
	return data
}

func TestPortfolioCycloneDXSanitizesAbsoluteDuplicateRepositoryLabels(t *testing.T) {
	reportData := Report{PortfolioComponents: []PortfolioComponent{
		{Repo: "api (/home/alice/platform/api)", RepoPath: "/home/alice/platform/api", Language: "go", Name: "example.com/lib", Version: "v1", PURL: "pkg:golang/example.com/lib@v1?variant=a"},
		{Repo: `api (C:\Users\alice\services\api)`, RepoPath: `C:\Users\alice\services\api`, Language: "go", Name: "example.com/lib", Version: "v1", PURL: "pkg:golang/example.com/lib@v1?variant=b"},
	}}

	output, bom := formatPortfolioCycloneDXForTest(t, reportData)
	for _, path := range []string{"/home/alice/platform/api", `C:\Users\alice\services\api`} {
		if strings.Contains(output, path) {
			t.Fatalf("absolute path leaked into CycloneDX output: %q", output)
		}
	}
	refs := map[string]struct{}{}
	for _, component := range bom.Components {
		if component.BOMRef == "" {
			t.Fatalf("expected dependency bom-ref, got %#v", component)
		}
		if _, exists := refs[component.BOMRef]; exists {
			t.Fatalf("expected distinct dependency bom-refs, got %#v", bom.Components)
		}
		refs[component.BOMRef] = struct{}{}
		assertCycloneDXProperty(t, component.Properties, "lopper:repo", "api")
	}

	relocated := reportData
	relocated.PortfolioComponents = append([]PortfolioComponent(nil), reportData.PortfolioComponents...)
	relocated.PortfolioComponents[0].Repo = "api (/srv/build/platform/api)"
	relocated.PortfolioComponents[0].RepoPath = "/srv/build/platform/api"
	relocated.PortfolioComponents[1].Repo = `api (D:\agents\services\api)`
	relocated.PortfolioComponents[1].RepoPath = `D:\agents\services\api`
	relocatedOutput, _ := formatPortfolioCycloneDXForTest(t, relocated)
	if output != relocatedOutput {
		t.Fatalf("expected relocated duplicate repositories to retain stable CycloneDX output\nfirst:\n%s\nrelocated:\n%s", output, relocatedOutput)
	}
}

func TestPortfolioCycloneDXKeepsRelativeDuplicateRepositoryLabels(t *testing.T) {
	path := "services/api"
	for _, testCase := range []struct {
		name string
		dep  PortfolioComponent
		want string
	}{
		{
			name: "named",
			dep:  PortfolioComponent{Repo: "api (services/api)", RepoPath: path, Language: "go", Name: "example.com/lib"},
			want: "api%20%28services%2Fapi%29",
		},
		{
			name: "nameless",
			dep:  PortfolioComponent{Repo: crossRepoRepositoryLabel(RepoInput{Path: path}, map[string]int{path: 1}), RepoPath: path, Language: "go", Name: "example.com/lib"},
			want: "services%2Fapi:services%2Fapi",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if ref := portfolioDependencyRef(testCase.dep); !strings.Contains(ref, testCase.want) {
				t.Fatalf("relative duplicate repository ref lost its distinct label/path: %q", ref)
			}
		})
	}
}

func TestPortfolioCycloneDXSanitizesNamelessAbsoluteRepositoryLabels(t *testing.T) {
	paths := []string{"/home/alice/platform/api", `C:\Users\alice\services\api`}
	reportData := Report{PortfolioComponents: make([]PortfolioComponent, 0, len(paths))}
	variants := []string{"a", "b"}
	for index, path := range paths {
		label := crossRepoRepositoryLabel(RepoInput{Path: path}, map[string]int{path: 1})
		reportData.PortfolioComponents = append(reportData.PortfolioComponents, PortfolioComponent{
			Repo: label, RepoPath: path, Language: "go", Name: "example.com/lib", Version: "v1", PURL: "pkg:golang/example.com/lib@v1?variant=" + variants[index],
		})
	}

	output, bom := formatPortfolioCycloneDXForTest(t, reportData)
	for _, path := range paths {
		if strings.Contains(output, path) {
			t.Fatalf("nameless absolute path leaked into CycloneDX output: %q", output)
		}
	}
	for _, component := range bom.Components {
		assertCycloneDXProperty(t, component.Properties, "lopper:repo", "")
	}

	relocated := reportData
	relocated.PortfolioComponents = append([]PortfolioComponent(nil), reportData.PortfolioComponents...)
	for index, path := range []string{"/srv/build/platform/api", `D:\agents\services\api`} {
		relocated.PortfolioComponents[index].Repo = crossRepoRepositoryLabel(RepoInput{Path: path}, map[string]int{path: 1})
		relocated.PortfolioComponents[index].RepoPath = path
	}
	relocatedOutput, _ := formatPortfolioCycloneDXForTest(t, relocated)
	if output != relocatedOutput {
		t.Fatalf("expected relocated nameless repositories to retain stable CycloneDX output\nfirst:\n%s\nrelocated:\n%s", output, relocatedOutput)
	}
}
