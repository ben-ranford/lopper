package dashboard

import "testing"

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
	for _, value := range []string{"services/api", "./services/api", `services\api`, `.\services\api`, " services/api "} {
		if got := stablePortfolioRefPath(value); got != "services/api" {
			t.Fatalf("relative path %q normalized to %q", value, got)
		}
	}
}
