package ui

import (
	"context"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/report"
)

type ActionRunner interface {
	ApplyCodemod(context.Context, CodemodApplyRequest) (report.Report, error)
	SaveBaseline(context.Context, BaselineSaveRequest) (report.Report, string, error)
}

type CodemodApplyRequest struct {
	RepoPath   string
	Dependency string
	TopN       int
	Language   string
	AllowDirty bool
	Features   featureflags.Set
}

type BaselineSaveRequest struct {
	RepoPath          string
	TopN              int
	Language          string
	BaselineStorePath string
	BaselineKey       string
	BaselineLabel     string
	Features          featureflags.Set
}
