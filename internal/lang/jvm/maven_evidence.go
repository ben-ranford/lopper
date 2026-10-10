package jvm

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report/model"
	"github.com/ben-ranford/lopper/internal/safeio"
)

type mavenManifestCatalog struct {
	entries []model.MavenManifest
	err     error
	decode  func([]byte) (shared.ParsedPOM, error)
}

func newMavenManifestCatalog() *mavenManifestCatalog {
	return &mavenManifestCatalog{decode: shared.DecodePOM}
}
func firstMavenCatalog(values []*mavenManifestCatalog) *mavenManifestCatalog {
	if len(values) == 0 {
		return nil
	}
	return values[0]
}
func (c *mavenManifestCatalog) retain(path string, parsed shared.ParsedPOM, stage string, err error) {
	if c == nil || c.err != nil {
		return
	}
	remainingBytes, remainingValues, budgetErr := c.remainingBudget()
	if budgetErr != nil {
		c.err = budgetErr
		return
	}
	view := shared.POMConsumerView{}
	kind := ""
	if err == nil {
		view = parsed.IdentityPolicy()
	} else {
		kind = mavenFailureKind(stage, err)
	}
	entry, entryErr := model.NewMavenManifestWithinBudget(path, view.Properties, view.Dependencies, view.ManagedDependencies, stage, kind, remainingBytes, remainingValues)
	if entryErr != nil {
		c.err = entryErr
		return
	}
	c.entries = append(c.entries, entry)
}
func (c *mavenManifestCatalog) remainingBudget() (int, int, error) {
	if len(c.entries) >= model.MavenAdapterEntryLimit {
		return 0, 0, model.ErrMavenEvidenceLimit
	}
	size, err := model.MavenEvidenceSize(c.entries)
	if err != nil {
		return 0, 0, err
	}
	if len(c.entries) > 0 {
		size++
	}
	values := model.MavenEvidenceValueLimit
	for _, previous := range c.entries {
		values -= previous.Values()
	}
	return model.MavenEvidenceByteLimit - size, values, nil
}

func mavenFailureKind(stage string, err error) string {
	if stage == "parse" {
		return "xml"
	}
	switch {
	case errors.Is(err, fs.ErrPermission):
		return "permission"
	case errors.Is(err, fs.ErrNotExist):
		return "missing"
	case errors.Is(err, safeio.ErrFileTooLarge):
		return "large"
	default:
		return "io"
	}
}
func (c *mavenManifestCatalog) readFailure(repo, path string, err error) {
	if c == nil || !strings.EqualFold(filepath.Base(path), pomXMLName) {
		return
	}
	c.retain(relativeBuildFilePath(repo, path), shared.ParsedPOM{}, "read", err)
}
