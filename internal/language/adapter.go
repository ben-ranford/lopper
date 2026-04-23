package language

import (
	"context"
	"errors"
	"time"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/report"
)

const Auto = "auto"
const All = "all"

type Request struct {
	RepoPath                          string
	Dependency                        string
	TopN                              int
	SuggestOnly                       bool
	RuntimeProfile                    string
	Features                          featureflags.Set
	MinUsagePercentForRecommendations *int
	RemovalCandidateWeights           *report.RemovalCandidateWeights
	IncludeRegistryProvenance         bool
}

type Detection struct {
	Matched    bool
	Confidence int
	Roots      []string
}

var errDetectLifecycleUnconfigured = errors.New("adapter detect lifecycle is not configured")

type DetectWithConfidenceFunc func(ctx context.Context, repoPath string) (Detection, error)

type AdapterContract struct {
	id      string
	aliases []string
}

func NewAdapterContract(id string, aliases ...string) AdapterContract {
	contract := AdapterContract{id: id}
	if len(aliases) > 0 {
		contract.aliases = append([]string(nil), aliases...)
	}
	return contract
}

func (c *AdapterContract) ID() string {
	return c.id
}

func (c *AdapterContract) Aliases() []string {
	if len(c.aliases) == 0 {
		return nil
	}
	return append([]string(nil), c.aliases...)
}

type AdapterLifecycle struct {
	AdapterContract
	Clock                func() time.Time
	detectWithConfidence DetectWithConfidenceFunc
}

func NewAdapterLifecycle(id string, aliases []string, detectWithConfidence DetectWithConfidenceFunc) AdapterLifecycle {
	return AdapterLifecycle{
		AdapterContract:      NewAdapterContract(id, aliases...),
		Clock:                time.Now,
		detectWithConfidence: detectWithConfidence,
	}
}

func (l *AdapterLifecycle) Detect(ctx context.Context, repoPath string) (bool, error) {
	return DetectMatched(ctx, repoPath, l.detectWithConfidence)
}

func DetectMatched(ctx context.Context, repoPath string, detectWithConfidence DetectWithConfidenceFunc) (bool, error) {
	if detectWithConfidence == nil {
		return false, errDetectLifecycleUnconfigured
	}
	detection, err := detectWithConfidence(ctx, repoPath)
	if err != nil {
		return false, err
	}
	return detection.Matched, nil
}

type AdapterIdentity interface {
	ID() string
	Aliases() []string
}

type Detector interface {
	Detect(ctx context.Context, repoPath string) (bool, error)
}

type ConfidenceProvider interface {
	Detector
	DetectWithConfidence(ctx context.Context, repoPath string) (Detection, error)
}

type Analyser interface {
	Analyse(ctx context.Context, req Request) (report.Report, error)
}

type Adapter interface {
	AdapterIdentity
	Detector
	Analyser
}

type CandidateAdapter interface {
	ID() string
	Analyser
}

type Candidate struct {
	Adapter   CandidateAdapter
	Detection Detection
}
