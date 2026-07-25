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
	storageRoot := c.storageRoot
	if storageRoot == "" {
		storageRoot = c.options.Path
	}
	canonicalStorageRoot, err := c.canonicalStorageRoot()
	if err == nil {
		storageRoot = canonicalStorageRoot
	}
	if c.options.ReadOnly {
		if _, err := os.Stat(storageRoot); os.IsNotExist(err) {
			c.metadata.Misses++
			return report.Report{}, false, nil
		} else if err != nil {
			return report.Report{}, false, err
		}
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

	pointerPath := filepath.Join("keys", entry.KeyDigest+".json")
	pointerData, _, readErr := root.ReadPinnedRegularFileUnderLimit(pointerPath, c.storageRootInfo, analysisCachePointerMaxBytes)
	err = readErr
	if err != nil {
		if os.IsNotExist(err) {
			c.metadata.Misses++
			return report.Report{}, false, nil
		}
		if errors.Is(err, safeio.ErrFileTooLarge) {
			c.metadata.Misses++
			c.metadata.Invalidations = append(c.metadata.Invalidations, report.CacheInvalidation{Key: entry.KeyLabel, Reason: "pointer-oversized"})
			return report.Report{}, false, nil
		}
		c.metadata.Misses++
		c.metadata.Invalidations = append(c.metadata.Invalidations, report.CacheInvalidation{Key: entry.KeyLabel, Reason: "pointer-read-error"})
		return report.Report{}, false, nil
	}
	var pointer cachePointer
	if err = json.Unmarshal(pointerData, &pointer); err != nil {
		c.metadata.Misses++
		c.metadata.Invalidations = append(c.metadata.Invalidations, report.CacheInvalidation{Key: entry.KeyLabel, Reason: "pointer-corrupt"})
		return report.Report{}, false, nil
	}
	if pointer.InputDigest != entry.InputDigest {
		c.metadata.Misses++
		c.metadata.Invalidations = append(c.metadata.Invalidations, report.CacheInvalidation{Key: entry.KeyLabel, Reason: "input-changed"})
		return report.Report{}, false, nil
	}
	trusted, err := c.pointerTrusted(entry, pointer)
	if err != nil {
		return report.Report{}, false, err
	}
	if !trusted {
		c.metadata.Misses++
		c.metadata.Invalidations = append(c.metadata.Invalidations, report.CacheInvalidation{Key: entry.KeyLabel, Reason: "pointer-untrusted"})
		return report.Report{}, false, nil
	}

	objectPath := filepath.Join("objects", pointer.ObjectDigest+".json")
	objectData, _, readErr := root.ReadPinnedRegularFileUnderLimit(objectPath, c.storageRootInfo, analysisCacheObjectMaxBytes)
	err = readErr
	if err != nil {
		c.metadata.Misses++
		reason := "object-read-error"
		if os.IsNotExist(err) {
			reason = "object-missing"
		} else if errors.Is(err, safeio.ErrFileTooLarge) {
			reason = "object-oversized"
		}
		c.metadata.Invalidations = append(c.metadata.Invalidations, report.CacheInvalidation{Key: entry.KeyLabel, Reason: reason})
		return report.Report{}, false, nil
	}
	if sha256Hex(objectData) != pointer.ObjectDigest {
		c.metadata.Misses++
		c.metadata.Invalidations = append(c.metadata.Invalidations, report.CacheInvalidation{Key: entry.KeyLabel, Reason: "object-tampered"})
		return report.Report{}, false, nil
	}

	var payload cachedPayload
	if err = json.Unmarshal(objectData, &payload); err != nil {
		c.metadata.Misses++
		c.metadata.Invalidations = append(c.metadata.Invalidations, report.CacheInvalidation{Key: entry.KeyLabel, Reason: "object-corrupt"})
		return report.Report{}, false, nil
	}
	c.metadata.Hits++
	return payload.Report, true, nil
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
