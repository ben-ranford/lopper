package advisory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
	"gopkg.in/yaml.v3"
)

const maxAdvisorySourceBytes = 8 * 1024 * 1024

type localDocument struct {
	Advisories []localAdvisory `json:"advisories" yaml:"advisories"`
}

type localAdvisory struct {
	ID           string   `json:"id" yaml:"id"`
	Package      string   `json:"package" yaml:"package"`
	Ecosystem    string   `json:"ecosystem" yaml:"ecosystem"`
	Severity     string   `json:"severity" yaml:"severity"`
	FixedVersion string   `json:"fixedVersion" yaml:"fixedVersion"`
	Source       string   `json:"source" yaml:"source"`
	Aliases      []string `json:"aliases" yaml:"aliases"`
}

type osvAdvisory struct {
	ID               string         `json:"id" yaml:"id"`
	Aliases          []string       `json:"aliases" yaml:"aliases"`
	Affected         []osvAffected  `json:"affected" yaml:"affected"`
	Severity         []osvSeverity  `json:"severity" yaml:"severity"`
	DatabaseSpecific map[string]any `json:"database_specific" yaml:"database_specific"`
	Modified         string         `json:"modified" yaml:"modified"`
	Published        string         `json:"published" yaml:"published"`
}

type osvAffected struct {
	Package           osvPackage     `json:"package" yaml:"package"`
	Ranges            []osvRange     `json:"ranges" yaml:"ranges"`
	EcosystemSpecific map[string]any `json:"ecosystem_specific" yaml:"ecosystem_specific"`
	DatabaseSpecific  map[string]any `json:"database_specific" yaml:"database_specific"`
}

type osvPackage struct {
	Ecosystem string `json:"ecosystem" yaml:"ecosystem"`
	Name      string `json:"name" yaml:"name"`
}

type osvRange struct {
	Events []osvEvent `json:"events" yaml:"events"`
}

type osvEvent struct {
	Fixed string `json:"fixed" yaml:"fixed"`
}

type osvSeverity struct {
	Type  string `json:"type" yaml:"type"`
	Score string `json:"score" yaml:"score"`
}

func Load(path string) ([]report.VulnerabilityAdvisory, error) {
	trimmedPath := strings.TrimSpace(path)
	if trimmedPath == "" {
		return nil, nil
	}
	data, err := safeio.ReadFileLimit(trimmedPath, maxAdvisorySourceBytes)
	if err != nil {
		return nil, fmt.Errorf("read advisory source %s: %w", trimmedPath, err)
	}
	advisories, err := parse(trimmedPath, data)
	if err != nil {
		return nil, fmt.Errorf("parse advisory source %s: %w", trimmedPath, err)
	}
	defaultSource := "local:" + filepath.ToSlash(trimmedPath)
	for i := range advisories {
		if strings.TrimSpace(advisories[i].Source) == "" {
			advisories[i].Source = defaultSource
		}
	}
	return advisories, nil
}

func parse(path string, data []byte) ([]report.VulnerabilityAdvisory, error) {
	if looksLikeSequence(data) {
		advisories, err := parseOSVDocument(path, data)
		if err != nil {
			return nil, err
		}
		return requireAdvisories(advisories)
	}
	if advisories, ok, err := parseLocalDocument(path, data); ok || err != nil {
		if err != nil {
			return nil, err
		}
		return requireAdvisories(advisories)
	}
	advisories, err := parseOSVDocument(path, data)
	if err != nil {
		return nil, err
	}
	return requireAdvisories(advisories)
}

func looksLikeSequence(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) > 0 && (trimmed[0] == '[' || trimmed[0] == '-')
}

func requireAdvisories(advisories []report.VulnerabilityAdvisory) ([]report.VulnerabilityAdvisory, error) {
	if len(advisories) == 0 {
		return nil, fmt.Errorf("no advisories found")
	}
	return advisories, nil
}

func parseLocalDocument(path string, data []byte) ([]report.VulnerabilityAdvisory, bool, error) {
	var doc localDocument
	if err := decodeStructured(path, data, &doc); err != nil {
		return nil, false, err
	}
	if len(doc.Advisories) == 0 {
		return nil, false, nil
	}
	advisories := make([]report.VulnerabilityAdvisory, 0, len(doc.Advisories))
	for _, item := range doc.Advisories {
		advisories = append(advisories, report.VulnerabilityAdvisory{
			ID:           item.ID,
			Package:      item.Package,
			Ecosystem:    item.Ecosystem,
			Severity:     item.Severity,
			FixedVersion: item.FixedVersion,
			Source:       item.Source,
			Aliases:      item.Aliases,
		})
	}
	return advisories, true, nil
}

func parseOSVDocument(path string, data []byte) ([]report.VulnerabilityAdvisory, error) {
	var list []osvAdvisory
	if err := decodeStructured(path, data, &list); err == nil && len(list) > 0 {
		return advisoriesFromOSV(list), nil
	}

	var wrapped struct {
		Vulns []osvAdvisory `json:"vulns" yaml:"vulns"`
	}
	if err := decodeStructured(path, data, &wrapped); err == nil && len(wrapped.Vulns) > 0 {
		return advisoriesFromOSV(wrapped.Vulns), nil
	}

	var single osvAdvisory
	if err := decodeStructured(path, data, &single); err != nil {
		return nil, err
	}
	return advisoriesFromOSV([]osvAdvisory{single}), nil
}

func decodeStructured(path string, data []byte, target any) error {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		decoder := json.NewDecoder(bytes.NewReader(data))
		if err := decoder.Decode(target); err != nil {
			return fmt.Errorf("invalid JSON advisory source: %w", err)
		}
	default:
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		if err := decoder.Decode(target); err != nil {
			return fmt.Errorf("invalid YAML advisory source: %w", err)
		}
	}
	return nil
}

func advisoriesFromOSV(items []osvAdvisory) []report.VulnerabilityAdvisory {
	advisories := make([]report.VulnerabilityAdvisory, 0)
	for _, item := range items {
		if strings.TrimSpace(item.ID) == "" {
			continue
		}
		severity := osvAdvisorySeverity(item)
		for _, affected := range item.Affected {
			if strings.TrimSpace(affected.Package.Name) == "" {
				continue
			}
			advisories = append(advisories, report.VulnerabilityAdvisory{
				ID:           item.ID,
				Package:      affected.Package.Name,
				Ecosystem:    affected.Package.Ecosystem,
				Severity:     severity,
				FixedVersion: firstFixedVersion(affected.Ranges),
				Aliases:      item.Aliases,
			})
		}
	}
	return advisories
}

func osvAdvisorySeverity(item osvAdvisory) string {
	if severity := stringValue(item.DatabaseSpecific, "severity"); severity != "" {
		return severity
	}
	for _, affected := range item.Affected {
		if severity := stringValue(affected.DatabaseSpecific, "severity"); severity != "" {
			return severity
		}
		if severity := stringValue(affected.EcosystemSpecific, "severity"); severity != "" {
			return severity
		}
	}
	for _, severity := range item.Severity {
		if value := cvssSeverity(severity.Score); value != "" {
			return value
		}
	}
	return "unknown"
}

func stringValue(values map[string]any, key string) string {
	if len(values) == 0 {
		return ""
	}
	value, ok := values[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func cvssSeverity(score string) string {
	score = strings.TrimSpace(score)
	if score == "" {
		return ""
	}
	var value float64
	if _, err := fmt.Sscanf(score, "%f", &value); err != nil {
		return ""
	}
	switch {
	case value >= 9:
		return "critical"
	case value >= 7:
		return "high"
	case value >= 4:
		return "medium"
	case value > 0:
		return "low"
	default:
		return ""
	}
}

func firstFixedVersion(ranges []osvRange) string {
	for _, item := range ranges {
		for _, event := range item.Events {
			if fixed := strings.TrimSpace(event.Fixed); fixed != "" {
				return fixed
			}
		}
	}
	return ""
}
