package powershell

import (
	"regexp"

	"github.com/ben-ranford/lopper/internal/lang/shared"
)

const (
	adapterID                   = "powershell"
	maxDetectFiles              = 2048
	maxScanFiles                = 8192
	maxScannablePowerShellBytes = 2 * 1024 * 1024
	moduleManifestExt           = ".psd1"
	moduleScriptExt             = ".psm1"
	scriptExt                   = ".ps1"
)

const (
	usageSourceImportModule   = "import-module"
	usageSourceUsingModule    = "using-module"
	usageSourceRequiresModule = "requires-modules"

	dependencySourceManifest = "module-manifest"
)

var (
	requiredModulesAssignmentPattern = regexp.MustCompile(`(?i)\brequiredmodules\b\s*=`)
	importModulePattern              = regexp.MustCompile(`(?i)^\s*import-module\b(.*)$`)
	usingModulePattern               = regexp.MustCompile(`(?i)^\s*using\s+module\s+(.+)$`)
	requiresDirectivePattern         = regexp.MustCompile(`(?i)^\s*#\s*requires\b(.*)$`)
	requiresModulesOptionPattern     = regexp.MustCompile(`(?i)-modules\b(.*)$`)
	moduleNamePattern                = regexp.MustCompile(`(?is)\bmodulename\b\s*=\s*([^;\r\n}]+)`)
	powerShellSkippedDirs            = map[string]bool{}
)

type importBinding struct {
	Record shared.ImportRecord
	Source string
}

type fileScan struct {
	Imports []importBinding
	Usage   map[string]int
}

type scanResult struct {
	Files                []fileScan
	Warnings             []string
	DeclaredDependencies map[string]struct{}
	DeclaredSources      map[string]powerShellDependencySource
	ImportedDependencies map[string]struct{}
}

type powerShellDependencySource struct {
	ManifestPaths map[string]struct{}
}

func (s *powerShellDependencySource) addManifest(path string) {
	if s.ManifestPaths == nil {
		s.ManifestPaths = make(map[string]struct{})
	}
	s.ManifestPaths[path] = struct{}{}
}
