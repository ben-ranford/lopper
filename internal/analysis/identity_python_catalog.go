package analysis

import (
	"errors"
	pythonlang "github.com/ben-ranford/lopper/internal/lang/python"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
	"io/fs"
	"path/filepath"
	"strings"
)

func collectPythonCatalogEvidence(repo string, index identityIndex, documents []report.PythonManifestDocument, warnings *identityWarningCollector) {
	for _, manifestPhase := range []bool{false, true} {
		for _, document := range documents {
			name := filepath.Base(document.Path)
			isManifest := name == pythonProjectFileName || name == pythonPipfileName
			if isManifest == manifestPhase {
				collectPythonCatalogDocument(repo, index, document, warnings)
			}
		}
	}
}

func collectPythonCatalogDocument(repo string, index identityIndex, document report.PythonManifestDocument, warnings *identityWarningCollector) {
	path := filepath.Join(repo, filepath.FromSlash(document.Path))
	if document.Deferred {
		decoded, err := pythonlang.ReadPackagingDocument(repo, path)
		document = decoded
		if err != nil {
			addPythonCatalogFailure(path, document, warnings)
			return
		}
	}
	if document.Failure != "" {
		addPythonCatalogFailure(path, document, warnings)
		return
	}
	switch filepath.Base(path) {
	case poetryLockFileName, uvLockFileName:
		collectPythonTOMLLockDocument(repo, path, index, document.Document)
	case "Pipfile.lock":
		collectPipfileLockDocument(repo, path, index, document.Document, warnings)
	case "requirements.txt":
		collectRequirementsContent(repo, path, index, document.Text)
	case pythonProjectFileName:
		collectPyprojectManifestEvidenceDocument(repo, path, index, document.Document)
	case pythonPipfileName:
		collectPipfileManifestEvidenceDocument(repo, path, index, document.Document)
	}
}

func addPythonCatalogFailure(path string, document report.PythonManifestDocument, warnings *identityWarningCollector) {
	kind := identityReadFailed
	if document.FailureStage == "parse" {
		kind = identityParseFailed
	}
	failure := errors.New(document.Failure)
	switch document.FailureKind {
	case "permission":
		failure = fs.ErrPermission
	case "missing":
		failure = fs.ErrNotExist
	case "large":
		failure = safeio.ErrFileTooLarge
	}
	warnings.addFailure(document.FailureStage, path, kind, failure)
}

func collectPipfileLockDocument(repo, path string, index identityIndex, document map[string]any, warnings *identityWarningCollector) {
	for _, section := range []string{"default", "develop"} {
		value, present := document[section]
		if !present || value == nil {
			continue
		}
		packages, ok := value.(map[string]any)
		if !ok || !validPipfileIdentitySection(packages) {
			warnings.addSectionParseFailure(path, section)
			continue
		}
		for name, raw := range packages {
			metadata, _ := raw.(map[string]any)
			version, _ := metadata["version"].(string)
			addPythonEvidence(index, name, strings.TrimPrefix(strings.TrimSpace(version), "=="), relativeIdentitySource(repo, path), "high")
		}
	}
}

func validPipfileIdentitySection(packages map[string]any) bool {
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
