package analysis

import (
	"context"
	"errors"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type Analyser interface {
	Analyse(ctx context.Context, req Request) (report.Report, error)
}

type Service struct {
	Registry *language.Registry
	InitErr  error
}

func (s *Service) Analyse(ctx context.Context, req Request) (report.Report, error) {
	pipeline, err := s.newAnalysisPipeline(ctx, req)
	if err != nil {
		return report.Report{}, err
	}
	defer pipeline.cleanup()

	if err := pipeline.execute(ctx); err != nil {
		return report.Report{}, err
	}
	return pipeline.finalReport()
}

func (s *Service) prepareAnalysis(req Request) (string, error) {
	if s.InitErr != nil {
		return "", s.InitErr
	}
	if s.Registry == nil {
		return "", errors.New("language registry is not configured")
	}
	repoPath, err := workspace.NormalizeRepoPath(req.RepoPath)
	if err != nil {
		return "", err
	}
	return repoPath, nil
}

func (s *Service) resolveCandidates(ctx context.Context, repoPath string, languageID string) ([]language.Candidate, error) {
	return s.Registry.Resolve(ctx, repoPath, languageID)
}
