package language

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

var (
	ErrUnknown           = errors.New("unknown language")
	ErrNoMatch           = errors.New("no language adapter matched")
	ErrMultipleLanguages = errors.New("multiple language adapters matched")
)

type Registry struct {
	adapters map[string]Adapter
}

type AdapterFilter func(Adapter) bool

func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]Adapter)}
}

func (r *Registry) Register(adapter Adapter) error {
	if r == nil {
		return errors.New("language registry is nil")
	}

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

func (r *Registry) Select(ctx context.Context, repoPath string, languageID string) (CandidateAdapter, error) {
	candidates, err := r.Resolve(ctx, repoPath, languageID)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, ErrNoMatch
	}
	return candidates[0].Adapter, nil
}

func (r *Registry) Resolve(ctx context.Context, repoPath string, languageID string) ([]Candidate, error) {
	return r.ResolveWithFilter(ctx, repoPath, languageID, nil)
}

func (r *Registry) ResolveWithFilter(ctx context.Context, repoPath string, languageID string, filter AdapterFilter) ([]Candidate, error) {
	if r == nil {
		return nil, errors.New("language registry is nil")
	}

	languageID = normalizeID(languageID)
	if languageID == "" || languageID == Auto {
		return r.resolveAuto(ctx, repoPath, filter)
	}
	if languageID == All {
		matches, err := r.detectMatches(ctx, repoPath, filter)
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, ErrNoMatch
		}
		return matches, nil
	}

	adapter, ok := r.adapters[languageID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknown, languageID)
	}
	if !adapterAllowed(adapter, filter) {
		return nil, ErrNoMatch
	}
	detection, err := detectAdapter(ctx, adapter, repoPath)
	if err != nil {
		return nil, err
	}
	if !detection.Matched {
		detection = normalizeDetection(repoPath, Detection{
			Matched:    true,
			Confidence: 100,
		})
	}
	return []Candidate{{Adapter: adapter, Detection: detection}}, nil
}

func (r *Registry) IDs() []string {
	if r == nil {
		return nil
	}

	seen := make(map[string]struct{})
	ids := make([]string, 0, len(r.adapters))
	for _, adapter := range r.adapters {
		adapterID := adapter.ID()
		if _, ok := seen[adapterID]; ok {
			continue
		}
		seen[adapterID] = struct{}{}
		ids = append(ids, adapterID)
	}

	sort.Strings(ids)
	return ids
}

func (r *Registry) resolveAuto(ctx context.Context, repoPath string, filter AdapterFilter) ([]Candidate, error) {
	matches, err := r.detectMatches(ctx, repoPath, filter)
	if err != nil {
		return nil, err
	}
	switch len(matches) {
	case 0:
		return nil, ErrNoMatch
	case 1:
		return matches[:1], nil
	default:
		if matches[0].Detection.Confidence == matches[1].Detection.Confidence {
			return nil, ErrMultipleLanguages
		}
		return matches[:1], nil
	}
}

func (r *Registry) detectMatches(ctx context.Context, repoPath string, filter AdapterFilter) ([]Candidate, error) {
	if len(r.adapters) == 0 {
		return nil, nil
	}

	matches := make([]Candidate, 0, len(r.adapters))
	seen := make(map[string]struct{})
	for _, adapter := range r.adapters {
		adapterID := adapter.ID()
		if _, ok := seen[adapterID]; ok {
			continue
		}
		seen[adapterID] = struct{}{}
		if !adapterAllowed(adapter, filter) {
			continue
		}

		detection, err := detectAdapter(ctx, adapter, repoPath)
		if err != nil {
			return nil, err
		}
		if detection.Matched {
			matches = append(matches, Candidate{
				Adapter:   adapter,
				Detection: detection,
			})
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Detection.Confidence == matches[j].Detection.Confidence {
			return matches[i].Adapter.ID() < matches[j].Adapter.ID()
		}
		return matches[i].Detection.Confidence > matches[j].Detection.Confidence
	})
	return matches, nil
}

func detectAdapter(ctx context.Context, adapter Detector, repoPath string) (Detection, error) {
	if detector, ok := adapter.(ConfidenceProvider); ok {
		detection, err := detector.DetectWithConfidence(ctx, repoPath)
		if err != nil {
			return Detection{}, err
		}
		detection.Confidence = clampConfidence(detection.Confidence)
		return normalizeDetection(repoPath, detection), nil
	}

	ok, err := adapter.Detect(ctx, repoPath)
	if err != nil {
		return Detection{}, err
	}
	if !ok {
		return Detection{Matched: false}, nil
	}
	return normalizeDetection(repoPath, Detection{
		Matched:    true,
		Confidence: 60,
	}), nil
}

func normalizeDetection(repoPath string, detection Detection) Detection {
	if detection.Matched && detection.Confidence == 0 {
		detection.Confidence = 1
	}
	if len(detection.Roots) == 0 && repoPath != "" {
		if abs, err := filepath.Abs(repoPath); err == nil {
			detection.Roots = []string{abs}
		} else {
			detection.Roots = []string{repoPath}
		}
	}
	return detection
}

func clampConfidence(value int) int {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func normalizeID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func adapterAllowed(adapter Adapter, filter AdapterFilter) bool {
	if adapter == nil {
		return false
	}
	if filter == nil {
		return true
	}
	return filter(adapter)
}
