// Package references provides deterministic allocation for string references.
package references

import "strconv"

// Allocator reserves base references before allocating unique values.
type Allocator struct {
	reserved map[string]struct{}
	used     map[string]struct{}
}

// NewAllocator creates an allocator that preserves any supplied base or
// suffixed reference during collision resolution.
func NewAllocator(bases []string) Allocator {
	reserved := make(map[string]struct{}, len(bases))
	for _, base := range bases {
		reserved[base] = struct{}{}
	}
	return Allocator{
		reserved: reserved,
		used:     make(map[string]struct{}, len(bases)),
	}
}

// Allocate returns base on its first use and otherwise the first available
// numeric suffix that is neither reserved nor used.
func (a *Allocator) Allocate(base string) string {
	if _, exists := a.used[base]; !exists {
		a.used[base] = struct{}{}
		return base
	}
	for suffix := 2; ; suffix++ {
		candidate := base + ":" + strconv.Itoa(suffix)
		if _, reserved := a.reserved[candidate]; reserved {
			continue
		}
		if _, exists := a.used[candidate]; exists {
			continue
		}
		a.used[candidate] = struct{}{}
		return candidate
	}
}
