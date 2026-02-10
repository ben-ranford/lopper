package language

import (
	"context"

	"github.com/ben-ranford/lopper/internal/report"
)

const Auto = "auto"
const All = "all"

type Request struct {
	RepoPath   string
	Dependency string
	TopN       int
}

type Detection struct {
	Matched    bool
	Confidence int
	Roots      []string
}

type Adapter interface {
	ID() string
	Aliases() []string
	Detect(ctx context.Context, repoPath string) (bool, error)
	Analyse(ctx context.Context, req Request) (report.Report, error)
}

type ConfidenceDetector interface {
	DetectWithConfidence(ctx context.Context, repoPath string) (Detection, error)
}

type Candidate struct {
	Adapter   Adapter
	Detection Detection
}
