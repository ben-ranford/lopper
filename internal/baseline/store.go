package baseline

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func SnapshotPath(dir, key string) string {
	return filepath.Join(strings.TrimSpace(dir), SanitizeKey(strings.TrimSpace(key))+".json")
}

func SaveJSON[T any](dir, key string, existsErr error, build func(trimmedKey string) T) (path string, err error) {
	trimmedDir, trimmedKey, err := validateInputs(dir, key)
	if err != nil {
		return "", err
	}
	if err := rejectStoreDirSymlink(trimmedDir); err != nil {
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

func rejectStoreDirSymlink(dir string) error {
	if info, err := os.Lstat(dir); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("baseline store directory must not be a symlink: %s", dir)
	}
	return nil
}

func openSnapshotWriter(dir, key string, existsErr error) (*os.Root, *os.File, string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, nil, "", err
	}

	sanitizedFileName := SanitizeKey(key) + ".json"
	snapshotPath := filepath.Join(dir, sanitizedFileName)

	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, "", err
	}

	file, err := root.OpenFile(sanitizedFileName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		err = errors.Join(err, root.Close())
		if errors.Is(err, os.ErrExist) && existsErr != nil {
			err = fmt.Errorf("%w: key %q (%s)", existsErr, key, snapshotPath)
		}
		return nil, nil, "", err
	}

	return root, file, snapshotPath, nil
}
