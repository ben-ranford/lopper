package ui

import "github.com/ben-ranford/lopper/internal/featureflags"

type Options struct {
	RepoPath          string
	Language          string
	TopN              int
	Filter            string
	Sort              string
	PageSize          int
	BaselinePath      string
	BaselineStorePath string
	BaselineKey       string
	Features          featureflags.Set
	UseStavePreview   bool
	Width             int
	ASCII             bool
	Color             *bool
}
