package analysis

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

type resolvedCacheOptions struct {
	Enabled      bool
	Path         string
	ReadOnly     bool
	ExplicitPath bool
}

type analysisCache struct {
	options          resolvedCacheOptions
	metadata         report.CacheMetadata
	warnings         []string
	cacheable        bool
	rootIdentity     fs.FileInfo
	rejectReadHits   bool
	inputDigestMemo  map[cacheInputDigestMemoKey]string
	stableRepoPath   string
	analysisRepoPath string
}

const analysisCacheQuarantineOwnerFile = ".lopper-owner"

type analysisCacheQuarantineReservation struct {
	name           string
	quarantineName string
	ownerName      string
	ownerToken     string
	info           fs.FileInfo
}

var (
	openAnalysisCacheAncestor = safeio.OpenRootExistingAncestorNoFollow
	mkdirAnalysisCacheDir     = func(root safeio.Root, name string, perm os.FileMode) (fs.FileInfo, error) {
		if err := root.Mkdir(name, perm); err != nil {
			return nil, err
		}
		info, infoErr := root.Lstat(name)
		child, err := root.OpenRoot(name)
		if err != nil {
			return info, err
		}
		childInfo, childErr := child.Lstat(".")
		closeErr := child.Close()
		if info == nil {
			info = childInfo
		}
		if infoErr != nil {
			return info, errors.Join(infoErr, closeErr)
		}
		if childErr != nil {
			return info, errors.Join(childErr, closeErr)
		}
		if info != nil && childInfo != nil && !os.SameFile(info, childInfo) {
			return info, errors.Join(errors.New("directory changed after creation"), closeErr)
		}
		return info, closeErr
	}
	closeAnalysisCacheRoot    = func(root safeio.Root) error { return root.Close() }
	validateAnalysisCacheRoot = safeio.VerifyDirectoryIdentity
)

type openedAnalysisCacheRoot struct {
	root           safeio.Root
	parent         safeio.Root
	rollbackParent safeio.Root
	name           string
	info           fs.FileInfo
	created        bool
}

func newAnalysisCache(req Request, repoPath string, analysisRepoPaths ...string) *analysisCache {
	options := resolveCacheOptions(req.Cache, repoPath)
	metadata := report.CacheMetadata{
		Enabled:  options.Enabled,
		Path:     options.Path,
		ReadOnly: options.ReadOnly,
	}
	cache := &analysisCache{
		options:          options,
		metadata:         metadata,
		warnings:         make([]string, 0),
		rejectReadHits:   !options.ExplicitPath,
		stableRepoPath:   filepath.Clean(repoPath),
		analysisRepoPath: filepath.Clean(repoPath),
	}
	if len(analysisRepoPaths) > 0 && strings.TrimSpace(analysisRepoPaths[0]) != "" {
		cache.analysisRepoPath = filepath.Clean(analysisRepoPaths[0])
	}
	if !options.Enabled {
		cache.cacheable = false
		return cache
	}
	if req.Cache == nil || strings.TrimSpace(req.Cache.Path) == "" {
		if cachePathEscapesRepo(options.Path, repoPath) {
			cache.cacheable = false
			cache.warn("analysis cache unavailable: cache path escapes repository root")
			return cache
		}
	}
	var (
		rootIdentity fs.FileInfo
		err          error
	)
	rootIdentity, err = prepareAnalysisCacheRoot(options)
	if err != nil {
		cache.cacheable = false
		cache.warn("analysis cache unavailable: " + err.Error())
		return cache
	}
	cache.rootIdentity = rootIdentity
	cache.cacheable = true
	return cache
}

func prepareAnalysisCacheRoot(options resolvedCacheOptions) (fs.FileInfo, error) {
	if options.ReadOnly {
		identity, err := prepareReadableAnalysisCacheRoot(options.Path)
		if err != nil {
			return nil, err
		}
		if err := validateAnalysisCacheRoot(options.Path, identity); err != nil {
			return nil, err
		}
		return identity, nil
	}
	return prepareWritableAnalysisCacheRoot(options.Path)
}

func prepareReadableAnalysisCacheRoot(cachePath string) (identity fs.FileInfo, returnErr error) {
	root, currentPath, missingParts, err := openAnalysisCacheAncestor(cachePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, root.Close())
	}()
	if len(missingParts) > 0 {
		return nil, os.ErrNotExist
	}
	return verifyPinnedAnalysisCacheDirectory(root, currentPath)
}

func (c *analysisCache) stableCacheRoot(rootPath string) string {
	if c == nil {
		return rootPath
	}
	rootPath = filepath.Clean(rootPath)
	rel, err := filepath.Rel(c.analysisRepoPath, rootPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return rootPath
	}
	return filepath.Join(c.stableRepoPath, rel)
}

func prepareWritableAnalysisCacheRoot(cachePath string) (identity fs.FileInfo, returnErr error) {
	root, currentPath, missingParts, err := openAnalysisCacheAncestor(cachePath)
	if err != nil {
		return nil, err
	}
	openedRoots := []openedAnalysisCacheRoot{{root: root}}
	defer func() {
		returnErr = cleanupOpenedAnalysisCacheRoots(openedRoots, returnErr)
	}()

	current := root
	for _, name := range missingParts {
		child, err := openOrCreatePinnedAnalysisCacheChild(current, currentPath, name)
		if err != nil {
			return nil, err
		}
		openedRoots = append(openedRoots, child)
		current = child.root
		currentPath = filepath.Join(currentPath, name)
		if _, err := verifyPinnedAnalysisCacheDirectory(current, currentPath); err != nil {
			return nil, err
		}
	}

	info, err := verifyPinnedAnalysisCacheDirectory(current, currentPath)
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"keys", "objects"} {
		child, err := openOrCreatePinnedAnalysisCacheChild(current, currentPath, name)
		if err != nil {
			return nil, err
		}
		openedRoots = append(openedRoots, child)
	}
	if err := validateAnalysisCacheRoot(cachePath, info); err != nil {
		return nil, err
	}
	return info, nil
}

func cleanupOpenedAnalysisCacheRoots(openedRoots []openedAnalysisCacheRoot, operationErr error) error {
	if operationErr != nil {
		operationErr = errors.Join(operationErr, removeCreatedAnalysisCacheRoots(openedRoots, false))
	}
	closeErr := closeOpenedAnalysisCacheRoots(openedRoots)
	if operationErr == nil && closeErr != nil {
		closeErr = errors.Join(closeErr, removeCreatedAnalysisCacheRoots(openedRoots, true))
	}
	return errors.Join(operationErr, closeErr, closeRollbackAnalysisCacheRoots(openedRoots))
}

func removeCreatedAnalysisCacheRoots(openedRoots []openedAnalysisCacheRoot, useRollbackParents bool) error {
	var rollbackErr error
	for index := len(openedRoots) - 1; index >= 0; index-- {
		opened := openedRoots[index]
		if !opened.created {
			continue
		}
		parent := opened.parent
		if useRollbackParents {
			parent = opened.rollbackParent
		}
		rollbackErr = errors.Join(rollbackErr, rollbackCreatedAnalysisCacheChild(parent, opened.name, nil, opened.info, opened.created))
	}
	return rollbackErr
}

func closeOpenedAnalysisCacheRoots(openedRoots []openedAnalysisCacheRoot) error {
	var closeErr error
	for index := len(openedRoots) - 1; index >= 0; index-- {
		closeErr = errors.Join(closeErr, closeAnalysisCacheRoot(openedRoots[index].root))
	}
	return closeErr
}

func closeRollbackAnalysisCacheRoots(openedRoots []openedAnalysisCacheRoot) error {
	var closeErr error
	for index := len(openedRoots) - 1; index >= 0; index-- {
		if openedRoots[index].rollbackParent != nil {
			closeErr = errors.Join(closeErr, openedRoots[index].rollbackParent.Close())
		}
	}
	return closeErr
}

func verifyPinnedAnalysisCacheDirectory(root safeio.Root, path string) (fs.FileInfo, error) {
	info, err := root.Lstat(".")
	if err != nil {
		return nil, err
	}
	if err := safeio.VerifyDirectoryIdentity(path, info); err != nil {
		return nil, err
	}
	return info, nil
}

func openOrCreatePinnedAnalysisCacheChild(root safeio.Root, parentPath, name string) (openedAnalysisCacheRoot, error) {
	if _, err := verifyPinnedAnalysisCacheDirectory(root, parentPath); err != nil {
		return openedAnalysisCacheRoot{}, err
	}
	childPath := filepath.Join(parentPath, name)
	info, created, err := loadOrCreateAnalysisCacheChildInfo(root, childPath, name)
	if err != nil {
		return openedAnalysisCacheRoot{}, err
	}
	if err := validateAnalysisCacheChildInfo(info, childPath); err != nil {
		return openedAnalysisCacheRoot{}, err
	}
	child, openedInfo, err := openPinnedAnalysisCacheChildRoot(root, parentPath, name, childPath, info, created)
	if err != nil {
		return openedAnalysisCacheRoot{}, err
	}
	opened := openedAnalysisCacheRoot{root: child, parent: root, name: name, info: openedInfo, created: created}
	if !opened.created {
		return opened, nil
	}
	return openAnalysisCacheRollbackParent(root, opened)
}

func loadOrCreateAnalysisCacheChildInfo(root safeio.Root, childPath, name string) (fs.FileInfo, bool, error) {
	info, err := root.Lstat(name)
	if err == nil {
		return info, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	return createOrLoadAnalysisCacheChildInfo(root, childPath, name)
}

func createOrLoadAnalysisCacheChildInfo(root safeio.Root, childPath, name string) (fs.FileInfo, bool, error) {
	createdInfo, created, err := mkdirOrReuseAnalysisCacheChild(root, childPath, name)
	if err != nil {
		return nil, false, err
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, false, errors.Join(err, rollbackCreatedAnalysisCacheChildInfo(root, childPath, name, createdInfo, created))
	}
	if err := verifyCreatedAnalysisCacheChildInfo(root, childPath, name, info, createdInfo, created); err != nil {
		return nil, false, err
	}
	return info, created, nil
}

func mkdirOrReuseAnalysisCacheChild(root safeio.Root, childPath, name string) (fs.FileInfo, bool, error) {
	createdInfo, err := mkdirAnalysisCacheDir(root, name, 0o750)
	if err == nil {
		return createdInfo, true, nil
	}
	if errors.Is(err, fs.ErrExist) {
		return createdInfo, false, nil
	}
	if createdInfo == nil {
		return nil, false, errors.Join(err, rollbackCreatedAnalysisCacheChildAtPath(root, childPath, name, nil, true))
	}
	return nil, false, errors.Join(err, rollbackCreatedAnalysisCacheChildAtPath(root, childPath, name, createdInfo, true))
}

func verifyCreatedAnalysisCacheChildInfo(root safeio.Root, childPath, name string, info, createdInfo fs.FileInfo, created bool) error {
	if !created {
		return nil
	}
	if createdInfo != nil && os.SameFile(createdInfo, info) {
		return nil
	}
	return errors.Join(errors.New("directory changed after creation: "+childPath), rollbackCreatedAnalysisCacheChildInfo(root, childPath, name, createdInfo, created))
}

func rollbackCreatedAnalysisCacheChildInfo(root safeio.Root, childPath, name string, childInfo fs.FileInfo, created bool) error {
	return rollbackCreatedAnalysisCacheChildAtPath(root, childPath, name, childInfo, created)
}

func validateAnalysisCacheChildInfo(info fs.FileInfo, childPath string) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("directory contains symlink: " + childPath)
	}
	if !info.IsDir() {
		return errors.New("directory is not a directory: " + childPath)
	}
	return nil
}

func openPinnedAnalysisCacheChildRoot(root safeio.Root, parentPath, name, childPath string, info fs.FileInfo, created bool) (safeio.Root, fs.FileInfo, error) {
	child, err := root.OpenRoot(name)
	if err != nil {
		return nil, nil, errors.Join(err, rollbackCreatedAnalysisCacheChild(root, name, nil, info, created))
	}
	openedInfo, err := child.Lstat(".")
	if err != nil {
		return nil, nil, errors.Join(err, rollbackCreatedAnalysisCacheChild(root, name, child, info, created))
	}
	if !os.SameFile(info, openedInfo) {
		return nil, nil, errors.Join(errors.New("directory changed while opening: "+childPath), rollbackCreatedAnalysisCacheChild(root, name, child, info, created))
	}
	if err := validateOpenedAnalysisCacheChild(root, parentPath, name, childPath, child, openedInfo, created); err != nil {
		return nil, nil, err
	}
	return child, openedInfo, nil
}

func validateOpenedAnalysisCacheChild(root safeio.Root, parentPath, name, childPath string, child safeio.Root, openedInfo fs.FileInfo, created bool) error {
	if _, err := verifyPinnedAnalysisCacheDirectory(root, parentPath); err != nil {
		return errors.Join(err, rollbackCreatedAnalysisCacheChild(root, name, child, openedInfo, created))
	}
	if err := safeio.VerifyDirectoryIdentity(childPath, openedInfo); err != nil {
		return errors.Join(err, rollbackCreatedAnalysisCacheChild(root, name, child, openedInfo, created))
	}
	return nil
}

func openAnalysisCacheRollbackParent(root safeio.Root, opened openedAnalysisCacheRoot) (openedAnalysisCacheRoot, error) {
	rollbackParent, err := root.OpenRoot(".")
	if err != nil {
		return openedAnalysisCacheRoot{}, errors.Join(err, rollbackCreatedAnalysisCacheChild(root, opened.name, opened.root, opened.info, opened.created))
	}
	opened.rollbackParent = rollbackParent
	return opened, nil
}

func openedAnalysisCacheChildInfo(root safeio.Root, name string) (fs.FileInfo, error) {
	child, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	childInfo, err := child.Lstat(".")
	if err != nil {
		return nil, errors.Join(err, closeAnalysisCacheRoot(child))
	}
	return childInfo, closeAnalysisCacheRoot(child)
}

func rollbackCreatedAnalysisCacheChildAtPath(root safeio.Root, childPath, name string, childInfo fs.FileInfo, created bool) error {
	if !created {
		return nil
	}
	return conditionallyRemoveAnalysisCacheChild(root, name, childInfo)
}

func rollbackCreatedAnalysisCacheChild(root safeio.Root, name string, child safeio.Root, childInfo fs.FileInfo, created bool) error {
	var closeErr error
	if child != nil {
		closeErr = closeAnalysisCacheRoot(child)
	}
	if !created {
		return closeErr
	}
	currentInfo, err := root.Lstat(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return closeErr
		}
		return errors.Join(closeErr, err)
	}
	if !sameAnalysisCacheRollbackTarget(currentInfo, childInfo) {
		return closeErr
	}
	if err := conditionallyRemoveAnalysisCacheChild(root, name, childInfo); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Join(closeErr, err)
	}
	return closeErr
}

func conditionallyRemoveAnalysisCacheChild(root safeio.Root, name string, childInfo fs.FileInfo) error {
	shouldRemove, lstatErr := analysisCacheRollbackCandidate(root, name, childInfo)
	if !shouldRemove {
		return nil
	}
	reservation, err := quarantineAnalysisCacheChildReservation(root, name, childInfo)
	return finishConditionalAnalysisCacheRemoval(root, reservation, lstatErr, err)
}

func analysisCacheRollbackCandidate(root safeio.Root, name string, childInfo fs.FileInfo) (bool, error) {
	if childInfo == nil {
		return false, nil
	}
	currentInfo, err := root.Lstat(name)
	if err == nil {
		return sameAnalysisCacheRollbackTarget(currentInfo, childInfo), nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return true, err
}

func finishConditionalAnalysisCacheRemoval(root safeio.Root, reservation analysisCacheQuarantineReservation, lstatErr, quarantineErr error) error {
	if quarantineErr != nil {
		return errors.Join(lstatErr, quarantineErr)
	}
	if reservation.quarantineName == "" {
		return nil
	}
	if err := removeAnalysisCacheQuarantine(root, reservation); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Join(lstatErr, err)
	}
	return lstatErr
}

func quarantineAnalysisCacheChild(root safeio.Root, name string, childInfo fs.FileInfo) (string, error) {
	reservation, err := quarantineAnalysisCacheChildReservation(root, name, childInfo)
	return reservation.quarantineName, err
}

func quarantineAnalysisCacheChildReservation(root safeio.Root, name string, childInfo fs.FileInfo) (analysisCacheQuarantineReservation, error) {
	if childInfo == nil {
		return analysisCacheQuarantineReservation{}, nil
	}
	for attempt := 0; attempt < 16; attempt++ {
		reservation, retry, err := quarantineAnalysisCacheChildAttempt(root, name, childInfo, attempt)
		if err != nil {
			return analysisCacheQuarantineReservation{}, err
		}
		if retry {
			continue
		}
		return reservation, nil
	}
	return analysisCacheQuarantineReservation{}, fmt.Errorf("unable to reserve rollback quarantine for %s", name)
}

func quarantineAnalysisCacheChildAttempt(root safeio.Root, name string, childInfo fs.FileInfo, attempt int) (analysisCacheQuarantineReservation, bool, error) {
	reservationName := fmt.Sprintf(".lopper-cache-rollback-%s-%d", filepath.Base(name), attempt)
	quarantineName := filepath.Join(reservationName, filepath.Base(name))
	reservation, retry, err := reserveAnalysisCacheQuarantine(root, reservationName, quarantineName)
	if retry || err != nil {
		return analysisCacheQuarantineReservation{}, retry, err
	}
	if err := safeio.RenameNoReplace(root, name, quarantineName); err != nil {
		return handleAnalysisCacheQuarantineRenameError(root, reservation, name, childInfo, err)
	}
	return verifyAnalysisCacheQuarantine(root, reservation, name, childInfo)
}

func reserveAnalysisCacheQuarantine(root safeio.Root, reservationName, quarantineName string) (analysisCacheQuarantineReservation, bool, error) {
	reservation := newAnalysisCacheQuarantineReservation(reservationName, quarantineName, generateAnalysisCacheQuarantineToken())
	if err := root.Mkdir(reservationName, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) || errors.Is(err, fs.ErrExist) {
			return analysisCacheQuarantineReservation{}, true, nil
		}
		return analysisCacheQuarantineReservation{}, false, err
	}
	info, err := root.Lstat(reservationName)
	if err != nil {
		return analysisCacheQuarantineReservation{}, false, err
	}
	reservation.info = info
	if err := writeAnalysisCacheQuarantineOwner(root, reservation); err != nil {
		return analysisCacheQuarantineReservation{}, false, err
	}
	if _, err := root.Lstat(quarantineName); err == nil {
		return analysisCacheQuarantineReservation{}, true, ignoreAnalysisCacheOccupiedReservationCleanup(removeAnalysisCacheQuarantineReservation(root, reservation))
	} else if !errors.Is(err, os.ErrNotExist) {
		return analysisCacheQuarantineReservation{}, false, errors.Join(err, removeAnalysisCacheQuarantineReservation(root, reservation))
	}
	return reservation, false, nil
}

func ignoreAnalysisCacheOccupiedReservationCleanup(err error) error {
	if isAnalysisCacheNonEmptyDirectoryError(err) {
		return nil
	}
	return err
}

func handleAnalysisCacheQuarantineRenameError(root safeio.Root, reservation analysisCacheQuarantineReservation, name string, childInfo fs.FileInfo, renameErr error) (analysisCacheQuarantineReservation, bool, error) {
	removeReservationErr := removeAnalysisCacheQuarantineReservation(root, reservation)
	if errors.Is(renameErr, os.ErrNotExist) {
		return analysisCacheQuarantineReservation{}, false, removeReservationErr
	}
	if errors.Is(renameErr, os.ErrExist) || analysisCacheQuarantineDestinationExists(root, name, reservation.quarantineName, childInfo) {
		if removeReservationErr != nil && !errors.Is(removeReservationErr, os.ErrNotExist) && !isAnalysisCacheNonEmptyDirectoryError(removeReservationErr) {
			return analysisCacheQuarantineReservation{}, false, errors.Join(renameErr, removeReservationErr)
		}
		return analysisCacheQuarantineReservation{}, true, nil
	}
	return analysisCacheQuarantineReservation{}, false, errors.Join(renameErr, removeReservationErr)
}

func verifyAnalysisCacheQuarantine(root safeio.Root, reservation analysisCacheQuarantineReservation, name string, childInfo fs.FileInfo) (analysisCacheQuarantineReservation, bool, error) {
	quarantineInfo, infoErr := root.Lstat(reservation.quarantineName)
	if infoErr == nil && sameAnalysisCacheRollbackTarget(quarantineInfo, childInfo) {
		return reservation, false, nil
	}
	if infoErr == nil {
		infoErr = errors.New("rollback target changed while quarantining: " + name)
		restoreErr := restoreMovedAnalysisCacheReplacement(root, reservation, name, quarantineInfo)
		return analysisCacheQuarantineReservation{}, false, errors.Join(infoErr, restoreErr)
	}
	return analysisCacheQuarantineReservation{}, false, infoErr
}

func restoreMovedAnalysisCacheReplacement(root safeio.Root, reservation analysisCacheQuarantineReservation, name string, movedInfo fs.FileInfo) error {
	if movedInfo == nil {
		return nil
	}
	if _, err := root.Lstat(name); err == nil {
		return errors.New("rollback replacement restore target occupied: " + name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := safeio.RenameNoReplace(root, reservation.quarantineName, name); err != nil {
		return err
	}
	restoredInfo, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !sameAnalysisCacheRollbackTarget(restoredInfo, movedInfo) {
		return errors.New("rollback replacement changed while restoring: " + name)
	}
	return removeAnalysisCacheQuarantineReservation(root, reservation)
}

func newAnalysisCacheQuarantineReservation(reservationName, quarantineName, ownerToken string) analysisCacheQuarantineReservation {
	return analysisCacheQuarantineReservation{
		name:           reservationName,
		quarantineName: quarantineName,
		ownerName:      filepath.Join(reservationName, analysisCacheQuarantineOwnerFile),
		ownerToken:     ownerToken,
	}
}

func writeAnalysisCacheQuarantineOwner(root safeio.Root, reservation analysisCacheQuarantineReservation) (returnErr error) {
	file, err := root.OpenFile(reservation.ownerName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, file.Close())
	}()
	_, err = file.Write([]byte(reservation.ownerToken))
	return err
}

func generateAnalysisCacheQuarantineToken() string {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(token[:])
}

func removeAnalysisCacheQuarantineReservation(root safeio.Root, reservation analysisCacheQuarantineReservation) error {
	if reservation.name == "." || reservation.name == string(filepath.Separator) {
		return nil
	}
	if !strings.HasPrefix(filepath.Base(reservation.name), ".lopper-cache-rollback-") {
		return nil
	}
	if reservation.ownerToken == "" || !analysisCacheQuarantineReservationOwned(root, reservation) {
		return nil
	}
	if !analysisCacheQuarantineReservationSameFile(root, reservation) {
		return nil
	}
	if err := root.Remove(reservation.ownerName); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !analysisCacheQuarantineReservationSameFile(root, reservation) {
		return nil
	}
	if err := root.Remove(reservation.name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func analysisCacheQuarantineReservationOwned(root safeio.Root, reservation analysisCacheQuarantineReservation) bool {
	return analysisCacheQuarantineOwnerTokenMatches(root, reservation.ownerName, reservation.ownerToken)
}

func analysisCacheQuarantineOwnerTokenMatches(root safeio.Root, ownerName, ownerToken string) bool {
	file, err := root.Open(ownerName)
	if err != nil {
		return false
	}
	data, err := io.ReadAll(file)
	closeErr := file.Close()
	return err == nil && closeErr == nil && string(data) == ownerToken
}

func analysisCacheQuarantineReservationSameFile(root safeio.Root, reservation analysisCacheQuarantineReservation) bool {
	if reservation.info == nil {
		return true
	}
	info, err := root.Lstat(reservation.name)
	return err == nil && sameAnalysisCacheRollbackTarget(info, reservation.info)
}

func removeAnalysisCacheQuarantine(root safeio.Root, reservation analysisCacheQuarantineReservation) error {
	reservationRoot, err := openOwnedAnalysisCacheQuarantineReservation(root, reservation)
	if err != nil {
		return err
	}
	removeErr := reservationRoot.Remove(filepath.Base(reservation.quarantineName))
	if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		closeErr := reservationRoot.Close()
		return errors.Join(removeErr, closeErr)
	}
	removeOwnerErr := reservationRoot.Remove(analysisCacheQuarantineOwnerFile)
	closeErr := reservationRoot.Close()
	if removeOwnerErr != nil && !errors.Is(removeOwnerErr, os.ErrNotExist) {
		return errors.Join(removeOwnerErr, closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	return removeAnalysisCacheQuarantineReservationDirectory(root, reservation)
}

func openOwnedAnalysisCacheQuarantineReservation(root safeio.Root, reservation analysisCacheQuarantineReservation) (safeio.Root, error) {
	if reservation.ownerToken == "" || reservation.info == nil {
		return nil, os.ErrNotExist
	}
	reservationRoot, err := root.OpenRoot(reservation.name)
	if err != nil {
		return nil, err
	}
	info, err := reservationRoot.Lstat(".")
	if err != nil {
		return nil, errors.Join(err, reservationRoot.Close())
	}
	if !sameAnalysisCacheRollbackTarget(info, reservation.info) {
		return nil, errors.Join(os.ErrNotExist, reservationRoot.Close())
	}
	if !analysisCacheQuarantineOwnerTokenMatches(reservationRoot, analysisCacheQuarantineOwnerFile, reservation.ownerToken) {
		return nil, errors.Join(os.ErrNotExist, reservationRoot.Close())
	}
	return reservationRoot, nil
}

func removeAnalysisCacheQuarantineReservationDirectory(root safeio.Root, reservation analysisCacheQuarantineReservation) error {
	if !analysisCacheQuarantineReservationSameFile(root, reservation) {
		return nil
	}
	if err := root.Remove(reservation.name); err != nil && !isAnalysisCacheNonEmptyDirectoryError(err) {
		return err
	}
	return nil
}

func isAnalysisCacheNonEmptyDirectoryError(err error) bool {
	if errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "not empty")
}

func analysisCacheQuarantineDestinationExists(root safeio.Root, name, quarantineName string, childInfo fs.FileInfo) bool {
	if _, err := root.Lstat(quarantineName); err != nil {
		return false
	}
	currentInfo, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	return err == nil && sameAnalysisCacheRollbackTarget(currentInfo, childInfo)
}

func sameAnalysisCacheRollbackTarget(currentInfo, childInfo fs.FileInfo) bool {
	if currentInfo == nil || childInfo == nil {
		return false
	}
	if !os.SameFile(currentInfo, childInfo) {
		return false
	}
	return sameAnalysisCacheRollbackOwner(currentInfo, childInfo)
}

func (c *analysisCache) openWriteRoot() (*safeio.WriteRoot, error) {
	if c.rootIdentity == nil {
		identity, err := prepareWritableAnalysisCacheRoot(c.options.Path)
		if err != nil {
			return nil, err
		}
		c.rootIdentity = identity
	}
	if err := validateAnalysisCacheRoot(c.options.Path, c.rootIdentity); err != nil {
		return nil, err
	}
	root, err := safeio.OpenCanonicalWriteRoot(c.options.Path)
	if err != nil {
		return nil, err
	}
	if err := root.VerifyIdentity(c.rootIdentity); err != nil {
		return nil, errors.Join(err, root.Close())
	}
	if err := validateAnalysisCacheRoot(c.options.Path, c.rootIdentity); err != nil {
		return nil, errors.Join(err, root.Close())
	}
	return root, nil
}

func cachePathEscapesRepo(cachePath, repoPath string) bool {
	if info, err := os.Lstat(cachePath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	resolvedCachePath, err := filepath.EvalSymlinks(cachePath)
	if err != nil {
		return false
	}
	resolvedRepoPath, err := filepath.EvalSymlinks(repoPath)
	if err != nil {
		resolvedRepoPath = filepath.Clean(repoPath)
	}
	rel, err := filepath.Rel(resolvedRepoPath, resolvedCachePath)
	if err != nil {
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func resolveCacheOptions(req *CacheOptions, repoPath string) resolvedCacheOptions {
	options := resolvedCacheOptions{
		Enabled:  true,
		Path:     filepath.Join(repoPath, ".lopper-cache"),
		ReadOnly: false,
	}
	if req == nil {
		return options
	}
	options.Enabled = req.Enabled
	if strings.TrimSpace(req.Path) != "" {
		options.Path = strings.TrimSpace(req.Path)
		options.ExplicitPath = true
	}
	if strings.TrimSpace(req.ResolvedPath) != "" {
		options.Path = strings.TrimSpace(req.ResolvedPath)
	}
	options.ReadOnly = req.ReadOnly
	return options
}

func normalizeCacheOptionsForRepository(req *CacheOptions, repoPath string) *CacheOptions {
	if req == nil {
		return nil
	}

	options := *req
	options.Path = strings.TrimSpace(options.Path)
	options.ResolvedPath = resolveCachePathForRepository(repoPath, options.ResolvedPath)
	if options.ResolvedPath == "" {
		options.ResolvedPath = resolveCachePathForRepository(repoPath, options.Path)
	}
	return &options
}

func resolveCachePathForRepository(repoPath, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(repoPath, path)
}

func (c *analysisCache) warn(message string) {
	if strings.TrimSpace(message) == "" {
		return
	}
	c.warnings = append(c.warnings, message)
}

func (c *analysisCache) takeWarnings() []string {
	if len(c.warnings) == 0 {
		return nil
	}
	out := append([]string(nil), c.warnings...)
	c.warnings = c.warnings[:0]
	return out
}

func (c *analysisCache) metadataSnapshot() *report.CacheMetadata {
	if c == nil {
		return nil
	}
	snapshot := c.metadata
	if len(c.metadata.Invalidations) > 0 {
		snapshot.Invalidations = append([]report.CacheInvalidation(nil), c.metadata.Invalidations...)
	}
	return &snapshot
}
