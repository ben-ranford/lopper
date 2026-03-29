package report

import "sort"

const (
	sarifSchemaURI = "https://json.schemastore.org/sarif-2.1.0.json"
	sarifVersion   = "2.1.0"
)

type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	InformationURI string      `json:"informationUri,omitempty"`
	Version        string      `json:"version,omitempty"`
	Rules          []sarifRule `json:"rules,omitempty"`
}

type sarifRule struct {
	ID               string         `json:"id"`
	Name             string         `json:"name,omitempty"`
	ShortDescription sarifMessage   `json:"shortDescription"`
	Help             *sarifMessage  `json:"help,omitempty"`
	Properties       map[string]any `json:"properties,omitempty"`
}

type sarifResult struct {
	RuleID     string          `json:"ruleId"`
	Level      string          `json:"level,omitempty"`
	Message    sarifMessage    `json:"message"`
	Locations  []sarifLocation `json:"locations,omitempty"`
	Properties map[string]any  `json:"properties,omitempty"`
}

type sarifMessage struct {
	Text string `json:"text"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           *sarifRegion          `json:"region,omitempty"`
}

type sarifArtifactLocation struct {
	URI string `json:"uri"`
}

type sarifRegion struct {
	StartLine   int `json:"startLine,omitempty"`
	StartColumn int `json:"startColumn,omitempty"`
}

type sarifRuleBuilder struct {
	rules map[string]sarifRule
}

func newSARIFRuleBuilder() *sarifRuleBuilder {
	return &sarifRuleBuilder{rules: make(map[string]sarifRule)}
}

func (b *sarifRuleBuilder) add(rule sarifRule) {
	if _, ok := b.rules[rule.ID]; ok {
		return
	}
	b.rules[rule.ID] = rule
}

func (b *sarifRuleBuilder) list() []sarifRule {
	ids := make([]string, 0, len(b.rules))
	for id := range b.rules {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := make([]sarifRule, 0, len(ids))
	for _, id := range ids {
		items = append(items, b.rules[id])
	}
	return items
}
