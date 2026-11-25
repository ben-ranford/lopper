package js

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	maxLicenseCandidates  = 5
	maxLicenseWalkEntries = 4096
	maxLicenseFileBytes   = 256 * 1024
)

func detectLicenseAndProvenance(depRoot string, includeRegistryProvenance bool) (*report.DependencyLicense, *report.DependencyProvenance, []string) {
	if strings.TrimSpace(depRoot) == "" {
		return unknownDependencyLicense(), unknownDependencyProvenance(), []string{"unable to resolve dependency root for license/provenance detection"}
	}
	pkg, warnings := loadDependencyPackageJSON(depRoot)
	license, fileStatus := detectLicenseFromMetadataOrFiles(pkg, depRoot)
	warnings = append(warnings, fileStatus.warning()...)
	provenance := buildProvenance(pkg, includeRegistryProvenance)
	return license, provenance, warnings
}

func detectLicenseFromMetadataOrFiles(pkg packageJSON, depRoot string) (*report.DependencyLicense, licenseFileStatus) {
	if license := detectLicenseFromPackageJSON(pkg); license != nil {
		return license, licenseFileStatus{}
	}
	license, status := detectLicenseFromFilesWithStatus(depRoot)
	if license != nil {
		return license, status
	}
	return unknownDependencyLicense(), status
}

func unknownDependencyLicense() *report.DependencyLicense {
	return &report.DependencyLicense{
		Source:     "unknown",
		Confidence: "low",
		Unknown:    true,
	}
}

func unknownDependencyProvenance() *report.DependencyProvenance {
	return &report.DependencyProvenance{
		Source:     "unknown",
		Confidence: "low",
	}
}

func detectLicenseFromPackageJSON(pkg packageJSON) *report.DependencyLicense {
	raw := packageJSONLicenseRaw(pkg)
	if raw == "" {
		return nil
	}
	return synthesizePackageJSONLicense(raw)
}

func packageJSONLicenseRaw(pkg packageJSON) string {
	raw := strings.TrimSpace(parsePackageJSONLicense(pkg.License))
	if raw == "" {
		for _, item := range pkg.Licenses {
			raw = strings.TrimSpace(parsePackageJSONLicense(item))
			if raw != "" {
				break
			}
		}
	}
	return raw
}

func synthesizePackageJSONLicense(raw string) *report.DependencyLicense {
	spdx := normalizeSPDXExpression(raw)
	unknown := strings.TrimSpace(spdx) == ""
	if unknown {
		spdx = ""
	}
	confidence := "medium"
	if !unknown {
		confidence = "high"
	}
	return &report.DependencyLicense{
		SPDX:       spdx,
		Raw:        raw,
		Source:     "package.json",
		Confidence: confidence,
		Unknown:    unknown,
	}
}

func parsePackageJSONLicense(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		if licenseType, ok := typed["type"].(string); ok {
			return licenseType
		}
	case json.RawMessage:
		var decodedLicense string
		if json.Unmarshal(typed, &decodedLicense) == nil {
			return decodedLicense
		}
	}
	return ""
}

func normalizeSPDXExpression(raw string) string {
	replaced := strings.TrimSpace(raw)
	replaced = strings.ReplaceAll(replaced, "(", " ( ")
	replaced = strings.ReplaceAll(replaced, ")", " ) ")
	replaced = strings.ReplaceAll(replaced, " and ", " AND ")
	replaced = strings.ReplaceAll(replaced, " or ", " OR ")
	replaced = strings.ReplaceAll(replaced, "\t", " ")
	replaced = strings.ReplaceAll(replaced, "\n", " ")
	replaced = strings.ReplaceAll(replaced, "\r", " ")
	parts := strings.Fields(replaced)
	if len(parts) == 0 {
		return ""
	}

	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		upper := strings.ToUpper(part)
		switch upper {
		case "AND", "OR", "WITH", "(", ")", "+":
			normalized = append(normalized, upper)
			continue
		}
		id := normalizeSPDXToken(part)
		if id == "" {
			continue
		}
		normalized = append(normalized, id)
	}
	if len(normalized) == 0 {
		return ""
	}
	return strings.Join(normalized, " ")
}

func normalizeSPDXToken(value string) string {
	return report.NormalizeSPDXID(value)
}

type licenseFileProbe struct {
	path       string
	spdx       string
	confidence string
}

type licenseFileStatus struct {
	limited          bool
	skippedLarge     bool
	skippedDirectory bool
	walkError        bool
}

func (s *licenseFileStatus) warning() []string {
	switch {
	case s.skippedLarge:
		return []string{"license file detection incomplete: a candidate exceeded the file size limit"}
	case s.skippedDirectory:
		return []string{"license file detection incomplete: a directory could not be read"}
	case s.limited:
		return []string{"license file detection incomplete: scan limit reached"}
	case s.walkError:
		return []string{"license file detection incomplete: directory traversal failed"}
	default:
		return nil
	}
}

func detectLicenseFromFiles(depRoot string) *report.DependencyLicense {
	license, _ := detectLicenseFromFilesWithStatus(depRoot)
	return license
}

func detectLicenseFromFilesWithStatus(depRoot string) (*report.DependencyLicense, licenseFileStatus) {
	probe, status := probeLicenseFiles(depRoot)
	if probe == nil {
		return nil, status
	}
	return synthesizeLicenseFromFileProbe(*probe), status
}

func probeLicenseFiles(depRoot string) (*licenseFileProbe, licenseFileStatus) {
	return probeLicenseFilesWithLimits(depRoot, maxLicenseCandidates, maxLicenseWalkEntries, maxLicenseFileBytes)
}

var errLicenseFound = errors.New("license file found")

func probeLicenseFilesWithLimits(depRoot string, maxCandidates, maxWalkEntries int, maxBytes int64) (*licenseFileProbe, licenseFileStatus) {
	status := licenseFileStatus{}
	var probe *licenseFileProbe
	budget := licenseWalkBudget(maxCandidates, maxWalkEntries, func(string, error) {
		status.skippedDirectory = true
	})
	err := walkLicenseFiles(depRoot, budget, func(file shared.RootedWalkFile) error {
		if !isLicenseCandidate(file.Path) {
			return nil
		}
		content, err := safeio.ReadRegularFileWithinRootLimit(file.Parent, file.Leaf, maxBytes)
		if err != nil {
			status.skippedLarge = status.skippedLarge || errors.Is(err, safeio.ErrFileTooLarge)
			return nil
		}
		spdx, confidence := detectSPDXFromLicenseContent(string(content))
		if spdx == "" {
			return nil
		}
		probe = &licenseFileProbe{path: file.Path, spdx: spdx, confidence: confidence}
		return errLicenseFound
	})
	if shared.IsPureSentinelError(err, errLicenseFound) {
		return probe, status
	}
	if err != nil {
		if _, limited := shared.RootedWalkBudgetWarning("license file detection", budget, err); limited {
			status.limited = true
		} else {
			status.walkError = true
		}
	}
	return probe, status
}

func synthesizeLicenseFromFileProbe(probe licenseFileProbe) *report.DependencyLicense {
	return &report.DependencyLicense{
		SPDX:       probe.spdx,
		Source:     "license-file",
		Confidence: probe.confidence,
		Evidence:   []string{filepath.Base(probe.path)},
	}
}

func findLicenseFiles(depRoot string) []string {
	files := make([]string, 0, maxLicenseCandidates)
	if err := walkLicenseFiles(depRoot, licenseWalkBudget(maxLicenseCandidates, maxLicenseWalkEntries, nil), func(file shared.RootedWalkFile) error {
		if isLicenseCandidate(file.Path) {
			files = append(files, file.Path)
		}
		return nil
	}); err != nil {
		return files
	}
	return files
}

func walkLicenseFiles(depRoot string, budget shared.RootedWalkBudget, visit shared.RootedWalkVisit) (err error) {
	root, err := safeio.OpenRootNoFollow(depRoot)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	return shared.WalkRepoFilesWithinRootPinned(context.Background(), depRoot, root, budget, shouldSkipLicenseDir, visit)
}

func licenseWalkBudget(maxCandidates, maxWalkEntries int, onSkippedDirectoryError func(string, error)) shared.RootedWalkBudget {
	return shared.RootedWalkBudget{
		MaxTraversalEntries: maxWalkEntries,
		MaxFiles:            maxCandidates,
		MaxWorkItems:        maxCandidates,
		CountCandidate: func(path string, _ fs.DirEntry) bool {
			return isLicenseCandidate(path)
		},
		OnSkippedDescendantDirectoryError: onSkippedDirectoryError,
	}
}

func shouldSkipLicenseDir(name string) bool {
	return strings.EqualFold(name, "node_modules")
}

func isLicenseCandidate(path string) bool {
	base := strings.ToUpper(filepath.Base(path))
	return strings.HasPrefix(base, "LICENSE") || strings.HasPrefix(base, "COPYING")
}

func detectSPDXFromLicenseContent(content string) (string, string) {
	text := strings.ToLower(content)
	switch {
	case strings.Contains(text, "mit license"):
		return "MIT", "medium"
	case strings.Contains(text, "apache license") && strings.Contains(text, "version 2.0"):
		return "APACHE-2.0", "medium"
	case strings.Contains(text, "gnu general public license"):
		return "GPL-3.0-OR-LATER", "low"
	case strings.Contains(text, "mozilla public license"):
		return "MPL-2.0", "low"
	case strings.Contains(text, "isc license"):
		return "ISC", "medium"
	case strings.Contains(text, "redistribution and use in source and binary forms"):
		return "BSD-3-CLAUSE", "low"
	default:
		return "", ""
	}
}

func buildProvenance(pkg packageJSON, includeRegistryProvenance bool) *report.DependencyProvenance {
	signals := collectManifestProvenanceSignals(pkg)
	source := "local-manifest"
	confidence := "medium"

	var registrySignals []string
	if includeRegistryProvenance {
		registrySignals = collectRegistryProvenanceSignals(pkg)
	}
	if len(registrySignals) > 0 {
		signals = append(signals, registrySignals...)
		source = "local+registry-heuristics"
		confidence = "high"
	}

	return &report.DependencyProvenance{
		Source:     source,
		Confidence: confidence,
		Signals:    signals,
	}
}

func collectManifestProvenanceSignals(pkg packageJSON) []string {
	signals := []string{"manifest:package.json"}
	if strings.TrimSpace(pkg.Name) != "" {
		signals = append(signals, "name:"+pkg.Name)
	}
	if strings.TrimSpace(pkg.Version) != "" {
		signals = append(signals, "version:"+pkg.Version)
	}
	return signals
}

func collectRegistryProvenanceSignals(pkg packageJSON) []string {
	signals := make([]string, 0, 4)
	if strings.TrimSpace(pkg.PublishConfig.Registry) != "" {
		signals = append(signals, "registry:"+strings.TrimSpace(pkg.PublishConfig.Registry))
	}
	if strings.TrimSpace(pkg.Resolved) != "" {
		signals = append(signals, "resolved")
	}
	if strings.TrimSpace(pkg.Integrity) != "" {
		signals = append(signals, "integrity")
	}
	if hasRepositorySignal(pkg.Repository) {
		signals = append(signals, "repository")
	}
	return signals
}

func hasRepositorySignal(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case map[string]any:
		if url, ok := typed["url"].(string); ok {
			return strings.TrimSpace(url) != ""
		}
	}
	return false
}

func loadDependencyPackageJSON(depRoot string) (packageJSON, []string) {
	pkgPath := filepath.Join(depRoot, "package.json")
	data, err := safeio.ReadFileUnder(depRoot, pkgPath)
	if err != nil {
		return packageJSON{}, []string{fmt.Sprintf("unable to read dependency metadata: %s", pkgPath)}
	}

	var pkg packageJSON
	if json.Unmarshal(data, &pkg) != nil {
		return packageJSON{}, []string{fmt.Sprintf("failed to parse dependency metadata: %s", pkgPath)}
	}
	return pkg, nil
}
