package analysis

import (
	"encoding/json"
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
	Report report.Report `json:"report"`
}

func (c *analysisCache) lookup(entry cacheEntryDescriptor) (report.Report, bool, error) {
	if c == nil || !c.options.Enabled || !c.cacheable {
		return report.Report{}, false, nil
	}
	pointerPath := filepath.Join(c.options.Path, "keys", entry.KeyDigest+".json")
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
	c.metadata.Hits++
	return payload.Report, true, nil
}

func (c *analysisCache) store(entry cacheEntryDescriptor, data report.Report) error {
	if c == nil || !c.options.Enabled || !c.cacheable || c.options.ReadOnly {
		return nil
	}
	payload := cachedPayload{Report: data}
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
