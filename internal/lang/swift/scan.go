package swift

import (
	"context"
	"fmt"
)

func scanRepo(ctx context.Context, repoPath string, catalog dependencyCatalog) (scanResult, error) {
	scanner := newRepoScanner(repoPath, catalog)
	err := scanner.walkRepo(ctx)
	if err != nil {
		return scanner.scan, err
	}
	scanner.finalize()
	return scanner.scan, nil
}

func newRepoScanner(repoPath string, catalog dependencyCatalog) *repoScanner {
	scan := scanResult{
		KnownDependencies:    make(map[string]struct{}),
		ImportedDependencies: make(map[string]struct{}),
	}
	for dependency := range catalog.Dependencies {
		scan.KnownDependencies[dependency] = struct{}{}
	}
	return &repoScanner{
		repoPath:          repoPath,
		catalog:           catalog,
		scan:              scan,
		unresolvedImports: make(map[string]int),
	}
}

func (s *repoScanner) finalize() {
	if !s.foundSwift {
		s.scan.Warnings = append(s.scan.Warnings, "no Swift files found for analysis")
	}
	if s.visited >= maxScanFiles {
		s.scan.Warnings = append(s.scan.Warnings, fmt.Sprintf("Swift scan capped at %d files", maxScanFiles))
	}
	if s.skippedLargeFiles > 0 {
		s.scan.Warnings = append(s.scan.Warnings, fmt.Sprintf("skipped %d Swift file(s) larger than %d bytes", s.skippedLargeFiles, maxScannableSwiftFile))
	}
	appendUnresolvedImportWarning(&s.scan, s.unresolvedImports, s.catalog)
}
