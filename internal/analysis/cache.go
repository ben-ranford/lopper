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
		operationErr = errors.Join(operationErr, removeCreatedAnalysisCacheRoots(openedRoots, true))
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
		if useRollbackParents && opened.rollbackParent != nil {
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
	rollbackParent, err := rollbackParentForMissingAnalysisCacheChild(root, name)
	if err != nil {
		return openedAnalysisCacheRoot{}, err
	}
	info, created, err := loadOrCreateAnalysisCacheChildInfo(root, rollbackParent, childPath, name)
	if err != nil {
		if rollbackParent != nil {
			err = errors.Join(err, rollbackParent.Close())
		}
		return openedAnalysisCacheRoot{}, err
	}
	if err := validateAnalysisCacheChildInfo(info, childPath); err != nil {
		if rollbackParent != nil {
			err = errors.Join(err, rollbackParent.Close())
		}
		return openedAnalysisCacheRoot{}, err
	}
	child, openedInfo, err := openPinnedAnalysisCacheChildRoot(root, rollbackParent, parentPath, name, childPath, info, created)
	if err != nil {
		if rollbackParent != nil {
			err = errors.Join(err, rollbackParent.Close())
		}
		return openedAnalysisCacheRoot{}, err
	}
	return openedAnalysisCacheRoot{root: child, parent: root, rollbackParent: rollbackParent, name: name, info: openedInfo, created: created}, nil
}

func rollbackParentForMissingAnalysisCacheChild(root safeio.Root, name string) (safeio.Root, error) {
	if _, err := root.Lstat(name); err == nil {
		return nil, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return root.OpenRoot(".")
}

func loadOrCreateAnalysisCacheChildInfo(root, rollbackParent safeio.Root, childPath, name string) (fs.FileInfo, bool, error) {
	info, err := root.Lstat(name)
	if err == nil {
		return info, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	return createOrLoadAnalysisCacheChildInfo(root, rollbackParent, childPath, name)
}

func createOrLoadAnalysisCacheChildInfo(root, rollbackParent safeio.Root, childPath, name string) (fs.FileInfo, bool, error) {
	createdInfo, created, err := mkdirOrReuseAnalysisCacheChild(root, analysisCacheRollbackRoot(root, rollbackParent), childPath, name)
	if err != nil {
		return nil, false, err
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, false, errors.Join(err, rollbackCreatedAnalysisCacheChildInfo(analysisCacheRollbackRoot(root, rollbackParent), childPath, name, createdInfo, created))
	}
	if err := verifyCreatedAnalysisCacheChildInfo(analysisCacheRollbackRoot(root, rollbackParent), childPath, name, info, createdInfo, created); err != nil {
		return nil, false, err
	}
	return info, created, nil
}

func mkdirOrReuseAnalysisCacheChild(root, rollbackRoot safeio.Root, childPath, name string) (fs.FileInfo, bool, error) {
	createdInfo, err := mkdirAnalysisCacheDir(root, name, 0o750)
	if err == nil {
		return createdInfo, true, nil
	}
	if errors.Is(err, fs.ErrExist) {
		return createdInfo, false, nil
	}
	if createdInfo == nil {
		return nil, false, errors.Join(err, rollbackCreatedAnalysisCacheChildAtPath(rollbackRoot, childPath, name, nil, true))
	}
	return nil, false, errors.Join(err, rollbackCreatedAnalysisCacheChildAtPath(rollbackRoot, childPath, name, createdInfo, true))
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

func openPinnedAnalysisCacheChildRoot(root, rollbackParent safeio.Root, parentPath, name, childPath string, info fs.FileInfo, created bool) (safeio.Root, fs.FileInfo, error) {
	rollbackRoot := analysisCacheRollbackRoot(root, rollbackParent)
	child, err := root.OpenRoot(name)
	if err != nil {
		return nil, nil, errors.Join(err, rollbackCreatedAnalysisCacheChild(rollbackRoot, name, nil, info, created))
	}
	openedInfo, err := child.Lstat(".")
	if err != nil {
		return nil, nil, errors.Join(err, rollbackCreatedAnalysisCacheChild(rollbackRoot, name, child, info, created))
	}
	if !os.SameFile(info, openedInfo) {
		return nil, nil, errors.Join(errors.New("directory changed while opening: "+childPath), rollbackCreatedAnalysisCacheChild(rollbackRoot, name, child, info, created))
	}
	rollback := analysisCacheChildRollback{
		root:    rollbackRoot,
		name:    name,
		child:   child,
		info:    openedInfo,
		created: created,
	}
	if err := validateOpenedAnalysisCacheChild(root, parentPath, childPath, rollback); err != nil {
		return nil, nil, err
	}
	return child, openedInfo, nil
}

type analysisCacheChildRollback struct {
	root    safeio.Root
	name    string
	child   safeio.Root
	info    fs.FileInfo
	created bool
}

func (r *analysisCacheChildRollback) run() error {
	return rollbackCreatedAnalysisCacheChild(r.root, r.name, r.child, r.info, r.created)
}

func validateOpenedAnalysisCacheChild(root safeio.Root, parentPath, childPath string, rollback analysisCacheChildRollback) error {
	if _, err := verifyPinnedAnalysisCacheDirectory(root, parentPath); err != nil {
		return errors.Join(err, rollback.run())
	}
	if err := safeio.VerifyDirectoryIdentity(childPath, rollback.info); err != nil {
		return errors.Join(err, rollback.run())
	}
	return nil
}

func analysisCacheRollbackRoot(root, rollbackParent safeio.Root) safeio.Root {
	if rollbackParent != nil {
		return rollbackParent
	}
	return root
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
	if err := removeAnalysisCacheQuarantine(root, reservation); err != nil && !analysisCacheQuarantineTargetConfirmedGone(root, reservation, err) {
		if errors.Is(err, os.ErrNotExist) {
			// err wraps os.ErrNotExist even though the quarantined target
			// was just confirmed present (the reservation's identity or
			// owner token was lost, not the target itself).
			// rollbackCreatedAnalysisCacheChild, the caller of
			// conditionallyRemoveAnalysisCacheChild, treats any
			// ErrNotExist-matching error as "nothing to roll back" and
			// discards it -- reformat so this failure can't be mistaken for
			// that unrelated, genuinely benign case.
			err = fmt.Errorf("rollback candidate %s remains in an unverifiable reservation: %s", reservation.quarantineName, err.Error())
		}
		return errors.Join(lstatErr, err)
	}
	return lstatErr
}

// analysisCacheQuarantineTargetConfirmedGone reports whether an ErrNotExist
// from removeAnalysisCacheQuarantine reflects the quarantined child actually
// being removed, rather than the reservation's owner marker or identity
// having been lost to a concurrent process -- openOwnedAnalysisCacheQuarantineReservation
// reports both cases as ErrNotExist, but only the former means rollback is
// complete.
func analysisCacheQuarantineTargetConfirmedGone(root safeio.Root, reservation analysisCacheQuarantineReservation, removeErr error) bool {
	if !errors.Is(removeErr, os.ErrNotExist) {
		return false
	}
	_, err := root.Lstat(reservation.quarantineName)
	return errors.Is(err, os.ErrNotExist)
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
	// Pin the reservation once and keep it open through the rename and its
	// immediate verification, rather than reopening it by path for each
	// step -- if another same-user process renamed the reservation away
	// and installed a replacement in between, a fresh path lookup could
	// silently resolve into that replacement instead of the reservation
	// this attempt actually moved the candidate into.
	reservationRoot, err := root.OpenRoot(reservation.name)
	if err != nil {
		return handleAnalysisCacheQuarantineRenameError(root, reservation, name, childInfo, err)
	}
	resultReservation, resultRetry, resultErr := quarantineAnalysisCacheChildIntoOpenReservation(root, reservationRoot, reservation, name, childInfo)
	if closeErr := reservationRoot.Close(); closeErr != nil && resultErr != nil {
		// Only fold the close error in alongside an already-failed attempt.
		// A close failure on an otherwise-verified quarantine must not
		// discard resultReservation here: quarantineAnalysisCacheChildReservation's
		// loop treats any non-nil error as "nothing usable," so joining
		// closeErr into a nil resultErr would turn a completed,
		// identity-verified quarantine into a lost one -- the created cache
		// child would remain stranded under .lopper-cache-rollback-* with
		// no further attempt to clean it up.
		resultErr = errors.Join(resultErr, closeErr)
	}
	return resultReservation, resultRetry, resultErr
}

func quarantineAnalysisCacheChildIntoOpenReservation(root, reservationRoot safeio.Root, reservation analysisCacheQuarantineReservation, name string, childInfo fs.FileInfo) (analysisCacheQuarantineReservation, bool, error) {
	if err := renameAnalysisCacheChildIntoReservation(root, reservationRoot, reservation, name); err != nil {
		return handleAnalysisCacheQuarantineRenameError(root, reservation, name, childInfo, err)
	}
	return verifyAnalysisCacheQuarantine(root, reservationRoot, reservation, name, childInfo)
}

// renameAnalysisCacheChildIntoReservation moves name into reservationRoot,
// the already-pinned reservation directory reserveAnalysisCacheQuarantine
// just created. Immediately before the rename -- as late as possible, to
// keep the window as narrow as this package can make it -- it re-verifies
// that name is still empty (another initializer may have populated it since
// the caller's own candidate check) and that the reservation's identity
// still matches what reserveAnalysisCacheQuarantine observed, then renames
// directly into the pinned handle rather than re-resolving the reservation
// by path. Neither check is atomic with the rename itself (no such
// primitive exists), so verifyAnalysisCacheQuarantine re-checks emptiness
// once more after the rename and restores the child if anything slipped
// through.
func renameAnalysisCacheChildIntoReservation(root, reservationRoot safeio.Root, reservation analysisCacheQuarantineReservation, name string) error {
	if empty, err := analysisCacheChildIsEmpty(root, name); err == nil && !empty {
		return fmt.Errorf("rollback candidate %s is no longer empty: %w", name, os.ErrNotExist)
	}
	info, err := reservationRoot.Lstat(".")
	if err != nil {
		return err
	}
	if !sameAnalysisCacheRollbackTarget(info, reservation.info) {
		return fmt.Errorf("reservation directory changed before rename: %w", os.ErrNotExist)
	}
	err = safeio.RenameNoReplaceInto(root, name, reservationRoot, filepath.Base(reservation.quarantineName))
	if errors.Is(err, fs.ErrInvalid) {
		// root does not support the pinned-destination rename trait (e.g. a
		// test double). Fall back to the path-based rename rather than
		// failing every caller of this optional capability.
		return safeio.RenameNoReplace(root, name, reservation.quarantineName)
	}
	return err
}

// analysisCacheChildIsEmpty reports whether name is an empty directory. A
// rollback candidate that is no longer empty may have been reused or
// populated by another cache initializer sharing the same directory; moving
// it into quarantine would hide that initializer's live cache data. Treat a
// non-empty directory as adopted by someone else rather than a rollback
// target.
func analysisCacheChildIsEmpty(root safeio.Root, name string) (bool, error) {
	dir, err := safeio.OpenPinnedDirectory(root, name)
	if err != nil {
		return false, err
	}
	entries, readErr := dir.ReadDir(1)
	closeErr := dir.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return false, errors.Join(readErr, closeErr)
	}
	if closeErr != nil {
		return false, closeErr
	}
	return len(entries) == 0, nil
}

func reserveAnalysisCacheQuarantine(root safeio.Root, reservationName, quarantineName string) (analysisCacheQuarantineReservation, bool, error) {
	token, err := generateAnalysisCacheQuarantineToken()
	if err != nil {
		return analysisCacheQuarantineReservation{}, false, err
	}
	reservation := newAnalysisCacheQuarantineReservation(reservationName, quarantineName, token)
	if err := root.Mkdir(reservationName, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) || errors.Is(err, fs.ErrExist) {
			return analysisCacheQuarantineReservation{}, true, nil
		}
		return analysisCacheQuarantineReservation{}, false, err
	}
	info, err := root.Lstat(reservationName)
	if err != nil {
		reservation.info = openedAnalysisCacheQuarantineReservationInfo(root, reservation)
		return analysisCacheQuarantineReservation{}, false, errors.Join(err, removeCreatedAnalysisCacheQuarantineReservation(root, reservation))
	}
	reservation.info = info
	if err := writeAnalysisCacheQuarantineOwner(root, reservation); err != nil {
		return analysisCacheQuarantineReservation{}, false, errors.Join(err, removeCreatedAnalysisCacheQuarantineReservation(root, reservation))
	}
	if _, err := root.Lstat(quarantineName); err == nil {
		return analysisCacheQuarantineReservation{}, true, ignoreAnalysisCacheOccupiedReservationCleanup(removeAnalysisCacheQuarantineReservation(root, reservation))
	} else if !errors.Is(err, os.ErrNotExist) {
		return analysisCacheQuarantineReservation{}, false, errors.Join(err, removeAnalysisCacheQuarantineReservation(root, reservation))
	}
	return reservation, false, nil
}

func ignoreAnalysisCacheOccupiedReservationCleanup(err error) error {
	if err == nil {
		return nil
	}
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

// verifyAnalysisCacheQuarantine looks up the just-quarantined child through
// reservationRoot, the same pinned handle renameAnalysisCacheChildIntoReservation
// just renamed into, rather than re-resolving reservation.quarantineName by
// path from root. A path-based lookup here would be vulnerable to the same
// reservation-swap race the pinned rename itself guards against: if another
// same-user process renamed the reservation away and installed a
// replacement between the rename and this verification, a fresh path lookup
// could resolve into that replacement (or ErrNotExist) instead of the
// reservation this attempt actually used.
func verifyAnalysisCacheQuarantine(root, reservationRoot safeio.Root, reservation analysisCacheQuarantineReservation, name string, childInfo fs.FileInfo) (analysisCacheQuarantineReservation, bool, error) {
	quarantineBase := filepath.Base(reservation.quarantineName)
	quarantineInfo, infoErr := reservationRoot.Lstat(quarantineBase)
	if infoErr == nil && sameAnalysisCacheRollbackTarget(quarantineInfo, childInfo) {
		// The emptiness check in renameAnalysisCacheChildIntoReservation is
		// not atomic with the rename itself. Re-check now: if another
		// initializer populated the candidate in that window, its data just
		// got moved into quarantine alongside ours. Restore it rather than
		// leaving it hidden and risking a later ENOTEMPTY cleanup failure.
		// Treat a failure to determine emptiness the same as "not empty" --
		// fail closed rather than accepting an unverified quarantine.
		if empty, emptyErr := analysisCacheChildIsEmpty(reservationRoot, quarantineBase); emptyErr != nil || !empty {
			restoreErr := restoreMovedAnalysisCacheReplacement(root, reservationRoot, reservation, name, quarantineInfo)
			verifyErr := emptyErr
			if verifyErr == nil {
				verifyErr = fmt.Errorf("rollback candidate %s was populated while quarantining", name)
			}
			return analysisCacheQuarantineReservation{}, false, errors.Join(verifyErr, restoreErr)
		}
		return reservation, false, nil
	}
	if infoErr == nil {
		infoErr = errors.New("rollback target changed while quarantining: " + name)
		restoreErr := restoreMovedAnalysisCacheReplacement(root, reservationRoot, reservation, name, quarantineInfo)
		return analysisCacheQuarantineReservation{}, false, errors.Join(infoErr, restoreErr)
	}
	return analysisCacheQuarantineReservation{}, false, infoErr
}

// restoreMovedAnalysisCacheReplacement moves the quarantined child back to
// name using reservationRoot, the pinned handle its caller already holds,
// rather than re-resolving the reservation by path.
func restoreMovedAnalysisCacheReplacement(root, reservationRoot safeio.Root, reservation analysisCacheQuarantineReservation, name string, movedInfo fs.FileInfo) error {
	if movedInfo == nil {
		return nil
	}
	if _, err := root.Lstat(name); err == nil {
		return errors.New("rollback replacement restore target occupied: " + name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	err := safeio.RenameNoReplaceInto(reservationRoot, filepath.Base(reservation.quarantineName), root, name)
	if errors.Is(err, fs.ErrInvalid) {
		// reservationRoot does not support the pinned-source rename trait
		// (e.g. a test double). Fall back to the path-based rename rather
		// than failing every caller of this optional capability.
		err = safeio.RenameNoReplace(root, reservation.quarantineName, name)
	}
	if err != nil {
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

var quarantineTokenEntropySource = rand.Read

func generateAnalysisCacheQuarantineToken() (string, error) {
	var token [16]byte
	if _, err := quarantineTokenEntropySource(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
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
	if !analysisCacheQuarantineReservationSameFile(root, reservation) && !analysisCacheQuarantineReservationSameOpenedRoot(root, reservation) {
		return nil
	}
	if err := root.Remove(reservation.ownerName); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return removeAnalysisCacheQuarantineReservationDirectory(root, reservation)
}

func removeCreatedAnalysisCacheQuarantineReservation(root safeio.Root, reservation analysisCacheQuarantineReservation) error {
	if reservation.name == "." || reservation.name == string(filepath.Separator) {
		return nil
	}
	if !strings.HasPrefix(filepath.Base(reservation.name), ".lopper-cache-rollback-") {
		return nil
	}
	if reservation.info == nil || !analysisCacheQuarantineReservationSameOpenedRoot(root, reservation) {
		return nil
	}
	if err := root.Remove(reservation.ownerName); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
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
	info, statErr := file.Stat()
	var data []byte
	switch {
	case statErr != nil:
		err = statErr
	case !info.Mode().IsRegular():
		err = fmt.Errorf("owner file is not a regular file: %s", ownerName)
	default:
		data, err = io.ReadAll(io.LimitReader(file, int64(len(ownerToken))+1))
	}
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

func analysisCacheQuarantineReservationSameOpenedRoot(root safeio.Root, reservation analysisCacheQuarantineReservation) bool {
	return sameAnalysisCacheRollbackTarget(openedAnalysisCacheQuarantineReservationInfo(root, reservation), reservation.info)
}

func openedAnalysisCacheQuarantineReservationInfo(root safeio.Root, reservation analysisCacheQuarantineReservation) fs.FileInfo {
	if reservation.name == "" {
		return nil
	}
	reservationRoot, err := root.OpenRoot(reservation.name)
	if err != nil {
		return nil
	}
	info, infoErr := reservationRoot.Lstat(".")
	closeErr := reservationRoot.Close()
	if infoErr != nil || closeErr != nil {
		return nil
	}
	return info
}

func removeAnalysisCacheQuarantine(root safeio.Root, reservation analysisCacheQuarantineReservation) error {
	reservationRoot, err := openOwnedAnalysisCacheQuarantineReservation(root, reservation)
	if err != nil {
		return err
	}
	removeErr := reservationRoot.Remove(filepath.Base(reservation.quarantineName))
	if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		if isAnalysisCacheNonEmptyDirectoryError(removeErr) {
			// Another initializer added an entry to the quarantined child
			// after verifyAnalysisCacheQuarantine's post-rename emptiness
			// check but before this removal -- that window can't be closed
			// entirely (the removal itself is a separate call, possibly
			// much later). Restore it rather than leaving that
			// initializer's live data hidden inside the reservation, using
			// the reservation handle already open and identity-verified
			// above instead of reopening it by path.
			restoreErr := restoreQuarantinedChildAfterFailedRemoval(root, reservationRoot, reservation)
			closeErr := reservationRoot.Close()
			return errors.Join(removeErr, restoreErr, closeErr)
		}
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

// restoreQuarantinedChildAfterFailedRemoval moves a quarantined child that
// turned out to be non-empty back to its original name, so whatever another
// initializer added to it stays visible at its expected path instead of
// being hidden inside a reservation that cleanup can no longer remove.
func restoreQuarantinedChildAfterFailedRemoval(root, reservationRoot safeio.Root, reservation analysisCacheQuarantineReservation) error {
	quarantineBase := filepath.Base(reservation.quarantineName)
	movedInfo, err := reservationRoot.Lstat(quarantineBase)
	if err != nil {
		return err
	}
	return restoreMovedAnalysisCacheReplacement(root, reservationRoot, reservation, quarantineBase, movedInfo)
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
	if reservation.info == nil {
		return nil
	}
	if !analysisCacheQuarantineReservationSameFile(root, reservation) && !analysisCacheQuarantineReservationSameOpenedRoot(root, reservation) {
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
