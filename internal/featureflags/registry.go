package featureflags

import (
	"fmt"
	"sort"
	"strings"
)

const featureCodePrefix = "LOP-FEAT-"
const maxFeatureCode = 9999

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
	Code        string    `json:"code" yaml:"code"`
	Name        string    `json:"name" yaml:"name"`
	Description string    `json:"description" yaml:"description"`
	Lifecycle   Lifecycle `json:"lifecycle" yaml:"lifecycle"`
}

type Registry struct {
	flags  []Flag
	byCode map[string]Flag
	byName map[string]Flag
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
		flags:  make([]Flag, 0, len(flags)),
		byCode: make(map[string]Flag, len(flags)),
		byName: make(map[string]Flag, len(flags)),
	}
	for _, flag := range flags {
		normalized, err := normalizeFlag(flag)
		if err != nil {
			return nil, err
		}
		if _, exists := registry.byCode[normalized.Code]; exists {
			return nil, fmt.Errorf("duplicate feature code: %s", normalized.Code)
		}
		if _, exists := registry.byName[normalized.Name]; exists {
			return nil, fmt.Errorf("duplicate feature name: %s", normalized.Name)
		}
		registry.flags = append(registry.flags, normalized)
		registry.byCode[normalized.Code] = normalized
		registry.byName[normalized.Name] = normalized
	}
	sort.Slice(registry.flags, func(i, j int) bool {
		return registry.flags[i].Code < registry.flags[j].Code
	})
	return registry, nil
}

func emptyRegistry() *Registry {
	return &Registry{
		flags:  []Flag{},
		byCode: map[string]Flag{},
		byName: map[string]Flag{},
	}
}

func (r *Registry) Flags() []Flag {
	if r == nil {
		return nil
	}
	flags := make([]Flag, len(r.flags))
	copy(flags, r.flags)
	return flags
}

func (r *Registry) Lookup(ref string) (Flag, bool) {
	if r == nil {
		return Flag{}, false
	}
	normalized := strings.TrimSpace(ref)
	if normalized == "" {
		return Flag{}, false
	}
	if flag, ok := r.byCode[normalized]; ok {
		return flag, true
	}
	flag, ok := r.byName[normalized]
	return flag, ok
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
	lifecycle, err := NormalizeLifecycle(string(flag.Lifecycle))
	if err != nil {
		return Flag{}, fmt.Errorf("feature %s: %w", flag.Code, err)
	}
	flag.Lifecycle = lifecycle
	return flag, nil
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
