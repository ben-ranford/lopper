package model

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	MavenEvidenceByteLimit   = 64 << 20
	MavenEvidenceEmptySize   = len(`{"version":1,"policy":"identity","entries":[]}`)
	MavenEvidenceEntryLimit  = 65536
	MavenEvidenceRecordLimit = 131072
	MavenEvidenceValueLimit  = 1048576
	MavenAdapterEntryLimit   = 2048
)

var ErrMavenEvidenceLimit = errors.New("maven evidence retention limit exceeded")

// MavenDeclaration retains unresolved declaration values for each consumer policy.
type MavenDeclaration struct {
	GroupID    string `xml:"groupId" json:"groupId"`
	ArtifactID string `xml:"artifactId" json:"artifactId"`
	Version    string `xml:"version" json:"version"`
	Type       string `xml:"type" json:"type"`
	Scope      string `xml:"scope" json:"scope"`
}

// MavenManifest is immutable live evidence. It deliberately has no JSON codec.
type MavenManifest struct {
	path                  string
	properties            map[string]string
	dependencies, managed []MavenDeclaration
	stage, kind           string
	size, values          int
}

// MavenEvidenceBudget is the caller's remaining retention allowance.
// Model limits still cap both dimensions before any evidence is copied.
type MavenEvidenceBudget struct {
	Bytes  int
	Values int
}

func NewMavenManifest(path string, properties map[string]string, dependencies, managed []MavenDeclaration, stage, kind string) (MavenManifest, error) {
	return NewMavenManifestWithinBudget(path, properties, dependencies, managed, stage, kind, MavenEvidenceBudget{Bytes: MavenEvidenceByteLimit, Values: MavenEvidenceValueLimit})
}

// NewMavenManifestWithinBudget admits borrowed inputs before copying retained data.
// The caller's remaining allowance can lower, but never raise, the model limits.
func NewMavenManifestWithinBudget(path string, properties map[string]string, dependencies, managed []MavenDeclaration, stage, kind string, budget MavenEvidenceBudget) (MavenManifest, error) {
	values := len(properties) + len(dependencies) + len(managed)
	if err := ValidateMavenManifestState(path, stage, kind, values); err != nil {
		return MavenManifest{}, err
	}
	if len(properties) > MavenEvidenceRecordLimit || len(dependencies)+len(managed) > MavenEvidenceRecordLimit || values > min(budget.Values, MavenEvidenceValueLimit) {
		return MavenManifest{}, ErrMavenEvidenceLimit
	}
	value := MavenManifest{path: path, properties: properties, dependencies: dependencies, managed: managed, stage: stage, kind: kind, values: values}
	value.size = mavenManifestSize(value)
	if value.size > min(budget.Bytes, MavenEvidenceByteLimit) {
		return MavenManifest{}, ErrMavenEvidenceLimit
	}
	value.path = strings.Clone(path)
	value.properties = cloneMavenProperties(properties)
	value.dependencies = cloneMavenDeclarations(dependencies)
	value.managed = cloneMavenDeclarations(managed)
	return value, nil
}

// ValidateMavenManifestState validates bounded metadata without retaining inputs.
func ValidateMavenManifestState(path, stage, kind string, values int) error {
	if err := ValidateMavenPath(path); err != nil {
		return err
	}
	if values < 0 || !validMavenFailure(stage, kind) || (stage != "" && values != 0) {
		return errors.New("invalid Maven evidence state")
	}
	return nil
}

func validMavenFailure(stage, kind string) bool {
	switch stage {
	case "":
		return kind == ""
	case "parse":
		return kind == "xml"
	case "read":
		return kind == "permission" || kind == "missing" || kind == "large" || kind == "io"
	default:
		return false
	}
}
func (m *MavenManifest) Path() string                  { return m.path }
func (m *MavenManifest) Properties() map[string]string { return maps.Clone(m.properties) }
func (m *MavenManifest) Dependencies() []MavenDeclaration {
	return append([]MavenDeclaration(nil), m.dependencies...)
}
func (m *MavenManifest) ManagedDependencies() []MavenDeclaration {
	return append([]MavenDeclaration(nil), m.managed...)
}
func (m *MavenManifest) Failure() (string, string) { return m.stage, m.kind }
func (m *MavenManifest) Size() int                 { return m.size }
func (m *MavenManifest) Values() int               { return m.values }
func (m *MavenManifest) WithPath(path string) (MavenManifest, error) {
	next := *m
	if err := ValidateMavenPath(path); err != nil {
		return MavenManifest{}, err
	}
	next.size += MavenJSONStringSize(path) - MavenJSONStringSize(next.path)
	next.path = strings.Clone(path)
	if next.size > MavenEvidenceByteLimit {
		return MavenManifest{}, ErrMavenEvidenceLimit
	}
	return next, nil
}
func (m *MavenManifest) Equal(other MavenManifest) bool {
	return m.path == other.path && m.stage == other.stage && m.kind == other.kind && maps.Equal(m.properties, other.properties) && slices.Equal(m.dependencies, other.dependencies) && slices.Equal(m.managed, other.managed)
}
func cloneMavenProperties(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[strings.Clone(key)] = strings.Clone(value)
	}
	return result
}
func cloneMavenDeclarations(values []MavenDeclaration) []MavenDeclaration {
	result := slices.Clone(values)
	for i := range result {
		d := &result[i]
		d.GroupID = strings.Clone(d.GroupID)
		d.ArtifactID = strings.Clone(d.ArtifactID)
		d.Version = strings.Clone(d.Version)
		d.Type = strings.Clone(d.Type)
		d.Scope = strings.Clone(d.Scope)
	}
	return result
}

// ValidateMavenPath follows the host filesystem, retaining raw Unix filename bytes.
func ValidateMavenPath(path string) error {
	if path == "" || strings.ContainsRune(path, 0) || filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return errors.New("invalid Maven evidence path")
	}
	for _, component := range strings.Split(path, "/") {
		if component == "" || component == "." || component == ".." {
			return errors.New("noncanonical Maven evidence path")
		}
		if runtime.GOOS == "windows" && invalidWindowsMavenComponent(component) {
			return errors.New("invalid Windows Maven evidence path")
		}
	}
	return nil
}
func invalidWindowsMavenComponent(value string) bool {
	if strings.ContainsAny(value, `\<>:"|?*`) || strings.HasSuffix(value, ".") || strings.HasSuffix(value, " ") {
		return true
	}
	for _, r := range value {
		if r < 32 {
			return true
		}
	}
	return !filepath.IsLocal(value)
}

// MavenEvidenceSize includes the canonical v1 envelope and record separators.
func MavenEvidenceSize(entries []MavenManifest) (int, error) {
	size := MavenEvidenceEmptySize
	values := 0
	if len(entries) > MavenEvidenceEntryLimit {
		return 0, ErrMavenEvidenceLimit
	}
	for i, entry := range entries {
		if entry.size == 0 {
			return 0, errors.New("uninitialized Maven evidence")
		}
		size += entry.size
		values += entry.values
		if i > 0 {
			size++
		}
		if size > MavenEvidenceByteLimit || values > MavenEvidenceValueLimit {
			return 0, ErrMavenEvidenceLimit
		}
	}
	return size, nil
}
func mavenManifestSize(m MavenManifest) int {
	size := len(`{"path":,"properties":{},"dependencies":[],"managed":[],"stage":,"kind":}`) + MavenJSONStringSize(m.path) + MavenJSONStringSize(m.stage) + MavenJSONStringSize(m.kind)
	i := 0
	for key, value := range m.properties {
		if i > 0 {
			size++
		}
		i++
		size += MavenJSONStringSize(key) + 1 + MavenJSONStringSize(value)
	}
	return size + mavenDeclarationsSize(m.dependencies) + mavenDeclarationsSize(m.managed)
}
func mavenDeclarationsSize(values []MavenDeclaration) int {
	size := 0
	for i, d := range values {
		if i > 0 {
			size++
		}
		size += len(`{"groupId":,"artifactId":,"version":,"type":,"scope":}`) + MavenJSONStringSize(d.GroupID) + MavenJSONStringSize(d.ArtifactID) + MavenJSONStringSize(d.Version) + MavenJSONStringSize(d.Type) + MavenJSONStringSize(d.Scope)
	}
	return size
}

// MavenJSONStringSize counts the canonical JSON spelling used for Maven evidence.
func MavenJSONStringSize(value string) int {
	size := 2
	for _, r := range value {
		size += mavenJSONRuneSize(r)
	}
	return size
}
func mavenJSONRuneSize(r rune) int {
	switch r {
	case '"', '\\', '\b', '\f', '\n', '\r', '\t':
		return 2
	case '<', '>', '&', '\u2028', '\u2029':
		return 6
	}
	if r < 32 {
		return 6
	}
	return utf8.RuneLen(r)
}

func RebaseMavenManifest(entry MavenManifest, from, to string) (MavenManifest, error) {
	prefix, err := filepath.Rel(to, from)
	if err != nil {
		return MavenManifest{}, err
	}
	path := filepath.ToSlash(filepath.Join(prefix, filepath.FromSlash(entry.Path())))
	rebased, err := entry.WithPath(path)
	if err != nil {
		return MavenManifest{}, fmt.Errorf("rebase Maven evidence: %w", err)
	}
	return rebased, nil
}
