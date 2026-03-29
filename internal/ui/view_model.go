package ui

import "github.com/ben-ranford/lopper/internal/report"

type summaryDependencyView struct {
	Language               string
	Name                   string
	UsedExportsCount       int
	TotalExportsCount      int
	UsedPercent            float64
	EstimatedUnusedBytes   int64
	TopUsedSymbols         []report.SymbolUsage
	RuntimeUsage           *report.RuntimeUsage
	ReachabilityConfidence *report.ReachabilityConfidence
	RemovalCandidate       *report.RemovalCandidate
	License                *report.DependencyLicense
	Provenance             *report.DependencyProvenance
	CodemodApply           *report.CodemodApplyReport
}

type summaryReportView struct {
	UsageUncertainty    *report.UsageUncertainty
	Scope               *report.ScopeMetadata
	Cache               *report.CacheMetadata
	EffectiveThresholds *report.EffectiveThresholds
	EffectivePolicy     *report.EffectivePolicy
	BaselineComparison  *report.BaselineComparison
	Warnings            []string
	Dependencies        []summaryDependencyView
}

type summaryDisplayView struct {
	UsageUncertainty    *report.UsageUncertainty
	Scope               *report.ScopeMetadata
	Cache               *report.CacheMetadata
	EffectiveThresholds *report.EffectiveThresholds
	EffectivePolicy     *report.EffectivePolicy
	BaselineComparison  *report.BaselineComparison
	Warnings            []string
	Summary             *report.Summary
	LanguageBreakdown   []report.LanguageSummary
	Dependencies        []summaryDependencyView
}

type summaryFormatter interface {
	FormatSummary(summaryDisplayView) (string, error)
}

type reportSummaryFormatter struct {
	formatter *report.Formatter
}

func newSummaryFormatter(formatter *report.Formatter) summaryFormatter {
	if formatter == nil {
		formatter = report.NewFormatter()
	}
	return reportSummaryFormatter{formatter: formatter}
}

func (f reportSummaryFormatter) FormatSummary(view summaryDisplayView) (string, error) {
	return f.formatter.Format(summaryDisplayViewToReport(view), report.FormatTable)
}

func mapSummaryReportView(reportData report.Report) summaryReportView {
	return summaryReportView{
		UsageUncertainty:    reportData.UsageUncertainty,
		Scope:               reportData.Scope,
		Cache:               reportData.Cache,
		EffectiveThresholds: reportData.EffectiveThresholds,
		EffectivePolicy:     reportData.EffectivePolicy,
		BaselineComparison:  reportData.BaselineComparison,
		Warnings:            append([]string(nil), reportData.Warnings...),
		Dependencies:        mapSummaryDependencies(reportData.Dependencies),
	}
}

func mapSummaryDependencies(dependencies []report.DependencyReport) []summaryDependencyView {
	views := make([]summaryDependencyView, 0, len(dependencies))
	for _, dep := range dependencies {
		views = append(views, summaryDependencyView{
			Language:               dep.Language,
			Name:                   dep.Name,
			UsedExportsCount:       dep.UsedExportsCount,
			TotalExportsCount:      dep.TotalExportsCount,
			UsedPercent:            dep.UsedPercent,
			EstimatedUnusedBytes:   dep.EstimatedUnusedBytes,
			TopUsedSymbols:         append([]report.SymbolUsage(nil), dep.TopUsedSymbols...),
			RuntimeUsage:           dep.RuntimeUsage,
			ReachabilityConfidence: dep.ReachabilityConfidence,
			RemovalCandidate:       dep.RemovalCandidate,
			License:                dep.License,
			Provenance:             dep.Provenance,
			CodemodApply:           summaryCodemodApplyView(dep.Codemod),
		})
	}
	return views
}

func buildSummaryDisplayView(base summaryReportView, sorted []summaryDependencyView, paged []summaryDependencyView) summaryDisplayView {
	summaryDeps := summaryViewDependenciesToReport(sorted)
	return summaryDisplayView{
		UsageUncertainty:    base.UsageUncertainty,
		Scope:               base.Scope,
		Cache:               base.Cache,
		EffectiveThresholds: base.EffectiveThresholds,
		EffectivePolicy:     base.EffectivePolicy,
		BaselineComparison:  base.BaselineComparison,
		Warnings:            append([]string(nil), base.Warnings...),
		Summary:             report.ComputeSummary(summaryDeps),
		LanguageBreakdown:   report.ComputeLanguageBreakdown(summaryDeps),
		Dependencies:        append([]summaryDependencyView(nil), paged...),
	}
}

func summaryDisplayViewToReport(view summaryDisplayView) report.Report {
	return report.Report{
		UsageUncertainty:    view.UsageUncertainty,
		Scope:               view.Scope,
		Cache:               view.Cache,
		EffectiveThresholds: view.EffectiveThresholds,
		EffectivePolicy:     view.EffectivePolicy,
		BaselineComparison:  view.BaselineComparison,
		Warnings:            append([]string(nil), view.Warnings...),
		Summary:             view.Summary,
		LanguageBreakdown:   append([]report.LanguageSummary(nil), view.LanguageBreakdown...),
		Dependencies:        summaryViewDependenciesToReport(view.Dependencies),
	}
}

func summaryViewDependenciesToReport(dependencies []summaryDependencyView) []report.DependencyReport {
	reportDeps := make([]report.DependencyReport, 0, len(dependencies))
	for _, dep := range dependencies {
		reportDeps = append(reportDeps, summaryDependencyToReport(dep))
	}
	return reportDeps
}

func summaryDependencyToReport(dep summaryDependencyView) report.DependencyReport {
	return report.DependencyReport{
		Language:               dep.Language,
		Name:                   dep.Name,
		UsedExportsCount:       dep.UsedExportsCount,
		TotalExportsCount:      dep.TotalExportsCount,
		UsedPercent:            dep.UsedPercent,
		EstimatedUnusedBytes:   dep.EstimatedUnusedBytes,
		TopUsedSymbols:         append([]report.SymbolUsage(nil), dep.TopUsedSymbols...),
		RuntimeUsage:           dep.RuntimeUsage,
		ReachabilityConfidence: dep.ReachabilityConfidence,
		RemovalCandidate:       dep.RemovalCandidate,
		License:                dep.License,
		Provenance:             dep.Provenance,
		Codemod:                summaryCodemodViewToReport(dep.CodemodApply),
	}
}

func summaryCodemodApplyView(codemod *report.CodemodReport) *report.CodemodApplyReport {
	if codemod == nil {
		return nil
	}
	return codemod.Apply
}

func summaryCodemodViewToReport(apply *report.CodemodApplyReport) *report.CodemodReport {
	if apply == nil {
		return nil
	}
	return &report.CodemodReport{Apply: apply}
}

type detailDependencyView struct {
	Name                   string
	Language               string
	UsedExportsCount       int
	TotalExportsCount      int
	UsedPercent            float64
	UsedImports            []detailImportView
	UnusedImports          []detailImportView
	UnusedExports          []detailSymbolRefView
	RiskCues               []detailRiskCueView
	Recommendations        []detailRecommendationView
	Codemod                *detailCodemodView
	RuntimeUsage           *detailRuntimeUsageView
	ReachabilityConfidence *detailReachabilityConfidenceView
	RemovalCandidate       *detailRemovalCandidateView
}

type detailImportView struct {
	Name       string
	Module     string
	Locations  []detailLocationView
	Provenance []string
}

type detailLocationView struct {
	File string
	Line int
}

type detailSymbolRefView struct {
	Name   string
	Module string
}

type detailRiskCueView struct {
	Code     string
	Severity string
	Message  string
}

type detailRecommendationView struct {
	Code      string
	Priority  string
	Message   string
	Rationale string
}

type detailCodemodView struct {
	Mode        string
	Suggestions []detailCodemodSuggestionView
	Skips       []detailCodemodSkipView
}

type detailCodemodSuggestionView struct {
	File       string
	Line       int
	FromModule string
	ToModule   string
}

type detailCodemodSkipView struct {
	File       string
	Line       int
	ReasonCode string
	Message    string
}

type detailRuntimeUsageView struct {
	LoadCount   int
	Correlation string
	RuntimeOnly bool
	Modules     []detailRuntimeModuleView
	TopSymbols  []detailRuntimeSymbolView
}

type detailRuntimeModuleView struct {
	Module string
	Count  int
}

type detailRuntimeSymbolView struct {
	Symbol string
	Module string
	Count  int
}

type detailReachabilityConfidenceView struct {
	Model          string
	Score          float64
	Summary        string
	RationaleCodes []string
	Signals        []detailReachabilitySignalView
}

type detailReachabilitySignalView struct {
	Code         string
	Score        float64
	Weight       float64
	Contribution float64
	Rationale    string
}

type detailRemovalCandidateView struct {
	Score      float64
	Usage      float64
	Impact     float64
	Confidence float64
	Rationale  []string
}

func mapDetailDependencyView(dep report.DependencyReport) detailDependencyView {
	return detailDependencyView{
		Name:                   dep.Name,
		Language:               dep.Language,
		UsedExportsCount:       dep.UsedExportsCount,
		TotalExportsCount:      dep.TotalExportsCount,
		UsedPercent:            dep.UsedPercent,
		UsedImports:            mapDetailImports(dep.UsedImports),
		UnusedImports:          mapDetailImports(dep.UnusedImports),
		UnusedExports:          mapDetailSymbolRefs(dep.UnusedExports),
		RiskCues:               mapDetailRiskCues(dep.RiskCues),
		Recommendations:        mapDetailRecommendations(dep.Recommendations),
		Codemod:                mapDetailCodemod(dep.Codemod),
		RuntimeUsage:           mapDetailRuntimeUsage(dep.RuntimeUsage),
		ReachabilityConfidence: mapDetailReachabilityConfidence(dep.ReachabilityConfidence),
		RemovalCandidate:       mapDetailRemovalCandidate(dep.RemovalCandidate),
	}
}

func mapDetailImports(imports []report.ImportUse) []detailImportView {
	views := make([]detailImportView, 0, len(imports))
	for _, imp := range imports {
		views = append(views, detailImportView{
			Name:       imp.Name,
			Module:     imp.Module,
			Locations:  mapDetailLocations(imp.Locations),
			Provenance: append([]string(nil), imp.Provenance...),
		})
	}
	return views
}

func mapDetailLocations(locations []report.Location) []detailLocationView {
	views := make([]detailLocationView, 0, len(locations))
	for _, location := range locations {
		views = append(views, detailLocationView{File: location.File, Line: location.Line})
	}
	return views
}

func mapDetailSymbolRefs(refs []report.SymbolRef) []detailSymbolRefView {
	views := make([]detailSymbolRefView, 0, len(refs))
	for _, ref := range refs {
		views = append(views, detailSymbolRefView{Name: ref.Name, Module: ref.Module})
	}
	return views
}

func mapDetailRiskCues(cues []report.RiskCue) []detailRiskCueView {
	views := make([]detailRiskCueView, 0, len(cues))
	for _, cue := range cues {
		views = append(views, detailRiskCueView{
			Code:     cue.Code,
			Severity: cue.Severity,
			Message:  cue.Message,
		})
	}
	return views
}

func mapDetailRecommendations(recommendations []report.Recommendation) []detailRecommendationView {
	views := make([]detailRecommendationView, 0, len(recommendations))
	for _, recommendation := range recommendations {
		views = append(views, detailRecommendationView{
			Code:      recommendation.Code,
			Priority:  recommendation.Priority,
			Message:   recommendation.Message,
			Rationale: recommendation.Rationale,
		})
	}
	return views
}

func mapDetailCodemod(codemod *report.CodemodReport) *detailCodemodView {
	if codemod == nil {
		return nil
	}
	return &detailCodemodView{
		Mode:        codemod.Mode,
		Suggestions: mapDetailCodemodSuggestions(codemod.Suggestions),
		Skips:       mapDetailCodemodSkips(codemod.Skips),
	}
}

func mapDetailCodemodSuggestions(suggestions []report.CodemodSuggestion) []detailCodemodSuggestionView {
	views := make([]detailCodemodSuggestionView, 0, len(suggestions))
	for _, suggestion := range suggestions {
		views = append(views, detailCodemodSuggestionView{
			File:       suggestion.File,
			Line:       suggestion.Line,
			FromModule: suggestion.FromModule,
			ToModule:   suggestion.ToModule,
		})
	}
	return views
}

func mapDetailCodemodSkips(skips []report.CodemodSkip) []detailCodemodSkipView {
	views := make([]detailCodemodSkipView, 0, len(skips))
	for _, skip := range skips {
		views = append(views, detailCodemodSkipView{
			File:       skip.File,
			Line:       skip.Line,
			ReasonCode: skip.ReasonCode,
			Message:    skip.Message,
		})
	}
	return views
}

func mapDetailRuntimeUsage(usage *report.RuntimeUsage) *detailRuntimeUsageView {
	if usage == nil {
		return nil
	}
	return &detailRuntimeUsageView{
		LoadCount:   usage.LoadCount,
		Correlation: string(usage.Correlation),
		RuntimeOnly: usage.RuntimeOnly,
		Modules:     mapDetailRuntimeModules(usage.Modules),
		TopSymbols:  mapDetailRuntimeSymbols(usage.TopSymbols),
	}
}

func mapDetailRuntimeModules(modules []report.RuntimeModuleUsage) []detailRuntimeModuleView {
	views := make([]detailRuntimeModuleView, 0, len(modules))
	for _, module := range modules {
		views = append(views, detailRuntimeModuleView{Module: module.Module, Count: module.Count})
	}
	return views
}

func mapDetailRuntimeSymbols(symbols []report.RuntimeSymbolUsage) []detailRuntimeSymbolView {
	views := make([]detailRuntimeSymbolView, 0, len(symbols))
	for _, symbol := range symbols {
		views = append(views, detailRuntimeSymbolView{
			Symbol: symbol.Symbol,
			Module: symbol.Module,
			Count:  symbol.Count,
		})
	}
	return views
}

func mapDetailReachabilityConfidence(confidence *report.ReachabilityConfidence) *detailReachabilityConfidenceView {
	if confidence == nil {
		return nil
	}
	return &detailReachabilityConfidenceView{
		Model:          confidence.Model,
		Score:          confidence.Score,
		Summary:        confidence.Summary,
		RationaleCodes: append([]string(nil), confidence.RationaleCodes...),
		Signals:        mapDetailReachabilitySignals(confidence.Signals),
	}
}

func mapDetailReachabilitySignals(signals []report.ReachabilitySignal) []detailReachabilitySignalView {
	views := make([]detailReachabilitySignalView, 0, len(signals))
	for _, signal := range signals {
		views = append(views, detailReachabilitySignalView{
			Code:         signal.Code,
			Score:        signal.Score,
			Weight:       signal.Weight,
			Contribution: signal.Contribution,
			Rationale:    signal.Rationale,
		})
	}
	return views
}

func mapDetailRemovalCandidate(candidate *report.RemovalCandidate) *detailRemovalCandidateView {
	if candidate == nil {
		return nil
	}
	return &detailRemovalCandidateView{
		Score:      candidate.Score,
		Usage:      candidate.Usage,
		Impact:     candidate.Impact,
		Confidence: candidate.Confidence,
		Rationale:  append([]string(nil), candidate.Rationale...),
	}
}
