package js

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func detectLicenseAndProvenance(depRoot string, includeRegistryProvenance bool) (*report.DependencyLicense, *report.DependencyProvenance, []string) {
	if strings.TrimSpace(depRoot) == "" {
		return &report.DependencyLicense{
				Source:     "unknown",
				Confidence: "low",
				Unknown:    true,
			}, &report.DependencyProvenance{
				Source:     "unknown",
				Confidence: "low",
			}, []string{"unable to resolve dependency root for license/provenance detection"}
	}
	pkg, warnings := loadDependencyPackageJSON(depRoot)
	license := detectLicenseFromPackageJSON(pkg)
	if license == nil {
		fallback := detectLicenseFromFiles(depRoot)
		if fallback != nil {
			license = fallback
		}
	}
	if license == nil {
		license = &report.DependencyLicense{
			Source:     "unknown",
			Confidence: "low",
			Unknown:    true,
		}
	}

	provenance := buildProvenance(pkg, includeRegistryProvenance)
	return license, provenance, warnings
}

func detectLicenseFromPackageJSON(pkg packageJSON) *report.DependencyLicense {
	raw := parsePackageJSONLicense(pkg.License)
	if raw == "" {
		for _, item := range pkg.Licenses {
			raw = parsePackageJSONLicense(item)
			if raw != "" {
				break
			}
		}
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

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

func detectLicenseFromFiles(depRoot string) *report.DependencyLicense {
	candidates := findLicenseFiles(depRoot)
	for _, candidate := range candidates {
		content, err := safeio.ReadFileUnder(depRoot, candidate)
		if err != nil {
			continue
		}
		spdx, confidence := detectSPDXFromLicenseContent(string(content))
		if spdx == "" {
			continue
		}
		return &report.DependencyLicense{
			SPDX:       spdx,
			Source:     "license-file",
			Confidence: confidence,
			Evidence:   []string{filepath.Base(candidate)},
		}
	}
	return nil
}

func findLicenseFiles(depRoot string) []string {
	files := make([]string, 0, 4)
	if err := filepath.WalkDir(depRoot, licenseWalkFunc(depRoot, &files)); err != nil {
		return files
	}
	return files
}

func licenseWalkFunc(depRoot string, files *[]string) fs.WalkDirFunc {
	return func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if shouldSkipLicenseDir(depRoot, path, d) {
			return filepath.SkipDir
		}
		if d.IsDir() || !isLicenseCandidate(path) {
			return nil
		}
		*files = append(*files, path)
		if len(*files) >= 5 {
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
	signals := []string{"manifest:package.json"}
	source := "local-manifest"
	confidence := "medium"

	if strings.TrimSpace(pkg.Name) != "" {
		signals = append(signals, "name:"+pkg.Name)
	}
	if strings.TrimSpace(pkg.Version) != "" {
		signals = append(signals, "version:"+pkg.Version)
	}

	if includeRegistryProvenance {
		registrySignalCount := 0
		if strings.TrimSpace(pkg.PublishConfig.Registry) != "" {
			signals = append(signals, "registry:"+strings.TrimSpace(pkg.PublishConfig.Registry))
			registrySignalCount++
		}
		if strings.TrimSpace(pkg.Resolved) != "" {
			signals = append(signals, "resolved")
			registrySignalCount++
		}
		if strings.TrimSpace(pkg.Integrity) != "" {
			signals = append(signals, "integrity")
			registrySignalCount++
		}
		if hasRepositorySignal(pkg.Repository) {
			signals = append(signals, "repository")
			registrySignalCount++
		}
		if registrySignalCount > 0 {
			source = "local+registry-heuristics"
			confidence = "high"
		}
	}

	return &report.DependencyProvenance{
		Source:     source,
		Confidence: confidence,
		Signals:    signals,
	}
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
	if err := json.Unmarshal(data, &pkg); err != nil {
		return packageJSON{}, []string{fmt.Sprintf("failed to parse dependency metadata: %s", pkgPath)}
	}
	return pkg, nil
}
