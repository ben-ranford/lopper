package analysis

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

type cachePointer struct {
	InputDigest  string `json:"inputDigest"`
	ObjectDigest string `json:"objectDigest"`
}

type cachedPayload struct {
	Report                              report.Report              `json:"report"`
	UsageIncompleteDependencies         []int                      `json:"usageIncompleteDependencies,omitempty"`
	SuppressedUnusedImportsByDependency map[int][]report.ImportUse `json:"suppressedUnusedImportsByDependency,omitempty"`
}

func (c *analysisCache) lookup(entry cacheEntryDescriptor) (report.Report, bool, error) {
	if c == nil || !c.options.Enabled || !c.cacheable {
		return report.Report{}, false, nil
	}
	pointerPath := filepath.Join(c.options.Path, "keys", entry.KeyDigest+".json")
	rejected, err := c.rejectDefaultCacheRead(entry)
	if err != nil {
		return report.Report{}, false, err
	}
	if rejected {
		return report.Report{}, false, nil
	}
	pointerData, err := safeio.ReadFileUnder(c.options.Path, pointerPath)
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

	objectPath := filepath.Join(c.options.Path, "objects", pointer.ObjectDigest+".json")
	objectData, err := safeio.ReadFileUnder(c.options.Path, objectPath)
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
	if !payload.restoreUsageIncomplete() {
		c.metadata.Misses++
		c.metadata.Invalidations = append(c.metadata.Invalidations, report.CacheInvalidation{Key: entry.KeyLabel, Reason: "object-corrupt"})
		return report.Report{}, false, nil
	}
	if !payload.restoreSuppressedUnusedImports() {
		c.metadata.Misses++
		c.metadata.Invalidations = append(c.metadata.Invalidations, report.CacheInvalidation{Key: entry.KeyLabel, Reason: "object-corrupt"})
		return report.Report{}, false, nil
	}
	c.metadata.Hits++
	return payload.Report, true, nil
}

func (c *analysisCache) rejectDefaultCacheRead(entry cacheEntryDescriptor) (bool, error) {
	if !c.rejectReadHits {
		return false, nil
	}
	pointerExists, err := cachePointerExists(c.options.Path, entry.KeyDigest)
	if err != nil {
		return false, err
	}
	c.metadata.Misses++
	if pointerExists {
		c.metadata.Invalidations = append(c.metadata.Invalidations, report.CacheInvalidation{Key: entry.KeyLabel, Reason: "default-local-untrusted"})
	}
	return true, nil
}

func cachePointerExists(cachePath, keyDigest string) (_ bool, err error) {
	root, err := safeio.OpenRootNoFollow(cachePath)
	if err != nil {
		return false, err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()

	_, err = root.Lstat(filepath.Join("keys", keyDigest+".json"))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (c *analysisCache) store(entry cacheEntryDescriptor, data report.Report) error {
	if c == nil || !c.options.Enabled || !c.cacheable || c.options.ReadOnly {
		return nil
	}
	payload := newCachedPayload(data)
	serializedPayload, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	objectDigest := sha256Hex(serializedPayload)
	objectPath := filepath.Join(c.options.Path, "objects", objectDigest+".json")
	if _, err := os.Stat(objectPath); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if err := writeFileAtomic(objectPath, serializedPayload); err != nil {
			return err
		}
	}

	pointer := cachePointer{InputDigest: entry.InputDigest, ObjectDigest: objectDigest}
	serializedPointer, err := json.Marshal(pointer)
	if err != nil {
		return err
	}
	pointerPath := filepath.Join(c.options.Path, "keys", entry.KeyDigest+".json")
	if err := writeFileAtomic(pointerPath, serializedPointer); err != nil {
		return err
	}
	c.metadata.Writes++
	return nil
}

func newCachedPayload(data report.Report) cachedPayload {
	payload := cachedPayload{Report: data}
	for index := range data.Dependencies {
		if data.Dependencies[index].UsageIncomplete {
			payload.UsageIncompleteDependencies = append(payload.UsageIncompleteDependencies, index)
		}
		if data.Dependencies[index].UsageIncomplete && len(data.Dependencies[index].SuppressedUnusedImports) > 0 {
			if payload.SuppressedUnusedImportsByDependency == nil {
				payload.SuppressedUnusedImportsByDependency = make(map[int][]report.ImportUse)
			}
			payload.SuppressedUnusedImportsByDependency[index] = append([]report.ImportUse(nil), data.Dependencies[index].SuppressedUnusedImports...)
		}
	}
	return payload
}

func (p *cachedPayload) restoreUsageIncomplete() bool {
	for _, index := range p.UsageIncompleteDependencies {
		if index < 0 || index >= len(p.Report.Dependencies) {
			return false
		}
		p.Report.Dependencies[index].UsageIncomplete = true
	}
	return true
}

func (p *cachedPayload) restoreSuppressedUnusedImports() bool {
	for index, imports := range p.SuppressedUnusedImportsByDependency {
		if index < 0 || index >= len(p.Report.Dependencies) {
			return false
		}
		if !p.Report.Dependencies[index].UsageIncomplete {
			return false
		}
		p.Report.Dependencies[index].SuppressedUnusedImports = append([]report.ImportUse(nil), imports...)
	}
	return true
}
