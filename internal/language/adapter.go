package language

import (
	"context"

	"github.com/ben-ranford/lopper/internal/report"
)

const Auto = "auto"

type Request struct {
	RepoPath   string
	Dependency string
	TopN       int
}

type Adapter interface {
	ID() string
	Aliases() []string
	Detect(ctx context.Context, repoPath string) (bool, error)
	Analyse(ctx context.Context, req Request) (report.Report, error)
}
