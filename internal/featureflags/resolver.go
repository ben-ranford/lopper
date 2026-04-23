package featureflags

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type ResolveOptions struct {
	Channel Channel
	Lock    *ReleaseLock
	Enable  []string
	Disable []string
}

type Set struct {
	enabled map[string]bool
	byCode  map[string]Flag
	byName  map[string]Flag
}

type ManifestEntry struct {
	Code             string    `json:"code"`
	Name             string    `json:"name"`
	Description      string    `json:"description,omitempty"`
	Lifecycle        Lifecycle `json:"lifecycle"`
	EnabledByDefault bool      `json:"enabledByDefault"`
}

func (r *Registry) Resolve(opts ResolveOptions) (Set, error) {
	if r == nil {
		r = DefaultRegistry()
	}
	channel, err := NormalizeChannel(string(opts.Channel))
	if err != nil {
		return Set{}, err
	}
	if err := r.ValidateReleaseLock(opts.Lock); err != nil {
		return Set{}, err
	}

	enabled := r.channelDefaults(channel)
	r.applyReleaseLockDefaults(channel, opts.Lock, enabled)
	if err := r.applyExplicitOverrides(enabled, opts.Enable, opts.Disable); err != nil {
		return Set{}, err
	}

	return Set{
		enabled: enabled,
		byCode:  copyFlagMap(r.byCode),
		byName:  copyFlagMap(r.byName),
	}, nil
}

func (r *Registry) channelDefaults(channel Channel) map[string]bool {
	enabled := make(map[string]bool, len(r.flags))
	enableAll := channel == ChannelRolling
	for _, flag := range r.flags {
		enabled[flag.Code] = enableAll || flag.Lifecycle == LifecycleStable
	}
	return enabled
}

func (r *Registry) applyReleaseLockDefaults(channel Channel, lock *ReleaseLock, enabled map[string]bool) {
	if channel != ChannelRelease || lock == nil {
		return
	}
	for _, ref := range lock.DefaultOn {
		flag, _ := r.Lookup(ref)
		enabled[flag.Code] = true
	}
}

func (r *Registry) applyExplicitOverrides(enabled map[string]bool, enable []string, disable []string) error {
	explicitEnable, err := r.resolveRefs(enable)
	if err != nil {
		return err
	}
	explicitDisable, err := r.resolveRefs(disable)
	if err != nil {
		return err
	}
	for code := range explicitEnable {
		if _, disabled := explicitDisable[code]; disabled {
			return fmt.Errorf("feature %s is both enabled and disabled", code)
		}
		enabled[code] = true
	}
	for code := range explicitDisable {
		enabled[code] = false
	}
	return nil
}

func (r *Registry) Manifest(opts ResolveOptions) ([]ManifestEntry, error) {
	if r == nil {
		r = DefaultRegistry()
	}
	resolved, err := r.Resolve(opts)
	if err != nil {
		return nil, err
	}
	entries := make([]ManifestEntry, 0, len(r.flags))
	for _, flag := range r.flags {
		entries = append(entries, ManifestEntry{
			Code:             flag.Code,
			Name:             flag.Name,
			Description:      flag.Description,
			Lifecycle:        flag.Lifecycle,
			EnabledByDefault: resolved.Enabled(flag.Code),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Code < entries[j].Code
	})
	return entries, nil
}

func FormatManifest(manifest []ManifestEntry) ([]byte, error) {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal feature manifest: %w", err)
	}
	return append(data, '\n'), nil
}

func (s *Set) Enabled(ref string) bool {
	flag, ok := s.lookup(ref)
	if !ok {
		return false
	}
	return s.enabled[flag.Code]
}

func (s *Set) EnabledCodes() []string {
	if s == nil || len(s.enabled) == 0 {
		return nil
	}
	codes := make([]string, 0, len(s.enabled))
	for code, enabled := range s.enabled {
		if enabled {
			codes = append(codes, code)
		}
	}
	sort.Strings(codes)
	return codes
}

func (s *Set) EnabledFlag(ref string) (bool, error) {
	flag, ok := s.lookup(ref)
	if !ok {
		return false, fmt.Errorf("unknown feature: %s", ref)
	}
	return s.enabled[flag.Code], nil
}

func (s *Set) Snapshot() map[string]bool {
	if s == nil || len(s.enabled) == 0 {
		return nil
	}
	snapshot := make(map[string]bool, len(s.enabled))
	for code, enabled := range s.enabled {
		snapshot[code] = enabled
	}
	return snapshot
}

func (s *Set) lookup(ref string) (Flag, bool) {
	ref = strings.TrimSpace(ref)
	if s == nil {
		return Flag{}, false
	}
	if flag, ok := s.byCode[ref]; ok {
		return flag, true
	}
	flag, ok := s.byName[ref]
	return flag, ok
}

func (r *Registry) resolveRefs(refs []string) (map[string]struct{}, error) {
	resolved := map[string]struct{}{}
	for _, ref := range normalizeRefs(refs) {
		flag, ok := r.Lookup(ref)
		if !ok {
			return nil, fmt.Errorf("unknown feature: %s", ref)
		}
		resolved[flag.Code] = struct{}{}
	}
	return resolved, nil
}

func copyFlagMap(source map[string]Flag) map[string]Flag {
	copied := make(map[string]Flag, len(source))
	for key, value := range source {
		copied[key] = value
	}
	return copied
}
