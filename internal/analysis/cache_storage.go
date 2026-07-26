package analysis

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

var (
	cacheStoreBeforeRootOpenFn = func() error { return nil }
	cacheLookupBeforeReadFn    = func() error { return nil }
)

type cachePointer struct {
	InputDigest  string `json:"inputDigest"`
	ObjectDigest string `json:"objectDigest"`
}

type cachedPayload struct {
	Report report.Report `json:"report"`
}

func (c *analysisCache) lookup(entry cacheEntryDescriptor) (_ report.Report, hit bool, err error) {
	if c == nil || !c.options.Enabled || !c.cacheable {
		return report.Report{}, false, nil
	}
	root, err := c.openPinnedWriteRoot()
	if err != nil {
		return report.Report{}, false, err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()

	pointerPath := filepath.Join("keys", entry.KeyDigest+".json")
	pointerData, err := c.readCacheFile(root, pointerPath)
	if err != nil {
		if os.IsNotExist(err) {
			c.metadata.Misses++
			return report.Report{}, false, nil
		}
		return report.Report{}, false, err
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

	objectPath := filepath.Join("objects", pointer.ObjectDigest+".json")
	objectData, err := c.readCacheFile(root, objectPath)
	if err != nil {
		c.metadata.Misses++
		reason := "object-read-error"
		if os.IsNotExist(err) {
			reason = "object-missing"
		}
		c.metadata.Invalidations = append(c.metadata.Invalidations, report.CacheInvalidation{Key: entry.KeyLabel, Reason: reason})
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

func (c *analysisCache) store(entry cacheEntryDescriptor, data report.Report) (err error) {
	if c == nil || !c.options.Enabled || !c.cacheable || c.options.ReadOnly {
		return nil
	}
	payload := cachedPayload{Report: data}
	serializedPayload, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	objectDigest := sha256Hex(serializedPayload)
	root, err := c.openStoreWriteRoot()
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()

	objectPath := filepath.Join("objects", objectDigest+".json")
	if _, err := root.Lstat(objectPath); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if err := root.WriteFileReplacingParents(objectPath, serializedPayload, 0o600, 0o750); err != nil {
			return err
		}
	}

	pointer := cachePointer{InputDigest: entry.InputDigest, ObjectDigest: objectDigest}
	serializedPointer, err := json.Marshal(pointer)
	if err != nil {
		return err
	}
	pointerPath := filepath.Join("keys", entry.KeyDigest+".json")
	if err := root.WriteFileReplacingParents(pointerPath, serializedPointer, 0o600, 0o750); err != nil {
		return err
	}
	c.metadata.Writes++
	return nil
}

func (c *analysisCache) openStoreWriteRoot() (*safeio.WriteRoot, error) {
	if err := c.validateWriteRoot(); err != nil {
		return nil, err
	}
	if err := cacheStoreBeforeRootOpenFn(); err != nil {
		return nil, err
	}
	return c.openPinnedWriteRoot()
}

func (c *analysisCache) openPinnedWriteRoot() (_ *safeio.WriteRoot, err error) {
	if c == nil {
		return nil, errors.New("analysis cache is required")
	}
	if c.writeRootOpener != nil {
		root, err := c.writeRootOpener()
		if err != nil {
			return nil, err
		}
		if err := c.validateOpenedWriteRoot(root); err != nil {
			return nil, errors.Join(err, root.Close())
		}
		return root, nil
	}
	rootPath := c.pinnedWritePath()
	if c.writeRootInfo == nil {
		rootPath, err = resolvePathWithinExistingTree(rootPath)
		if err != nil {
			return nil, err
		}
	}
	root, err := safeio.OpenCanonicalWriteRoot(rootPath)
	if err != nil {
		return nil, err
	}
	if err := c.validateOpenedWriteRoot(root); err != nil {
		if closeErr := root.Close(); closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
		return nil, err
	}
	return root, nil
}

func (c *analysisCache) validateOpenedWriteRoot(root *safeio.WriteRoot) error {
	if c == nil || c.writeRootInfo == nil {
		return nil
	}
	info, err := root.Lstat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(c.writeRootInfo, info) {
		return errors.New("cache root changed while pinned")
	}
	return nil
}

func (c *analysisCache) readCacheFile(root *safeio.WriteRoot, path string) ([]byte, error) {
	if err := cacheLookupBeforeReadFn(); err != nil {
		return nil, err
	}
	if err := c.validateWriteRoot(); err != nil {
		return nil, err
	}
	if err := c.validateOpenedWriteRoot(root); err != nil {
		return nil, err
	}
	return root.ReadFile(path)
}
