package shared

import (
	"encoding/xml"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

// POMByteLimit matches the existing JVM build-file boundary (2 MiB).
const POMByteLimit = 2 * 1024 * 1024

// ParsedPOM owns private decoded evidence. Consumer views are defensive copies.
// It is intentionally absent from the public report and has no cache integration.
type ParsedPOM struct{ project pomProjectModel }

// POMConsumerView is caller-owned evidence, before consumer-specific resolution.
type POMConsumerView struct {
	Properties          map[string]string
	Dependencies        []POMDependency
	ManagedDependencies []POMDependency
}

// DecodePOM is the single XML decoding boundary. Callers retain read policy,
// cancellation, warning formatting, resolution budgets and evidence projection.
func DecodePOM(data []byte) (ParsedPOM, error) {
	if len(data) > POMByteLimit {
		return ParsedPOM{}, safeio.ErrFileTooLarge
	}
	var project pomProjectModel
	if err := xml.Unmarshal(data, &project); err != nil {
		return ParsedPOM{}, err
	}
	return ParsedPOM{project: project}, nil
}

// InventoryPolicy seeds aliases and discards blank declarations. The JVM
// consumer retains #1717 bounded embedded expansion and BOM warning policy.
func (p *ParsedPOM) InventoryPolicy() POMConsumerView {
	return p.view(inventoryPOMProperties(p.project))
}

// IdentityPolicy uses only explicit properties, including blanks; analysis
// retains whole-token eight-step resolution, managed fallback and conflicts.
func (p *ParsedPOM) IdentityPolicy() POMConsumerView {
	return p.view(p.project.Properties.values())
}

func (p *ParsedPOM) view(properties map[string]string) POMConsumerView {
	return POMConsumerView{
		Properties:          properties,
		Dependencies:        append([]POMDependency(nil), p.project.Dependencies...),
		ManagedDependencies: append([]POMDependency(nil), p.project.DependencyManagement.Dependencies...),
	}
}

// Values returns a fresh map of explicit property declarations.
func (p *pomPropertiesModel) values() map[string]string {
	values := make(map[string]string, len(p.Properties))
	for _, entry := range p.Properties {
		name := strings.TrimSpace(entry.XMLName.Local)
		if name != "" {
			values[name] = strings.TrimSpace(entry.Value)
		}
	}
	return values
}

type pomProjectModel struct {
	GroupID              string             `xml:"groupId"`
	ArtifactID           string             `xml:"artifactId"`
	Version              string             `xml:"version"`
	Parent               pomParentModel     `xml:"parent"`
	Properties           pomPropertiesModel `xml:"properties"`
	Dependencies         []POMDependency    `xml:"dependencies>dependency"`
	DependencyManagement struct {
		Dependencies []POMDependency `xml:"dependencies>dependency"`
	} `xml:"dependencyManagement"`
}

type pomParentModel struct {
	GroupID string `xml:"groupId"`
	Version string `xml:"version"`
}

type pomPropertiesModel struct {
	Properties []pomPropertyModel `xml:",any"`
}

type pomPropertyModel struct {
	XMLName xml.Name
	Value   string `xml:",chardata"`
}

type POMDependency struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Version    string `xml:"version"`
	Type       string `xml:"type"`
	Scope      string `xml:"scope"`
}

func inventoryPOMProperties(project pomProjectModel) map[string]string {
	properties := make(map[string]string)
	for _, property := range project.Properties.Properties {
		key := strings.TrimSpace(property.XMLName.Local)
		value := strings.TrimSpace(property.Value)
		if key == "" || value == "" {
			continue
		}
		properties[key] = value
	}

	groupID := strings.TrimSpace(project.GroupID)
	if groupID == "" {
		groupID = strings.TrimSpace(project.Parent.GroupID)
	}
	version := strings.TrimSpace(project.Version)
	if version == "" {
		version = strings.TrimSpace(project.Parent.Version)
	}
	artifactID := strings.TrimSpace(project.ArtifactID)

	setPomPropertyValue(properties, "project.groupId", groupID)
	setPomPropertyValue(properties, "pom.groupId", groupID)
	setPomPropertyValue(properties, "groupId", groupID)

	setPomPropertyValue(properties, "project.version", version)
	setPomPropertyValue(properties, "pom.version", version)
	setPomPropertyValue(properties, "version", version)

	setPomPropertyValue(properties, "project.artifactId", artifactID)
	setPomPropertyValue(properties, "pom.artifactId", artifactID)
	setPomPropertyValue(properties, "artifactId", artifactID)

	setPomPropertyValue(properties, "project.parent.groupId", strings.TrimSpace(project.Parent.GroupID))
	setPomPropertyValue(properties, "project.parent.version", strings.TrimSpace(project.Parent.Version))
	return properties
}

func setPomPropertyValue(properties map[string]string, key, value string) {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return
	}
	properties[key] = value
}
