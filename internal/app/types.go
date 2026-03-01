package app

import (
	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

type Mode string

const (
	ModeTUI     Mode = "tui"
	ModeAnalyse Mode = "analyse"

	ScopeModeRepo            = analysis.ScopeModeRepo
	ScopeModePackage         = analysis.ScopeModePackage
	ScopeModeChangedPackages = analysis.ScopeModeChangedPackages
)

type Request struct {
	Mode     Mode
	RepoPath string
	Analyse  AnalyseRequest
	TUI      TUIRequest
}

type AnalyseRequest struct {
	Dependency         string
	TopN               int
	ScopeMode          string
	SuggestOnly        bool
	Format             report.Format
	Language           string
	CacheEnabled       bool
	CachePath          string
	CacheReadOnly      bool
	RuntimeProfile     string
	BaselinePath       string
	BaselineStorePath  string
	BaselineKey        string
	BaselineLabel      string
	SaveBaseline       bool
	RuntimeTracePath   string
	RuntimeTestCommand string
	IncludePatterns    []string
	ExcludePatterns    []string
	ConfigPath         string
	PolicySources      []string
	Thresholds         thresholds.Values
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
			ScopeMode:      ScopeModePackage,
			CacheEnabled:   true,
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
