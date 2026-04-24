package golang

const (
	goModName                          = "go.mod"
	goWorkName                         = "go.work"
	goVendoredProvenancePreviewFeature = "go-vendored-provenance-preview"
	vendorModulesTxtName               = "vendor/modules.txt"
	maxScannableGoFile                 = 2 * 1024 * 1024
)

var goSkippedDirs = map[string]bool{
	"bin":        true,
	".artifacts": true,
}
