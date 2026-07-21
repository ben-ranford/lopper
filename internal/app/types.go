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
	ModeBaseline  Mode = "baseline"
	ModePRReview  Mode = "pr-review"
	ModeFeatures  Mode = "features"
	ModeProfile   Mode = "profile"
	ModeMCP       Mode = "mcp"
	ModeAdvisory  Mode = "advisory"

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
	Baseline  BaselineRequest
	PRReview  PRReviewRequest
	Features  FeaturesRequest
	Profile   ProfileRequest
	MCP       MCPRequest
	Advisory  AdvisoryRequest
}

type AnalyseRequest struct {
	Dependency              string
	TopN                    int
	ScopeMode               string
	SuggestOnly             bool
	ApplyCodemod            bool
	AllowDirty              bool
	Format                  report.Format
	OutputPath              string
	Language                string
	CacheEnabled            bool
	CachePath               string
	CacheReadOnly           bool
	RuntimeProfile          string
	BaselinePath            string
	BaselineStorePath       string
	BaselineKey             string
	BaselineLabel           string
	SaveBaseline            bool
	RuntimeTracePath        string
	RuntimeTestCommand      string
	AdvisorySourcePath      string
	IncludePatterns         []string
	ExcludePatterns         []string
	ConfigPath              string
	PolicySources           []string
	PolicyTrace             []report.PolicyMergeTrace
	VulnerabilityExceptions []report.VulnerabilityException
	Features                featureflags.Set
	Thresholds              thresholds.Values
	Notifications           notify.Config
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

type BaselineRequest struct {
	Action    string
	StorePath string
	Key       string
	Format    string
	Limit     int
	Features  featureflags.Set
}

type PRReviewRequest struct {
	BaseSHA                 string
	HeadSHA                 string
	ChangedFiles            []string
	ChangedFilesExplicit    bool
	Format                  string
	OutputPath              string
	Language                string
	TopN                    int
	ScopeMode               string
	ConfigPath              string
	AdvisorySourcePath      string
	PolicySources           []string
	PolicyTrace             []report.PolicyMergeTrace
	VulnerabilityExceptions []report.VulnerabilityException
	IncludePatterns         []string
	ExcludePatterns         []string
	FailOnRegression        bool
	MaterialWasteBytes      int64
	MaxRows                 int
	Features                featureflags.Set
	Thresholds              thresholds.Values
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

type AdvisoryRequest struct {
	Command    string
	Provider   string
	CachePath  string
	SourceURL  string
	OutputPath string
	Features   featureflags.Set
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
		Baseline: BaselineRequest{
			StorePath: ".artifacts/lopper-baselines",
			Format:    "table",
			Limit:     50,
		},
		PRReview: PRReviewRequest{
			Format:             "markdown",
			Language:           "auto",
			TopN:               50,
			ScopeMode:          ScopeModeRepo,
			MaterialWasteBytes: 1024,
			MaxRows:            20,
			Thresholds:         thresholds.Defaults(),
		},
		Features: FeaturesRequest{
			Format: "table",
		},
	}
}
