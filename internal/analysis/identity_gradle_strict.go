package analysis

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

type identityDiscoveryPolicy struct {
	limit int
	visit func(string) error
}

func identitySnapshotForPolicy(ctx context.Context, repo string, warnings *identityWarningCollector, policy identityDiscoveryPolicy) (identityManifestSnapshot, error) {
	if policy.limit == 0 {
		return discoverIdentityManifestSnapshotWithContext(ctx, repo, warnings), nil
	}
	return discoverStrictIdentitySnapshotWithPolicy(ctx, repo, policy)
}

func discoverStrictIdentitySnapshot(ctx context.Context, repo string, limit int) (identityManifestSnapshot, error) {
	return discoverStrictIdentitySnapshotWithPolicy(ctx, repo, identityDiscoveryPolicy{limit: limit})
}
func discoverStrictIdentitySnapshotWithPolicy(ctx context.Context, repo string, policy identityDiscoveryPolicy) (identityManifestSnapshot, error) {
	limit := policy.limit
	if limit <= 0 {
		return identityManifestSnapshot{}, shared.GradleDiscoveryFailure(repo, "identity discovery", fmt.Errorf("invalid identity file limit: %d", limit))
	}
	snapshot := identityManifestSnapshot{}
	truncated, err := shared.WalkRepoFilesWithErrors(ctx, repo, limit, shouldSkipIdentityDir, func(path string, entry fs.DirEntry) error {
		recordDiscoveredIdentityManifest(&snapshot, relativeIdentitySource(repo, path), path, entry.Name())
		snapshot.recordGradleEntryKind(path, entry)
		if policy.visit != nil {
			return policy.visit(path)
		}
		return nil
	}, func(_ string, err error) error { return err })
	err = errors.Join(err, ctx.Err())
	if truncated {
		err = errors.Join(err, &shared.GradleDiscoveryError{Path: repo, Operation: "identity discovery", Resource: "identity entries", Limit: int64(limit), ObservedAtLeast: int64(limit) + 1, Err: shared.ErrGradleDiscoveryLimit})
	}
	if err != nil {
		return identityManifestSnapshot{}, shared.GradleDiscoveryFailure(repo, "identity discovery", err)
	}
	sortIdentityManifestSnapshot(&snapshot)
	return snapshot, nil
}

func collectJVMIdentityForPolicy(ctx context.Context, repo string, index identityIndex, snapshot identityManifestSnapshot, warnings *identityWarningCollector, documents []report.MavenManifest, strict bool) error {
	if !strict {
		collectJVMIdentityEvidenceFromSnapshot(ctx, repo, index, snapshot, warnings, documents...)
		return nil
	}
	collectMavenCatalogEvidence(ctx, repo, index, snapshot.pomFiles, documents, warnings)
	return collectStrictGradleIdentity(ctx, repo, index, snapshot, warnings)
}

func collectStrictGradleIdentity(ctx context.Context, repo string, index identityIndex, snapshot identityManifestSnapshot, warnings *identityWarningCollector) (err error) {
	root, err := safeio.OpenRootNoFollow(repo)
	if err != nil {
		return shared.GradleDiscoveryFailure(repo, "identity root", err)
	}
	defer func() {
		err = shared.GradleDiscoveryFailure(repo, "identity", errors.Join(err, root.Close(), ctx.Err()))
	}()
	budget := shared.NewGradleDiscoveryBudget(repo, snapshot.gradleRegularCatalogFiles...)
	resolver, messages, err := shared.LoadGradleCatalogResolverStrict(ctx, repo, root, budget)
	if err != nil {
		return err
	}
	for _, message := range messages {
		warnings.append(message)
	}
	declarations := make(map[string]map[string]struct{}, len(snapshot.gradleBuildFiles))
	for _, path := range snapshot.gradleBuildFiles {
		if snapshot.skipNonRegularGradle(path, warnings) {
			continue
		}
		if err := collectStrictGradleDeclaration(ctx, repo, path, root, budget, &resolver, index, declarations, warnings); err != nil {
			return err
		}
	}
	for _, path := range snapshot.gradleLockFiles {
		if snapshot.skipNonRegularGradle(path, warnings) {
			continue
		}
		data, err := readStrictGradleIdentity(ctx, repo, path, root, budget)
		if err != nil {
			return err
		}
		if err := collectStrictGradleLock(ctx, relativeIdentitySource(repo, path), data, index, declarations[filepath.ToSlash(filepath.Dir(path))]); err != nil {
			return err
		}
	}
	return nil
}

func readStrictGradleIdentity(ctx context.Context, repo, path string, root safeio.Root, budget *shared.GradleDiscoveryBudget) ([]byte, error) {
	rel, err := filepath.Rel(repo, path)
	if err != nil {
		return nil, shared.GradleDiscoveryFailure(path, "identity read", err)
	}
	return budget.ReadWithinRoot(ctx, root, rel, path)
}

func collectStrictGradleDeclaration(ctx context.Context, repo, path string, root safeio.Root, budget *shared.GradleDiscoveryBudget, resolver *shared.GradleCatalogResolver, index identityIndex, declarations map[string]map[string]struct{}, warnings *identityWarningCollector) error {
	data, err := readStrictGradleIdentity(ctx, repo, path, root, budget)
	if err != nil {
		return err
	}
	coordinates, err := shared.ParseGradleDependencyCoordinatesForFileContext(ctx, path, data)
	if err != nil {
		return err
	}
	libraries, messages, err := resolver.ParseDependencyReferencesContext(ctx, path, data)
	if err != nil {
		return err
	}
	for _, message := range messages {
		warnings.append(message)
	}
	source := relativeIdentitySource(repo, path)
	project := filepath.ToSlash(filepath.Dir(path))
	if declarations[project] == nil {
		declarations[project] = make(map[string]struct{})
	}
	for _, coordinate := range coordinates {
		addMavenEvidence(index, coordinate.Group, coordinate.Artifact, coordinate.Version, source, identityStatusDeclared)
		declarations[project][gradleCoordinateKey(coordinate.Group, coordinate.Artifact)] = struct{}{}
	}
	for _, library := range libraries {
		addMavenEvidence(index, library.Group, library.Artifact, library.Version, source, identityStatusDeclared)
		declarations[project][gradleCoordinateKey(library.Group, library.Artifact)] = struct{}{}
	}
	return shared.GradleDiscoveryFailure(path, "identity parse", ctx.Err())
}

func collectStrictGradleLock(ctx context.Context, source string, data []byte, index identityIndex, declarations map[string]struct{}) error {
	for line := range strings.SplitSeq(string(data), "\n") {
		if err := ctx.Err(); err != nil {
			return shared.GradleDiscoveryFailure(source, "lock parse", err)
		}
		matches := gradleLockPattern.FindStringSubmatch(strings.TrimSpace(line))
		if len(matches) != 4 {
			continue
		}
		if _, ok := declarations[gradleCoordinateKey(matches[1], matches[2])]; ok {
			addMavenEvidence(index, strings.Clone(matches[1]), strings.Clone(matches[2]), strings.Clone(matches[3]), source, identityStatusResolved)
		}
	}
	return nil
}

func (s *identityManifestSnapshot) recordGradleEntryKind(path string, entry fs.DirEntry) {
	if entry.Type().IsRegular() {
		switch strings.ToLower(entry.Name()) {
		case "settings.gradle", "settings.gradle.kts", "libs.versions.toml":
			s.gradleRegularCatalogFiles = append(s.gradleRegularCatalogFiles, path)
		}
		return
	}
	switch entry.Name() {
	case "build.gradle", "build.gradle.kts", "gradle.lockfile":
		if s.gradleNonRegular == nil {
			s.gradleNonRegular = make(map[string]fs.FileMode)
		}
		s.gradleNonRegular[path] = entry.Type()
	}
}

func (s *identityManifestSnapshot) skipNonRegularGradle(path string, warnings *identityWarningCollector) bool {
	mode, known := s.gradleNonRegular[path]
	if !known {
		return false
	}
	err := fs.ErrInvalid
	if mode&fs.ModeSymlink != 0 {
		err = safeio.ErrTargetPathSymlink
	}
	warnings.addFailure("read", path, identityReadFailed, err)
	return true
}
