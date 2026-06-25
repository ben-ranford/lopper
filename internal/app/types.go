package app

import (
	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/notify"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

type Mode string

const (
	ModeTUI       Mode = "tui"
	ModeAnalyse   Mode = "analyse"
	ModeDashboard Mode = "dashboard"
	ModeFeatures  Mode = "features"
	ModeProfile   Mode = "profile"
	ModeMCP       Mode = "mcp"

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
	Features  FeaturesRequest
	Profile   ProfileRequest
	MCP       MCPRequest
}

type AnalyseRequest struct {
	Dependency         string
	TopN               int
	ScopeMode          string
	SuggestOnly        bool
	ApplyCodemod       bool
	AllowDirty         bool
	Format             report.Format
	OutputPath         string
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
	AdvisorySourcePath string
	IncludePatterns    []string
	ExcludePatterns    []string
	ConfigPath         string
	PolicySources      []string
	PolicyTrace        []report.PolicyMergeTrace
	Features           featureflags.Set
	Thresholds         thresholds.Values
	Notifications      notify.Config
}

type TUIRequest struct {
	Language          string
	SnapshotPath      string
	Filter            string
	Sort              string
	TopN              int
	PageSize          int
	BaselinePath      string
	BaselineStorePath string
	BaselineKey       string
}

type DashboardRepo struct {
	Name     string
	Path     string
	Language string
}

type DashboardRequest struct {
	Repos             []DashboardRepo
	ConfigPath        string
	Format            string
	OutputPath        string
	TopN              int
	DefaultLanguage   string
	BaselineStorePath string
	BaselineKey       string
	BaselineLabel     string
	SaveBaseline      bool
	Features          featureflags.Set
}

type FeaturesRequest struct {
	Format     string
	OutputPath string
	Channel    string
	Release    string
}

type ProfileRequest struct {
	Name       string
	OutputPath string
	Force      bool
	Features   featureflags.Set
}

type MCPRequest struct {
	Features featureflags.Set
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
		Features: FeaturesRequest{
			Format: "table",
		},
	}
}
