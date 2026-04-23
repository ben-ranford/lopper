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
	if enabledFeatures := req.Features.EnabledCodes(); len(enabledFeatures) > 0 {
		baseKey["enabledFeatures"] = enabledFeatures
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
