package shared

// ScannedFile holds source attribution and the import usage observed by an
// adapter. Adapters can embed it alongside language-specific scan metadata.
type ScannedFile struct {
	Path    string
	Imports []ImportRecord
	Usage   map[string]int
}

// dependencyUsage projects the scan into the shared reporting model. Imports
// and usage retain their backing storage; reporting treats them as read-only.
func (f *ScannedFile) dependencyUsage() FileUsage {
	return FileUsage{Imports: f.Imports, Usage: f.Usage}
}

type dependencyUsager interface {
	dependencyUsage() FileUsage
}

type fileUsageProvider[T any] interface {
	*T
	dependencyUsager
}

// FileUsages preserves scan order and returns an allocated slice, including for
// an empty scan. Adapters may embed ScannedFile alongside their own metadata.
func FileUsages[T any, P fileUsageProvider[T]](files []T) []FileUsage {
	usages := make([]FileUsage, len(files))
	for i := range files {
		usages[i] = P(&files[i]).dependencyUsage()
	}
	return usages
}
