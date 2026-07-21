package report

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

const spdxDocumentRef = "SPDXRef-DOCUMENT"

type spdxDocument struct {
	SPDXVersion       string             `json:"spdxVersion"`
	DataLicense       string             `json:"dataLicense"`
	SPDXID            string             `json:"SPDXID"`
	Name              string             `json:"name"`
	DocumentNamespace string             `json:"documentNamespace"`
	CreationInfo      spdxCreationInfo   `json:"creationInfo"`
	Packages          []spdxPackage      `json:"packages"`
	Relationships     []spdxRelationship `json:"relationships,omitempty"`
	Annotations       []spdxAnnotation   `json:"annotations,omitempty"`
}

type spdxCreationInfo struct {
	Created  string   `json:"created"`
	Creators []string `json:"creators"`
}

type spdxPackage struct {
	Name                  string            `json:"name"`
	SPDXID                string            `json:"SPDXID"`
	VersionInfo           string            `json:"versionInfo,omitempty"`
	DownloadLocation      string            `json:"downloadLocation"`
	FilesAnalyzed         bool              `json:"filesAnalyzed"`
	LicenseConcluded      string            `json:"licenseConcluded"`
	LicenseDeclared       string            `json:"licenseDeclared"`
	CopyrightText         string            `json:"copyrightText"`
	ExternalRefs          []spdxExternalRef `json:"externalRefs,omitempty"`
	AttributionTexts      []string          `json:"attributionTexts,omitempty"`
	PrimaryPackagePurpose string            `json:"primaryPackagePurpose,omitempty"`
}

type spdxExternalRef struct {
	ReferenceCategory string `json:"referenceCategory"`
	ReferenceType     string `json:"referenceType"`
	ReferenceLocator  string `json:"referenceLocator"`
}

type spdxRelationship struct {
	SPDXElementID      string `json:"spdxElementId"`
	RelationshipType   string `json:"relationshipType"`
	RelatedSPDXElement string `json:"relatedSpdxElement"`
}

type spdxAnnotation struct {
	AnnotationDate string `json:"annotationDate"`
	AnnotationType string `json:"annotationType"`
	Annotator      string `json:"annotator"`
	Comment        string `json:"comment"`
}

type spdxDependencyRef struct {
	dependency DependencyReport
	spdxID     string
}

func formatSPDXJSON(reportData Report) (string, error) {
	created := spdxCreatedAt(reportData.GeneratedAt)
	documentID := spdxDocumentNamespace(reportData)
	doc := spdxDocument{
		SPDXVersion:       "SPDX-2.3",
		DataLicense:       "CC0-1.0",
		SPDXID:            spdxDocumentRef,
		Name:              spdxDocumentName(reportData),
		DocumentNamespace: documentID,
		CreationInfo: spdxCreationInfo{
			Created:  created,
			Creators: []string{"Tool: lopper"},
		},
		Packages:      spdxPackages(reportData.Dependencies),
		Relationships: spdxRelationships(reportData.Dependencies),
		Annotations:   spdxAnnotations(reportData, created),
	}
	payload, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return string(payload) + "\n", nil
}

func spdxCreatedAt(value time.Time) string {
	if value.IsZero() {
		value = time.Unix(0, 0).UTC()
	}
	return value.UTC().Format(time.RFC3339)
}

func spdxDocumentName(reportData Report) string {
	if strings.TrimSpace(reportData.RepoPath) != "" {
		return "lopper-" + strings.TrimSpace(reportData.RepoPath)
	}
	return "lopper-analysis"
}

func spdxDocumentNamespace(reportData Report) string {
	seed := reportData.RepoPath + "\x00" + reportData.SchemaVersion
	for _, dep := range sortedDependenciesForCSV(reportData.Dependencies) {
		seed += "\x00" + DependencyVersionlessKey(dep)
		if dep.Identity != nil {
			seed += "\x00" + dep.Identity.Version + "\x00" + dep.Identity.PURL
		}
	}
	sum := sha256.Sum256([]byte(seed))
	return "https://lopper.local/spdx/" + hex.EncodeToString(sum[:16])
}

func spdxPackages(dependencies []DependencyReport) []spdxPackage {
	refs := spdxDependencyRefs(dependencies)
	packages := make([]spdxPackage, 0, len(refs))
	for _, ref := range refs {
		dep := ref.dependency
		pkg := spdxPackage{
			Name:                  firstSPDXValue(identityName(dep), dep.Name, "unknown"),
			SPDXID:                ref.spdxID,
			VersionInfo:           identityVersion(dep),
			DownloadLocation:      "NOASSERTION",
			FilesAnalyzed:         false,
			LicenseConcluded:      "NOASSERTION",
			LicenseDeclared:       spdxLicenseDeclared(dep.License),
			CopyrightText:         "NOASSERTION",
			ExternalRefs:          spdxExternalRefs(dep),
			AttributionTexts:      spdxAttributionTexts(dep),
			PrimaryPackagePurpose: "LIBRARY",
		}
		packages = append(packages, pkg)
	}
	return packages
}

func spdxRelationships(dependencies []DependencyReport) []spdxRelationship {
	refs := spdxDependencyRefs(dependencies)
	relationships := make([]spdxRelationship, 0, len(refs))
	for _, ref := range refs {
		relationships = append(relationships, spdxRelationship{
			SPDXElementID:      spdxDocumentRef,
			RelationshipType:   "DESCRIBES",
			RelatedSPDXElement: ref.spdxID,
		})
	}
	return relationships
}

func spdxDependencyRefs(dependencies []DependencyReport) []spdxDependencyRef {
	deps := sortedDependenciesForCSV(dependencies)
	refs := make([]spdxDependencyRef, 0, len(deps))
	seenIDs := make(map[string]int, len(deps))
	for _, dep := range deps {
		baseID := spdxPackageID(dep)
		seenIDs[baseID]++
		spdxID := baseID
		if seenIDs[baseID] > 1 {
			spdxID = baseID + "-" + strconv.Itoa(seenIDs[baseID])
		}
		refs = append(refs, spdxDependencyRef{dependency: dep, spdxID: spdxID})
	}
	return refs
}

func spdxAnnotations(reportData Report, created string) []spdxAnnotation {
	if len(reportData.Warnings) == 0 {
		return nil
	}
	return []spdxAnnotation{{
		AnnotationDate: created,
		AnnotationType: "OTHER",
		Annotator:      "Tool: lopper",
		Comment:        strings.Join(sortedStrings(reportData.Warnings), " | "),
	}}
}

func spdxPackageID(dep DependencyReport) string {
	seed := DependencyVersionlessKey(dep)
	sum := sha256.Sum256([]byte(seed))
	return "SPDXRef-Package-" + hex.EncodeToString(sum[:8])
}

func spdxLicenseDeclared(license *DependencyLicense) string {
	if license == nil {
		return "NOASSERTION"
	}
	if strings.TrimSpace(license.SPDX) != "" && !license.Unknown {
		return strings.TrimSpace(license.SPDX)
	}
	return "NOASSERTION"
}

func spdxExternalRefs(dep DependencyReport) []spdxExternalRef {
	if dep.Identity == nil || strings.TrimSpace(dep.Identity.PURL) == "" {
		return nil
	}
	return []spdxExternalRef{{
		ReferenceCategory: "PACKAGE-MANAGER",
		ReferenceType:     "purl",
		ReferenceLocator:  strings.TrimSpace(dep.Identity.PURL),
	}}
}

func spdxAttributionTexts(dep DependencyReport) []string {
	values := make([]string, 0, 4)
	if dep.Identity != nil {
		if dep.Identity.VersionStatus != "" {
			values = append(values, "lopper:versionStatus="+dep.Identity.VersionStatus)
		}
		if dep.Identity.Source != "" {
			values = append(values, "lopper:identitySource="+dep.Identity.Source)
		}
	}
	if dep.ReachabilityConfidence != nil {
		values = append(values, "lopper:reachability="+dep.ReachabilityConfidence.Summary)
	}
	return values
}

func identityName(dep DependencyReport) string {
	if dep.Identity == nil {
		return ""
	}
	return strings.TrimSpace(dep.Identity.Name)
}

func identityVersion(dep DependencyReport) string {
	if dep.Identity == nil {
		return ""
	}
	return strings.TrimSpace(dep.Identity.Version)
}

func firstSPDXValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
