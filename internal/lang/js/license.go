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

const licenseFileReadMaxBytes = jsPackageJSONReadMaxBytes

var openLicenseValidatedRoot = openValidatedRootNoFollow

const dependencyRootOpaqueLayoutWarning = "skipped dependency-root metadata reads because the node_modules layout could not be safely pinned (symlinked or opaque layout)"
const dependencyLicenseWalkWarningFormat = "unable to inspect dependency license files: %s"

func detectLicenseAndProvenance(depRoot string, includeRegistryProvenance bool) (license *report.DependencyLicense, provenance *report.DependencyProvenance, warnings []string) {
	if strings.TrimSpace(depRoot) == "" {
		return unknownDependencyLicense(), unknownDependencyProvenance(), []string{dependencyRootOpaqueLayoutWarning}
	}
	root, validatedDepRoot, err := openLicenseValidatedRoot(depRoot)
	if err != nil {
		return unknownDependencyLicense(), unknownDependencyProvenance(), []string{dependencyRootOpaqueLayoutWarning}
	}
	defer closeRootAppendWarning(root, &warnings, "failed to close dependency root after license/provenance detection")

	pkg, warnings := loadDependencyPackageJSONFromRoot(root, validatedDepRoot)
	var licenseWarnings []string
	license, licenseWarnings = detectLicenseFromMetadataOrFiles(pkg, root, validatedDepRoot)
	warnings = append(warnings, licenseWarnings...)
	provenance = buildProvenance(pkg, includeRegistryProvenance)
	return license, provenance, warnings
}

func detectLicenseFromMetadataOrFiles(pkg packageJSON, root safeio.Root, depRoot string) (*report.DependencyLicense, []string) {
	if license := detectLicenseFromPackageJSON(pkg); license != nil {
		return license, nil
	}
	if license, warnings := detectLicenseFromFilesWithinRoot(root, depRoot); license != nil {
		return license, warnings
	} else if len(warnings) > 0 {
		return unknownDependencyLicense(), warnings
	}
	return unknownDependencyLicense(), nil
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
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '.', r == '+':
			b.WriteRune(r)
		}
	}
	return b.String()
}

type licenseFileProbe struct {
	path       string
	spdx       string
	confidence string
}

func detectLicenseFromFiles(depRoot string) (license *report.DependencyLicense, warnings []string) {
	root, validatedDepRoot, err := openLicenseValidatedRoot(depRoot)
	if err != nil {
		return nil, nil
	}
	defer func() {
		warnings = dedupeStrings(warnings)
	}()
	defer func() {
		if closeErr := closeRootResetLicense(root, &license, "failed to close dependency root after license file detection"); closeErr != nil {
			warnings = append(warnings, closeErr.Error())
		}
	}()
	license, warnings = detectLicenseFromFilesWithinRoot(root, validatedDepRoot)
	return license, warnings
}

func detectLicenseFromFilesWithinRoot(root safeio.Root, depRoot string) (*report.DependencyLicense, []string) {
	probe, warnings := probeLicenseFilesWithinRoot(root, depRoot)
	if probe == nil {
		return nil, warnings
	}
	return synthesizeLicenseFromFileProbe(*probe), warnings
}

func probeLicenseFiles(depRoot string) (probe *licenseFileProbe, warnings []string) {
	root, validatedDepRoot, err := openLicenseValidatedRoot(depRoot)
	if err != nil {
		return nil, nil
	}
	defer func() {
		warnings = dedupeStrings(warnings)
	}()
	defer func() {
		if closeErr := closeRootResetProbe(root, &probe, "failed to close dependency root after license file probing"); closeErr != nil {
			warnings = append(warnings, closeErr.Error())
		}
	}()
	probe, warnings = probeLicenseFilesWithinRoot(root, validatedDepRoot)
	return probe, warnings
}

func probeLicenseFilesWithinRoot(root safeio.Root, depRoot string) (*licenseFileProbe, []string) {
	candidates, warnings := findLicenseFilesWithinRoot(root, depRoot)
	probe, probeWarnings := probeLicenseCandidatesWithinRoot(root, depRoot, candidates)
	return probe, append(warnings, probeWarnings...)
}

func probeLicenseCandidates(depRoot string, candidates []string) (probe *licenseFileProbe, warnings []string) {
	root, validatedDepRoot, err := openLicenseValidatedRoot(depRoot)
	if err != nil {
		return nil, nil
	}
	defer func() {
		warnings = dedupeStrings(warnings)
	}()
	defer func() {
		if closeErr := closeRootResetProbe(root, &probe, "failed to close dependency root after license candidate probing"); closeErr != nil {
			warnings = append(warnings, closeErr.Error())
		}
	}()
	probe, warnings = probeLicenseCandidatesWithinRoot(root, validatedDepRoot, candidates)
	return probe, warnings
}

func probeLicenseCandidatesWithinRoot(root safeio.Root, depRoot string, candidates []string) (*licenseFileProbe, []string) {
	warnings := make([]string, 0)
	for _, candidate := range candidates {
		probe, warning := probeLicenseCandidateWithinRoot(root, depRoot, candidate)
		if warning != "" {
			warnings = append(warnings, warning)
		}
		if probe != nil {
			return probe, warnings
		}
	}
	return nil, warnings
}

func probeLicenseCandidate(depRoot, candidate string) (probe *licenseFileProbe, warnings []string) {
	root, validatedDepRoot, err := openLicenseValidatedRoot(depRoot)
	if err != nil {
		return nil, nil
	}
	defer func() {
		warnings = dedupeStrings(warnings)
	}()
	defer func() {
		if closeErr := closeRootResetProbe(root, &probe, "failed to close dependency root after license candidate probing"); closeErr != nil {
			warnings = append(warnings, closeErr.Error())
		}
	}()
	probe, warning := probeLicenseCandidateWithinRoot(root, validatedDepRoot, candidate)
	if warning != "" {
		warnings = append(warnings, warning)
	}
	return probe, warnings
}

func probeLicenseCandidateWithinRoot(root safeio.Root, depRoot, candidate string) (*licenseFileProbe, string) {
	relCandidate, err := relativePathWithinRoot(depRoot, candidate)
	if err != nil {
		return nil, ""
	}
	content, err := safeio.ReadFileWithinRootLimit(root, relCandidate, licenseFileReadMaxBytes)
	if err != nil {
		if errors.Is(err, safeio.ErrFileTooLarge) {
			return nil, fmt.Sprintf("skipped license candidate %s above %d bytes", filepath.Base(candidate), licenseFileReadMaxBytes)
		}
		return nil, ""
	}
	spdx, confidence := detectSPDXFromLicenseContent(string(content))
	if spdx == "" {
		return nil, ""
	}
	return &licenseFileProbe{
		path:       candidate,
		spdx:       spdx,
		confidence: confidence,
	}, ""
}

func synthesizeLicenseFromFileProbe(probe licenseFileProbe) *report.DependencyLicense {
	return &report.DependencyLicense{
		SPDX:       probe.spdx,
		Source:     "license-file",
		Confidence: probe.confidence,
		Evidence:   []string{filepath.Base(probe.path)},
	}
}

func findLicenseFiles(depRoot string) (files []string, warnings []string) {
	validatedDepRoot, err := validateDirectoryPathNoFollow(depRoot)
	if err != nil {
		return nil, nil
	}
	root, err := openConstrainedRoot(validatedDepRoot)
	if err != nil {
		return nil, nil
	}
	defer func() {
		if closeErr := closeRootResetSlice(root, &files, "failed to close dependency root after license file discovery"); closeErr != nil {
			warnings = append(warnings, closeErr.Error())
		}
	}()
	files, warnings = findLicenseFilesWithinRoot(root, validatedDepRoot)
	return files, warnings
}

func findLicenseFilesWithinRoot(root safeio.Root, depRoot string) ([]string, []string) {
	files := make([]string, 0, 4)
	if err := walkRootNoFollowBestEffort(root, licenseWalkFunc(depRoot, &files)); err != nil {
		return files, []string{fmt.Sprintf(dependencyLicenseWalkWarningFormat, depRoot)}
	}
	return files, nil
}

func licenseWalkFunc(depRoot string, files *[]string) rootWalkFunc {
	return func(relPath string, info fs.FileInfo) (bool, bool, error) {
		if shouldSkipLicenseDir(relPath, info) {
			return true, false, nil
		}
		if info.IsDir() || !isLicenseCandidate(relPath) {
			return false, false, nil
		}
		*files = append(*files, filepath.Join(depRoot, relPath))
		return false, len(*files) >= 5, nil
	}
}

func shouldSkipLicenseDir(path string, info fs.FileInfo) bool {
	return info.IsDir() && path != "" && strings.EqualFold(filepath.Base(path), "node_modules")
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
	signals := []string{"manifest:" + jsPackageFile}
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

func loadDependencyPackageJSON(depRoot string) (pkg packageJSON, warnings []string) {
	root, validatedDepRoot, err := openLicenseValidatedRoot(depRoot)
	if err != nil {
		pkgPath := filepath.Join(depRoot, jsPackageFile)
		return packageJSON{}, []string{fmt.Sprintf("unable to read dependency metadata: %s", pkgPath)}
	}
	defer func() {
		if closeRootAppendWarning(root, &warnings, fmt.Sprintf("unable to read dependency metadata: %s", filepath.Join(validatedDepRoot, jsPackageFile))) {
			pkg = packageJSON{}
		}
	}()
	return loadDependencyPackageJSONFromRoot(root, validatedDepRoot)
}

func loadDependencyPackageJSONFromRoot(root safeio.Root, validatedDepRoot string) (packageJSON, []string) {
	pkgPath := filepath.Join(validatedDepRoot, jsPackageFile)
	data, err := safeio.ReadFileWithinRootLimit(root, jsPackageFile, jsPackageJSONReadMaxBytes)
	if err != nil {
		return packageJSON{}, []string{fmt.Sprintf("unable to read dependency metadata: %s", pkgPath)}
	}

	var pkg packageJSON
	if json.Unmarshal(data, &pkg) != nil {
		return packageJSON{}, []string{fmt.Sprintf("failed to parse dependency metadata: %s", pkgPath)}
	}
	return pkg, nil
}
