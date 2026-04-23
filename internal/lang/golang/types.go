package golang

import "github.com/ben-ranford/lopper/internal/lang/shared"

type importBinding = shared.ImportRecord

type fileScan struct {
	Path    string
	Imports []importBinding
	Usage   map[string]int
}

type scanResult struct {
	Files                         []fileScan
	Warnings                      []string
	BlankImportsByDependency      map[string]int
	UndeclaredImportsByDependency map[string]int
	DependencyProvenanceByDep     map[string]goDependencyProvenance
	SkippedGeneratedFiles         int
	SkippedBuildTaggedFiles       int
	SkippedLargeFiles             int
	SkippedNestedModuleDirs       int
}

type moduleInfo struct {
	ModulePath                 string
	LocalModulePaths           []string
	DeclaredDependencies       []string
	ReplacementImports         map[string]string
	VendoredImportDependencies map[string]string
	VendoredDependencies       map[string]vendoredDependencyMetadata
	VendoringWarnings          []string
	VendoredProvenanceEnabled  bool
}

type goDependencyProvenance struct {
	Declared    bool
	Replacement bool
	Vendored    bool
}

type moduleLoadOptions struct {
	EnableVendoredProvenance bool
}

type vendoredDependencyMetadata struct {
	ModulePath         string
	Explicit           bool
	Replacement        bool
	ReplacementTarget  string
	PackageCount       int
	GoVersionDirective string
}

type vendoredModuleMetadata struct {
	ManifestFound      bool
	ImportToDependency map[string]string
	Dependencies       map[string]vendoredDependencyMetadata
	Warnings           []string
}
