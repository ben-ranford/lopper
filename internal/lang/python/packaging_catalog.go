package python

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/pelletier/go-toml/v2"
)

// Catalog lifetime is one adapter analysis. Parsed documents feed inventory and
// identity independently, including locks that inventory does not need as fallback.
type packagingCatalog struct {
	documents     map[string]report.PythonManifestDocument
	errors        map[string]error
	bytes         int64
	identityBytes int64
	identitySizes map[string]int64
}

const maxPackagingCatalogBytes int64 = 64 << 20
const maxPackagingIdentityProjectionBytes int64 = 64 << 20

func newPackagingCatalog() *packagingCatalog {
	return &packagingCatalog{
		documents:     make(map[string]report.PythonManifestDocument),
		errors:        make(map[string]error),
		identitySizes: make(map[string]int64),
	}
}

// ReadPackagingDocument decodes one bounded Python packaging file using the
// same representation as adapter inventory and cached identity evidence.
func ReadPackagingDocument(repo, path string) (report.PythonManifestDocument, error) {
	return newPackagingCatalog().read(repo, path)
}

func (c *packagingCatalog) read(repo, path string) (report.PythonManifestDocument, error) {
	if document, ok := c.documents[path]; ok && !document.Deferred {
		return document, c.errors[path]
	}
	document := report.PythonManifestDocument{Path: relativePackagingPath(repo, path)}
	limit := PackagingReadLimitBytes
	name := filepath.Base(path)
	if name == pythonPyprojectFile || name == pythonPipfileName {
		limit = ManifestReadLimitBytes
	}
	data, err := safeio.ReadFileUnderLimit(repo, path, limit)
	if err != nil {
		document.FailureStage = "read"
	} else {
		err = decodePackagingDocument(name, data, &document)
		if err != nil {
			document.FailureStage = "parse"
		}
	}
	if err != nil {
		document.Document = nil
		document.Failure = err.Error()
		switch {
		case errors.Is(err, os.ErrPermission):
			document.FailureKind = "permission"
		case errors.Is(err, os.ErrNotExist):
			document.FailureKind = "missing"
		case errors.Is(err, safeio.ErrFileTooLarge):
			document.FailureKind = "large"
		}
	}
	c.retain(path, document, err, int64(len(data)))
	return document, err
}

func (c *packagingCatalog) retain(path string, document report.PythonManifestDocument, err error, size int64) {
	if previousSize := c.identitySizes[path]; previousSize != 0 {
		c.identityBytes -= previousSize
		delete(c.identitySizes, path)
	}
	if err == nil && size > maxPackagingCatalogBytes-c.bytes {
		deferred := report.PythonManifestDocument{Path: document.Path, Deferred: true}
		deferred.IdentityProjection, deferred.IdentityText, deferred.IdentityProjectionSet = pythonIdentityProjection(filepath.Base(path), document)
		if deferred.IdentityProjectionSet {
			projectionBytes, marshalErr := json.Marshal(struct {
				Document map[string]any `json:"document,omitempty"`
				Text     string         `json:"text,omitempty"`
			}{Document: deferred.IdentityProjection, Text: deferred.IdentityText})
			switch {
			case marshalErr != nil:
				deferred.IdentityProjectionError = fmt.Sprintf("encode bounded identity projection: %v", marshalErr)
				deferred.IdentityProjection = nil
				deferred.IdentityText = ""
			case int64(len(projectionBytes)) > maxPackagingIdentityProjectionBytes-c.identityBytes:
				deferred.IdentityProjectionError = fmt.Sprintf("Python identity projection exceeds the %d-byte catalog limit", maxPackagingIdentityProjectionBytes)
				deferred.IdentityProjection = nil
				deferred.IdentityText = ""
			default:
				c.identityBytes += int64(len(projectionBytes))
				c.identitySizes[path] = int64(len(projectionBytes))
			}
		}
		c.documents[path] = deferred
		return
	}
	c.documents[path] = document
	c.errors[path] = err
	if err == nil {
		c.bytes += size
	}
}

// pythonIdentityProjection retains only the fields consumed by identity
// enrichment. The full decoded document remains bounded by maxPackagingCatalogBytes;
// overflow documents can still enrich identities without another source read.
func pythonIdentityProjection(name string, document report.PythonManifestDocument) (map[string]any, string, bool) {
	if document.Failure != "" {
		return nil, "", false
	}
	switch name {
	case pythonPyprojectFile:
		return pyprojectIdentityProjection(document.Document), "", true
	case pythonPipfileName:
		return pythonIdentityPackageSections(document.Document, "packages", "dev-packages"), "", true
	case pythonPoetryLockName, pythonUVLockName:
		return pythonLockIdentityProjection(document.Document), "", true
	case pythonPipfileLockName:
		return pipfileLockIdentityProjection(document.Document), "", true
	case pythonRequirementsTxt:
		return nil, compactRequirementsIdentityText(document.Text), true
	default:
		return nil, "", false
	}
}

func pythonIdentityFields(source map[string]any, keys ...string) map[string]any {
	if source == nil {
		return nil
	}
	result := make(map[string]any, len(keys))
	for _, key := range keys {
		if value, ok := source[key]; ok {
			result[key] = value
		}
	}
	return result
}

func pythonIdentityTable(value any, keys ...string) any {
	if table, ok := value.(map[string]any); ok {
		return pythonIdentityFields(table, keys...)
	}
	return value
}

func pythonIdentityPackageSections(document map[string]any, sections ...string) map[string]any {
	return projectPythonIdentityCollections(pythonIdentityFields(document, sections...), pythonIdentityPackageTable)
}

func projectPythonIdentityCollections[T []string | map[string]any](document map[string]any, project func(any) T) map[string]any {
	result := make(map[string]any, len(document))
	for name, raw := range document {
		if value := project(raw); len(value) != 0 {
			result[name] = value
		}
	}
	return result
}

func pythonIdentityPackageTable(value any) map[string]any {
	packages, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	projected := make(map[string]any, len(packages))
	for name, raw := range packages {
		if entry := pythonIdentityPackageEntry(raw); entry != nil {
			projected[name] = entry
		}
	}
	return projected
}

func pythonIdentityPackageEntry(value any) any {
	if version, ok := value.(string); ok {
		return version
	}
	entry, _ := value.(map[string]any)
	if _, ok := entry["version"].(string); !ok {
		return nil
	}
	result := pythonIdentityFields(entry, "version")
	// Unsupported sources reject by key presence; their values are not consumed.
	for _, field := range []string{"file", "git", "path", "ref", "url"} {
		if _, exists := entry[field]; exists {
			result[field] = nil
		}
	}
	if optional, ok := entry["optional"].(bool); ok {
		result["optional"] = optional
	}
	return result
}

// ManifestRequirementStrings selects the strings consumed by manifest identity enrichment.
func ManifestRequirementStrings(value any) []string {
	switch requirements := value.(type) {
	case []string:
		return requirements
	case []any:
		result := make([]string, 0, len(requirements))
		for _, raw := range requirements {
			if requirement, ok := raw.(string); ok {
				result = append(result, requirement)
			}
		}
		return result
	default:
		return nil
	}
}

func pythonIdentityRequirementFields(document map[string]any, fields ...string) map[string]any {
	return projectPythonIdentityCollections(pythonIdentityFields(document, fields...), ManifestRequirementStrings)
}

func pythonIdentityRequirementGroups(value any) map[string]any {
	groups, _ := value.(map[string]any)
	return projectPythonIdentityCollections(groups, ManifestRequirementStrings)
}

func pyprojectIdentityProjection(document map[string]any) map[string]any {
	result := make(map[string]any, 3)
	if groups := pythonIdentityRequirementGroups(document["dependency-groups"]); len(groups) != 0 {
		result["dependency-groups"] = groups
	}
	if project := pythonProjectIdentityProjection(document["project"]); len(project) != 0 {
		result["project"] = project
	}
	if tool := pythonToolIdentityProjection(document["tool"]); len(tool) != 0 {
		result["tool"] = tool
	}
	return result
}

func pythonProjectIdentityProjection(value any) map[string]any {
	project, _ := value.(map[string]any)
	result := pythonIdentityRequirementFields(project, "dependencies")
	if groups := pythonIdentityRequirementGroups(project["optional-dependencies"]); len(groups) != 0 {
		result["optional-dependencies"] = groups
	}
	return result
}

func pythonToolIdentityProjection(value any) map[string]any {
	tool, _ := value.(map[string]any)
	result := make(map[string]any, 2)
	uv, _ := tool["uv"].(map[string]any)
	if projected := pythonIdentityRequirementFields(uv, "dev-dependencies"); len(projected) != 0 {
		result["uv"] = projected
	}
	if poetry := poetryIdentityProjection(tool["poetry"]); len(poetry) != 0 {
		result["poetry"] = poetry
	}
	return result
}

func poetryIdentityProjection(value any) map[string]any {
	poetry, _ := value.(map[string]any)
	result := pythonIdentityPackageSections(poetry, "dependencies", "dev-dependencies")
	if groups := poetryIdentityGroups(poetry["group"]); len(groups) != 0 {
		result["group"] = groups
	}
	return result
}

func poetryIdentityGroups(value any) map[string]any {
	groups, _ := value.(map[string]any)
	return projectPythonIdentityCollections(groups, poetryIdentityGroup)
}

func poetryIdentityGroup(value any) map[string]any {
	group, _ := value.(map[string]any)
	result := pythonIdentityPackageSections(group, "dependencies")
	if optional, ok := group["optional"].(bool); ok {
		result["optional"] = optional
	}
	return result
}

func pythonLockIdentityProjection(document map[string]any) map[string]any {
	entries, ok := document["package"].([]any)
	if !ok {
		return map[string]any{}
	}
	projected := make([]any, 0, len(entries))
	for _, entry := range entries {
		if table, ok := entry.(map[string]any); ok {
			fields := pythonIdentityFields(table, "name", "version")
			for key, value := range fields {
				if _, ok := value.(string); !ok {
					delete(fields, key)
				}
			}
			projected = append(projected, fields)
		}
	}
	return map[string]any{"package": projected}
}

func pipfileLockIdentityProjection(document map[string]any) map[string]any {
	result := make(map[string]any, 2)
	for _, section := range []string{"default", "develop"} {
		if value, exists := document[section]; exists {
			result[section] = pipfileLockIdentitySection(value)
		}
	}
	return result
}

func pipfileLockIdentitySection(value any) any {
	if value == nil {
		return nil
	}
	packages, ok := value.(map[string]any)
	if !ok || !ValidPipfileIdentitySection(packages) {
		// Identity rejects the whole section with a generic warning. A non-null,
		// non-map sentinel preserves that rejection without retaining its payload.
		return false
	}
	projected := make(map[string]any, len(packages))
	for name, raw := range packages {
		projected[name] = pythonIdentityTable(raw, "version")
	}
	return projected
}

// ValidPipfileIdentitySection reports whether every entry can supply identity evidence.
func ValidPipfileIdentitySection(packages map[string]any) bool {
	for _, raw := range packages {
		if raw == nil {
			continue
		}
		metadata, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		version := metadata["version"]
		if version != nil {
			if _, ok := version.(string); !ok {
				return false
			}
		}
	}
	return true
}

func (c *packagingCatalog) parse(repo, path string) (map[string]struct{}, []string, error) {
	document, err := c.read(repo, path)
	name := filepath.Base(path)
	if err != nil {
		return packagingCatalogFailure(repo, path, document.FailureStage, err)
	}
	switch name {
	case pythonPyprojectFile:
		return parsePyprojectDependenciesDocument(repo, path, document.Document, nil)
	case pythonPipfileName:
		return parsePipfileDependenciesDocument(repo, path, document.Document, nil)
	case pythonRequirementsTxt:
		return parseRequirementsContent(document.Path, []byte(document.Text))
	case pythonPipfileLockName:
		return parsePipfileLockDependenciesDocument(repo, path, document.Document, nil)
	default:
		return parsePackageLockDependenciesDocument(repo, path, document.Document, nil)
	}
}

func decodePackagingDocument(name string, data []byte, document *report.PythonManifestDocument) error {
	switch name {
	case pythonRequirementsTxt:
		document.Text = string(data)
		return nil
	case pythonPipfileLockName:
		return json.Unmarshal(data, &document.Document)
	default:
		if err := toml.Unmarshal(data, &document.Document); err != nil {
			return err
		}
		normalizePackagingValue(document.Document)
		return nil
	}
}

// Non-finite TOML numbers cannot carry dependency evidence or be encoded in JSON.
// Normalize them before either inventory or identity consumes the cached document.
func normalizePackagingValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		for key, item := range value {
			value[key] = normalizePackagingValue(item)
		}
	case []any:
		for index, item := range value {
			value[index] = normalizePackagingValue(item)
		}
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil
		}
	}
	return value
}

func packagingCatalogFailure(repo, path, stage string, err error) (map[string]struct{}, []string, error) {
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]struct{}), nil, nil
	}
	name := filepath.Base(path)
	label := relativePackagingPath(repo, path)
	if stage == "parse" {
		format := "%s: skipped TOML parsing after decode error: %v"
		if name == pythonPipfileLockName {
			format = "%s: skipped Pipfile.lock parsing after JSON decode error: %v"
		}
		return make(map[string]struct{}), []string{fmt.Sprintf(format, label, err)}, nil
	}
	if isPurePythonPackagingFileTooLargeError(err) && name != pythonPyprojectFile && name != pythonPipfileName {
		return make(map[string]struct{}), []string{packagingCatalogSizeWarning(label, name)}, nil
	}
	return nil, nil, fmt.Errorf("read %s: %w", label, err)
}

func packagingCatalogSizeWarning(label, name string) string {
	switch name {
	case pythonPipfileLockName:
		return fmt.Sprintf("%s: skipped Pipfile.lock larger than %d bytes", label, PackagingReadLimitBytes)
	case pythonRequirementsTxt:
		return fmt.Sprintf("%s: skipped requirements.txt above %d bytes", label, maxRequirementsTxtBytes)
	default:
		return fmt.Sprintf("%s: skipped packaging file larger than %d bytes", label, PackagingReadLimitBytes)
	}
}

func (c *packagingCatalog) snapshot() []report.PythonManifestDocument {
	paths := make([]string, 0, len(c.documents))
	for path := range c.documents {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	documents := make([]report.PythonManifestDocument, 0, len(paths))
	for _, path := range paths {
		documents = append(documents, c.documents[path])
	}
	return documents
}
