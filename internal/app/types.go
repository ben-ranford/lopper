package app

import (
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

type Mode string

const (
	ModeTUI     Mode = "tui"
	ModeAnalyse Mode = "analyse"
)

type Request struct {
	Mode     Mode
	RepoPath string
	Analyse  AnalyseRequest
	TUI      TUIRequest
}

type AnalyseRequest struct {
	Dependency       string
	TopN             int
	Format           report.Format
	Language         string
	RuntimeProfile   string
	BaselinePath     string
	RuntimeTracePath string
	ConfigPath       string
	Thresholds       thresholds.Values
}

type TUIRequest struct {
	Language     string
	SnapshotPath string
	Filter       string
	Sort         string
	TopN         int
	PageSize     int
}

func DefaultRequest() Request {
	return Request{
		Mode:     ModeTUI,
		RepoPath: ".",
		Analyse: AnalyseRequest{
			Format:         report.FormatTable,
			Language:       "auto",
			RuntimeProfile: "node-import",
			Thresholds:     thresholds.Defaults(),
		},
		TUI: TUIRequest{
			Language: "auto",
			Sort:     "waste",
			TopN:     50,
			PageSize: 10,
		},
	}
}
