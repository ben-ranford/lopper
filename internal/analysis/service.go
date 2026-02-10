package analysis

import (
	"context"
	"errors"

	"github.com/ben-ranford/lopper/internal/lang/js"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

type Analyzer interface {
	Analyse(ctx context.Context, req Request) (report.Report, error)
}

type Service struct {
	Registry *language.Registry
	InitErr  error
}

func NewService() *Service {
	registry := language.NewRegistry()
	err := registry.Register(js.NewAdapter())

	return &Service{
		Registry: registry,
		InitErr:  err,
	}
}

func (s *Service) Analyse(ctx context.Context, req Request) (report.Report, error) {
	if s.InitErr != nil {
		return report.Report{}, s.InitErr
	}
	if s.Registry == nil {
		return report.Report{}, errors.New("language registry is not configured")
	}

	adapter, err := s.Registry.Select(ctx, req.RepoPath, req.Language)
	if err != nil {
		return report.Report{}, err
	}

	reportData, err := adapter.Analyse(ctx, language.Request{
		RepoPath:   req.RepoPath,
		Dependency: req.Dependency,
		TopN:       req.TopN,
	})
	if err != nil {
		return report.Report{}, err
	}
	reportData.SchemaVersion = report.SchemaVersion
	return reportData, nil
}
