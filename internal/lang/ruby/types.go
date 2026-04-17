package ruby

import (
	"regexp"

	"github.com/ben-ranford/lopper/internal/lang/shared"
)

const (
	gemfileName     = "Gemfile"
	gemfileLockName = "Gemfile.lock"
	gemspecExt      = ".gemspec"
	maxDetectFiles  = 1024
)

const (
	rubyDependencySourceBundler  = "bundler"
	rubyDependencySourceRubygems = "rubygems"
	rubyDependencySourceGit      = "git"
	rubyDependencySourcePath     = "path"
	rubyGemfileSectionGem        = "GEM"
	rubyGemfileSectionGit        = "GIT"
	rubyGemfileSectionPath       = "PATH"
	rubyGemfileSpecsSection      = "specs:"
)

var (
	gemDeclarationPattern       = regexp.MustCompile(`^\s*gem\s+["']([^"']+)["']`)
	gemGitOptionPattern         = regexp.MustCompile(`(?:^|[,\s])(?::?git\s*=>|git\s*:)`)
	gemPathOptionPattern        = regexp.MustCompile(`(?:^|[,\s])(?::?path\s*=>|path\s*:)`)
	gemSpecPattern              = regexp.MustCompile(`^\s{2,}([A-Za-z0-9_.-]+)\s+\(`)
	gemTopLevelSpecPattern      = regexp.MustCompile(`^\s{4}([A-Za-z0-9_.-]+)\s+\(`)
	gemspecDependencyPattern    = regexp.MustCompile(`^\s*(?:[A-Za-z_][A-Za-z0-9_]*\.)?add(?:_runtime|_development)?_dependency\s*(?:\(\s*)?["']([^"']+)["']`)
	gemspecDependencyLineSignal = regexp.MustCompile(`\badd(?:_runtime|_development)?_dependency\b`)
	requirePattern              = regexp.MustCompile(`^\s*require(_relative)?\s+["']([^"']+)["']`)
	rubySkippedDirs             = map[string]bool{
		".bundle":  true,
		"coverage": true,
	}
)

type importBinding = shared.ImportRecord

type fileScan struct {
	Imports []importBinding
	Usage   map[string]int
}

type scanResult struct {
	Files                []fileScan
	Warnings             []string
	DeclaredDependencies map[string]struct{}
	DeclaredSources      map[string]rubyDependencySource
	ImportedDependencies map[string]struct{}
}

type rubyDependencySource struct {
	Rubygems        bool
	Git             bool
	Path            bool
	DeclaredGemfile bool
	DeclaredLock    bool
}
