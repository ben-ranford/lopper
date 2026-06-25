package featureflags

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const featureCodePrefix = "LOP-FEAT-"
const maxFeatureCode = 9999

var stableReleaseVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)

type Lifecycle string

const (
	LifecyclePreview Lifecycle = "preview"
	LifecycleStable  Lifecycle = "stable"
)

type Channel string

const (
	ChannelDev     Channel = "dev"
	ChannelRolling Channel = "rolling"
	ChannelRelease Channel = "release"
)

type Flag struct {
	Code               string    `json:"code" yaml:"code"`
	Name               string    `json:"name" yaml:"name"`
	DeprecatedNames    []string  `json:"deprecatedNames,omitempty" yaml:"deprecatedNames,omitempty"`
	Description        string    `json:"description" yaml:"description"`
	Lifecycle          Lifecycle `json:"lifecycle" yaml:"lifecycle"`
	FirstStableRelease string    `json:"firstStableRelease,omitempty" yaml:"firstStableRelease,omitempty"`
}

type LookupResult struct {
	Flag           Flag
	Reference      string
	Deprecated     bool
	ReplacementRef string
}

type Registry struct {
	flags            []Flag
	byCode           map[string]Flag
	byName           map[string]Flag
	deprecatedByName map[string]Flag
}

func DefaultRegistry() *Registry {
	if defaultRegistryErr != nil {
		return emptyRegistry()
	}
	return defaultRegistry
}

func ValidateDefaultRegistry() error {
	return defaultRegistryErr
}

func NewRegistry(flags []Flag) (*Registry, error) {
	registry := &Registry{
		flags:            make([]Flag, 0, len(flags)),
		byCode:           make(map[string]Flag, len(flags)),
		byName:           make(map[string]Flag, len(flags)),
		deprecatedByName: make(map[string]Flag),
	}
	for _, flag := range flags {
		normalized, err := normalizeFlag(flag)
		if err != nil {
			return nil, err
		}
		if _, exists := registry.byCode[normalized.Code]; exists {
			return nil, fmt.Errorf("duplicate feature code: %s", normalized.Code)
		}
		if err := registry.registerFeatureName(normalized.Name, normalized, false); err != nil {
			return nil, err
		}
		registry.flags = append(registry.flags, normalized)
		registry.byCode[normalized.Code] = normalized
		for _, deprecatedName := range normalized.DeprecatedNames {
			if err := registry.registerFeatureName(deprecatedName, normalized, true); err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(registry.flags, func(i, j int) bool {
		return registry.flags[i].Code < registry.flags[j].Code
	})
	return registry, nil
}

func (r *Registry) registerFeatureName(name string, flag Flag, deprecated bool) error {
	if _, exists := r.byName[name]; exists {
		return fmt.Errorf("duplicate feature name: %s", name)
	}
	copied := cloneFlag(flag)
	r.byName[name] = copied
	if deprecated {
		r.deprecatedByName[name] = copied
	}
	return nil
}

func emptyRegistry() *Registry {
	return &Registry{
		flags:            []Flag{},
		byCode:           map[string]Flag{},
		byName:           map[string]Flag{},
		deprecatedByName: map[string]Flag{},
	}
}

func (r *Registry) Flags() []Flag {
	if r == nil {
		return nil
	}
	flags := make([]Flag, len(r.flags))
	for i, flag := range r.flags {
		flags[i] = cloneFlag(flag)
	}
	return flags
}

func (r *Registry) Lookup(ref string) (Flag, bool) {
	result, ok := r.LookupReference(ref)
	if !ok {
		return Flag{}, false
	}
	return result.Flag, true
}

func (r *Registry) LookupReference(ref string) (LookupResult, bool) {
	if r == nil {
		return LookupResult{}, false
	}
	normalized := strings.TrimSpace(ref)
	if normalized == "" {
		return LookupResult{}, false
	}
	if flag, ok := r.byCode[normalized]; ok {
		return LookupResult{Flag: cloneFlag(flag), Reference: normalized}, true
	}
	flag, ok := r.byName[normalized]
	if !ok {
		return LookupResult{}, false
	}
	result := LookupResult{Flag: cloneFlag(flag), Reference: normalized}
	if _, deprecated := r.deprecatedByName[normalized]; deprecated {
		result.Deprecated = true
		result.ReplacementRef = flag.Name
	}
	return result, true
}

func (r *Registry) NextCode() (string, error) {
	if r == nil {
		r = DefaultRegistry()
	}
	highest := 0
	for _, flag := range r.flags {
		value, err := featureCodeNumber(flag.Code)
		if err != nil {
			return "", err
		}
		if value > highest {
			highest = value
		}
	}
	if highest >= maxFeatureCode {
		return "", fmt.Errorf("feature code space exhausted")
	}
	return fmt.Sprintf("%s%04d", featureCodePrefix, highest+1), nil
}

func NormalizeLifecycle(value string) (Lifecycle, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(LifecyclePreview), "experimental":
		return LifecyclePreview, nil
	case string(LifecycleStable), "done":
		return LifecycleStable, nil
	default:
		return "", fmt.Errorf("invalid feature lifecycle: %q", value)
	}
}

func NormalizeChannel(value string) (Channel, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", string(ChannelDev):
		return ChannelDev, nil
	case string(ChannelRolling):
		return ChannelRolling, nil
	case string(ChannelRelease):
		return ChannelRelease, nil
	default:
		return "", fmt.Errorf("invalid feature build channel: %q", value)
	}
}

func normalizeFlag(flag Flag) (Flag, error) {
	flag.Code = strings.TrimSpace(flag.Code)
	flag.Name = strings.TrimSpace(flag.Name)
	flag.Description = strings.TrimSpace(flag.Description)
	if err := validateFeatureCode(flag.Code); err != nil {
		return Flag{}, err
	}
	if err := validateFeatureName(flag.Name); err != nil {
		return Flag{}, err
	}
	deprecatedNames, err := normalizeDeprecatedFeatureNames(flag.Name, flag.DeprecatedNames)
	if err != nil {
		return Flag{}, err
	}
	flag.DeprecatedNames = deprecatedNames
	lifecycle, err := NormalizeLifecycle(string(flag.Lifecycle))
	if err != nil {
		return Flag{}, fmt.Errorf("feature %s: %w", flag.Code, err)
	}
	flag.Lifecycle = lifecycle
	firstStableRelease, err := normalizeStableReleaseVersion(flag.FirstStableRelease)
	if err != nil {
		return Flag{}, fmt.Errorf("feature %s: %w", flag.Code, err)
	}
	flag.FirstStableRelease = firstStableRelease
	return flag, nil
}

func normalizeDeprecatedFeatureNames(canonicalName string, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if err := validateFeatureName(name); err != nil {
			return nil, err
		}
		if name == canonicalName {
			return nil, fmt.Errorf("deprecated feature name %q duplicates canonical name", name)
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		normalized = append(normalized, name)
	}
	if len(normalized) == 0 {
		return nil, nil
	}
	return normalized, nil
}

func normalizeStableReleaseVersion(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(strings.ToLower(value), "v") {
		value = "v" + strings.TrimSpace(value[1:])
	} else {
		value = "v" + value
	}
	if !stableReleaseVersionPattern.MatchString(value) {
		return "", fmt.Errorf("invalid first stable release %q: must use vMAJOR.MINOR.PATCH", value)
	}
	return value, nil
}

func validateFeatureCode(code string) error {
	_, err := featureCodeNumber(code)
	return err
}

func featureCodeNumber(code string) (int, error) {
	if code == "" {
		return 0, fmt.Errorf("feature code is required")
	}
	if !strings.HasPrefix(code, featureCodePrefix) {
		return 0, fmt.Errorf("invalid feature code %q: must use %sNNNN", code, featureCodePrefix)
	}
	suffix := strings.TrimPrefix(code, featureCodePrefix)
	if len(suffix) != 4 {
		return 0, fmt.Errorf("invalid feature code %q: must use %sNNNN", code, featureCodePrefix)
	}
	value := 0
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid feature code %q: suffix must be numeric", code)
		}
		value = value*10 + int(r-'0')
	}
	return value, nil
}

func validateFeatureName(name string) error {
	if name == "" {
		return fmt.Errorf("feature name is required")
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return fmt.Errorf("invalid feature name %q: use lowercase letters, digits, and hyphens", name)
	}
	return nil
}

func cloneFlag(flag Flag) Flag {
	if len(flag.DeprecatedNames) > 0 {
		flag.DeprecatedNames = append([]string{}, flag.DeprecatedNames...)
	}
	return flag
}
