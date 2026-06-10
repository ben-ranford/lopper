package php

import (
	"regexp"
	"strings"

	"github.com/ben-ranford/lopper/internal/language"
)

type Adapter struct {
	language.AdapterLifecycle
}

func NewAdapter() *Adapter {
	adapter := &Adapter{}
	adapter.AdapterLifecycle = language.NewAdapterLifecycle("php", []string{"php8", "php7"}, adapter.DetectWithConfidence)
	return adapter
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
