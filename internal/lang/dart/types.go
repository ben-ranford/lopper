package dart

import (
	"regexp"
	"time"

	"github.com/ben-ranford/lopper/internal/lang/shared"
)

type Adapter struct {
	Clock func() time.Time
}

const (
	pubspecYAMLName      = "pubspec.yaml"
	pubspecYMLName       = "pubspec.yml"
	pubspecLockName      = "pubspec.lock"
	maxDetectionEntries  = 2048
	maxManifestCount     = 256
	maxScanFiles         = 4096
	maxScannableDartFile = 2 * 1024 * 1024
	maxWarningSamples    = 5
)

var dartRootSignals = []shared.RootSignal{
	{Name: pubspecYAMLName, Confidence: 60},
	{Name: pubspecYMLName, Confidence: 60},
	{Name: pubspecLockName, Confidence: 20},
}

type dependencyInfo struct {
	Runtime    bool
	Dev        bool
	Override   bool
	LocalPath  bool
	FlutterSDK bool
	PluginLike bool
	Source     string
	Version    string
}

type packageManifest struct {
	Root                     string
	ManifestPath             string
	Dependencies             map[string]dependencyInfo
	HasLock                  bool
	HasFlutterSection        bool
	HasFlutterPluginMetadata bool
}

type pubspecManifest struct {
	Dependencies        map[string]any `yaml:"dependencies"`
	DevDependencies     map[string]any `yaml:"dev_dependencies"`
	DependencyOverrides map[string]any `yaml:"dependency_overrides"`
	Flutter             any            `yaml:"flutter"`
}

type pubspecLock struct {
	Packages map[string]pubspecLockPackage `yaml:"packages"`
}

type pubspecLockPackage struct {
	Dependency  string `yaml:"dependency"`
	Description any    `yaml:"description"`
	Source      string `yaml:"source"`
	Version     string `yaml:"version"`
}

type importBinding = shared.ImportRecord

type fileScan struct {
	Path    string
	Imports []importBinding
	Usage   map[string]int
}

type scanResult struct {
	Files                []fileScan
	Warnings             []string
	DeclaredDependencies map[string]dependencyInfo
	UnresolvedImports    map[string]int
	HasFlutterProject    bool
	HasPluginMetadata    bool
	SkippedLargeFiles    int
	SkippedFilesByBound  bool
}

var (
	directivePattern = regexp.MustCompile(`^\s*(import|export)\s+['"]([^'"]+)['"]([^;]*);`)
	aliasPattern     = regexp.MustCompile(`\bas\s+([A-Za-z_][A-Za-z0-9_]*)`)
	identPattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)
