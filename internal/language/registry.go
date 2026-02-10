package language

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

var (
	ErrUnknownLanguage   = errors.New("unknown language")
	ErrNoLanguageMatch   = errors.New("no language adapter matched")
	ErrMultipleLanguages = errors.New("multiple language adapters matched")
)

type Registry struct {
	adapters map[string]Adapter
}

func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]Adapter)}
}

func (r *Registry) Register(adapter Adapter) error {
	if adapter == nil {
		return errors.New("adapter is nil")
	}

	ids := append([]string{adapter.ID()}, adapter.Aliases()...)
	for _, id := range ids {
		key := normalizeID(id)
		if key == "" {
			return errors.New("adapter id cannot be empty")
		}
		if _, exists := r.adapters[key]; exists {
			return fmt.Errorf("adapter id already registered: %s", id)
		}
	}

	for _, id := range ids {
		r.adapters[normalizeID(id)] = adapter
	}

	return nil
}

func (r *Registry) Select(ctx context.Context, repoPath string, languageID string) (Adapter, error) {
	if r == nil {
		return nil, errors.New("language registry is nil")
	}

	languageID = normalizeID(languageID)
	if languageID == "" || languageID == Auto {
		return r.detect(ctx, repoPath)
	}

	adapter, ok := r.adapters[languageID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownLanguage, languageID)
	}
	return adapter, nil
}

func (r *Registry) IDs() []string {
	if r == nil {
		return nil
	}

	seen := make(map[Adapter]struct{})
	ids := make([]string, 0, len(r.adapters))
	for _, adapter := range r.adapters {
		if _, ok := seen[adapter]; ok {
			continue
		}
		seen[adapter] = struct{}{}
		ids = append(ids, adapter.ID())
	}

	sort.Strings(ids)
	return ids
}

func (r *Registry) detect(ctx context.Context, repoPath string) (Adapter, error) {
	if len(r.adapters) == 0 {
		return nil, ErrNoLanguageMatch
	}

	matches := make([]Adapter, 0, len(r.adapters))
	seen := make(map[Adapter]struct{})
	for _, adapter := range r.adapters {
		if _, ok := seen[adapter]; ok {
			continue
		}
		seen[adapter] = struct{}{}

		ok, err := adapter.Detect(ctx, repoPath)
		if err != nil {
			return nil, err
		}
		if ok {
			matches = append(matches, adapter)
		}
	}

	switch len(matches) {
	case 0:
		return nil, ErrNoLanguageMatch
	case 1:
		return matches[0], nil
	default:
		return nil, ErrMultipleLanguages
	}
}

func normalizeID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
