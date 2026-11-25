package js

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

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
	candidateLimit bool
	walkLimit      bool
	skippedLarge   bool
}

func (s licenseFileStatus) warning() []string {
	switch {
	case s.skippedLarge:
		return []string{"license file detection incomplete: a candidate exceeded the file size limit"}
	case s.candidateLimit:
		return []string{"license file detection incomplete: candidate limit reached"}
	case s.walkLimit:
		return []string{"license file detection incomplete: traversal limit reached"}
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
	discovery := findLicenseFilesWithLimits(depRoot, maxLicenseCandidates, maxLicenseWalkEntries)
	probe, status := probeLicenseCandidatesWithLimit(depRoot, discovery.files, maxLicenseFileBytes)
	status.candidateLimit = discovery.candidateLimit
	status.walkLimit = discovery.walkLimit
	return probe, status
}

func probeLicenseCandidates(depRoot string, candidates []string) *licenseFileProbe {
	probe, _ := probeLicenseCandidatesWithLimit(depRoot, candidates, maxLicenseFileBytes)
	return probe
}

func probeLicenseCandidatesWithLimit(depRoot string, candidates []string, maxBytes int64) (*licenseFileProbe, licenseFileStatus) {
	status := licenseFileStatus{}
	for _, candidate := range candidates {
		probe, skippedLarge := probeLicenseCandidateWithLimit(depRoot, candidate, maxBytes)
		status.skippedLarge = status.skippedLarge || skippedLarge
		if probe != nil {
			return probe, status
		}
	}
	return nil, status
}

func probeLicenseCandidate(depRoot, candidate string) *licenseFileProbe {
	probe, _ := probeLicenseCandidateWithLimit(depRoot, candidate, maxLicenseFileBytes)
	return probe
}

func probeLicenseCandidateWithLimit(depRoot, candidate string, maxBytes int64) (*licenseFileProbe, bool) {
	content, err := safeio.ReadFileUnderLimit(depRoot, candidate, maxBytes)
	if err != nil {
		return nil, errors.Is(err, safeio.ErrFileTooLarge)
	}
	spdx, confidence := detectSPDXFromLicenseContent(string(content))
	if spdx == "" {
		return nil, false
	}
	return &licenseFileProbe{
		path:       candidate,
		spdx:       spdx,
		confidence: confidence,
	}, false
}

func synthesizeLicenseFromFileProbe(probe licenseFileProbe) *report.DependencyLicense {
	return &report.DependencyLicense{
		SPDX:       probe.spdx,
		Source:     "license-file",
		Confidence: probe.confidence,
		Evidence:   []string{filepath.Base(probe.path)},
	}
}

type licenseFileDiscovery struct {
	files          []string
	candidateLimit bool
	walkLimit      bool
}

func findLicenseFiles(depRoot string) []string {
	return findLicenseFilesWithLimits(depRoot, maxLicenseCandidates, maxLicenseWalkEntries).files
}

func findLicenseFilesWithLimits(depRoot string, maxCandidates, maxWalkEntries int) licenseFileDiscovery {
	files := make([]string, 0, 4)
	walkEntries := 0
	discovery := licenseFileDiscovery{files: files}
	_ = filepath.WalkDir(depRoot, licenseWalkFunc(depRoot, &discovery, &walkEntries, maxCandidates, maxWalkEntries))
	return discovery
}

func licenseWalkFunc(depRoot string, discovery *licenseFileDiscovery, walkEntries *int, maxCandidates, maxWalkEntries int) fs.WalkDirFunc {
	return func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		*walkEntries = *walkEntries + 1
		if *walkEntries > maxWalkEntries {
			discovery.walkLimit = true
			return fs.SkipAll
		}
		if shouldSkipLicenseDir(depRoot, path, d) {
			return filepath.SkipDir
		}
		if d.IsDir() || !isLicenseCandidate(path) {
			return nil
		}
		discovery.files = append(discovery.files, path)
		if len(discovery.files) >= maxCandidates {
			discovery.candidateLimit = true
			return fs.SkipAll
		}
		return nil
	}
}

func shouldSkipLicenseDir(depRoot, path string, d fs.DirEntry) bool {
	return d.IsDir() && path != depRoot && strings.EqualFold(d.Name(), "node_modules")
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
