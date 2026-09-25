package python

import (
	"encoding/json"
	"errors"
	"fmt"
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
	documents map[string]report.PythonManifestDocument
	errors    map[string]error
	bytes     int64
}

const maxPackagingCatalogBytes int64 = 64 << 20

func newPackagingCatalog() *packagingCatalog {
	return &packagingCatalog{documents: make(map[string]report.PythonManifestDocument), errors: make(map[string]error)}
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
	if err == nil && size > maxPackagingCatalogBytes-c.bytes {
		c.documents[path] = report.PythonManifestDocument{Path: document.Path, Deferred: true}
		return
	}
	c.documents[path] = document
	c.errors[path] = err
	if err == nil {
		c.bytes += size
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
		return toml.Unmarshal(data, &document.Document)
	}
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
