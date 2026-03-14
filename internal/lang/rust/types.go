package rust

import (
	"regexp"

	"github.com/ben-ranford/lopper/internal/lang/shared"
)

const (
	rustAdapterID        = "rust"
	cargoTomlName        = "Cargo.toml"
	cargoLockName        = "Cargo.lock"
	maxDetectionEntries  = 2048
	maxScanFiles         = 2048
	maxScannableRustFile = 2 * 1024 * 1024
	maxManifestCount     = 256
	maxWarningSamples    = 5
	localModuleCacheSep  = "\x00"
	workspaceFieldStart  = "members"
)

type dependencyInfo struct {
	Canonical string
	LocalPath bool
	Renamed   bool
}

type manifestMeta struct {
	HasPackage       bool
	WorkspaceMembers []string
}

type importBinding = shared.ImportRecord

type fileScan struct {
	Path    string
	Imports []importBinding
	Usage   map[string]int
}

type scanResult struct {
	Files                    []fileScan
	Warnings                 []string
	UnresolvedImports        map[string]int
	RenamedAliasesByDep      map[string][]string
	LocalModuleCache         map[string]bool
	MacroAmbiguityDetected   bool
	SkippedLargeFiles        int
	SkippedFilesByBoundLimit bool
}

type useImportContext struct {
	FilePath  string
	Line      int
	Column    int
	CrateRoot string
	DepLookup map[string]dependencyInfo
	Scan      *scanResult
}

type usePathEntry struct {
	Path     string
	Symbol   string
	Local    string
	Wildcard bool
}

var (
	tablePattern       = regexp.MustCompile(`^\s*\[([^\]]+)\]\s*$`)
	stringFieldPattern = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_-]*)\s*=\s*("(?:[^"]*)"|'(?:[^']*)')`)
	externCratePattern = regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?extern\s+crate\s+([A-Za-z_][A-Za-z0-9_]*)(?:\s+as\s+([A-Za-z_][A-Za-z0-9_]*))?\s*;`)
	useStmtPattern     = regexp.MustCompile(`(?ms)^\s*(?:pub(?:\([^)]*\))?\s+)?use\s+(.+?);`)
	macroInvokePattern = regexp.MustCompile(`(?m)\b[A-Za-z_][A-Za-z0-9_]*!\s*(?:\(|\{|\[)`)
)

var rustStdRoots = map[string]bool{
	"alloc":      true,
	"core":       true,
	"proc-macro": true,
	"std":        true,
	"test":       true,
}
