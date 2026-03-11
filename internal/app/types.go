package app

import (
	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/notify"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

type Mode string

const (
	ModeTUI       Mode = "tui"
	ModeAnalyse   Mode = "analyse"
	ModeDashboard Mode = "dashboard"

	ScopeModeRepo            = analysis.ScopeModeRepo
	ScopeModePackage         = analysis.ScopeModePackage
	ScopeModeChangedPackages = analysis.ScopeModeChangedPackages
)

type Request struct {
	Mode      Mode
	RepoPath  string
	Analyse   AnalyseRequest
	TUI       TUIRequest
	Dashboard DashboardRequest
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
	Notifications      notify.Config
}

type TUIRequest struct {
	Language     string
	SnapshotPath string
	Filter       string
	Sort         string
	TopN         int
	PageSize     int
}

type DashboardRepo struct {
	Name     string
	Path     string
	Language string
}

type DashboardRequest struct {
	Repos           []DashboardRepo
	ConfigPath      string
	Format          string
	OutputPath      string
	TopN            int
	DefaultLanguage string
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
			Notifications:  notify.DefaultConfig(),
		},
		TUI: TUIRequest{
			Language: "auto",
			Sort:     "waste",
			TopN:     50,
			PageSize: 10,
		},
		Dashboard: DashboardRequest{
			TopN:            20,
			DefaultLanguage: "auto",
		},
	}
}
