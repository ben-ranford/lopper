package analysis

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

const analysisCacheSchemaVersion = "v1"

type cacheEntryDescriptor struct {
	KeyLabel    string
	KeyDigest   string
	InputDigest string
}

type cacheDigestInput struct {
	sortKey      string
	path         string
	allowMissing bool
}

type cacheInputDigestMemoKey struct {
	normalizedRoot          string
	cleanConfigIdentityPath string
	cleanConfigContentPath  string
}

func (c *analysisCache) prepareEntry(req Request, adapterID, normalizedRoot string) (cacheEntryDescriptor, error) {
	if c == nil || !c.options.Enabled || !c.cacheable {
		return cacheEntryDescriptor{}, nil
	}
	adapterID = strings.TrimSpace(adapterID)
	normalizedRoot = filepath.Clean(normalizedRoot)
	stableRoot := c.stableCacheRoot(normalizedRoot)
	baseKey := map[string]any{
		"schema":         analysisCacheSchemaVersion,
		"adapter":        adapterID,
		"root":           stableRoot,
		"dependency":     req.Dependency,
		"language":       normalizeCacheLanguage(req.Language),
		"topN":           req.TopN,
		"suggestOnly":    req.SuggestOnly,
		"runtimeProfile": req.RuntimeProfile,
		"configPath":     stableConfigCachePath(req),
	}
	if command := strings.TrimSpace(req.RuntimeTestCommand); command != "" {
		baseKey["runtimeTestCommand"] = command
		baseKey["runtimeTracePathExplicit"] = req.RuntimeTracePathExplicit
		if tracePath := stableRuntimeTraceCachePath(req); tracePath != "" {
			baseKey["runtimeTracePath"] = tracePath
		}
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
	if featureSnapshot := req.Features.Snapshot(); len(featureSnapshot) > 0 {
		baseKey["features"] = featureSnapshot
	}
	if len(req.LicenseDenyList) > 0 {
		baseKey["licenseDeny"] = req.LicenseDenyList
	}
	if scopeIdentity := normalizedScopeCacheIdentity(req); scopeIdentity != nil {
		baseKey["pathScope"] = scopeIdentity
	}
	baseKey["includeRegistryProvenance"] = req.IncludeRegistryProvenance
	baseDigest, err := hashJSON(baseKey)
	if err != nil {
		return cacheEntryDescriptor{}, err
	}
	inputDigest, err := c.memoizedInputDigest(normalizedRoot, stableConfigCachePath(req), req.ConfigPath)
	if err != nil {
		return cacheEntryDescriptor{}, err
	}
	return cacheEntryDescriptor{
		KeyLabel:    adapterID + ":" + stableRoot,
		KeyDigest:   baseDigest,
		InputDigest: inputDigest,
	}, nil
}

func (c *analysisCache) memoizedInputDigest(rootPath, configIdentityPath, configContentPath string) (string, error) {
	if c.inputDigestMemo == nil {
		c.inputDigestMemo = make(map[cacheInputDigestMemoKey]string)
	}
	memoKey := cacheInputDigestMemoKey{
		normalizedRoot:          filepath.Clean(rootPath),
		cleanConfigIdentityPath: cleanConfigPath(configIdentityPath),
		cleanConfigContentPath:  cleanConfigPath(configContentPath),
	}
	if digest, ok := c.inputDigestMemo[memoKey]; ok {
		return digest, nil
	}
	digest, err := c.computeInputDigestWithConfigIdentity(memoKey.normalizedRoot, memoKey.cleanConfigIdentityPath, memoKey.cleanConfigContentPath)
	if err != nil {
		return "", err
	}
	c.inputDigestMemo[memoKey] = digest
	return digest, nil
}

func (c *analysisCache) computeInputDigest(rootPath, configPath string) (string, error) {
	return c.computeInputDigestWithConfigIdentity(rootPath, configPath, configPath)
}

func (c *analysisCache) computeInputDigestWithConfigIdentity(rootPath, configIdentityPath, configContentPath string) (string, error) {
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

	cleanedConfigIdentityPath := cleanConfigPath(configIdentityPath)
	cleanedConfigContentPath := cleanConfigPath(configContentPath)
	if cleanedConfigContentPath != "" {
		inputs = append(inputs, cacheDigestInput{
			sortKey:      "config\x00" + cleanedConfigIdentityPath,
			path:         cleanedConfigContentPath,
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

func cleanConfigPath(configPath string) string {
	if strings.TrimSpace(configPath) == "" {
		return ""
	}
	return filepath.Clean(strings.TrimSpace(configPath))
}

func cleanRuntimeTracePath(tracePath string) string {
	if strings.TrimSpace(tracePath) == "" {
		return ""
	}
	return filepath.Clean(strings.TrimSpace(tracePath))
}

func stableConfigCachePath(req Request) string {
	if strings.TrimSpace(req.ConfigCachePath) != "" {
		return cleanConfigPath(req.ConfigCachePath)
	}
	return cleanConfigPath(req.ConfigPath)
}

func stableRuntimeTraceCachePath(req Request) string {
	if strings.TrimSpace(req.RuntimeTraceCachePath) != "" {
		return cleanRuntimeTracePath(req.RuntimeTraceCachePath)
	}
	return cleanRuntimeTracePath(req.RuntimeTracePath)
}

func normalizeCacheLanguage(languageID string) string {
	return strings.ToLower(strings.TrimSpace(languageID))
}

type scopeCacheIdentity struct {
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

func normalizedScopeCacheIdentity(req Request) *scopeCacheIdentity {
	identity := &scopeCacheIdentity{
		Include: normalizePatterns(req.IncludePatterns),
		Exclude: normalizePatterns(req.ExcludePatterns),
	}
	if len(identity.Include) == 0 && len(identity.Exclude) == 0 {
		return nil
	}
	return identity
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
