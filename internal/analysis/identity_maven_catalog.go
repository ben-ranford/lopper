package analysis

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/report/model"
	"github.com/ben-ranford/lopper/internal/safeio"
)

var readMavenIdentityFile = safeio.ReadFileUnderLimit
var decodeMavenIdentityFile = shared.DecodePOM

func eligibleMavenIdentityPath(path string) bool {
	parts := strings.Split(path, "/")
	if parts[len(parts)-1] != "pom.xml" {
		return false
	}
	for _, part := range parts[:len(parts)-1] {
		if shouldSkipIdentityDir(part) {
			return false
		}
	}
	return true
}
func collectMavenCatalogEvidence(ctx context.Context, repo string, index identityIndex, paths []string, documents []report.MavenManifest, warnings *identityWarningCollector) {
	captured := make(map[string]report.MavenManifest, len(documents))
	union := make(map[string]struct{}, len(paths)+len(documents))
	for _, path := range paths {
		union[relativeIdentitySource(repo, path)] = struct{}{}
	}
	for _, document := range documents {
		if eligibleMavenIdentityPath(document.Path()) {
			captured[document.Path()] = document
			union[document.Path()] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(union))
	for path := range union {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	for _, path := range ordered {
		if ctx.Err() != nil {
			return
		}
		document, ok := captured[path]
		if !ok {
			collectPomIdentityEvidence(repo, filepath.Join(repo, filepath.FromSlash(path)), index, warnings)
			continue
		}
		collectMavenCatalogDocument(repo, document, index, warnings)
	}
}
func collectMavenCatalogDocument(repo string, document report.MavenManifest, index identityIndex, warnings *identityWarningCollector) {
	stage, kind := document.Failure()
	if stage != "" {
		operation := identityReadFailed
		if stage == "parse" {
			operation = identityParseFailed
		}
		warnings.addFailure(stage, filepath.Join(repo, filepath.FromSlash(document.Path())), operation, mavenCatalogFailure(kind))
		return
	}
	addPomIdentityEvidence(shared.POMConsumerView{Properties: document.Properties(), Dependencies: document.Dependencies(), ManagedDependencies: document.ManagedDependencies()}, document.Path(), index)
}
func mavenCatalogFailure(kind string) error {
	switch kind {
	case "permission":
		return fs.ErrPermission
	case "missing":
		return fs.ErrNotExist
	case "large":
		return safeio.ErrFileTooLarge
	default:
		return errors.New("maven evidence failure")
	}
}

func mergedMavenEvidence(identityRoot string, reports []report.Report) ([]report.MavenManifest, bool, error) {
	entries := make(map[string]report.MavenManifest)
	present := false
	for _, current := range reports {
		present = present || current.MavenManifestCatalog
		for _, document := range current.MavenManifests {
			rebased, err := model.RebaseMavenManifest(document, current.RepoPath, identityRoot)
			if err != nil {
				return nil, false, err
			}
			if previous, ok := entries[rebased.Path()]; ok && !previous.Equal(rebased) {
				return nil, false, errors.New("conflicting captured Maven evidence")
			}
			entries[rebased.Path()] = rebased
		}
	}
	paths := make([]string, 0, len(entries))
	for path := range entries {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	result := make([]report.MavenManifest, 0, len(paths))
	for _, path := range paths {
		result = append(result, entries[path])
	}
	if _, err := model.MavenEvidenceSize(result); err != nil {
		return nil, false, err
	}
	return result, present, nil
}
func clearMavenEvidence(value *report.Report) {
	value.MavenManifests = nil
	value.MavenManifestCatalog = false
}

type mavenEvidenceAccumulator struct {
	root         string
	entries      map[string]report.MavenManifest
	size, values int
}

func newMavenEvidenceAccumulator(root string) *mavenEvidenceAccumulator {
	size := model.MavenEvidenceEmptySize
	return &mavenEvidenceAccumulator{root: root, entries: make(map[string]report.MavenManifest), size: size}
}
func (a *mavenEvidenceAccumulator) accept(current report.Report) error {
	for _, document := range current.MavenManifests {
		rebased, err := model.RebaseMavenManifest(document, current.RepoPath, a.root)
		if err != nil {
			return err
		}
		if err := a.add(rebased); err != nil {
			return err
		}
	}
	return nil
}
func (a *mavenEvidenceAccumulator) add(document report.MavenManifest) error {
	if document.Size() == 0 {
		return errors.New("uninitialized Maven evidence")
	}
	if previous, ok := a.entries[document.Path()]; ok {
		if !previous.Equal(document) {
			return errors.New("conflicting captured Maven evidence")
		}
		return nil
	}
	size := a.size + document.Size()
	if len(a.entries) > 0 {
		size++
	}
	values := a.values + document.Values()
	if len(a.entries) >= model.MavenEvidenceEntryLimit || size > model.MavenEvidenceByteLimit || values > model.MavenEvidenceValueLimit {
		return model.ErrMavenEvidenceLimit
	}
	a.size = size
	a.values = values
	a.entries[document.Path()] = document
	return nil
}
