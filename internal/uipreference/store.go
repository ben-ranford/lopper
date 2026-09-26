// Package uipreference stores personal consent for the Stave UI preview.
package uipreference

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	Stave  = "stave"
	Legacy = "legacy"
	Ask    = "ask"
)

type Storage interface {
	Load() (string, error)
	Save(string) error
	Clear() error
}

type Store struct{ ConfigDir func() (string, error) }

type document struct {
	Version int    `json:"version"`
	UI      string `json:"ui"`
}

func (s *Store) path() (string, error) {
	dir := s.ConfigDir
	if dir == nil {
		dir = os.UserConfigDir
	}
	root, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "lopper", "ui-preferences.json"), nil
}

func (s *Store) Load() (string, error) {
	path, err := s.path()
	if err != nil {
		return "", err
	}
	data, err := readPreference(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var value document
	if err := json.Unmarshal(data, &value); err != nil {
		return "", fmt.Errorf("invalid UI preference: %w", err)
	}
	if value.Version != 1 || (value.UI != Stave && value.UI != Legacy) {
		return "", fmt.Errorf("unsupported UI preference version or choice")
	}
	return value.UI, nil
}

// An atomic rename may replace the preference between lookup and open. A
// confined root allows reading either complete version without a pinned-identity
// check that would reject an otherwise valid concurrent save.
func readPreference(path string) (_ []byte, result error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { result = errors.Join(result, root.Close()) }()
	return root.ReadFile(filepath.Base(path))
}

func (s *Store) Save(choice string) error {
	if choice != Stave && choice != Legacy {
		return fmt.Errorf("invalid UI preference %q", choice)
	}
	path, err := s.path()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data := []byte("{\"version\":1,\"ui\":\"" + choice + "\"}\n")
	return writePreference(dir, filepath.Base(path), data)
}

func (s *Store) Clear() error {
	path, err := s.path()
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Publish a newly created private file instead of preserving permissions from a
// previous preference file. The rooted temp and rename keep concurrent readers
// on complete documents, and never follow an existing destination symlink.
func writePreference(dir, name string, data []byte) (result error) {
	root, err := safeio.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, root.Close()) }()
	return publishPreference(root, name, data)
}

func publishPreference(root safeio.Root, name string, data []byte) (result error) {
	temp, file, err := safeio.CreateTempFileWithinRoot(root, "", 0o600)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, safeio.CleanupTempFileWithinRoot(root, temp, file)) }()
	if n, err := file.Write(data); err != nil {
		return err
	} else if n != len(data) {
		return io.ErrShortWrite
	}
	if err := file.Close(); err != nil {
		return err
	}
	return root.Rename(temp, name)
}
