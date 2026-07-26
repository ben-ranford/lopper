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
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/windowspath"
)

const (
	analysisCacheAuthDirName     = "analysis-cache-auth"
	analysisCacheAuthKeyLength   = 32
	analysisCacheAuthKeyMaxBytes = analysisCacheAuthKeyLength*2 + 8
	analysisCacheAuthKeyPerm     = 0o600
	analysisCacheAuthSchemaV1    = "v1"
	analysisCacheAuthRotateTag   = ".rotate-"
	analysisCacheAuthRetryLimit  = 200
	analysisCacheAuthRetryDelay  = 5 * time.Millisecond
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
	analysisCacheStorageIdentityFn    = storageDirectoryIdentity
	analysisCacheReadAuthKeyFn        = readAnalysisCacheAuthKey
	analysisCacheReadPrivateAuthKeyFn = func(root *safeio.WriteRoot, keyName string, maxBytes int64) ([]byte, fs.FileInfo, bool, error) {
		return root.ReadRegularFilePrivateToOwnerUnderLimit(keyName, maxBytes)
	}
	analysisCachePublishMissingAuthKeyFn    = publishMissingAuthKey
	analysisCacheRotateInvalidAuthKeyFn     = rotateInvalidAuthKey
	analysisCacheRotateCompromisedAuthKeyFn = rotateCompromisedAuthKey
	analysisCacheInvalidKeyGenerationFn     = invalidAuthKeyGeneration
	analysisCacheSleepFn                    = time.Sleep
	analysisCacheAuthAfterCompromisedReadFn = func() error { return nil }
	analysisCacheRandReadFn                 = rand.Read
	analysisCacheWritePointerSigPartsFn     = writePointerSignatureParts
	analysisCachePathRelFn                  = filepath.Rel
	analysisCachePathLstatFn                = os.Lstat
	analysisCachePathSameFileFn             = os.SameFile
	analysisCacheAuthLinkFn                 = func(root *safeio.WriteRoot, oldPath, newPath string) error {
		return root.Link(oldPath, newPath)
	}
	analysisCacheAuthLockDirectoryFn = func(root *safeio.WriteRoot) (io.Closer, error) {
		return root.LockDirectory()
	}
	analysisCacheAuthRenameNoReplaceFn = func(root *safeio.WriteRoot, oldPath, newPath string) error {
		return root.RenameNoReplace(oldPath, newPath)
	}
	analysisCacheAuthBeforeFallbackInstallFn      = func() error { return nil }
	analysisCacheMissingAncestryCaseInsensitiveFn = func() bool {
		return runtime.GOOS == "windows"
	}
)

var (
	errAnalysisCacheAuthKeyMissing = errors.New("cache auth key missing")
	errAnalysisCacheAuthKeyInvalid = errors.New("cache auth key invalid")
	errAnalysisCacheAuthKeyChanged = errors.New("cache auth key changed")
)

type compromisedAuthKeyState struct {
	contentDigest string
	generation    string
	identity      fs.FileInfo
}

type compromisedAuthKeyError struct {
	state  compromisedAuthKeyState
	reason string
}

func (e *compromisedAuthKeyError) Error() string {
	return fmt.Sprintf("%s: %s", errAnalysisCacheAuthKeyInvalid, e.reason)
}

func (*compromisedAuthKeyError) Unwrap() error {
	return errAnalysisCacheAuthKeyInvalid
}

type authKeyReadRoot interface {
	ReadRegularFileUnderLimit(string, int64) ([]byte, fs.FileInfo, error)
	ReadRegularFilePrivateToOwnerUnderLimit(string, int64) ([]byte, fs.FileInfo, bool, error)
	Lstat(string) (fs.FileInfo, error)
}

type authKeyTempRoot interface {
	CreatePrivateTempFile() (string, safeio.File, error)
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
		return c.createOrRotateAuthKeyFromError(authRoot, keyName, false, err)
	case errors.Is(err, errAnalysisCacheAuthKeyInvalid):
		c.warn("analysis cache auth key invalid; rotating key and treating prior pointers as untrusted")
		return c.createOrRotateAuthKeyFromError(authRoot, keyName, true, err)
	case errors.Is(err, errAnalysisCacheAuthKeyChanged):
		return c.createOrRotateAuthKeyFromError(authRoot, keyName, true, err)
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
	relativePath, err := analysisCachePathRelFn(root, path)
	if err != nil {
		return true
	}
	if relativePath == "." ||
		(relativePath != ".." && !strings.HasPrefix(relativePath, ".."+string(filepath.Separator))) {
		return true
	}
	identityComparison, ok, identityErr := pathAtOrBelowByExistingIdentity(path, root)
	if identityErr != nil {
		return true
	}
	if ok {
		return identityComparison
	}
	return false
}

func pathAtOrBelowWindows(path, root string) (bool, bool) {
	pathInfo := windowspath.Classify(path)
	rootInfo := windowspath.Classify(root)
	if !isWindowsAbsoluteRoot(rootInfo) {
		return false, false
	}
	if decision, handled := pathAtOrBelowWindowsPreflight(path, root, pathInfo, rootInfo); handled {
		return decision, true
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

func pathAtOrBelowWindowsPreflight(path, root string, pathInfo, rootInfo windowspath.Classification) (bool, bool) {
	if strings.ContainsRune(path, 0) || strings.ContainsRune(root, 0) {
		return true, true
	}
	if pathAtOrBelowWindowsUnsafe(path, root, pathInfo, rootInfo) {
		return true, true
	}
	if !rootInfo.IsAbsolute() || !pathInfo.IsAbsolute() {
		return true, true
	}
	return false, false
}

func pathAtOrBelowWindowsUnsafe(path, root string, pathInfo, rootInfo windowspath.Classification) bool {
	return pathInfo.Kind == windowspath.KindAmbiguous ||
		rootInfo.Kind == windowspath.KindAmbiguous ||
		windowspath.HasReservedDOSNameComponent(path) ||
		windowspath.HasReservedDOSNameComponent(root) ||
		windowspath.HasTrimmedComponentAlias(path) ||
		windowspath.HasTrimmedComponentAlias(root)
}

func isWindowsAbsoluteRoot(info windowspath.Classification) bool {
	return info.Kind == windowspath.KindDriveAbsolute || info.Kind == windowspath.KindUNCAbsolute
}

type existingPathAncestor struct {
	path string
	info fs.FileInfo
}

type existingPathAncestry struct {
	ancestors []existingPathAncestor
	remainder []string
}

func inspectExistingPathAncestry(path string) (existingPathAncestry, error) {
	current := filepath.Clean(path)
	remainder := make([]string, 0, 2)
	var info fs.FileInfo
	for {
		var err error
		info, err = analysisCachePathLstatFn(current)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return existingPathAncestry{}, fmt.Errorf("inspect path ancestry: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return existingPathAncestry{}, nil
		}
		remainder = append([]string{filepath.Base(current)}, remainder...)
		current = parent
	}

	ancestry := existingPathAncestry{remainder: remainder}
	for {
		ancestry.ancestors = append(ancestry.ancestors, existingPathAncestor{
			path: current,
			info: info,
		})
		parent := filepath.Dir(current)
		if parent == current {
			return ancestry, nil
		}
		current = parent
		var err error
		info, err = analysisCachePathLstatFn(current)
		if err != nil {
			return existingPathAncestry{}, fmt.Errorf("inspect existing path ancestor: %w", err)
		}
	}
}

func revalidateExistingPathAncestry(ancestry existingPathAncestry) error {
	for _, ancestor := range ancestry.ancestors {
		currentInfo, err := analysisCachePathLstatFn(ancestor.path)
		if err != nil {
			return fmt.Errorf("revalidate existing path ancestor: %w", err)
		}
		if !analysisCachePathSameFileFn(ancestor.info, currentInfo) {
			return fmt.Errorf("%w: path ancestor %s", safeio.ErrFileChanged, ancestor.path)
		}
	}
	return nil
}

func pathAtOrBelowByExistingIdentity(path, root string) (bool, bool, error) {
	pathAncestry, err := inspectExistingPathAncestry(path)
	if err != nil {
		return false, false, err
	}
	rootAncestry, err := inspectExistingPathAncestry(root)
	if err != nil {
		return false, false, err
	}
	if len(pathAncestry.ancestors) == 0 || len(rootAncestry.ancestors) == 0 {
		return false, false, nil
	}
	if err := revalidateExistingPathAncestry(pathAncestry); err != nil {
		return false, false, err
	}
	if err := revalidateExistingPathAncestry(rootAncestry); err != nil {
		return false, false, err
	}

	rootPrefix := rootAncestry.ancestors[0].info
	if len(rootAncestry.remainder) == 0 {
		return ancestryContainsIdentity(pathAncestry, rootPrefix), true, nil
	}
	return missingAncestryAtOrBelow(pathAncestry, rootAncestry, rootPrefix), true, nil
}

func ancestryContainsIdentity(ancestry existingPathAncestry, identity fs.FileInfo) bool {
	for _, ancestor := range ancestry.ancestors {
		if analysisCachePathSameFileFn(ancestor.info, identity) {
			return true
		}
	}
	return false
}

func missingAncestryAtOrBelow(pathAncestry, rootAncestry existingPathAncestry, rootPrefix fs.FileInfo) bool {
	if !analysisCachePathSameFileFn(pathAncestry.ancestors[0].info, rootPrefix) ||
		len(rootAncestry.remainder) > len(pathAncestry.remainder) {
		return false
	}
	for i := range rootAncestry.remainder {
		if !missingAncestryComponentEqual(pathAncestry.remainder[i], rootAncestry.remainder[i]) {
			return false
		}
	}
	return true
}

func missingAncestryComponentEqual(pathComponent, rootComponent string) bool {
	if analysisCacheMissingAncestryCaseInsensitiveFn() {
		return strings.EqualFold(pathComponent, rootComponent)
	}
	return pathComponent == rootComponent
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

func readAnalysisCacheAuthKey(root *safeio.WriteRoot, keyName string, _ bool) ([]byte, error) {
	keyHex, info, private, err := analysisCacheReadPrivateAuthKeyFn(root, keyName, analysisCacheAuthKeyMaxBytes)
	switch {
	case err == nil:
		if !private {
			return nil, newCompromisedAuthKeyError(keyHex, info)
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
	return c.createOrRotateAuthKeyFromError(root, keyName, replaceInvalid, nil)
}

func (c *analysisCache) createOrRotateAuthKeyFromError(root *safeio.WriteRoot, keyName string, replaceInvalid bool, initialErr error) ([]byte, error) {
	compromisedStates := make(map[string]compromisedAuthKeyState)
	if err := observeCompromisedAuthKey(compromisedStates, initialErr); err != nil {
		return nil, err
	}
	for attempt := 0; attempt < analysisCacheAuthRetryLimit; attempt++ {
		key, readErr := analysisCacheReadAuthKeyFn(root, keyName, true)
		if err := observeCompromisedAuthKey(compromisedStates, readErr); err != nil {
			return nil, err
		}
		resolvedKey, retry, err := c.handleAuthKeyRead(root, keyName, key, readErr, replaceInvalid, compromisedStates)
		if err != nil {
			return nil, err
		}
		if !retry {
			return resolvedKey, nil
		}
		analysisCacheSleepFn(analysisCacheAuthRetryDelay)
	}
	return nil, fmt.Errorf("initialize cache auth key: timed out waiting for persisted winner")
}

func observeCompromisedAuthKey(states map[string]compromisedAuthKeyState, err error) error {
	if !rememberCompromisedAuthKey(states, err) {
		return nil
	}
	return analysisCacheAuthAfterCompromisedReadFn()
}

func (c *analysisCache) handleAuthKeyRead(root *safeio.WriteRoot, keyName string, key []byte, readErr error, replaceInvalid bool, compromisedStates map[string]compromisedAuthKeyState) ([]byte, bool, error) {
	switch {
	case readErr == nil:
		return c.handleResolvedAuthKey(root, keyName, key, replaceInvalid, compromisedStates)
	case errors.Is(readErr, errAnalysisCacheAuthKeyMissing):
		return nil, true, analysisCachePublishMissingAuthKeyFn(root, keyName)
	case errors.Is(readErr, errAnalysisCacheAuthKeyInvalid) && replaceInvalid:
		return nil, true, authKeyMutationError(rotateRejectedAuthKey(root, keyName, readErr))
	case errors.Is(readErr, errAnalysisCacheAuthKeyChanged):
		return nil, true, nil
	default:
		return nil, false, readErr
	}
}

func (c *analysisCache) handleResolvedAuthKey(root *safeio.WriteRoot, keyName string, key []byte, replaceInvalid bool, compromisedStates map[string]compromisedAuthKeyState) ([]byte, bool, error) {
	state, compromised, err := currentCompromisedAuthKeyState(root, keyName, key, compromisedStates)
	if err != nil {
		return nil, errors.Is(err, errAnalysisCacheAuthKeyChanged), authKeyMutationError(err)
	}
	if !compromised {
		c.authKey = append(c.authKey[:0], key...)
		return append([]byte(nil), c.authKey...), false, nil
	}
	if !replaceInvalid {
		return nil, false, errAnalysisCacheAuthKeyInvalid
	}
	err = analysisCacheRotateCompromisedAuthKeyFn(root, keyName, state)
	return nil, true, authKeyMutationError(err)
}

func rotateRejectedAuthKey(root *safeio.WriteRoot, keyName string, err error) error {
	state, compromised := compromisedAuthKeyStateFromError(err)
	if compromised {
		return analysisCacheRotateCompromisedAuthKeyFn(root, keyName, state)
	}
	return analysisCacheRotateInvalidAuthKeyFn(root, keyName)
}

func authKeyMutationError(err error) error {
	if errors.Is(err, errAnalysisCacheAuthKeyChanged) {
		return nil
	}
	return err
}

func newCompromisedAuthKeyError(data []byte, info fs.FileInfo) error {
	return &compromisedAuthKeyError{
		state:  compromisedAuthKeyStateForData(data, info),
		reason: fmt.Sprintf("permissions are not owner-only (%o)", info.Mode().Perm()),
	}
}

func compromisedAuthKeyStateForData(data []byte, info fs.FileInfo) compromisedAuthKeyState {
	return compromisedAuthKeyState{
		contentDigest: authKeyContentDigest(data),
		generation:    invalidAuthKeyStateDigest(data, info),
		identity:      info,
	}
}

func compromisedAuthKeyStateFromError(err error) (compromisedAuthKeyState, bool) {
	var compromisedErr *compromisedAuthKeyError
	if !errors.As(err, &compromisedErr) {
		return compromisedAuthKeyState{}, false
	}
	return compromisedErr.state, true
}

func rememberCompromisedAuthKey(states map[string]compromisedAuthKeyState, err error) bool {
	state, ok := compromisedAuthKeyStateFromError(err)
	if !ok {
		return false
	}
	if _, exists := states[state.contentDigest]; exists {
		return false
	}
	states[state.contentDigest] = state
	return true
}

func currentCompromisedAuthKeyState(root *safeio.WriteRoot, keyName string, key []byte, states map[string]compromisedAuthKeyState) (compromisedAuthKeyState, bool, error) {
	if len(states) == 0 {
		return compromisedAuthKeyState{}, false, nil
	}
	data, info, private, err := recheckAnalysisCacheAuthKey(root, keyName, key)
	if err != nil {
		return compromisedAuthKeyState{}, false, err
	}
	if !private {
		return rememberCurrentCompromisedAuthKey(data, info, states)
	}
	return retainedCompromisedAuthKeyState(data, info, states)
}

func recheckAnalysisCacheAuthKey(root *safeio.WriteRoot, keyName string, key []byte) ([]byte, fs.FileInfo, bool, error) {
	data, info, private, err := analysisCacheReadPrivateAuthKeyFn(root, keyName, analysisCacheAuthKeyMaxBytes)
	if err != nil {
		return nil, nil, false, changedOrWrappedAuthKeyError(err, "recheck cache auth key after compromise")
	}
	currentKey, err := decodeAuthKey(strings.TrimSpace(string(data)))
	if err != nil || !hmac.Equal(currentKey, key) {
		return nil, nil, false, errAnalysisCacheAuthKeyChanged
	}
	return data, info, private, nil
}

func changedOrWrappedAuthKeyError(err error, context string) error {
	if os.IsNotExist(err) || errors.Is(err, safeio.ErrFileChanged) {
		return errAnalysisCacheAuthKeyChanged
	}
	return fmt.Errorf("%s: %w", context, err)
}

func rememberCurrentCompromisedAuthKey(data []byte, info fs.FileInfo, states map[string]compromisedAuthKeyState) (compromisedAuthKeyState, bool, error) {
	state := compromisedAuthKeyStateForData(data, info)
	if _, exists := states[state.contentDigest]; exists {
		return state, true, nil
	}
	states[state.contentDigest] = state
	if err := analysisCacheAuthAfterCompromisedReadFn(); err != nil {
		return compromisedAuthKeyState{}, false, err
	}
	return state, true, nil
}

func retainedCompromisedAuthKeyState(data []byte, info fs.FileInfo, states map[string]compromisedAuthKeyState) (compromisedAuthKeyState, bool, error) {
	currentDigest := authKeyContentDigest(data)
	if _, ok := states[currentDigest]; ok {
		return compromisedAuthKeyStateForData(data, info), true, nil
	}
	return compromisedAuthKeyState{}, false, nil
}

func authKeyContentDigest(data []byte) string {
	key, err := decodeAuthKey(strings.TrimSpace(string(data)))
	if err == nil {
		return sha256Hex(key)
	}
	return sha256Hex(data)
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
	if err := analysisCacheAuthLinkFn(root, candidatePath, keyName); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return syncObservedAuthKeyWinner(root)
		}
		if authKeyPublishFallbackAllowed(err) {
			return publishMissingAuthKeyWithoutHardLink(root, candidatePath, keyName)
		}
		return fmt.Errorf("publish cache auth key winner: %w", err)
	}
	if err := analysisCacheAuthSyncDirFn(root); err != nil {
		return fmt.Errorf("sync cache auth key directory after publish: %w", err)
	}
	return nil
}

func authKeyPublishFallbackAllowed(err error) bool {
	return errors.Is(err, os.ErrPermission) ||
		errors.Is(err, fs.ErrPermission) ||
		errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.EOPNOTSUPP) ||
		errors.Is(err, syscall.EPERM)
}

func publishMissingAuthKeyWithoutHardLink(root *safeio.WriteRoot, candidatePath, keyName string) (returnErr error) {
	lock, err := analysisCacheAuthLockDirectoryFn(root)
	if err != nil {
		return fmt.Errorf("acquire cache auth key publish lock: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, lock.Close())
	}()
	if _, statErr := root.Lstat(keyName); statErr == nil {
		return syncObservedAuthKeyWinner(root)
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("inspect cache auth key winner before fallback publish: %w", statErr)
	}
	if err := analysisCacheAuthBeforeFallbackInstallFn(); err != nil {
		return err
	}
	if err := analysisCacheAuthRenameNoReplaceFn(root, candidatePath, keyName); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return syncObservedAuthKeyWinner(root)
		}
		return fmt.Errorf("publish cache auth key winner without hard link: %w", err)
	}
	if err := analysisCacheAuthSyncDirFn(root); err != nil {
		return fmt.Errorf("sync cache auth key directory after fallback publish: %w", err)
	}
	return nil
}

func syncObservedAuthKeyWinner(root *safeio.WriteRoot) error {
	if err := analysisCacheAuthSyncDirFn(root); err != nil {
		return fmt.Errorf("sync cache auth key directory after observing winner: %w", err)
	}
	return nil
}

func rotateInvalidAuthKey(root *safeio.WriteRoot, keyName string) error {
	generation, err := analysisCacheInvalidKeyGenerationFn(root, keyName)
	if err != nil {
		return err
	}
	return rotateAuthKey(root, keyName, generation, func() error {
		currentGeneration, err := analysisCacheInvalidKeyGenerationFn(root, keyName)
		if err != nil {
			return err
		}
		if currentGeneration != generation {
			return errAnalysisCacheAuthKeyChanged
		}
		return nil
	})
}

func rotateCompromisedAuthKey(root *safeio.WriteRoot, keyName string, state compromisedAuthKeyState) error {
	return rotateAuthKey(root, keyName, state.generation, func() error {
		data, info, err := root.ReadRegularFileUnderLimit(keyName, analysisCacheAuthKeyMaxBytes)
		if err != nil {
			if os.IsNotExist(err) || errors.Is(err, safeio.ErrFileChanged) {
				return errAnalysisCacheAuthKeyChanged
			}
			return fmt.Errorf("recheck compromised cache auth key: %w", err)
		}
		if authKeyContentDigest(data) != state.contentDigest || invalidAuthKeyStateDigest(data, info) != state.generation {
			return errAnalysisCacheAuthKeyChanged
		}
		return nil
	})
}

func rotateAuthKey(root *safeio.WriteRoot, keyName, generation string, revalidate func() error) error {
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
	lock, err := analysisCacheAuthLockDirectoryFn(root)
	if err != nil {
		return fmt.Errorf("acquire cache auth key rotation lock: %w", err)
	}
	defer func() {
		err = errors.Join(err, lock.Close())
	}()
	if err := revalidate(); err != nil {
		return err
	}
	if err := root.WritePrivateFileReplacingAtomically(keyName, encodedKey); err != nil {
		return fmt.Errorf("install rotated cache auth key: %w", err)
	}
	return nil
}

func invalidAuthKeyGeneration(root *safeio.WriteRoot, keyName string) (string, error) {
	return invalidAuthKeyGenerationWith(root, keyName)
}

func invalidAuthKeyGenerationWith(root authKeyReadRoot, keyName string) (string, error) {
	data, info, private, err := root.ReadRegularFilePrivateToOwnerUnderLimit(keyName, analysisCacheAuthKeyMaxBytes)
	if err != nil {
		return invalidAuthKeyGenerationReadError(root, keyName, err)
	}
	if !private {
		return invalidAuthKeyStateDigest(data, info), nil
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
	state := fmt.Appendf(nil, "oversized:%o:%d:%d", info.Mode().Perm(), info.Size(), info.ModTime().UnixNano())
	return sha256Hex(state), nil
}

func invalidAuthKeyStateDigest(data []byte, info fs.FileInfo) string {
	state := append([]byte(nil), data...)
	state = append(state, 0)
	state = fmt.Appendf(state, "%o:%d:%d", info.Mode().Perm(), info.Size(), info.ModTime().UnixNano())
	return sha256Hex(state)
}

func writeAuthKeyCandidate(root *safeio.WriteRoot, encodedKey []byte) (candidatePath string, returnErr error) {
	return writeAuthKeyCandidateWith(root, encodedKey)
}

func writeAuthKeyCandidateWith(root authKeyTempRoot, encodedKey []byte) (candidatePath string, returnErr error) {
	candidatePath, candidate, err := root.CreatePrivateTempFile()
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
