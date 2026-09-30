package shared

// ScannedFile holds source attribution and the import usage observed by an
// adapter. Adapters can embed it alongside language-specific scan metadata.
type ScannedFile struct {
	Path    string
	Imports []ImportRecord
	Usage   map[string]int
}

// DependencyUsage projects the scan into the shared reporting model. Imports
// and usage retain their backing storage; reporting treats them as read-only.
func (file ScannedFile) DependencyUsage() FileUsage {
	return FileUsage{Imports: file.Imports, Usage: file.Usage}
}

// FileUsages preserves scan order and returns a non-nil slice, including for an
// empty scan. The constraint permits adapters to embed ScannedFile with metadata.
func FileUsages[T interface{ DependencyUsage() FileUsage }](files []T) []FileUsage {
	return MapSlice(files, T.DependencyUsage)
}
