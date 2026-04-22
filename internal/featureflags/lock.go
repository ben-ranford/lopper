package featureflags

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type ReleaseLock struct {
	Release   string            `json:"release"`
	DefaultOn []string          `json:"defaultOn"`
	Notes     map[string]string `json:"notes,omitempty"`
}

func ParseReleaseLock(data []byte) (*ReleaseLock, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var lock ReleaseLock
	if err := decoder.Decode(&lock); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, fmt.Errorf("invalid JSON: multiple JSON values")
	}
	lock.Release = strings.TrimSpace(lock.Release)
	lock.DefaultOn = normalizeRefs(lock.DefaultOn)
	lock.Notes = normalizeNotes(lock.Notes)
	if lock.Release == "" {
		return nil, fmt.Errorf("release is required")
	}
	return &lock, nil
}

func (r *Registry) ValidateReleaseLock(lock *ReleaseLock) error {
	if lock == nil {
		return nil
	}
	if strings.TrimSpace(lock.Release) == "" {
		return fmt.Errorf("release is required")
	}
	seen := map[string]struct{}{}
	for _, ref := range lock.DefaultOn {
		flag, ok := r.Lookup(ref)
		if !ok {
			return fmt.Errorf("unknown feature in release lock: %s", ref)
		}
		if _, exists := seen[flag.Code]; exists {
			return fmt.Errorf("duplicate feature in release lock: %s", ref)
		}
		seen[flag.Code] = struct{}{}
	}
	for ref := range lock.Notes {
		if _, ok := r.Lookup(ref); !ok {
			return fmt.Errorf("unknown feature note in release lock: %s", ref)
		}
	}
	return nil
}

func normalizeRefs(refs []string) []string {
	normalized := make([]string, 0, len(refs))
	for _, ref := range refs {
		trimmed := strings.TrimSpace(ref)
		if trimmed != "" {
			normalized = append(normalized, trimmed)
		}
	}
	return normalized
}

func normalizeNotes(notes map[string]string) map[string]string {
	if len(notes) == 0 {
		return nil
	}
	normalized := make(map[string]string, len(notes))
	for key, value := range notes {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		normalized[trimmedKey] = strings.TrimSpace(value)
	}
	return normalized
}
