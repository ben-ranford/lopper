package advisory

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Streaming validation retains only the count and a bounded ecosystem set.
// Reserve half the manifest budget for its other fields and cached snapshots.
const maxOSVZipEcosystemBytes = int(maxCacheManifestBytes / 2)

type osvZipInventory struct {
	ecosystems     map[string]struct{}
	ecosystemBytes int
	entryCount     int
}

func recordOSVJSONAdvisory(shape osvJSONAdvisoryShape, inventory *osvZipInventory) error {
	if err := requireUsableOSVJSONAdvisory(shape); err != nil {
		return err
	}
	if inventory != nil {
		inventory.entryCount++
	}
	return nil
}

func (i *osvZipInventory) readEcosystem(decoder *json.Decoder) error {
	var ecosystem string
	if err := decoder.Decode(&ecosystem); err != nil {
		return fmt.Errorf("read package ecosystem: %w", err)
	}
	return i.addEcosystem(ecosystem)
}

func (i *osvZipInventory) merge(other *osvZipInventory) error {
	if i == nil {
		return nil
	}
	for ecosystem := range other.ecosystems {
		if err := i.addEcosystem(ecosystem); err != nil {
			return err
		}
	}
	i.entryCount += other.entryCount
	return nil
}

func (i *osvZipInventory) addEcosystem(value string) error {
	ecosystem := strings.TrimSpace(value)
	if ecosystem == "" {
		return nil
	}
	if _, exists := i.ecosystems[ecosystem]; exists {
		return nil
	}
	// Count JSON escaping without allocating the encoded string. The allowance
	// covers indentation, separators, and set/sorted-output bookkeeping.
	cost := 2 + 128 // JSON string quotes and bookkeeping.
	remaining := maxOSVZipEcosystemBytes - i.ecosystemBytes
	for offset := 0; offset < len(ecosystem) && cost <= remaining; {
		r, size := utf8.DecodeRuneInString(ecosystem[offset:])
		cost += ecosystemJSONRuneBytes(r, size)
		offset += size
	}
	if cost > remaining {
		return fmt.Errorf("zip ecosystem metadata exceeds %d-byte manifest budget", maxOSVZipEcosystemBytes)
	}

	if i.ecosystems == nil {
		i.ecosystems = make(map[string]struct{})
	}
	i.ecosystems[strings.Clone(ecosystem)] = struct{}{}
	i.ecosystemBytes += cost
	return nil
}

func (i *osvZipInventory) sortedEcosystems() []string {
	ecosystems := make([]string, 0, len(i.ecosystems))
	for ecosystem := range i.ecosystems {
		ecosystems = append(ecosystems, ecosystem)
	}
	sort.Strings(ecosystems)
	return ecosystems
}

func (i *osvZipInventory) forSingleAdvisory() *osvZipInventory {
	if i == nil {
		return nil
	}
	return &osvZipInventory{}
}

func recordOSVJSONSingleAdvisory(shape osvJSONAdvisoryShape, inventory, objectInventory *osvZipInventory) error {
	if err := recordOSVJSONAdvisory(shape, objectInventory); err != nil {
		return err
	}
	return inventory.merge(objectInventory)
}

// Match encoding/json's HTML-safe string escaping, including invalid UTF-8.
func ecosystemJSONRuneBytes(r rune, size int) int {
	switch r {
	case '"', '\\', '\b', '\f', '\n', '\r', '\t':
		return 2
	case '<', '>', '&', '\u2028', '\u2029':
		return 6
	case utf8.RuneError:
		if size == 1 {
			return 6
		}
	}
	if r < 0x20 {
		return 6
	}
	return size
}
