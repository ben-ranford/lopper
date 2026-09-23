package advisory

import (
	"fmt"
	"sort"
	"strings"
)

// Each ZIP entry is parsed independently within the existing JSON entry limit.
// Retain only a bounded ecosystem set and the advisory count across entries.
type osvZipInventory struct {
	ecosystems     map[string]struct{}
	ecosystemBytes int
	entryCount     int
}

func (i *osvZipInventory) add(payload []byte) error {
	if i.ecosystems == nil {
		i.ecosystems = make(map[string]struct{})
	}
	advisories := snapshotOSVAdvisories(payload)
	i.entryCount += len(advisories)
	for _, advisory := range advisories {
		for _, affected := range advisory.Affected {
			if err := i.addEcosystem(affected.Package.Ecosystem); err != nil {
				return err
			}
		}
	}
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
	// Include a conservative per-item allowance for the map and sorted output.
	cost := len(ecosystem) + 128
	if cost > maxSyncMetadataBytes-i.ecosystemBytes {
		return fmt.Errorf("zip ecosystem metadata exceeds %d-byte limit", maxSyncMetadataBytes)
	}
	i.ecosystems[ecosystem] = struct{}{}
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
