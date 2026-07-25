package analysis

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/windowspath"
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
	analysisCacheAuthAbsFn             = filepath.Abs
	analysisCacheAuthOpenRootFn        = safeio.OpenCanonicalWriteRoot
	analysisCacheAuthMkdirAllDurableFn = func(root *safeio.WriteRoot, path string, perm os.FileMode) error {
		return root.MkdirAllDurable(path, perm)
	}
	analysisCacheStorageIdentityFn       = storageDirectoryIdentity
	analysisCacheReadAuthKeyFn           = readAnalysisCacheAuthKey
	analysisCachePublishMissingAuthKeyFn = publishMissingAuthKey
	analysisCacheRotateInvalidAuthKeyFn  = rotateInvalidAuthKey
	analysisCacheInvalidKeyGenerationFn  = invalidAuthKeyGeneration
	analysisCacheSleepFn                 = time.Sleep
	analysisCacheRandReadFn              = rand.Read
	analysisCacheWritePointerSigPartsFn  = writePointerSignatureParts
)

var (
	errAnalysisCacheAuthKeyMissing = errors.New("cache auth key missing")
	errAnalysisCacheAuthKeyInvalid = errors.New("cache auth key invalid")
	errAnalysisCacheAuthKeyChanged = errors.New("cache auth key changed")
)

type authKeyReadRoot interface {
	ReadRegularFileUnderLimit(string, int64) ([]byte, fs.FileInfo, error)
	Lstat(string) (fs.FileInfo, error)
}

type authKeyTempRoot interface {
	CreateTempFile(os.FileMode) (string, safeio.File, error)
	CleanupTempFile(string, safeio.File) error
}

func (c *analysisCache) resolveAuthKey() (key []byte, returnErr error) {
	if c == nil {
		return nil, fmt.Errorf("resolve cache auth key: cache is nil")
	}
	if cachedKey := c.cachedAuthKey(); len(cachedKey) != 0 {
		return cachedKey, nil
	}
	authRoot, keyName, err := c.openAuthStore()
	if err != nil {
		return c.handleOpenAuthStoreError(err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, authRoot.Close())
	}()

	key, err = analysisCacheReadAuthKeyFn(authRoot, keyName, !c.options.ReadOnly)
	return c.finishResolvedAuthKey(key, err, authRoot, keyName)
}

func (c *analysisCache) openAuthStore() (*safeio.WriteRoot, string, error) {
	canonicalUserCacheDir, err := c.resolveCanonicalUserCacheDir()
	if err != nil {
		return nil, "", fmt.Errorf("resolve user cache dir: %w", err)
	}
	authRelativePath := filepath.Join("lopper", analysisCacheAuthDirName)
	authRootPath := filepath.Join(canonicalUserCacheDir, authRelativePath)
	if c.authRootInControlledStorage(authRootPath) {
		return nil, "", fmt.Errorf("cache auth store resolves inside repository-controlled storage: %s", authRootPath)
	}
	if err := c.ensureAuthStorePath(canonicalUserCacheDir, authRelativePath, authRootPath); err != nil {
		return nil, "", err
	}

	authRoot, err := analysisCacheAuthOpenRootFn(authRootPath)
	if err != nil {
		return nil, "", fmt.Errorf("open canonical cache auth store: %w", err)
	}
	storageRoot, err := c.canonicalStorageRoot()
	if err != nil {
		return nil, "", errors.Join(err, authRoot.Close())
	}
	keyName, err := c.authKeyName(storageRoot)
	if err != nil {
		return nil, "", errors.Join(err, authRoot.Close())
	}
	return authRoot, keyName, nil
}

func (c *analysisCache) cachedAuthKey() []byte {
	if len(c.authKey) != analysisCacheAuthKeyLength {
		return nil
	}
	return append([]byte(nil), c.authKey...)
}

func (c *analysisCache) handleOpenAuthStoreError(err error) ([]byte, error) {
	if !c.options.ReadOnly {
		return nil, err
	}
	if !os.IsNotExist(err) {
		c.warn("analysis cache auth store unavailable; treating cache as cold in read-only mode")
	}
	return nil, nil
}

func (c *analysisCache) finishResolvedAuthKey(key []byte, err error, authRoot *safeio.WriteRoot, keyName string) ([]byte, error) {
	if err == nil {
		c.authKey = append(c.authKey[:0], key...)
		return append([]byte(nil), c.authKey...), nil
	}
	if c.options.ReadOnly {
		return c.finishReadOnlyResolvedAuthKey(err)
	}
	return c.finishWritableResolvedAuthKey(authRoot, keyName, err)
}

func (c *analysisCache) finishReadOnlyResolvedAuthKey(err error) ([]byte, error) {
	switch {
	case errors.Is(err, errAnalysisCacheAuthKeyMissing), errors.Is(err, errAnalysisCacheAuthKeyChanged):
		return nil, nil
	case errors.Is(err, errAnalysisCacheAuthKeyInvalid):
		c.warn("analysis cache auth key invalid; treating cache as cold in read-only mode")
		return nil, nil
	default:
		c.warn("analysis cache auth key unavailable; treating cache as cold in read-only mode")
		return nil, nil
	}
}

func (c *analysisCache) finishWritableResolvedAuthKey(authRoot *safeio.WriteRoot, keyName string, err error) ([]byte, error) {
	switch {
	case errors.Is(err, errAnalysisCacheAuthKeyMissing):
		return c.createOrRotateAuthKey(authRoot, keyName, false)
	case errors.Is(err, errAnalysisCacheAuthKeyInvalid):
		c.warn("analysis cache auth key invalid; rotating key and treating prior pointers as untrusted")
		return c.createOrRotateAuthKey(authRoot, keyName, true)
	case errors.Is(err, errAnalysisCacheAuthKeyChanged):
		return c.createOrRotateAuthKey(authRoot, keyName, true)
	default:
		return nil, err
	}
}

func (c *analysisCache) resolveCanonicalUserCacheDir() (string, error) {
	userCacheDir, err := analysisCacheUserCacheDirFn()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(userCacheDir) == "" {
		return "", fmt.Errorf("empty path")
	}
	return canonicalUserCacheDir(userCacheDir, c.options.ReadOnly, c.repoRoot, c.storageRoot)
}

func (c *analysisCache) authRootInControlledStorage(authRootPath string) bool {
	return pathAtOrBelow(authRootPath, c.repoRoot) || pathAtOrBelow(authRootPath, c.storageRoot)
}

func (c *analysisCache) ensureAuthStorePath(canonicalUserCacheDir, authRelativePath, authRootPath string) error {
	if c.options.ReadOnly {
		return nil
	}
	authStoreMissing, err := authStoreMissing(authRootPath)
	if err != nil {
		return err
	}
	return createAuthStorePath(canonicalUserCacheDir, authRelativePath, authStoreMissing)
}

func authStoreMissing(authRootPath string) (bool, error) {
	_, statErr := os.Lstat(authRootPath)
	if statErr == nil {
		return false, nil
	}
	if os.IsNotExist(statErr) {
		return true, nil
	}
	return false, fmt.Errorf("inspect cache auth store: %w", statErr)
}

func createAuthStorePath(canonicalUserCacheDir, authRelativePath string, authStoreMissing bool) (returnErr error) {
	userRoot, err := analysisCacheAuthOpenRootFn(canonicalUserCacheDir)
	if err != nil {
		return fmt.Errorf("open canonical user cache root: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, userRoot.Close())
	}()
	if err := analysisCacheAuthMkdirAllDurableFn(userRoot, authRelativePath, 0o750); err != nil {
		if authStoreMissing {
			return fmt.Errorf("sync cache auth store parent after creation: %w", err)
		}
		return fmt.Errorf("create cache auth store: %w", err)
	}
	return nil
}

func canonicalUserCacheDir(userCacheDir string, readOnly bool, forbiddenRoots ...string) (canonicalDir string, returnErr error) {
	if err := validateRawUserCacheDir(userCacheDir); err != nil {
		return "", err
	}
	cacheDir, err := analysisCacheAuthAbsFn(userCacheDir)
	if err != nil {
		return "", fmt.Errorf("resolve user cache dir: %w", err)
	}
	info, err := os.Lstat(cacheDir)
	switch {
	case err == nil:
		return canonicalExistingUserCacheDir(cacheDir, info)
	case !os.IsNotExist(err):
		return "", fmt.Errorf("inspect user cache dir: %w", err)
	case readOnly:
		return "", fmt.Errorf("user cache dir missing: %w", os.ErrNotExist)
	default:
		return canonicalMissingUserCacheDir(cacheDir, forbiddenRoots...)
	}
}

func canonicalExistingUserCacheDir(cacheDir string, info fs.FileInfo) (string, error) {
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("user cache dir is a symlink: %s", cacheDir)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("user cache path is not a directory: %s", cacheDir)
	}
	canonicalDir, err := filepath.EvalSymlinks(cacheDir)
	if err != nil {
		return "", fmt.Errorf("resolve canonical user cache dir: %w", err)
	}
	return canonicalDir, nil
}

func canonicalMissingUserCacheDir(cacheDir string, forbiddenRoots ...string) (canonicalDir string, returnErr error) {
	current, missingParts, err := findUserCacheAncestor(cacheDir)
	if err != nil {
		return "", err
	}
	canonicalAncestor, err := filepath.EvalSymlinks(current)
	if err != nil {
		return "", fmt.Errorf("resolve canonical user cache ancestor: %w", err)
	}
	ancestorRoot, err := analysisCacheAuthOpenRootFn(canonicalAncestor)
	if err != nil {
		return "", fmt.Errorf("open canonical user cache ancestor: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, ancestorRoot.Close())
	}()
	missingPath := filepath.Join(missingParts...)
	canonicalDir = filepath.Join(canonicalAncestor, missingPath)
	authRootPath := filepath.Join(canonicalDir, "lopper", analysisCacheAuthDirName)
	if err := validateAuthRootPath(authRootPath, forbiddenRoots...); err != nil {
		return "", err
	}
	if err := analysisCacheAuthMkdirAllDurableFn(ancestorRoot, missingPath, 0o700); err != nil {
		return "", fmt.Errorf("sync user cache parent after creation: %w", err)
	}
	return canonicalDir, nil
}

func findUserCacheAncestor(cacheDir string) (string, []string, error) {
	current := cacheDir
	missingParts := make([]string, 0, 2)
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				return "", nil, fmt.Errorf("user cache ancestor is not a directory: %s", current)
			}
			return current, missingParts, nil
		}
		if !os.IsNotExist(err) {
			return "", nil, fmt.Errorf("inspect user cache ancestor: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", nil, fmt.Errorf("resolve existing user cache ancestor: %w", os.ErrNotExist)
		}
		missingParts = append([]string{filepath.Base(current)}, missingParts...)
		current = parent
	}
}

func validateAuthRootPath(authRootPath string, forbiddenRoots ...string) error {
	for _, forbiddenRoot := range forbiddenRoots {
		if pathAtOrBelow(authRootPath, forbiddenRoot) {
			return fmt.Errorf("cache auth store resolves inside repository-controlled storage: %s", authRootPath)
		}
	}
	return nil
}

func pathAtOrBelow(path, root string) bool {
	if strings.TrimSpace(root) == "" {
		return false
	}
	if windowsComparison, ok := pathAtOrBelowWindows(path, root); ok {
		return windowsComparison
	}
	relativePath, err := filepath.Rel(root, path)
	if err != nil {
		return true
	}
	return relativePath == "." ||
		(relativePath != ".." && !strings.HasPrefix(relativePath, ".."+string(filepath.Separator)))
}

func pathAtOrBelowWindows(path, root string) (bool, bool) {
	if strings.ContainsRune(path, 0) || strings.ContainsRune(root, 0) {
		return true, true
	}

	pathInfo := windowspath.Classify(path)
	rootInfo := windowspath.Classify(root)
	if !isWindowsAbsoluteRoot(rootInfo) {
		return false, false
	}
	if pathInfo.Kind == windowspath.KindAmbiguous || rootInfo.Kind == windowspath.KindAmbiguous {
		return true, true
	}
	if windowspath.HasReservedDOSNameComponent(path) || windowspath.HasReservedDOSNameComponent(root) {
		return true, true
	}
	if windowspath.HasTrimmedComponentAlias(path) || windowspath.HasTrimmedComponentAlias(root) {
		return true, true
	}
	if !rootInfo.IsAbsolute() {
		return true, true
	}
	if !pathInfo.IsAbsolute() {
		return true, true
	}
	if pathInfo.Kind != rootInfo.Kind {
		return false, true
	}
	if !strings.EqualFold(pathInfo.Volume, rootInfo.Volume) {
		return false, true
	}
	cleanPath := strings.ToLower(windowspath.Clean(pathInfo.Path))
	cleanRoot := strings.ToLower(windowspath.Clean(rootInfo.Path))
	return cleanRoot == "/" || cleanPath == cleanRoot || strings.HasPrefix(cleanPath, cleanRoot+"/"), true
}

func isWindowsAbsoluteRoot(info windowspath.Classification) bool {
	return info.Kind == windowspath.KindDriveAbsolute || info.Kind == windowspath.KindUNCAbsolute
}

func (c *analysisCache) authKeyName(storageRoot string) (string, error) {
	storageRootInfo := c.storageRootInfo
	if storageRootInfo == nil {
		var err error
		storageRootInfo, err = analysisCacheStatFn(storageRoot)
		if err != nil {
			return "", fmt.Errorf("inspect cache storage directory identity: %w", err)
		}
	}
	return analysisCacheAuthKeyName(storageRoot, storageRootInfo)
}

func analysisCacheAuthKeyName(storageRoot string, storageRootInfo fs.FileInfo) (string, error) {
	identity, err := analysisCacheStorageIdentityFn(storageRoot, storageRootInfo)
	if err != nil {
		return "", fmt.Errorf("identify cache storage directory: %w", err)
	}
	return sha256Hex([]byte(identity)) + ".key", nil
}

func readAnalysisCacheAuthKey(root *safeio.WriteRoot, keyName string, repairPerms bool) ([]byte, error) {
	keyHex, info, err := root.ReadRegularFileUnderLimit(keyName, analysisCacheAuthKeyMaxBytes)
	switch {
	case err == nil:
		if authKeyFileModeTooPermissive(info.Mode()) {
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
		key, err := analysisCacheReadAuthKeyFn(root, keyName, true)
		switch {
		case err == nil:
			c.authKey = append(c.authKey[:0], key...)
			return append([]byte(nil), c.authKey...), nil
		case errors.Is(err, errAnalysisCacheAuthKeyMissing):
			if err := analysisCachePublishMissingAuthKeyFn(root, keyName); err != nil {
				return nil, err
			}
		case errors.Is(err, errAnalysisCacheAuthKeyInvalid) && replaceInvalid:
			if err := analysisCacheRotateInvalidAuthKeyFn(root, keyName); err != nil && !errors.Is(err, errAnalysisCacheAuthKeyChanged) {
				return nil, err
			}
		case errors.Is(err, errAnalysisCacheAuthKeyChanged):
		default:
			return nil, err
		}
		analysisCacheSleepFn(analysisCacheAuthRetryDelay)
	}
	return nil, fmt.Errorf("initialize cache auth key: timed out waiting for persisted winner")
}

func publishMissingAuthKey(root *safeio.WriteRoot, keyName string) (returnErr error) {
	key := make([]byte, analysisCacheAuthKeyLength)
	if _, err := analysisCacheRandReadFn(key); err != nil {
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

func rotateInvalidAuthKey(root *safeio.WriteRoot, keyName string) error {
	generation, err := analysisCacheInvalidKeyGenerationFn(root, keyName)
	if err != nil {
		return err
	}
	rotationPath := keyName + analysisCacheAuthRotateTag + generation
	// The generation path is immutable so delayed contenders can only install
	// the same replacement key, never a second in-memory winner.
	if _, err := analysisCacheReadAuthKeyFn(root, rotationPath, true); errors.Is(err, errAnalysisCacheAuthKeyMissing) {
		if err := analysisCachePublishMissingAuthKeyFn(root, rotationPath); err != nil {
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
	currentGeneration, err := analysisCacheInvalidKeyGenerationFn(root, keyName)
	if err != nil {
		return err
	}
	if currentGeneration != generation {
		return errAnalysisCacheAuthKeyChanged
	}
	if err := root.WriteFileReplacingAtomicallyWithExactPermissions(keyName, encodedKey, analysisCacheAuthKeyPerm); err != nil {
		return fmt.Errorf("install rotated cache auth key: %w", err)
	}
	return nil
}

func invalidAuthKeyGeneration(root *safeio.WriteRoot, keyName string) (string, error) {
	return invalidAuthKeyGenerationWith(root, keyName)
}

func invalidAuthKeyGenerationWith(root authKeyReadRoot, keyName string) (string, error) {
	data, info, err := root.ReadRegularFileUnderLimit(keyName, analysisCacheAuthKeyMaxBytes)
	if err != nil {
		return invalidAuthKeyGenerationReadError(root, keyName, err)
	}
	if _, err := decodeAuthKey(strings.TrimSpace(string(data))); err == nil {
		return "", errAnalysisCacheAuthKeyChanged
	}
	return invalidAuthKeyStateDigest(data, info), nil
}

func invalidAuthKeyGenerationReadError(root authKeyReadRoot, keyName string, err error) (string, error) {
	switch {
	case os.IsNotExist(err), errors.Is(err, safeio.ErrFileChanged):
		return "", errAnalysisCacheAuthKeyChanged
	case errors.Is(err, safeio.ErrFileTooLarge):
		return oversizedInvalidAuthKeyGeneration(root, keyName, err)
	default:
		return "", fmt.Errorf("read invalid cache auth key: %w", err)
	}
}

func oversizedInvalidAuthKeyGeneration(root authKeyReadRoot, keyName string, readErr error) (string, error) {
	info, statErr := root.Lstat(keyName)
	if statErr != nil {
		if os.IsNotExist(statErr) || errors.Is(statErr, safeio.ErrFileChanged) {
			return "", errAnalysisCacheAuthKeyChanged
		}
		return "", fmt.Errorf("read invalid cache auth key: %w", statErr)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("read invalid cache auth key: %w", readErr)
	}
	state := fmt.Appendf(nil, "oversized:%d:%d", info.Size(), info.ModTime().UnixNano())
	return sha256Hex(state), nil
}

func invalidAuthKeyStateDigest(data []byte, info fs.FileInfo) string {
	state := append([]byte(nil), data...)
	state = append(state, 0)
	state = fmt.Appendf(state, "%d:%d", info.Size(), info.ModTime().UnixNano())
	return sha256Hex(state)
}

func writeAuthKeyCandidate(root *safeio.WriteRoot, encodedKey []byte) (candidatePath string, returnErr error) {
	return writeAuthKeyCandidateWith(root, encodedKey)
}

func writeAuthKeyCandidateWith(root authKeyTempRoot, encodedKey []byte) (candidatePath string, returnErr error) {
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
	if err := analysisCacheWritePointerSigPartsFn(mac, entry, objectDigest); err != nil {
		return "", err
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
	if err := analysisCacheWritePointerSigPartsFn(mac, entry, objectDigest); err != nil {
		return "", err
	}
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func writePointerSignatureParts(w io.Writer, entry cacheEntryDescriptor, objectDigest string) error {
	for _, part := range []string{
		analysisCacheAuthSchemaV1,
		entry.KeyDigest,
		entry.InputDigest,
		objectDigest,
	} {
		if _, err := w.Write([]byte(part)); err != nil {
			return err
		}
		if _, err := w.Write([]byte{0}); err != nil {
			return err
		}
	}
	return nil
}
