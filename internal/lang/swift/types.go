package swift

import "github.com/ben-ranford/lopper/internal/lang/shared"

const (
	swiftAdapterID               = "swift"
	swiftCarthagePreviewFlagName = "swift-carthage-preview"
	packageManifestName          = "Package.swift"
	packageResolvedName          = "Package.resolved"
	podManifestName              = "Podfile"
	podLockName                  = "Podfile.lock"
	carthageManifestName         = "Cartfile"
	carthageResolvedName         = "Cartfile.resolved"
	maxDetectFiles               = 2048
	maxScanFiles                 = 4096
	maxScannableSwiftFile        = 2 * 1024 * 1024
	maxManifestDeclarations      = 512
	maxPodDeclarations           = 512
	maxCarthageDeclarations      = 512
	maxWarningSamples            = 5
	ambiguousDependencyKey       = "\x00"
	swiftPackageManager          = "swiftpm"
	cocoaPodsManager             = "cocoapods"
	carthageManager              = "carthage"
)

type importBinding = shared.ImportRecord

type fileScan struct {
	Path    string
	Imports []importBinding
	Usage   map[string]int
}

type dependencyMeta struct {
	Declared             bool
	Resolved             bool
	Version              string
	Revision             string
	Source               string
	DeclaredViaSwiftPM   bool
	ResolvedViaSwiftPM   bool
	DeclaredViaCocoaPods bool
	ResolvedViaCocoaPods bool
	DeclaredViaCarthage  bool
	ResolvedViaCarthage  bool
}

type dependencyCatalog struct {
	Dependencies       map[string]dependencyMeta
	AliasToDependency  map[string]string
	ModuleToDependency map[string]string
	LocalModules       map[string]struct{}
	HasSwiftPM         bool
	HasCocoaPods       bool
	HasCarthage        bool
}

type scanResult struct {
	Files                []fileScan
	Warnings             []string
	KnownDependencies    map[string]struct{}
	ImportedDependencies map[string]struct{}
}

type repoScanner struct {
	repoPath          string
	catalog           dependencyCatalog
	scan              scanResult
	unresolvedImports map[string]int
	foundSwift        bool
	skippedLargeFiles int
	visited           int
}

type swiftStringScanState struct {
	inString     bool
	multiline    bool
	rawHashCount int
	escaped      bool
	blockDepth   int
}

type resolvedPin struct {
	Identity      string `json:"identity"`
	Package       string `json:"package"`
	Location      string `json:"location"`
	RepositoryURL string `json:"repositoryURL"`
	State         struct {
		Version  string `json:"version"`
		Revision string `json:"revision"`
		Branch   string `json:"branch"`
	} `json:"state"`
}

type resolvedDocument struct {
	Pins   []resolvedPin `json:"pins"`
	Object struct {
		Pins []resolvedPin `json:"pins"`
	} `json:"object"`
}

type podLockDocument struct {
	Pods            []any                     `yaml:"PODS"`
	Dependencies    []string                  `yaml:"DEPENDENCIES"`
	ExternalSources map[string]map[string]any `yaml:"EXTERNAL SOURCES"`
	CheckoutOptions map[string]map[string]any `yaml:"CHECKOUT OPTIONS"`
}

type podLockEntry struct {
	Name    string
	Version string
	Source  string
}

type carthageDependency struct {
	Kind       string
	Source     string
	Reference  string
	Dependency string
}
