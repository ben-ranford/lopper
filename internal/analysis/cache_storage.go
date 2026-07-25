package analysis

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	analysisCachePointerMaxBytes = 4 << 10
	analysisCacheObjectMaxBytes  = 8 << 20
)

var analysisCacheWriteFileFn = func(root *safeio.WriteRoot, path string, data []byte, perm, parentPerm os.FileMode) error {
	return root.WriteFileCreatingParents(path, data, perm, parentPerm)
}

type cachePointer struct {
	InputDigest  string `json:"inputDigest"`
	ObjectDigest string `json:"objectDigest"`
	Signature    string `json:"signature,omitempty"`
}

type cachedPayload struct {
	Report report.Report `json:"report"`
}

func (c *analysisCache) lookup(entry cacheEntryDescriptor) (report.Report, bool, error) {
	if c == nil || !c.options.Enabled || !c.cacheable {
		return report.Report{}, false, nil
	}
	storageRoot, err := c.lookupStorageRoot()
	if err != nil {
		return report.Report{}, false, err
	}
	if storageRoot == "" {
		return report.Report{}, false, nil
	}
	root, err := safeio.OpenCanonicalWriteRoot(storageRoot)
	if err != nil {
		return report.Report{}, false, err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	pointer, err := c.readTrustedPointer(root, entry)
	if err != nil || pointer == nil {
		return report.Report{}, false, err
	}
	payload, err := c.readCachedPayload(root, entry, *pointer)
	if err != nil || payload == nil {
		return report.Report{}, false, err
	}
	c.metadata.Hits++
	return payload.Report, true, nil
}

func (c *analysisCache) lookupStorageRoot() (string, error) {
	storageRoot := c.storageRoot
	if storageRoot == "" {
		storageRoot = c.options.Path
	}
	if canonicalStorageRoot, err := c.canonicalStorageRoot(); err == nil {
		storageRoot = canonicalStorageRoot
	}
	if !c.options.ReadOnly {
		return storageRoot, nil
	}
	if _, err := os.Stat(storageRoot); os.IsNotExist(err) {
		c.metadata.Misses++
		return "", nil
	} else if err != nil {
		return "", err
	}
	return storageRoot, nil
}

func (c *analysisCache) lookupPointerReadError(entry cacheEntryDescriptor, err error) (report.Report, bool, error) {
	switch {
	case os.IsNotExist(err):
		c.metadata.Misses++
		return report.Report{}, false, nil
	case errors.Is(err, safeio.ErrFileTooLarge):
		return c.lookupMiss(entry, "pointer-oversized"), false, nil
	default:
		return c.lookupMiss(entry, "pointer-read-error"), false, nil
	}
}

func (c *analysisCache) readTrustedPointer(root *safeio.WriteRoot, entry cacheEntryDescriptor) (*cachePointer, error) {
	pointerPath := filepath.Join("keys", entry.KeyDigest+".json")
	pointerData, _, err := root.ReadPinnedRegularFileUnderLimit(pointerPath, c.storageRootInfo, analysisCachePointerMaxBytes)
	if err != nil {
		_, _, missErr := c.lookupPointerReadError(entry, err)
		return nil, missErr
	}
	var pointer cachePointer
	if err := json.Unmarshal(pointerData, &pointer); err != nil {
		c.lookupMiss(entry, "pointer-corrupt")
		return nil, nil
	}
	if pointer.InputDigest != entry.InputDigest {
		c.lookupMiss(entry, "input-changed")
		return nil, nil
	}
	trusted, err := c.pointerTrusted(entry, pointer)
	if err != nil {
		return nil, err
	}
	if !trusted {
		c.lookupMiss(entry, "pointer-untrusted")
		return nil, nil
	}
	return &pointer, nil
}

func (c *analysisCache) lookupObjectReadError(entry cacheEntryDescriptor, err error) (report.Report, bool, error) {
	reason := "object-read-error"
	if os.IsNotExist(err) {
		reason = "object-missing"
	} else if errors.Is(err, safeio.ErrFileTooLarge) {
		reason = "object-oversized"
	}
	return c.lookupMiss(entry, reason), false, nil
}

func (c *analysisCache) readCachedPayload(root *safeio.WriteRoot, entry cacheEntryDescriptor, pointer cachePointer) (*cachedPayload, error) {
	objectPath := filepath.Join("objects", pointer.ObjectDigest+".json")
	objectData, _, err := root.ReadPinnedRegularFileUnderLimit(objectPath, c.storageRootInfo, analysisCacheObjectMaxBytes)
	if err != nil {
		_, _, missErr := c.lookupObjectReadError(entry, err)
		return nil, missErr
	}
	if sha256Hex(objectData) != pointer.ObjectDigest {
		c.lookupMiss(entry, "object-tampered")
		return nil, nil
	}
	var payload cachedPayload
	if err := json.Unmarshal(objectData, &payload); err != nil {
		c.lookupMiss(entry, "object-corrupt")
		return nil, nil
	}
	return &payload, nil
}

func (c *analysisCache) lookupMiss(entry cacheEntryDescriptor, reason string) report.Report {
	c.metadata.Misses++
	if reason != "" {
		c.metadata.Invalidations = append(c.metadata.Invalidations, report.CacheInvalidation{Key: entry.KeyLabel, Reason: reason})
	}
	return report.Report{}
}

func (c *analysisCache) openPinnedStorageRoot() (_ *safeio.WriteRoot, returnErr error) {
	storageRoot, err := c.canonicalStorageRoot()
	if err != nil {
		return nil, err
	}
	root, err := analysisCacheOpenRootFn(storageRoot)
	if err != nil {
		return nil, err
	}
	if c.storageRootInfo == nil {
		return root, nil
	}
	currentInfo, err := analysisCacheRootLstatFn(root, ".")
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	if !analysisCacheSameFileFn(c.storageRootInfo, currentInfo) {
		return nil, errors.Join(fmt.Errorf("%w: %s", safeio.ErrFileChanged, storageRoot), root.Close())
	}
	return root, nil
}

func (c *analysisCache) store(entry cacheEntryDescriptor, data report.Report) (returnErr error) {
	if c == nil || !c.options.Enabled || !c.cacheable || c.options.ReadOnly {
		return nil
	}
	payload := cachedPayload{Report: data}
	serializedPayload, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	objectDigest := sha256Hex(serializedPayload)

	signature, err := c.signPointer(entry, objectDigest)
	if err != nil {
		return err
	}
	pointer := cachePointer{InputDigest: entry.InputDigest, ObjectDigest: objectDigest, Signature: signature}
	serializedPointer, err := json.Marshal(pointer)
	if err != nil {
		return err
	}

	root, err := c.openPinnedStorageRoot()
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, root.Close())
	}()
	objectPath := filepath.Join("objects", objectDigest+".json")
	if err := analysisCacheWriteFileFn(root, objectPath, serializedPayload, 0o600, 0o750); err != nil {
		return fmt.Errorf("write cache object: %w", err)
	}
	pointerPath := filepath.Join("keys", entry.KeyDigest+".json")
	if err := analysisCacheWriteFileFn(root, pointerPath, serializedPointer, 0o600, 0o750); err != nil {
		return fmt.Errorf("write cache pointer: %w", err)
	}
	c.metadata.Writes++
	return nil
}
