package analysis

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"unicode/utf8"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/report/model"
	"github.com/ben-ranford/lopper/internal/safeio"
)

var afterMavenCacheInputValidated = func(string) {
	// Tests remove the POM after input validation to verify cached evidence survives;
	// production needs no action at this boundary.
}

const mavenCacheEnvelopeVersion = 1
const mavenCacheIdentityPolicy = "identity"

const mavenCachePointerLimit = 4 << 10
const mavenCacheObjectLimit = 128 << 20

type mavenCacheEnvelope struct {
	Version int                   `json:"version"`
	Policy  string                `json:"policy"`
	Entries *[]mavenCacheDocument `json:"entries"`
}
type mavenCacheDocument struct {
	Path         string                    `json:"path"`
	Properties   map[string]string         `json:"properties"`
	Dependencies []report.MavenDeclaration `json:"dependencies"`
	Managed      []report.MavenDeclaration `json:"managed"`
	Stage        string                    `json:"stage"`
	Kind         string                    `json:"kind"`
}

func newMavenCacheEnvelope(result report.Report) *mavenCacheEnvelope {
	if !result.MavenManifestCatalog {
		return nil
	}
	documents := make([]mavenCacheDocument, 0, len(result.MavenManifests))
	envelope := &mavenCacheEnvelope{Version: mavenCacheEnvelopeVersion, Policy: mavenCacheIdentityPolicy, Entries: &documents}
	for _, entry := range result.MavenManifests {
		stage, kind := entry.Failure()
		properties := entry.Properties()
		if properties == nil {
			properties = map[string]string{}
		}
		dependencies := append([]report.MavenDeclaration{}, entry.Dependencies()...)
		managed := append([]report.MavenDeclaration{}, entry.ManagedDependencies()...)
		documents = append(documents, mavenCacheDocument{entry.Path(), properties, dependencies, managed, stage, kind})
	}
	return envelope
}
func (e *mavenCacheEnvelope) restore() ([]report.MavenManifest, error) {
	if e == nil || e.Version != mavenCacheEnvelopeVersion || e.Policy != mavenCacheIdentityPolicy || e.Entries == nil || len(*e.Entries) > model.MavenAdapterEntryLimit {
		return nil, errors.New("invalid Maven cache envelope")
	}
	entries := make([]report.MavenManifest, 0, len(*e.Entries))
	seen := make(map[string]report.MavenManifest)
	for _, item := range *e.Entries {
		if !utf8.ValidString(item.Path) {
			return nil, errors.New("unrepresentable Maven cache path")
		}
		entry, err := model.NewMavenManifest(item.Path, item.Properties, item.Dependencies, item.Managed, item.Stage, item.Kind)
		if err != nil {
			return nil, err
		}
		if previous, ok := seen[entry.Path()]; ok {
			if !previous.Equal(entry) {
				return nil, errors.New("conflicting Maven cache entries")
			}
			continue
		}
		seen[entry.Path()] = entry
		entries = append(entries, entry)
	}
	if _, err := model.MavenEvidenceSize(entries); err != nil {
		return nil, err
	}
	return entries, nil
}
func mavenCacheableReport(result report.Report) error {
	if !result.MavenManifestCatalog {
		return errors.New("missing Maven cache evidence")
	}
	for _, entry := range result.MavenManifests {
		if !utf8.ValidString(entry.Path()) {
			return errors.New("maven cache cannot represent filename encoding")
		}
	}
	_, err := model.MavenEvidenceSize(result.MavenManifests)
	return err
}
func readMavenCachePointer(root, path string) (cachePointer, error) {
	data, err := safeio.ReadFileUnderLimit(root, path, mavenCachePointerLimit)
	if err != nil {
		return cachePointer{}, err
	}
	if err := preflightMavenJSON(data, mavenPointerJSON); err != nil {
		return cachePointer{}, err
	}
	var pointer cachePointer
	if err := json.Unmarshal(data, &pointer); err != nil {
		return cachePointer{}, err
	}
	if !validMavenObjectDigest(pointer.ObjectDigest) {
		return cachePointer{}, errors.New("invalid cache object digest")
	}
	return pointer, nil
}
func validMavenObjectDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, ch := range value {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}
func readMavenCachedPayload(root, digest string) (cachedPayload, string, error) {
	if !validMavenObjectDigest(digest) {
		return cachedPayload{}, cacheObjectCorruptReason, nil
	}
	data, err := safeio.ReadFileUnderLimit(root, filepath.Join(root, "objects", digest+".json"), mavenCacheObjectLimit)
	if err != nil {
		return cachedPayload{}, "object-read-error", nil
	}
	if sha256Hex(data) != digest || preflightMavenJSON(data, mavenPayloadJSON) != nil {
		return cachedPayload{}, cacheObjectCorruptReason, nil
	}
	var payload cachedPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return cachedPayload{}, cacheObjectCorruptReason, nil
	}
	entries, err := payload.Maven.restore()
	if err != nil || !payload.restoreUsageIncomplete() || !payload.restoreSuppressedUnusedImports() {
		return cachedPayload{}, cacheObjectCorruptReason, nil
	}
	payload.Report.MavenManifests = entries
	payload.Report.MavenManifestCatalog = true
	payload.Maven = nil
	return payload, "", nil
}
func (c *analysisCache) mavenCachePayloadBytes(payload cachedPayload) ([]byte, error) {
	// Maven evidence is validated separately. This post-Marshal check limits
	// published bytes, not ordinary Report encoding or DTO-copy allocations.
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(data) > c.mavenPublicationBudget() {
		return nil, errors.New("maven cache object exceeds admission limit")
	}
	return data, nil
}
func mavenJSONTrailing(decoder *json.Decoder) error {
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing Maven cache JSON")
	}
	return nil
}
func (c *analysisCache) readCachePointer(entry cacheEntryDescriptor, path string) (cachePointer, string, error) {
	if entry.AdapterID == "jvm" {
		pointer, err := readMavenCachePointer(c.options.Path, path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return cachePointer{}, "missing", nil
			}
			return cachePointer{}, "pointer-corrupt", nil
		}
		return pointer, "", nil
	}
	data, err := safeio.ReadFileUnder(c.options.Path, path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cachePointer{}, "missing", nil
		}
		return cachePointer{}, "", err
	}
	var pointer cachePointer
	if err := json.Unmarshal(data, &pointer); err != nil {
		return cachePointer{}, "pointer-corrupt", nil
	}
	return pointer, "", nil
}
func (c *analysisCache) serializeCachedPayload(entry cacheEntryDescriptor, payload cachedPayload) ([]byte, error) {
	if entry.AdapterID == "jvm" {
		return c.mavenCachePayloadBytes(payload)
	}
	return json.Marshal(payload)
}

func (c *analysisCache) mavenPublicationBudget() int {
	if c.mavenPublicationLimit > 0 {
		return min(c.mavenPublicationLimit, mavenCacheObjectLimit)
	}
	return mavenCacheObjectLimit
}
