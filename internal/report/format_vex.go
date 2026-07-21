package report

import (
	"encoding/json"
	"strings"
)

var cycloneDXVEXJustificationValues = map[string]string{
	"code_not_present":                "code_not_present",
	"code_not_reachable":              "code_not_reachable",
	"requires_configuration":          "requires_configuration",
	"requires_dependency":             "requires_dependency",
	"requires_environment":            "requires_environment",
	"protected_by_compiler":           "protected_by_compiler",
	"protected_at_runtime":            "protected_at_runtime",
	"protected_at_perimeter":          "protected_at_perimeter",
	"protected_by_mitigating_control": "protected_by_mitigating_control",
}

type cycloneDXVulnerability struct {
	BOMRef     string                          `json:"bom-ref,omitempty"`
	ID         string                          `json:"id"`
	Source     *cycloneDXVulnerabilitySource   `json:"source,omitempty"`
	Ratings    []cycloneDXVulnerabilityRating  `json:"ratings,omitempty"`
	Analysis   *cycloneDXVulnerabilityAnalysis `json:"analysis,omitempty"`
	Affects    []cycloneDXAffects              `json:"affects,omitempty"`
	Properties []cycloneDXProperty             `json:"properties,omitempty"`
}

type cycloneDXVulnerabilitySource struct {
	Name string `json:"name,omitempty"`
	URL  string `json:"url,omitempty"`
}

type cycloneDXVulnerabilityRating struct {
	Severity string  `json:"severity,omitempty"`
	Method   string  `json:"method,omitempty"`
	Score    float64 `json:"score,omitempty"`
}

type cycloneDXVulnerabilityAnalysis struct {
	State         string   `json:"state,omitempty"`
	Justification string   `json:"justification,omitempty"`
	Response      []string `json:"response,omitempty"`
	Detail        string   `json:"detail,omitempty"`
}

type cycloneDXAffects struct {
	Ref string `json:"ref"`
}

func formatCycloneDXVEXJSON(reportData Report) (string, error) {
	instances := buildCycloneDXDependencyInstances(reportData)
	bom := cycloneDXBOM{
		Schema:          cycloneDXSchemaURL,
		BOMFormat:       cycloneDXBOMFormat,
		SpecVersion:     cycloneDXSpecVersion,
		Version:         1,
		Metadata:        formatCycloneDXMetadata(reportData),
		Components:      formatCycloneDXComponents(instances),
		Properties:      formatCycloneDXVEXBOMProperties(reportData),
		Vulnerabilities: formatCycloneDXVulnerabilities(instances),
	}
	payload, err := json.MarshalIndent(bom, "", "  ")
	if err != nil {
		return "", err
	}
	return string(payload) + "\n", nil
}

func formatCycloneDXVEXBOMProperties(reportData Report) []cycloneDXProperty {
	props := append([]cycloneDXProperty{}, formatCycloneDXBOMProperties(reportData)...)
	for i := range props {
		if props[i].Name == "lopper:export:format" {
			props[i].Value = string(FormatVEX)
			return props
		}
	}
	appendCycloneDXProperty(&props, "lopper:export:format", string(FormatVEX))
	return sortedCycloneDXProperties(props)
}

func formatCycloneDXVulnerabilities(instances []cycloneDXDependencyInstance) []cycloneDXVulnerability {
	items := make([]cycloneDXVulnerability, 0)
	baseRefs := make([]string, 0)
	for _, instance := range instances {
		dep := instance.dependency
		for _, finding := range dep.Vulnerabilities {
			baseRefs = append(baseRefs, "lopper:vulnerability:"+escapeCycloneDXBOMRefPart(dep.Name)+":"+escapeCycloneDXBOMRefPart(finding.AdvisoryID))
		}
	}
	refAllocator := newCycloneDXRefAllocator(baseRefs)
	for _, instance := range instances {
		dep := instance.dependency
		for _, finding := range dep.Vulnerabilities {
			items = append(items, cycloneDXVulnerability{
				BOMRef:     refAllocator.allocate("lopper:vulnerability:" + escapeCycloneDXBOMRefPart(dep.Name) + ":" + escapeCycloneDXBOMRefPart(finding.AdvisoryID)),
				ID:         finding.AdvisoryID,
				Source:     cycloneDXVulnerabilitySourceForFinding(finding),
				Ratings:    cycloneDXVulnerabilityRatings(finding),
				Analysis:   cycloneDXVulnerabilityAnalysisForFinding(finding),
				Affects:    []cycloneDXAffects{{Ref: instance.component.BOMRef}},
				Properties: cycloneDXVulnerabilityProperties(finding, dep),
			})
		}
	}
	return items
}

func cycloneDXVulnerabilitySourceForFinding(finding VulnerabilityFinding) *cycloneDXVulnerabilitySource {
	if strings.TrimSpace(finding.Source) == "" {
		return nil
	}
	return &cycloneDXVulnerabilitySource{Name: finding.Source}
}

func cycloneDXVulnerabilityRatings(finding VulnerabilityFinding) []cycloneDXVulnerabilityRating {
	if strings.TrimSpace(finding.Severity) == "" && finding.PriorityScore == 0 {
		return nil
	}
	return []cycloneDXVulnerabilityRating{{
		Severity: normalizeSeverity(finding.Severity),
		Method:   "other",
		Score:    finding.PriorityScore,
	}}
}

func cycloneDXVulnerabilityAnalysisForFinding(finding VulnerabilityFinding) *cycloneDXVulnerabilityAnalysis {
	state := "exploitable"
	justification := ""
	response := []string{"update"}
	detail := "reachable=" + strings.ToLower(strings.TrimSpace(boolString(finding.Reachable)))
	if finding.Decision != nil && !finding.Decision.Expired {
		state = cycloneDXVEXState(finding.Decision.Status)
		justification = cycloneDXVEXJustification(finding.Decision.Justification)
		response = cycloneDXVEXResponse(finding.Decision.Status)
		detail = strings.TrimSpace(finding.Decision.Reason)
	}
	return &cycloneDXVulnerabilityAnalysis{
		State:         state,
		Justification: justification,
		Response:      response,
		Detail:        detail,
	}
}

func cycloneDXVEXJustification(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	return cycloneDXVEXJustificationValues[normalized]
}

func CycloneDXVEXJustification(value string) string {
	return cycloneDXVEXJustification(value)
}

func cycloneDXVEXState(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "not-affected":
		return "not_affected"
	case "resolved":
		return "resolved"
	case "under-investigation":
		return "in_triage"
	default:
		return "exploitable"
	}
}

func cycloneDXVEXResponse(status string) []string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "accepted-risk":
		return nil
	case "not-affected":
		return []string{"will_not_fix"}
	}
	return []string{"update"}
}

func cycloneDXVulnerabilityProperties(finding VulnerabilityFinding, dep DependencyReport) []cycloneDXProperty {
	props := make([]cycloneDXProperty, 0, 12)
	appendCycloneDXProperty(&props, "lopper:vulnerability:package", finding.Package)
	appendCycloneDXProperty(&props, "lopper:vulnerability:priority", finding.Priority)
	appendCycloneDXProperty(&props, "lopper:vulnerability:reachable", boolString(finding.Reachable))
	appendCycloneDXProperty(&props, "lopper:vulnerability:fixed-version", finding.FixedVersion)
	if dep.Identity != nil {
		appendCycloneDXProperty(&props, "lopper:dependency:purl", dep.Identity.PURL)
	}
	if finding.Decision != nil {
		appendCycloneDXProperty(&props, "lopper:vex:decision:status", finding.Decision.Status)
		appendCycloneDXProperty(&props, "lopper:vex:decision:owner", finding.Decision.Owner)
		appendCycloneDXProperty(&props, "lopper:vex:decision:expires", finding.Decision.Expires)
		appendCycloneDXProperty(&props, "lopper:vex:decision:source", finding.Decision.Source)
		appendCycloneDXProperty(&props, "lopper:vex:decision:expired", boolString(finding.Decision.Expired))
	}
	return sortedCycloneDXProperties(props)
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
