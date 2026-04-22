package featureflags

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

func ParseReleaseLocks(data []byte) ([]ReleaseLock, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var locks []ReleaseLock
	if err := decoder.Decode(&locks); err != nil {
		return nil, fmt.Errorf("invalid release locks JSON: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, fmt.Errorf("invalid release locks JSON: multiple JSON values")
	}

	seen := map[string]struct{}{}
	for index := range locks {
		lock := &locks[index]
		lock.Release = strings.TrimSpace(lock.Release)
		lock.DefaultOn = normalizeRefs(lock.DefaultOn)
		lock.Notes = normalizeNotes(lock.Notes)
		if lock.Release == "" {
			return nil, fmt.Errorf("release lock %d: release is required", index)
		}
		key := normalizeReleaseKey(lock.Release)
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate release lock: %s", lock.Release)
		}
		seen[key] = struct{}{}
	}
	return locks, nil
}

func DefaultReleaseLock(release string) (*ReleaseLock, error) {
	locks, err := ParseReleaseLocks(embeddedReleaseLocks)
	if err != nil {
		return nil, err
	}
	wanted := normalizeReleaseKey(release)
	if wanted == "" {
		return nil, nil
	}
	for _, lock := range locks {
		if normalizeReleaseKey(lock.Release) == wanted {
			copied := copyReleaseLock(lock)
			if err := DefaultRegistry().ValidateReleaseLock(&copied); err != nil {
				return nil, err
			}
			return &copied, nil
		}
	}
	return nil, nil
}

func ValidateDefaultReleaseLocks() error {
	locks, err := ParseReleaseLocks(embeddedReleaseLocks)
	if err != nil {
		return err
	}
	registry := DefaultRegistry()
	for index := range locks {
		if err := registry.ValidateReleaseLock(&locks[index]); err != nil {
			return fmt.Errorf("release lock %s: %w", locks[index].Release, err)
		}
	}
	return nil
}

func copyReleaseLock(lock ReleaseLock) ReleaseLock {
	copied := ReleaseLock{
		Release:   lock.Release,
		DefaultOn: append([]string{}, lock.DefaultOn...),
	}
	if len(lock.Notes) > 0 {
		copied.Notes = make(map[string]string, len(lock.Notes))
		for key, value := range lock.Notes {
			copied.Notes[key] = value
		}
	}
	return copied
}

func normalizeReleaseKey(release string) string {
	release = strings.ToLower(strings.TrimSpace(release))
	return strings.TrimPrefix(release, "v")
}
