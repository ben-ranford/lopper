package analysis

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func writeFileDigest(w io.Writer, path string) error {
	digest, err := hashFileDigest(path)
	if err != nil {
		return err
	}
	return writeHexDigest(w, digest)
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
