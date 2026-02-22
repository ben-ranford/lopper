package analysis

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

const analysisCacheSchemaVersion = "v1"

type cacheEntryDescriptor struct {
	KeyLabel    string
	KeyDigest   string
	InputDigest string
}

type cachePointer struct {
	InputDigest  string `json:"inputDigest"`
	ObjectDigest string `json:"objectDigest"`
}

type cachedPayload struct {
	Report report.Report `json:"report"`
}

type resolvedCacheOptions struct {
	Enabled  bool
	Path     string
	ReadOnly bool
}

type analysisCache struct {
	options   resolvedCacheOptions
	metadata  report.CacheMetadata
	warnings  []string
	cacheable bool
}

func newAnalysisCache(req Request, repoPath string) *analysisCache {
	options := resolveCacheOptions(req.Cache, repoPath)
	metadata := report.CacheMetadata{
		Enabled:  options.Enabled,
		Path:     options.Path,
		ReadOnly: options.ReadOnly,
	}
	cache := &analysisCache{
		options:  options,
		metadata: metadata,
		warnings: make([]string, 0),
	}
	if !options.Enabled {
		cache.cacheable = false
		return cache
	}
	if err := os.MkdirAll(filepath.Join(options.Path, "keys"), 0o750); err != nil {
		cache.cacheable = false
		cache.warn("analysis cache unavailable: " + err.Error())
		return cache
	}
	if err := os.MkdirAll(filepath.Join(options.Path, "objects"), 0o750); err != nil {
		cache.cacheable = false
		cache.warn("analysis cache unavailable: " + err.Error())
		return cache
	}
	cache.cacheable = true
	return cache
}

func resolveCacheOptions(req *CacheOptions, repoPath string) resolvedCacheOptions {
	options := resolvedCacheOptions{
		Enabled:  true,
		Path:     filepath.Join(repoPath, ".lopper-cache"),
		ReadOnly: false,
	}
	if req == nil {
		return options
	}
	options.Enabled = req.Enabled
	if strings.TrimSpace(req.Path) != "" {
		options.Path = strings.TrimSpace(req.Path)
	}
	options.ReadOnly = req.ReadOnly
	return options
}

func (c *analysisCache) warn(message string) {
	if strings.TrimSpace(message) == "" {
		return
	}
	c.warnings = append(c.warnings, message)
}

func (c *analysisCache) takeWarnings() []string {
	if len(c.warnings) == 0 {
		return nil
	}
	out := append([]string(nil), c.warnings...)
	c.warnings = c.warnings[:0]
	return out
}

func (c *analysisCache) metadataSnapshot() *report.CacheMetadata {
	if c == nil {
		return nil
	}
	snapshot := c.metadata
	if len(c.metadata.Invalidations) > 0 {
		snapshot.Invalidations = append([]report.CacheInvalidation(nil), c.metadata.Invalidations...)
	}
	return &snapshot
}

func (c *analysisCache) prepareEntry(req Request, adapterID, normalizedRoot string) (cacheEntryDescriptor, error) {
	if c == nil || !c.options.Enabled || !c.cacheable {
		return cacheEntryDescriptor{}, nil
	}
	adapterID = strings.TrimSpace(adapterID)
	normalizedRoot = filepath.Clean(normalizedRoot)
	baseKey := map[string]any{
		"schema":         analysisCacheSchemaVersion,
		"adapter":        adapterID,
		"root":           normalizedRoot,
		"dependency":     req.Dependency,
		"topN":           req.TopN,
		"runtimeProfile": req.RuntimeProfile,
		"configPath":     strings.TrimSpace(req.ConfigPath),
	}
	if req.MinUsagePercentForRecommendations != nil {
		baseKey["minUsagePercent"] = *req.MinUsagePercentForRecommendations
	}
	if req.RemovalCandidateWeights != nil {
		baseKey["weights"] = req.RemovalCandidateWeights
	}
	if req.LowConfidenceWarningPercent != nil {
		baseKey["lowConfidenceWarningPercent"] = *req.LowConfidenceWarningPercent
	}
	baseDigest, err := hashJSON(baseKey)
	if err != nil {
		return cacheEntryDescriptor{}, err
	}
	inputDigest, err := c.computeInputDigest(normalizedRoot, req.ConfigPath)
	if err != nil {
		return cacheEntryDescriptor{}, err
	}
	return cacheEntryDescriptor{
		KeyLabel:    adapterID + ":" + normalizedRoot,
		KeyDigest:   baseDigest,
		InputDigest: inputDigest,
	}, nil
}

func (c *analysisCache) lookup(entry cacheEntryDescriptor) (report.Report, bool, error) {
	if c == nil || !c.options.Enabled || !c.cacheable {
		return report.Report{}, false, nil
	}
	pointerPath := filepath.Join(c.options.Path, "keys", entry.KeyDigest+".json")
	pointerData, err := safeio.ReadFileUnder(c.options.Path, pointerPath)
	if err != nil {
		if os.IsNotExist(err) {
			c.metadata.Misses++
			return report.Report{}, false, nil
		}
		return report.Report{}, false, err
	}
	var pointer cachePointer
	if err = json.Unmarshal(pointerData, &pointer); err != nil {
		c.metadata.Misses++
		c.metadata.Invalidations = append(c.metadata.Invalidations, report.CacheInvalidation{Key: entry.KeyLabel, Reason: "pointer-corrupt"})
		return report.Report{}, false, nil
	}
	if pointer.InputDigest != entry.InputDigest {
		c.metadata.Misses++
		c.metadata.Invalidations = append(c.metadata.Invalidations, report.CacheInvalidation{Key: entry.KeyLabel, Reason: "input-changed"})
		return report.Report{}, false, nil
	}

	objectPath := filepath.Join(c.options.Path, "objects", pointer.ObjectDigest+".json")
	objectData, err := safeio.ReadFileUnder(c.options.Path, objectPath)
	if err != nil {
		c.metadata.Misses++
		reason := "object-read-error"
		if os.IsNotExist(err) {
			reason = "object-missing"
		}
		c.metadata.Invalidations = append(c.metadata.Invalidations, report.CacheInvalidation{Key: entry.KeyLabel, Reason: reason})
		return report.Report{}, false, nil
	}

	var payload cachedPayload
	if err = json.Unmarshal(objectData, &payload); err != nil {
		c.metadata.Misses++
		c.metadata.Invalidations = append(c.metadata.Invalidations, report.CacheInvalidation{Key: entry.KeyLabel, Reason: "object-corrupt"})
		return report.Report{}, false, nil
	}
	c.metadata.Hits++
	return payload.Report, true, nil
}

func (c *analysisCache) store(entry cacheEntryDescriptor, data report.Report) error {
	if c == nil || !c.options.Enabled || !c.cacheable || c.options.ReadOnly {
		return nil
	}
	payload := cachedPayload{Report: data}
	serializedPayload, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	objectDigest := sha256Hex(serializedPayload)
	objectPath := filepath.Join(c.options.Path, "objects", objectDigest+".json")
	if _, err := os.Stat(objectPath); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if err := writeFileAtomic(objectPath, serializedPayload); err != nil {
			return err
		}
	}

	pointer := cachePointer{InputDigest: entry.InputDigest, ObjectDigest: objectDigest}
	serializedPointer, err := json.Marshal(pointer)
	if err != nil {
		return err
	}
	pointerPath := filepath.Join(c.options.Path, "keys", entry.KeyDigest+".json")
	if err := writeFileAtomic(pointerPath, serializedPointer); err != nil {
		return err
	}
	c.metadata.Writes++
	return nil
}

func (c *analysisCache) computeInputDigest(rootPath, configPath string) (string, error) {
	rootPath = filepath.Clean(rootPath)
	records, err := c.collectRelevantFiles(rootPath)
	if err != nil {
		return "", err
	}

	if strings.TrimSpace(configPath) != "" {
		configDigest, digestErr := hashFileOrMissing(strings.TrimSpace(configPath))
		if digestErr != nil {
			return "", digestErr
		}
		records = append(records, "config\x00"+filepath.Clean(strings.TrimSpace(configPath))+"\x00"+configDigest)
	}

	sort.Strings(records)
	hasher := sha256.New()
	for _, record := range records {
		_, _ = io.WriteString(hasher, record)
		_, _ = io.WriteString(hasher, "\n")
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func (c *analysisCache) collectRelevantFiles(rootPath string) ([]string, error) {
	records := make([]string, 0, 128)
	err := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, walkErr error) error {
		return collectFileRecord(rootPath, path, d, walkErr, &records)
	})
	if err != nil {
		return nil, err
	}
	return records, nil
}

func collectFileRecord(rootPath, path string, d fs.DirEntry, walkErr error, records *[]string) error {
	if walkErr != nil {
		return walkErr
	}
	if path == rootPath {
		return nil
	}
	if shouldSkipDirEntry(d) {
		return filepath.SkipDir
	}
	if !shouldHashFile(path, d) {
		return nil
	}
	record, err := buildFileRecord(rootPath, path)
	if err != nil {
		return err
	}
	*records = append(*records, record)
	return nil
}

func shouldSkipDirEntry(d fs.DirEntry) bool {
	return d.IsDir() && shouldSkipCacheDir(d.Name())
}

func shouldHashFile(path string, d fs.DirEntry) bool {
	return d.Type().IsRegular() && isCacheRelevantFile(path)
}

func buildFileRecord(rootPath, path string) (string, error) {
	rel, err := filepath.Rel(rootPath, path)
	if err != nil {
		return "", err
	}
	digest, err := hashFile(path)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel) + "\x00" + digest, nil
}

func shouldSkipCacheDir(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == ".lopper-cache" {
		return true
	}
	return shared.ShouldSkipCommonDir(normalized)
}

func isCacheRelevantFile(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if lockOrConfigFile(base) {
		return true
	}
	ext := strings.ToLower(filepath.Ext(base))
	switch ext {
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".py", ".go", ".rs", ".php", ".java", ".kt", ".kts", ".cs", ".fs", ".fsx", ".c", ".cc", ".cpp", ".cxx", ".h", ".hpp":
		return true
	default:
		return false
	}
}

func lockOrConfigFile(base string) bool {
	switch base {
	case "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "package.json", "tsconfig.json", "composer.lock", "composer.json", "cargo.lock", "cargo.toml", "go.mod", "go.sum", "requirements.txt", "requirements-dev.txt", "pipfile", "pipfile.lock", "poetry.lock", "pyproject.toml", "pom.xml", "build.gradle", "build.gradle.kts", "gradle.lockfile", "settings.gradle", "settings.gradle.kts", "packages.lock.json", ".lopper.yml", ".lopper.yaml", "lopper.json":
		return true
	default:
		return false
	}
}

func hashFile(path string) (string, error) {
	data, err := safeio.ReadFile(path)
	if err != nil {
		return "", err
	}

	hasher := sha256.New()
	if _, err := hasher.Write(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func hashFileOrMissing(path string) (string, error) {
	digest, err := hashFile(path)
	if err == nil {
		return digest, nil
	}
	if os.IsNotExist(err) {
		return "missing", nil
	}
	return "", err
}

func hashJSON(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return sha256Hex(payload), nil
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmpFile, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if os.Rename(tmpPath, path) == nil {
		return nil
	}
	_ = os.Remove(tmpPath)
	// Windows cannot atomically rename over existing files; fall back to overwrite.
	return os.WriteFile(path, data, 0o600)
}
