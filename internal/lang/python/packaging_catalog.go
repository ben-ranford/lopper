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
			if marshalErr != nil {
				deferred.IdentityProjectionError = fmt.Sprintf("encode bounded identity projection: %v", marshalErr)
				deferred.IdentityProjection = nil
				deferred.IdentityText = ""
			} else if int64(len(projectionBytes)) > maxPackagingIdentityProjectionBytes-c.identityBytes {
				deferred.IdentityProjectionError = fmt.Sprintf("Python identity projection exceeds the %d-byte catalog limit", maxPackagingIdentityProjectionBytes)
				deferred.IdentityProjection = nil
				deferred.IdentityText = ""
			} else {
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
	copyKeys := func(source map[string]any, keys ...string) map[string]any {
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
	switch name {
	case pythonPyprojectFile:
		result := copyKeys(document.Document, "project", "dependency-groups")
		tool, ok := document.Document["tool"].(map[string]any)
		if !ok {
			if value, exists := document.Document["tool"]; exists {
				result["tool"] = value
			}
		} else {
			projectTools := make(map[string]any, 2)
			for _, key := range []string{"uv", "poetry"} {
				value, exists := tool[key]
				if !exists {
					continue
				}
				switch key {
				case "uv":
					if table, valid := value.(map[string]any); valid {
						projectTools[key] = copyKeys(table, "dev-dependencies")
					} else {
						projectTools[key] = value
					}
				case "poetry":
					if table, valid := value.(map[string]any); valid {
						projectTools[key] = copyKeys(table, "dependencies", "dev-dependencies", "group")
					} else {
						projectTools[key] = value
					}
				}
			}
			if len(projectTools) != 0 {
				result["tool"] = projectTools
			}
		}
		return result, "", true
	case pythonPipfileName:
		return copyKeys(document.Document, "packages", "dev-packages"), "", true
	case pythonPoetryLockName, pythonUVLockName:
		packages, exists := document.Document["package"]
		if !exists {
			return map[string]any{}, "", true
		}
		entries, ok := packages.([]any)
		if !ok {
			return map[string]any{"package": packages}, "", true
		}
		projected := make([]any, 0, len(entries))
		for _, entry := range entries {
			if table, valid := entry.(map[string]any); valid {
				projected = append(projected, copyKeys(table, "name", "version"))
			} else {
				projected = append(projected, entry)
			}
		}
		return map[string]any{"package": projected}, "", true
	case pythonPipfileLockName:
		result := make(map[string]any, 2)
		for _, section := range []string{"default", "develop"} {
			value, exists := document.Document[section]
			if !exists {
				continue
			}
			packages, ok := value.(map[string]any)
			if !ok {
				result[section] = value
				continue
			}
			projected := make(map[string]any, len(packages))
			for name, raw := range packages {
				if metadata, valid := raw.(map[string]any); valid {
					projected[name] = copyKeys(metadata, "version")
				} else {
					projected[name] = raw
				}
			}
			result[section] = projected
		}
		return result, "", true
	case pythonRequirementsTxt:
		return nil, document.Text, true
	default:
		return nil, "", false
	}
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
