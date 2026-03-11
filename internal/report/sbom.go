package report

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	cycloneDXSpecVersion = "1.4"
	spdxVersion          = "SPDX-2.3"
	spdxDataLicense      = "CC0-1.0"
	spdxNoAssertion      = "NOASSERTION"
)

type sbomComponent struct {
	Name           string
	Version        string
	Language       string
	PURL           string
	License        string
	SHA256         string
	UsedPercent    float64
	WasteScore     float64
	Recommendation string
	SPDXID         string
}

type cyclonedxBOM struct {
	BOMFormat   string               `json:"bomFormat"`
	SpecVersion string               `json:"specVersion"`
	Version     int                  `json:"version"`
	Metadata    *cyclonedxMetadata   `json:"metadata,omitempty"`
	Components  []cyclonedxComponent `json:"components,omitempty"`
}

type cyclonedxMetadata struct {
	Timestamp string          `json:"timestamp,omitempty"`
	Tools     []cyclonedxTool `json:"tools,omitempty"`
}

type cyclonedxTool struct {
	Vendor  string `json:"vendor,omitempty"`
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

type cyclonedxComponent struct {
	Type       string                    `json:"type"`
	BOMRef     string                    `json:"bom-ref,omitempty"`
	Name       string                    `json:"name"`
	Version    string                    `json:"version,omitempty"`
	PURL       string                    `json:"purl,omitempty"`
	Licenses   []cyclonedxLicenseWrapper `json:"licenses,omitempty"`
	Hashes     []cyclonedxHash           `json:"hashes,omitempty"`
	Properties []cyclonedxProperty       `json:"properties,omitempty"`
}

type cyclonedxLicenseWrapper struct {
	License cyclonedxLicense `json:"license"`
}

type cyclonedxLicense struct {
	ID string `json:"id,omitempty"`
}

type cyclonedxHash struct {
	Algorithm string `json:"alg"`
	Content   string `json:"content"`
}

type cyclonedxProperty struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type cyclonedxXMLBOM struct {
	XMLName     xml.Name                `xml:"bom"`
	Xmlns       string                  `xml:"xmlns,attr"`
	Version     int                     `xml:"version,attr"`
	SpecVersion string                  `xml:"specVersion,attr"`
	Metadata    *cyclonedxXMLMetadata   `xml:"metadata,omitempty"`
	Components  *cyclonedxXMLComponents `xml:"components,omitempty"`
}

type cyclonedxXMLMetadata struct {
	Timestamp string             `xml:"timestamp,omitempty"`
	Tools     *cyclonedxXMLTools `xml:"tools,omitempty"`
}

type cyclonedxXMLTools struct {
	Tools []cyclonedxXMLTool `xml:"tool"`
}

type cyclonedxXMLTool struct {
	Vendor  string `xml:"vendor,omitempty"`
	Name    string `xml:"name,omitempty"`
	Version string `xml:"version,omitempty"`
}

type cyclonedxXMLComponents struct {
	Components []cyclonedxXMLComponent `xml:"component"`
}

type cyclonedxXMLComponent struct {
	Type       string                  `xml:"type,attr"`
	BOMRef     string                  `xml:"bom-ref,attr,omitempty"`
	Name       string                  `xml:"name"`
	Version    string                  `xml:"version,omitempty"`
	PURL       string                  `xml:"purl,omitempty"`
	Licenses   *cyclonedxXMLLicenses   `xml:"licenses,omitempty"`
	Hashes     *cyclonedxXMLHashes     `xml:"hashes,omitempty"`
	Properties *cyclonedxXMLProperties `xml:"properties,omitempty"`
}

type cyclonedxXMLLicenses struct {
	Licenses []cyclonedxXMLLicenseWrapper `xml:"license"`
}

type cyclonedxXMLLicenseWrapper struct {
	ID string `xml:"id,omitempty"`
}

type cyclonedxXMLHashes struct {
	Hashes []cyclonedxXMLHash `xml:"hash"`
}

type cyclonedxXMLHash struct {
	Algorithm string `xml:"alg,attr"`
	Content   string `xml:",chardata"`
}

type cyclonedxXMLProperties struct {
	Properties []cyclonedxXMLProperty `xml:"property"`
}

type cyclonedxXMLProperty struct {
	Name  string `xml:"name,attr"`
	Value string `xml:",chardata"`
}

type spdxDocument struct {
	SPDXVersion       string             `json:"spdxVersion"`
	DataLicense       string             `json:"dataLicense"`
	SPDXID            string             `json:"SPDXID"`
	Name              string             `json:"name"`
	DocumentNamespace string             `json:"documentNamespace"`
	CreationInfo      spdxCreationInfo   `json:"creationInfo"`
	Packages          []spdxPackage      `json:"packages,omitempty"`
	Relationships     []spdxRelationship `json:"relationships,omitempty"`
}

type spdxCreationInfo struct {
	Created  string   `json:"created"`
	Creators []string `json:"creators"`
}

type spdxPackage struct {
	Name             string            `json:"name"`
	SPDXID           string            `json:"SPDXID"`
	VersionInfo      string            `json:"versionInfo,omitempty"`
	DownloadLocation string            `json:"downloadLocation"`
	FilesAnalyzed    bool              `json:"filesAnalyzed"`
	LicenseConcluded string            `json:"licenseConcluded"`
	LicenseDeclared  string            `json:"licenseDeclared"`
	Checksums        []spdxChecksum    `json:"checksums,omitempty"`
	ExternalRefs     []spdxExternalRef `json:"externalRefs,omitempty"`
	Comment          string            `json:"comment,omitempty"`
}

type spdxChecksum struct {
	Algorithm     string `json:"algorithm"`
	ChecksumValue string `json:"checksumValue"`
}

type spdxExternalRef struct {
	ReferenceCategory string `json:"referenceCategory"`
	ReferenceType     string `json:"referenceType"`
	ReferenceLocator  string `json:"referenceLocator"`
}

type spdxRelationship struct {
	SPDXElementID    string `json:"spdxElementId"`
	RelationshipType string `json:"relationshipType"`
	RelatedSPDXID    string `json:"relatedSpdxElement"`
}

func formatCycloneDXJSON(rep Report) (string, error) {
	timestamp := sbomTimestamp(rep)
	components := buildSBOMComponents(rep.Dependencies)
	bom := cyclonedxBOM{
		BOMFormat:   "CycloneDX",
		SpecVersion: cycloneDXSpecVersion,
		Version:     1,
		Metadata: &cyclonedxMetadata{
			Timestamp: timestamp.Format(time.RFC3339),
			Tools: []cyclonedxTool{
				{
					Vendor:  "lopper",
					Name:    "lopper",
					Version: reportVersion(rep),
				},
			},
		},
		Components: makeCycloneDXJSONComponents(components),
	}
	payload, err := json.MarshalIndent(bom, "", "  ")
	if err != nil {
		return "", err
	}
	return string(payload) + "\n", nil
}

func formatCycloneDXXML(rep Report) (string, error) {
	timestamp := sbomTimestamp(rep)
	components := buildSBOMComponents(rep.Dependencies)
	doc := cyclonedxXMLBOM{
		Xmlns:       "http://cyclonedx.org/schema/bom/1.4",
		Version:     1,
		SpecVersion: cycloneDXSpecVersion,
		Metadata: &cyclonedxXMLMetadata{
			Timestamp: timestamp.Format(time.RFC3339),
			Tools: &cyclonedxXMLTools{
				Tools: []cyclonedxXMLTool{
					{
						Vendor:  "lopper",
						Name:    "lopper",
						Version: reportVersion(rep),
					},
				},
			},
		},
		Components: &cyclonedxXMLComponents{
			Components: makeCycloneDXXMLComponents(components),
		},
	}
	payload, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return xml.Header + string(payload) + "\n", nil
}

func formatSPDXJSON(rep Report) (string, error) {
	timestamp := sbomTimestamp(rep)
	components := buildSBOMComponents(rep.Dependencies)
	documentName := sbomDocumentName(rep.RepoPath)
	packages := make([]spdxPackage, 0, len(components))
	relationships := make([]spdxRelationship, 0, len(components))
	for _, component := range components {
		pkg := spdxPackage{
			Name:             component.Name,
			SPDXID:           component.SPDXID,
			DownloadLocation: spdxNoAssertion,
			FilesAnalyzed:    false,
			LicenseConcluded: spdxLicenseOrNoAssertion(component.License),
			LicenseDeclared:  spdxLicenseOrNoAssertion(component.License),
			Comment:          spdxPackageComment(component),
		}
		if strings.TrimSpace(component.Version) != "" {
			pkg.VersionInfo = component.Version
		}
		if strings.TrimSpace(component.PURL) != "" {
			pkg.ExternalRefs = append(pkg.ExternalRefs, spdxExternalRef{
				ReferenceCategory: "PACKAGE-MANAGER",
				ReferenceType:     "purl",
				ReferenceLocator:  component.PURL,
			})
		}
		if strings.TrimSpace(component.SHA256) != "" {
			pkg.Checksums = append(pkg.Checksums, spdxChecksum{
				Algorithm:     "SHA256",
				ChecksumValue: component.SHA256,
			})
		}
		packages = append(packages, pkg)
		relationships = append(relationships, spdxRelationship{
			SPDXElementID:    "SPDXRef-DOCUMENT",
			RelationshipType: "DESCRIBES",
			RelatedSPDXID:    component.SPDXID,
		})
	}
	doc := spdxDocument{
		SPDXVersion:       spdxVersion,
		DataLicense:       spdxDataLicense,
		SPDXID:            "SPDXRef-DOCUMENT",
		Name:              documentName,
		DocumentNamespace: spdxDocumentNamespace(documentName, timestamp),
		CreationInfo: spdxCreationInfo{
			Created: timestamp.Format(time.RFC3339),
			Creators: []string{
				"Tool: lopper-" + reportVersion(rep),
			},
		},
		Packages:      packages,
		Relationships: relationships,
	}
	payload, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return string(payload) + "\n", nil
}

func formatSPDXTagValue(rep Report) (string, error) {
	timestamp := sbomTimestamp(rep)
	components := buildSBOMComponents(rep.Dependencies)
	documentName := sbomDocumentName(rep.RepoPath)
	var builder strings.Builder
	builder.WriteString("SPDXVersion: " + spdxVersion + "\n")
	builder.WriteString("DataLicense: " + spdxDataLicense + "\n")
	builder.WriteString("SPDXID: SPDXRef-DOCUMENT\n")
	builder.WriteString("DocumentName: " + documentName + "\n")
	builder.WriteString("DocumentNamespace: " + spdxDocumentNamespace(documentName, timestamp) + "\n")
	builder.WriteString("Creator: Tool: lopper-" + reportVersion(rep) + "\n")
	builder.WriteString("Created: " + timestamp.Format(time.RFC3339) + "\n")

	for _, component := range components {
		builder.WriteString("\n")
		builder.WriteString("##### Package: " + component.Name + "\n")
		builder.WriteString("PackageName: " + component.Name + "\n")
		builder.WriteString("SPDXID: " + component.SPDXID + "\n")
		if strings.TrimSpace(component.Version) != "" {
			builder.WriteString("PackageVersion: " + component.Version + "\n")
		}
		builder.WriteString("PackageDownloadLocation: " + spdxNoAssertion + "\n")
		builder.WriteString("FilesAnalyzed: false\n")
		builder.WriteString("PackageLicenseConcluded: " + spdxLicenseOrNoAssertion(component.License) + "\n")
		builder.WriteString("PackageLicenseDeclared: " + spdxLicenseOrNoAssertion(component.License) + "\n")
		if strings.TrimSpace(component.PURL) != "" {
			builder.WriteString("ExternalRef: PACKAGE-MANAGER purl " + component.PURL + "\n")
		}
		if strings.TrimSpace(component.SHA256) != "" {
			builder.WriteString("PackageChecksum: SHA256: " + component.SHA256 + "\n")
		}
		builder.WriteString("PackageComment: " + spdxPackageComment(component) + "\n")
		builder.WriteString("Relationship: SPDXRef-DOCUMENT DESCRIBES " + component.SPDXID + "\n")
	}
	return builder.String(), nil
}

func makeCycloneDXJSONComponents(components []sbomComponent) []cyclonedxComponent {
	result := make([]cyclonedxComponent, 0, len(components))
	for _, component := range components {
		cdx := cyclonedxComponent{
			Type:    "library",
			BOMRef:  component.PURL,
			Name:    component.Name,
			Version: component.Version,
			PURL:    component.PURL,
			Properties: []cyclonedxProperty{
				{Name: "lopper:waste-score", Value: sbomFloat(component.WasteScore)},
				{Name: "lopper:used-percent", Value: sbomFloat(component.UsedPercent)},
				{Name: "lopper:recommendation", Value: component.Recommendation},
			},
		}
		if strings.TrimSpace(component.PURL) == "" {
			cdx.BOMRef = ""
		}
		if strings.TrimSpace(component.License) != "" {
			cdx.Licenses = []cyclonedxLicenseWrapper{
				{
					License: cyclonedxLicense{ID: component.License},
				},
			}
		}
		if strings.TrimSpace(component.SHA256) != "" {
			cdx.Hashes = []cyclonedxHash{
				{
					Algorithm: "SHA-256",
					Content:   component.SHA256,
				},
			}
		}
		result = append(result, cdx)
	}
	return result
}

func makeCycloneDXXMLComponents(components []sbomComponent) []cyclonedxXMLComponent {
	result := make([]cyclonedxXMLComponent, 0, len(components))
	for _, component := range components {
		cdx := cyclonedxXMLComponent{
			Type:    "library",
			BOMRef:  component.PURL,
			Name:    component.Name,
			Version: component.Version,
			PURL:    component.PURL,
			Properties: &cyclonedxXMLProperties{
				Properties: []cyclonedxXMLProperty{
					{Name: "lopper:waste-score", Value: sbomFloat(component.WasteScore)},
					{Name: "lopper:used-percent", Value: sbomFloat(component.UsedPercent)},
					{Name: "lopper:recommendation", Value: component.Recommendation},
				},
			},
		}
		if strings.TrimSpace(component.PURL) == "" {
			cdx.BOMRef = ""
		}
		if strings.TrimSpace(component.License) != "" {
			cdx.Licenses = &cyclonedxXMLLicenses{
				Licenses: []cyclonedxXMLLicenseWrapper{
					{ID: component.License},
				},
			}
		}
		if strings.TrimSpace(component.SHA256) != "" {
			cdx.Hashes = &cyclonedxXMLHashes{
				Hashes: []cyclonedxXMLHash{
					{
						Algorithm: "SHA-256",
						Content:   component.SHA256,
					},
				},
			}
		}
		result = append(result, cdx)
	}
	return result
}

func buildSBOMComponents(dependencies []DependencyReport) []sbomComponent {
	sorted := append([]DependencyReport(nil), dependencies...)
	sort.Slice(sorted, func(i, j int) bool {
		leftLanguage := strings.ToLower(strings.TrimSpace(sorted[i].Language))
		rightLanguage := strings.ToLower(strings.TrimSpace(sorted[j].Language))
		if leftLanguage != rightLanguage {
			return leftLanguage < rightLanguage
		}
		leftName := strings.ToLower(strings.TrimSpace(sorted[i].Name))
		rightName := strings.ToLower(strings.TrimSpace(sorted[j].Name))
		return leftName < rightName
	})
	components := make([]sbomComponent, 0, len(sorted))
	for idx, dep := range sorted {
		name, version := normalizeDependencyIdentity(dep)
		usedPercent := dependencyUsedPercent(dep)
		wasteScore := 100 - usedPercent
		if wasteScore < 0 {
			wasteScore = 0
		}
		component := sbomComponent{
			Name:           name,
			Version:        version,
			Language:       strings.TrimSpace(dep.Language),
			License:        dependencySPDX(dep),
			SHA256:         dependencySHA256(dep),
			UsedPercent:    usedPercent,
			WasteScore:     wasteScore,
			Recommendation: dependencyRecommendation(dep),
			SPDXID:         fmt.Sprintf("SPDXRef-Package-%d", idx+1),
		}
		component.PURL = dependencyPURL(component.Language, component.Name, component.Version)
		components = append(components, component)
	}
	return components
}

func normalizeDependencyIdentity(dep DependencyReport) (string, string) {
	name := strings.TrimSpace(dep.Name)
	version := dependencyVersion(dep)
	if trimmed, inlineVersion, ok := splitInlineVersion(name); ok {
		name = trimmed
		if strings.TrimSpace(version) == "" {
			version = inlineVersion
		}
	}
	return name, strings.TrimSpace(version)
}

func splitInlineVersion(name string) (string, string, bool) {
	value := strings.TrimSpace(name)
	if value == "" {
		return value, "", false
	}
	if strings.Count(value, ":") >= 2 {
		parts := strings.Split(value, ":")
		version := strings.TrimSpace(parts[len(parts)-1])
		if isLikelyVersion(version) {
			prefix := strings.Join(parts[:len(parts)-1], ":")
			return strings.TrimSpace(prefix), version, true
		}
	}
	lastAt := strings.LastIndex(value, "@")
	if lastAt <= 0 || lastAt >= len(value)-1 {
		return value, "", false
	}
	if strings.HasPrefix(value, "@") {
		slash := strings.Index(value, "/")
		if slash == -1 || lastAt <= slash {
			return value, "", false
		}
	}
	version := strings.TrimSpace(value[lastAt+1:])
	if !isLikelyVersion(version) {
		return value, "", false
	}
	return strings.TrimSpace(value[:lastAt]), version, true
}

func isLikelyVersion(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return false
	}
	hasDigit := false
	for _, ch := range value {
		if ch >= '0' && ch <= '9' {
			hasDigit = true
			break
		}
	}
	return hasDigit
}

func dependencyVersion(dep DependencyReport) string {
	if dep.Provenance == nil {
		return ""
	}
	for _, signal := range dep.Provenance.Signals {
		value := strings.TrimSpace(signal)
		if value == "" {
			continue
		}
		if strings.HasPrefix(value, "version:") {
			return strings.TrimSpace(strings.TrimPrefix(value, "version:"))
		}
	}
	return ""
}

func dependencySHA256(dep DependencyReport) string {
	if dep.Provenance == nil {
		return ""
	}
	for _, signal := range dep.Provenance.Signals {
		value := strings.TrimSpace(signal)
		if value == "" {
			continue
		}
		if strings.HasPrefix(value, "sha256:") {
			hash := strings.TrimSpace(strings.TrimPrefix(value, "sha256:"))
			if isHexHash(hash, 64) {
				return strings.ToLower(hash)
			}
		}
	}
	return ""
}

func isHexHash(value string, size int) bool {
	if len(value) != size {
		return false
	}
	for _, ch := range value {
		isDigit := ch >= '0' && ch <= '9'
		isLower := ch >= 'a' && ch <= 'f'
		isUpper := ch >= 'A' && ch <= 'F'
		if !isDigit && !isLower && !isUpper {
			return false
		}
	}
	return true
}

func dependencyRecommendation(dep DependencyReport) string {
	if len(dep.Recommendations) == 0 {
		return "none"
	}
	codes := make([]string, 0, len(dep.Recommendations))
	for _, recommendation := range dep.Recommendations {
		code := strings.TrimSpace(recommendation.Code)
		if code == "" {
			continue
		}
		codes = append(codes, code)
	}
	if len(codes) == 0 {
		return "none"
	}
	sort.Strings(codes)
	return strings.Join(codes, ",")
}

func dependencySPDX(dep DependencyReport) string {
	if dep.License == nil {
		return ""
	}
	if dep.License.Unknown {
		return ""
	}
	return strings.TrimSpace(dep.License.SPDX)
}

func dependencyPURL(language, name, version string) string {
	packageType := purlTypeForLanguage(language)
	normalizedName := purlName(packageType, name)
	if normalizedName == "" {
		return ""
	}
	value := "pkg:" + packageType + "/" + normalizedName
	if strings.TrimSpace(version) != "" {
		value += "@" + url.PathEscape(version)
	}
	return value
}

func purlTypeForLanguage(language string) string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "js-ts":
		return "npm"
	case "python":
		return "pypi"
	case "go":
		return "golang"
	case "php":
		return "composer"
	case "ruby":
		return "gem"
	case "rust":
		return "cargo"
	case "dotnet":
		return "nuget"
	case "jvm":
		return "maven"
	case "elixir":
		return "hex"
	case "cpp":
		return "generic"
	default:
		return "generic"
	}
}

func purlName(packageType, name string) string {
	clean := strings.TrimSpace(name)
	if clean == "" {
		return ""
	}
	if packageType == "maven" {
		parts := strings.Split(clean, ":")
		if len(parts) >= 2 {
			return escapePURLPath(parts[0]) + "/" + escapePURLPath(parts[1])
		}
	}
	return escapePURLPath(clean)
}

func escapePURLPath(value string) string {
	parts := strings.Split(strings.TrimSpace(value), "/")
	if len(parts) == 0 {
		return ""
	}
	escaped := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		escaped = append(escaped, url.PathEscape(part))
	}
	return strings.Join(escaped, "/")
}

func dependencyUsedPercent(dep DependencyReport) float64 {
	usedPercent := dep.UsedPercent
	if usedPercent <= 0 && dep.TotalExportsCount > 0 {
		usedPercent = (float64(dep.UsedExportsCount) / float64(dep.TotalExportsCount)) * 100
	}
	if usedPercent < 0 {
		return 0
	}
	if usedPercent > 100 {
		return 100
	}
	return usedPercent
}

func sbomTimestamp(rep Report) time.Time {
	if !rep.GeneratedAt.IsZero() {
		return rep.GeneratedAt.UTC().Truncate(time.Second)
	}
	return time.Unix(0, 0).UTC()
}

func sbomDocumentName(repoPath string) string {
	base := filepath.Base(strings.TrimSpace(repoPath))
	switch base {
	case "", ".", string(filepath.Separator):
		return "lopper-sbom"
	default:
		return base
	}
}

func spdxDocumentNamespace(documentName string, timestamp time.Time) string {
	return "https://lopper.dev/spdx/" + url.PathEscape(documentName) + "/" + timestamp.Format("20060102T150405Z")
}

func spdxPackageComment(component sbomComponent) string {
	return "lopper:waste-score=" + sbomFloat(component.WasteScore) +
		"; lopper:used-percent=" + sbomFloat(component.UsedPercent) +
		"; lopper:recommendation=" + sanitizeSBOMComment(component.Recommendation)
}

func sanitizeSBOMComment(value string) string {
	replacer := strings.NewReplacer("\n", " ", "\r", " ", "\t", " ", ";", ",")
	return strings.TrimSpace(replacer.Replace(value))
}

func spdxLicenseOrNoAssertion(license string) string {
	value := strings.TrimSpace(license)
	if value == "" {
		return spdxNoAssertion
	}
	return value
}

func sbomFloat(value float64) string {
	return fmt.Sprintf("%.1f", value)
}
