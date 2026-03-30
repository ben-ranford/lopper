package analysis

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
)

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
	if len(req.LicenseDenyList) > 0 {
		baseKey["licenseDeny"] = req.LicenseDenyList
	}
	baseKey["includeRegistryProvenance"] = req.IncludeRegistryProvenance
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
	files, err := c.collectRelevantFiles(rootPath)
	if err != nil {
		return "", err
	}

	inputs := make([]cacheDigestInput, 0, len(files)+1)
	for _, file := range files {
		inputs = append(inputs, cacheDigestInput{
			sortKey: file.relativePath,
			path:    file.absolutePath,
		})
	}

	if strings.TrimSpace(configPath) != "" {
		cleanConfigPath := filepath.Clean(strings.TrimSpace(configPath))
		inputs = append(inputs, cacheDigestInput{
			sortKey:      "config\x00" + cleanConfigPath,
			path:         cleanConfigPath,
			allowMissing: true,
		})
	}

	sort.Slice(inputs, func(i, j int) bool {
		return inputs[i].sortKey < inputs[j].sortKey
	})
	hasher := sha256.New()
	for _, input := range inputs {
		if err := writeInputDigestRecord(hasher, input); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func writeInputDigestRecord(w io.Writer, input cacheDigestInput) error {
	if _, err := io.WriteString(w, input.sortKey); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "\x00"); err != nil {
		return err
	}
	if input.allowMissing {
		if err := writeFileDigestOrMissing(w, input.path); err != nil {
			return err
		}
	} else if err := writeFileDigest(w, input.path); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "\n"); err != nil {
		return err
	}
	return nil
}

func (c *analysisCache) collectRelevantFiles(rootPath string) ([]cacheRelevantFile, error) {
	files := make([]cacheRelevantFile, 0, 128)
	err := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, walkErr error) error {
		return collectRelevantFile(rootPath, path, d, walkErr, &files)
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func collectRelevantFile(rootPath, path string, d fs.DirEntry, walkErr error, files *[]cacheRelevantFile) error {
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
	record, err := buildRelevantFile(rootPath, path)
	if err != nil {
		return err
	}
	*files = append(*files, record)
	return nil
}

func shouldSkipDirEntry(d fs.DirEntry) bool {
	return d.IsDir() && shouldSkipCacheDir(d.Name())
}

func shouldHashFile(path string, d fs.DirEntry) bool {
	return d.Type().IsRegular() && isCacheRelevantFile(path)
}

func buildRelevantFile(rootPath, path string) (cacheRelevantFile, error) {
	rel, err := filepath.Rel(rootPath, path)
	if err != nil {
		return cacheRelevantFile{}, err
	}
	return cacheRelevantFile{
		absolutePath: path,
		relativePath: filepath.ToSlash(rel),
	}, nil
}

func writeFileDigest(w io.Writer, path string) error {
	digest, err := hashFileDigest(path)
	if err != nil {
		return err
	}
	return writeHexDigest(w, digest)
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
	if shared.IsGradleVersionCatalogFile(base) {
		return true
	}
	switch base {
	case "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "package.json", "tsconfig.json", "composer.lock", "composer.json", "cargo.lock", "cargo.toml", "go.mod", "go.sum", "requirements.txt", "requirements-dev.txt", "pipfile", "pipfile.lock", "poetry.lock", "pyproject.toml", "pom.xml", "build.gradle", "build.gradle.kts", "gradle.lockfile", "settings.gradle", "settings.gradle.kts", "packages.lock.json", ".lopper.yml", ".lopper.yaml", "lopper.json":
		return true
	default:
		return false
	}
}

func writeFileDigestOrMissing(w io.Writer, path string) error {
	digest, err := hashFileDigest(path)
	if err == nil {
		return writeHexDigest(w, digest)
	}
	if os.IsNotExist(err) {
		_, writeErr := io.WriteString(w, "missing")
		return writeErr
	}
	return err
}

func hashFileDigest(path string) (digest [sha256.Size]byte, err error) {
	file, err := safeio.OpenFile(path)
	if err != nil {
		return digest, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	hasher := sha256.New()
	var copyBuffer [32 * 1024]byte
	if _, err := io.CopyBuffer(hasher, file, copyBuffer[:]); err != nil {
		return digest, err
	}
	hasher.Sum(digest[:0])
	return digest, nil
}

func hashFile(path string) (string, error) {
	digest, err := hashFileDigest(path)
	if err != nil {
		return "", err
	}
	return encodeHexDigest(digest), nil
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

func encodeHexDigest(digest [sha256.Size]byte) string {
	return hex.EncodeToString(digest[:])
}

func writeHexDigest(w io.Writer, digest [sha256.Size]byte) error {
	var encoded [sha256.Size * 2]byte
	hex.Encode(encoded[:], digest[:])
	_, err := w.Write(encoded[:])
	return err
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
		return errors.Join(err, cleanupTempFile(tmpFile, tmpPath))
	}
	if err := tmpFile.Close(); err != nil {
		return errors.Join(err, removeIfPresent(tmpPath))
	}
	renameErr := os.Rename(tmpPath, path)
	if renameErr == nil {
		return nil
	}
	if err := removeIfPresent(tmpPath); err != nil {
		return err
	}
	// Windows cannot atomically rename over existing files; fall back to overwrite.
	return os.WriteFile(path, data, 0o600)
}

func cleanupTempFile(tmpFile *os.File, tmpPath string) error {
	return errors.Join(closeIfPresent(tmpFile), removeIfPresent(tmpPath))
}

func closeIfPresent(tmpFile *os.File) error {
	if tmpFile == nil {
		return nil
	}
	if err := tmpFile.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		return err
	}
	return nil
}

func removeIfPresent(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
