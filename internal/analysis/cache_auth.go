package analysis

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	analysisCacheAuthDirName          = "analysis-cache-auth"
	analysisCacheAuthKeyLength        = 32
	analysisCacheAuthKeyMaxBytes      = analysisCacheAuthKeyLength*2 + 8
	analysisCacheAuthKeyPerm          = 0o600
	analysisCacheAuthSchemaV1         = "v1"
	analysisCacheAuthLegacyLockSuffix = ".init-lock"
	analysisCacheAuthRotateTag        = ".rotate-"
	analysisCacheAuthRetryLimit       = 200
	analysisCacheAuthRetryDelay       = 5 * time.Millisecond
)

var (
	analysisCacheUserCacheDirFn = os.UserCacheDir
	analysisCacheAuthSyncDirFn  = func(root *safeio.WriteRoot) error {
		return root.Sync()
	}
)

var (
	errAnalysisCacheAuthKeyMissing = errors.New("cache auth key missing")
	errAnalysisCacheAuthKeyInvalid = errors.New("cache auth key invalid")
	errAnalysisCacheAuthKeyChanged = errors.New("cache auth key changed")
)

func (c *analysisCache) resolveAuthKey() (key []byte, returnErr error) {
	if c == nil {
		return nil, fmt.Errorf("resolve cache auth key: cache is nil")
	}
	if len(c.authKey) == analysisCacheAuthKeyLength {
		return append([]byte(nil), c.authKey...), nil
	}
	authRoot, keyName, err := c.openAuthStore()
	if err != nil {
		if c.options.ReadOnly {
			if !os.IsNotExist(err) {
				c.warn("analysis cache auth store unavailable; treating cache as cold in read-only mode")
			}
			return nil, nil
		}
		return nil, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, authRoot.Close())
	}()

	key, err = readAnalysisCacheAuthKey(authRoot, keyName, !c.options.ReadOnly)
	switch {
	case err == nil:
		c.authKey = append(c.authKey[:0], key...)
		return append([]byte(nil), c.authKey...), nil
	case errors.Is(err, errAnalysisCacheAuthKeyMissing):
		if c.options.ReadOnly {
			return nil, nil
		}
		return c.createOrRotateAuthKey(authRoot, keyName, false)
	case errors.Is(err, errAnalysisCacheAuthKeyInvalid):
		if c.options.ReadOnly {
			c.warn("analysis cache auth key invalid; treating cache as cold in read-only mode")
			return nil, nil
		}
		c.warn("analysis cache auth key invalid; rotating key and treating prior pointers as untrusted")
		return c.createOrRotateAuthKey(authRoot, keyName, true)
	case errors.Is(err, errAnalysisCacheAuthKeyChanged):
		if c.options.ReadOnly {
			return nil, nil
		}
		return c.createOrRotateAuthKey(authRoot, keyName, true)
	default:
		if c.options.ReadOnly {
			c.warn("analysis cache auth key unavailable; treating cache as cold in read-only mode")
			return nil, nil
		}
		return nil, err
	}
}

func (c *analysisCache) openAuthStore() (*safeio.WriteRoot, string, error) {
	userCacheDir, err := analysisCacheUserCacheDirFn()
	if err != nil {
		return nil, "", fmt.Errorf("resolve user cache dir: %w", err)
	}
	if strings.TrimSpace(userCacheDir) == "" {
		return nil, "", fmt.Errorf("resolve user cache dir: empty path")
	}

	canonicalUserCacheDir, err := canonicalUserCacheDir(userCacheDir, c.options.ReadOnly, c.repoRoot, c.storageRoot)
	if err != nil {
		return nil, "", err
	}
	authRelativePath := filepath.Join("lopper", analysisCacheAuthDirName)
	authRootPath := filepath.Join(canonicalUserCacheDir, authRelativePath)
	if pathAtOrBelow(authRootPath, c.repoRoot) || pathAtOrBelow(authRootPath, c.storageRoot) {
		return nil, "", fmt.Errorf("cache auth store resolves inside repository-controlled storage: %s", authRootPath)
	}

	if !c.options.ReadOnly {
		_, statErr := os.Lstat(authRootPath)
		authStoreMissing := os.IsNotExist(statErr)
		if statErr != nil && !authStoreMissing {
			return nil, "", fmt.Errorf("inspect cache auth store: %w", statErr)
		}
		userRoot, err := safeio.OpenCanonicalWriteRoot(canonicalUserCacheDir)
		if err != nil {
			return nil, "", fmt.Errorf("open canonical user cache root: %w", err)
		}
		if err := userRoot.MkdirAll(authRelativePath, 0o750); err != nil {
			return nil, "", errors.Join(fmt.Errorf("create cache auth store: %w", err), userRoot.Close())
		}
		if authStoreMissing {
			if err := analysisCacheAuthSyncDirFn(userRoot); err != nil {
				return nil, "", errors.Join(fmt.Errorf("sync cache auth store parent after creation: %w", err), userRoot.Close())
			}
		}
		if err := userRoot.Close(); err != nil {
			return nil, "", fmt.Errorf("close canonical user cache root: %w", err)
		}
	}

	authRoot, err := safeio.OpenCanonicalWriteRoot(authRootPath)
	if err != nil {
		return nil, "", fmt.Errorf("open canonical cache auth store: %w", err)
	}
	storageRoot, err := c.canonicalStorageRoot()
	if err != nil {
		return nil, "", errors.Join(err, authRoot.Close())
	}
	return authRoot, analysisCacheAuthKeyName(storageRoot), nil
}

func canonicalUserCacheDir(userCacheDir string, readOnly bool, forbiddenRoots ...string) (canonicalDir string, returnErr error) {
	cacheDir, err := filepath.Abs(userCacheDir)
	if err != nil {
		return "", fmt.Errorf("resolve user cache dir: %w", err)
	}
	info, err := os.Lstat(cacheDir)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("user cache dir is a symlink: %s", cacheDir)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("user cache path is not a directory: %s", cacheDir)
		}
		canonicalDir, err = filepath.EvalSymlinks(cacheDir)
		if err != nil {
			return "", fmt.Errorf("resolve canonical user cache dir: %w", err)
		}
		return canonicalDir, nil
	case !os.IsNotExist(err):
		return "", fmt.Errorf("inspect user cache dir: %w", err)
	case readOnly:
		return "", fmt.Errorf("user cache dir missing: %w", os.ErrNotExist)
	}

	current := cacheDir
	missingParts := make([]string, 0, 2)
	for {
		info, err = os.Lstat(current)
		if err == nil {
			if !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				return "", fmt.Errorf("user cache ancestor is not a directory: %s", current)
			}
			break
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect user cache ancestor: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("resolve existing user cache ancestor: %w", os.ErrNotExist)
		}
		missingParts = append([]string{filepath.Base(current)}, missingParts...)
		current = parent
	}

	canonicalAncestor, err := filepath.EvalSymlinks(current)
	if err != nil {
		return "", fmt.Errorf("resolve canonical user cache ancestor: %w", err)
	}
	ancestorRoot, err := safeio.OpenCanonicalWriteRoot(canonicalAncestor)
	if err != nil {
		return "", fmt.Errorf("open canonical user cache ancestor: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, ancestorRoot.Close())
	}()
	missingPath := filepath.Join(missingParts...)
	canonicalDir = filepath.Join(canonicalAncestor, missingPath)
	authRootPath := filepath.Join(canonicalDir, "lopper", analysisCacheAuthDirName)
	for _, forbiddenRoot := range forbiddenRoots {
		if pathAtOrBelow(authRootPath, forbiddenRoot) {
			return "", fmt.Errorf("cache auth store resolves inside repository-controlled storage: %s", authRootPath)
		}
	}
	if err := ancestorRoot.MkdirAll(missingPath, 0o700); err != nil {
		return "", fmt.Errorf("create user cache dir: %w", err)
	}
	if err := analysisCacheAuthSyncDirFn(ancestorRoot); err != nil {
		return "", fmt.Errorf("sync user cache parent after creation: %w", err)
	}
	return canonicalDir, nil
}

func pathAtOrBelow(path, root string) bool {
	if strings.TrimSpace(root) == "" {
		return false
	}
	relativePath, err := filepath.Rel(root, path)
	if err != nil {
		return true
	}
	return relativePath == "." ||
		(relativePath != ".." && !strings.HasPrefix(relativePath, ".."+string(filepath.Separator)))
}

func analysisCacheAuthKeyName(storageRoot string) string {
	return sha256Hex([]byte(filepath.Clean(storageRoot))) + ".key"
}

func readAnalysisCacheAuthKey(root *safeio.WriteRoot, keyName string, repairPerms bool) ([]byte, error) {
	keyHex, info, err := root.ReadRegularFileUnderLimit(keyName, analysisCacheAuthKeyMaxBytes)
	switch {
	case err == nil:
		if info.Mode().Perm()&0o077 != 0 {
			if !repairPerms {
				return nil, fmt.Errorf("%w: permissive mode %o", errAnalysisCacheAuthKeyInvalid, info.Mode().Perm())
			}
			if chmodErr := root.Chmod(keyName, analysisCacheAuthKeyPerm); chmodErr != nil {
				return nil, fmt.Errorf("repair cache auth key permissions: %w", chmodErr)
			}
		}
		key, decodeErr := decodeAuthKey(strings.TrimSpace(string(keyHex)))
		if decodeErr != nil {
			return nil, fmt.Errorf("%w: %w", errAnalysisCacheAuthKeyInvalid, decodeErr)
		}
		return key, nil
	case errors.Is(err, safeio.ErrFileTooLarge):
		return nil, fmt.Errorf("%w: exceeds %d-byte limit", errAnalysisCacheAuthKeyInvalid, analysisCacheAuthKeyMaxBytes)
	case os.IsNotExist(err):
		return nil, errAnalysisCacheAuthKeyMissing
	case errors.Is(err, safeio.ErrFileChanged):
		return nil, errAnalysisCacheAuthKeyChanged
	default:
		return nil, fmt.Errorf("read cache auth key: %w", err)
	}
}

func (c *analysisCache) createOrRotateAuthKey(root *safeio.WriteRoot, keyName string, replaceInvalid bool) ([]byte, error) {
	for attempt := 0; attempt < analysisCacheAuthRetryLimit; attempt++ {
		key, err := readAnalysisCacheAuthKey(root, keyName, true)
		switch {
		case err == nil:
			c.authKey = append(c.authKey[:0], key...)
			return append([]byte(nil), c.authKey...), nil
		case errors.Is(err, errAnalysisCacheAuthKeyMissing):
			if err := publishMissingAuthKey(root, keyName); err != nil {
				return nil, err
			}
		case errors.Is(err, errAnalysisCacheAuthKeyInvalid) && replaceInvalid:
			if err := rotateInvalidAuthKey(root, keyName); err != nil && !errors.Is(err, errAnalysisCacheAuthKeyChanged) {
				return nil, err
			}
		case errors.Is(err, errAnalysisCacheAuthKeyChanged):
		default:
			return nil, err
		}
		time.Sleep(analysisCacheAuthRetryDelay)
	}
	return nil, fmt.Errorf("initialize cache auth key: timed out waiting for persisted winner")
}

func publishMissingAuthKey(root *safeio.WriteRoot, keyName string) (returnErr error) {
	key := make([]byte, analysisCacheAuthKeyLength)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("generate cache auth key: %w", err)
	}
	candidatePath, err := writeAuthKeyCandidate(root, []byte(hex.EncodeToString(key)))
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, removeAuthFileIfPresent(root, candidatePath))
	}()
	// Linking a complete candidate is the creation CAS: only one contender can
	// publish the absent path, and losers must re-read that persisted winner.
	if err := root.Link(candidatePath, keyName); err != nil {
		if os.IsExist(err) {
			return nil
		}
		return fmt.Errorf("publish cache auth key winner: %w", err)
	}
	if err := analysisCacheAuthSyncDirFn(root); err != nil {
		return fmt.Errorf("sync cache auth key directory after publish: %w", err)
	}
	return nil
}

func rotateInvalidAuthKey(root *safeio.WriteRoot, keyName string) (returnErr error) {
	generation, err := invalidAuthKeyGeneration(root, keyName)
	if err != nil {
		return err
	}
	rotationPath := keyName + analysisCacheAuthRotateTag + generation
	// The generation path is immutable so delayed contenders can only install
	// the same replacement key, never a second in-memory winner.
	if _, err := readAnalysisCacheAuthKey(root, rotationPath, true); errors.Is(err, errAnalysisCacheAuthKeyMissing) {
		if err := publishMissingAuthKey(root, rotationPath); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("read cache auth key rotation candidate: %w", err)
	}

	encodedKey, _, err := root.ReadRegularFileUnderLimit(rotationPath, analysisCacheAuthKeyMaxBytes)
	if err != nil {
		return fmt.Errorf("read cache auth key rotation candidate: %w", err)
	}
	if _, err := decodeAuthKey(strings.TrimSpace(string(encodedKey))); err != nil {
		return fmt.Errorf("validate cache auth key rotation candidate: %w", err)
	}
	currentGeneration, err := invalidAuthKeyGeneration(root, keyName)
	if err != nil {
		return err
	}
	if currentGeneration != generation {
		return errAnalysisCacheAuthKeyChanged
	}

	installPath, err := writeAuthKeyCandidate(root, encodedKey)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, removeAuthFileIfPresent(root, installPath))
	}()
	if err := root.Rename(installPath, keyName); err != nil {
		return fmt.Errorf("install rotated cache auth key: %w", err)
	}
	if err := analysisCacheAuthSyncDirFn(root); err != nil {
		return fmt.Errorf("sync cache auth key directory after rotation: %w", err)
	}
	return nil
}

func invalidAuthKeyGeneration(root *safeio.WriteRoot, keyName string) (string, error) {
	data, info, err := root.ReadRegularFileUnderLimit(keyName, analysisCacheAuthKeyMaxBytes)
	if err != nil {
		if os.IsNotExist(err) {
			return "", errAnalysisCacheAuthKeyChanged
		}
		if errors.Is(err, safeio.ErrFileChanged) {
			return "", errAnalysisCacheAuthKeyChanged
		}
		if errors.Is(err, safeio.ErrFileTooLarge) {
			info, statErr := root.Lstat(keyName)
			if statErr != nil {
				if os.IsNotExist(statErr) || errors.Is(statErr, safeio.ErrFileChanged) {
					return "", errAnalysisCacheAuthKeyChanged
				}
				return "", fmt.Errorf("read invalid cache auth key: %w", statErr)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return "", fmt.Errorf("read invalid cache auth key: %w", err)
			}
			state := fmt.Appendf(nil, "oversized:%d:%d", info.Size(), info.ModTime().UnixNano())
			return sha256Hex(state), nil
		}
		return "", fmt.Errorf("read invalid cache auth key: %w", err)
	}
	if _, err := decodeAuthKey(strings.TrimSpace(string(data))); err == nil {
		return "", errAnalysisCacheAuthKeyChanged
	}
	state := append([]byte(nil), data...)
	state = append(state, 0)
	state = fmt.Appendf(state, "%d:%d", info.Size(), info.ModTime().UnixNano())
	return sha256Hex(state), nil
}

func writeAuthKeyCandidate(root *safeio.WriteRoot, encodedKey []byte) (candidatePath string, returnErr error) {
	candidatePath, candidate, err := root.CreateTempFile(0o600)
	if err != nil {
		return "", fmt.Errorf("create cache auth key candidate: %w", err)
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, root.CleanupTempFile(candidatePath, candidate))
		}
	}()
	n, err := candidate.Write(encodedKey)
	if err != nil {
		return "", fmt.Errorf("write cache auth key candidate: %w", err)
	}
	if n != len(encodedKey) {
		return "", fmt.Errorf("write cache auth key candidate: %w", io.ErrShortWrite)
	}
	if err := candidate.Sync(); err != nil {
		return "", fmt.Errorf("sync cache auth key candidate: %w", err)
	}
	if err := candidate.Close(); err != nil {
		return "", fmt.Errorf("close cache auth key candidate: %w", err)
	}
	return candidatePath, nil
}

func removeAuthFileIfPresent(root *safeio.WriteRoot, path string) error {
	if err := root.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func decodeAuthKey(value string) ([]byte, error) {
	key, err := hex.DecodeString(value)
	if err != nil {
		return nil, err
	}
	if len(key) != analysisCacheAuthKeyLength {
		return nil, fmt.Errorf("unexpected key length %d", len(key))
	}
	return key, nil
}

func (c *analysisCache) signPointer(entry cacheEntryDescriptor, objectDigest string) (string, error) {
	key, err := c.resolveAuthKey()
	if err != nil {
		return "", err
	}
	if len(key) != analysisCacheAuthKeyLength {
		return "", fmt.Errorf("cache auth key unavailable")
	}
	mac := hmac.New(sha256.New, key)
	for _, part := range []string{
		analysisCacheAuthSchemaV1,
		entry.KeyDigest,
		entry.InputDigest,
		objectDigest,
	} {
		if _, err := mac.Write([]byte(part)); err != nil {
			return "", err
		}
		if _, err := mac.Write([]byte{0}); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (c *analysisCache) pointerTrusted(entry cacheEntryDescriptor, pointer cachePointer) (bool, error) {
	if strings.TrimSpace(pointer.Signature) == "" {
		return false, nil
	}
	key, err := c.resolveAuthKey()
	if err != nil {
		return false, err
	}
	if len(key) != analysisCacheAuthKeyLength {
		return false, nil
	}
	expected, err := pointerSignature(key, entry, pointer.ObjectDigest)
	if err != nil {
		return false, err
	}
	return hmac.Equal([]byte(expected), []byte(pointer.Signature)), nil
}

func pointerSignature(key []byte, entry cacheEntryDescriptor, objectDigest string) (string, error) {
	mac := hmac.New(sha256.New, key)
	for _, part := range []string{
		analysisCacheAuthSchemaV1,
		entry.KeyDigest,
		entry.InputDigest,
		objectDigest,
	} {
		if _, err := mac.Write([]byte(part)); err != nil {
			return "", err
		}
		if _, err := mac.Write([]byte{0}); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(mac.Sum(nil)), nil
}
