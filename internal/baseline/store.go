package baseline

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	maxSnapshotSlugLength = 80
	MaxSnapshotBytes      = 64 * 1024 * 1024
)

var ErrSnapshotTooLarge = errors.New("baseline snapshot exceeds size limit")

func SnapshotPath(dir, key string) string {
	return filepath.Join(strings.TrimSpace(dir), SnapshotFileName(key))
}

func SnapshotFileName(key string) string {
	trimmedKey := strings.TrimSpace(key)
	slug := SanitizeKey(trimmedKey)
	if len(slug) > maxSnapshotSlugLength {
		slug = strings.TrimRight(slug[:maxSnapshotSlugLength], "._-")
	}
	digest := sha256.Sum256([]byte(trimmedKey))
	return fmt.Sprintf("%s--%x.json", slug, digest)
}

func LegacySnapshotPath(dir, key string) string {
	return filepath.Join(strings.TrimSpace(dir), LegacySnapshotFileName(key))
}

func LegacySnapshotFileName(key string) string {
	return SanitizeKey(strings.TrimSpace(key)) + ".json"
}

func ResolveSnapshotPath(dir, key string) string {
	path := SnapshotPath(dir, key)
	if _, err := os.Lstat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
		return path
	}
	return LegacySnapshotPath(dir, key)
}

func SaveJSON[T any](dir, key string, existsErr error, build func(trimmedKey string) T) (path string, err error) {
	trimmedDir, trimmedKey, err := validateInputs(dir, key)
	if err != nil {
		return "", err
	}
	if err := ValidateStorePath(trimmedDir); err != nil {
		return "", err
	}
	if err := rejectMatchingLegacySnapshot(trimmedDir, trimmedKey, existsErr); err != nil {
		return "", err
	}

	root, file, snapshotPath, err := openSnapshotWriter(trimmedDir, trimmedKey, existsErr)
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			if removeErr := os.Remove(snapshotPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, removeErr)
			}
		}
		err = errors.Join(err, file.Close(), root.Close())
	}()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(build(trimmedKey)); err != nil {
		return "", err
	}

	return snapshotPath, nil
}

func SanitizeKey(key string) string {
	if key == "" {
		return "baseline"
	}
	var b strings.Builder
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	sanitized := strings.Trim(b.String(), "._-")
	if sanitized == "" {
		return "baseline"
	}
	return sanitized
}

func validateInputs(dir, key string) (string, string, error) {
	trimmedDir := strings.TrimSpace(dir)
	if trimmedDir == "" {
		return "", "", fmt.Errorf("baseline store directory is required")
	}

	trimmedKey := strings.TrimSpace(key)
	if trimmedKey == "" {
		return "", "", fmt.Errorf("baseline key is required")
	}

	return trimmedDir, trimmedKey, nil
}

func ValidateStorePath(dir string) error {
	_, err := resolveStorePath(dir)
	return err
}

func resolveStorePath(dir string) (string, error) {
	trimmedDir := strings.TrimSpace(dir)
	if trimmedDir == "" {
		return "", fmt.Errorf("baseline store directory is required")
	}
	absDir, err := filepath.Abs(trimmedDir)
	if err != nil {
		return "", fmt.Errorf("resolve baseline store directory: %w", err)
	}

	paths := make([]string, 0, 8)
	for current := filepath.Clean(absDir); ; current = filepath.Dir(current) {
		paths = append(paths, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	for index := len(paths) - 1; index >= 0; index-- {
		path := paths[index]
		info, statErr := os.Lstat(path)
		if errors.Is(statErr, os.ErrNotExist) {
			return absDir, nil
		}
		if statErr != nil {
			return "", fmt.Errorf("inspect baseline store path %s: %w", path, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if isAllowedSystemRootSymlink(path) {
				continue
			}
			return "", fmt.Errorf("baseline store path component must not be a symlink: %s", path)
		}
	}
	return absDir, nil
}

func openSnapshotWriter(dir, key string, existsErr error) (*os.Root, *os.File, string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, nil, "", err
	}
	if err := ValidateStorePath(dir); err != nil {
		return nil, nil, "", err
	}

	fileName := SnapshotFileName(key)
	snapshotPath := filepath.Join(dir, fileName)

	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, "", err
	}

	file, err := root.OpenFile(fileName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		err = errors.Join(err, root.Close())
		if errors.Is(err, os.ErrExist) && existsErr != nil {
			err = fmt.Errorf("%w: key %q (%s)", existsErr, key, snapshotPath)
		}
		return nil, nil, "", err
	}

	return root, file, snapshotPath, nil
}

func rejectMatchingLegacySnapshot(dir, key string, existsErr error) error {
	legacyName := LegacySnapshotFileName(key)
	data, err := ReadStoreEntry(dir, legacyName, MaxSnapshotBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var identity struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(data, &identity); err != nil || strings.TrimSpace(identity.Key) != key {
		return nil
	}
	if existsErr == nil {
		return fmt.Errorf("baseline snapshot already exists: key %q (%s)", key, filepath.Join(dir, legacyName))
	}
	return fmt.Errorf("%w: key %q (%s)", existsErr, key, filepath.Join(dir, legacyName))
}

func ListStoreEntries(dir string) ([]string, error) {
	absDir, err := resolveStorePath(dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(absDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list baseline store: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

func ReadStoreEntry(dir, name string, maxBytes int64) (data []byte, err error) {
	absDir, err := resolveStorePath(dir)
	if err != nil {
		return nil, err
	}
	if err := validateStoreEntryName(name); err != nil {
		return nil, err
	}
	path := filepath.Join(absDir, name)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("baseline snapshot must not be a symlink: %s", path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("baseline snapshot must be a regular file: %s", path)
	}
	data, err = safeio.ReadFileUnderLimit(absDir, path, maxBytes)
	if errors.Is(err, safeio.ErrFileTooLarge) {
		return nil, fmt.Errorf("%w: %s", ErrSnapshotTooLarge, path)
	}
	return data, err
}

func validateStoreEntryName(name string) error {
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" || trimmedName == "." || filepath.Base(trimmedName) != trimmedName {
		return fmt.Errorf("invalid baseline store entry name: %q", name)
	}
	return nil
}
