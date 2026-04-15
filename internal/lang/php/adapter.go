package php

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
)

type Adapter struct {
	language.AdapterLifecycle
}

const (
	composerJSONName = "composer.json"
	composerLockName = "composer.lock"
	maxDetectFiles   = 1024
	maxScanFiles     = 2048
)

func NewAdapter() *Adapter {
	adapter := &Adapter{}
	adapter.AdapterLifecycle = language.NewAdapterLifecycle("php", []string{"php8", "php7"}, adapter.DetectWithConfidence)
	return adapter
}

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	repoPath = shared.DefaultRepoPath(repoPath)
	detection := language.Detection{}
	roots := make(map[string]struct{})

	if err := applyPHPRootSignals(repoPath, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	visited := 0
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return walkPHPDetectionEntry(path, entry, roots, &detection, &visited, maxDetectFiles)
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func applyPHPRootSignals(repoPath string, detection *language.Detection, roots map[string]struct{}) error {
	signals := []struct {
		name       string
		confidence int
	}{
		{name: composerJSONName, confidence: 60},
		{name: composerLockName, confidence: 30},
	}
	for _, signal := range signals {
		candidate := filepath.Join(repoPath, signal.name)
		if _, err := os.Stat(candidate); err == nil {
			detection.Matched = true
			detection.Confidence += signal.confidence
			roots[repoPath] = struct{}{}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func walkPHPDetectionEntry(path string, entry fs.DirEntry, roots map[string]struct{}, detection *language.Detection, visited *int, maxFiles int) error {
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}

	*visited++
	if *visited > maxFiles {
		return fs.SkipAll
	}

	switch strings.ToLower(entry.Name()) {
	case composerJSONName, composerLockName:
		detection.Matched = true
		detection.Confidence += 12
		roots[filepath.Dir(path)] = struct{}{}
	}

	if strings.EqualFold(filepath.Ext(path), ".php") {
		detection.Matched = true
		detection.Confidence += 2
	}
	return nil
}

func normalizeDependencyID(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	return strings.ReplaceAll(value, "_", "-")
}

func normalizePackagePart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, "_", "-")
	value = strings.ReplaceAll(value, ".", "-")
	parts := make([]rune, 0, len(value)+4)
	for i, r := range value {
		if i > 0 && r >= 'A' && r <= 'Z' && parts[len(parts)-1] != '-' {
			parts = append(parts, '-')
		}
		parts = append(parts, r)
	}
	cleaned := strings.ToLower(string(parts))
	cleaned = strings.Trim(cleaned, "-")
	cleaned = regexp.MustCompile(`-+`).ReplaceAllString(cleaned, "-")
	return cleaned
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".idea", "node_modules", "vendor", "dist", "build", ".next", ".turbo", "coverage", "tmp", "cache":
		return true
	default:
		return false
	}
}
