package report

import "github.com/ben-ranford/lopper/internal/report/model"

const SchemaVersion = model.SchemaVersion

type Report = model.Report

type DependencyReport = model.DependencyReport
type DependencyLicense = model.DependencyLicense
type DependencyProvenance = model.DependencyProvenance
type CodemodReport = model.CodemodReport
type CodemodSuggestion = model.CodemodSuggestion
type CodemodSkip = model.CodemodSkip
type CodemodApplyReport = model.CodemodApplyReport
type CodemodApplyResult = model.CodemodApplyResult
type RemovalCandidate = model.RemovalCandidate
type ReachabilityConfidence = model.ReachabilityConfidence
type ReachabilitySignal = model.ReachabilitySignal
type ReachabilityRollup = model.ReachabilityRollup
type RemovalCandidateWeights = model.RemovalCandidateWeights
type RiskCue = model.RiskCue
type Recommendation = model.Recommendation
type RuntimeUsage = model.RuntimeUsage
type RuntimeCorrelation = model.RuntimeCorrelation
type RuntimeModuleUsage = model.RuntimeModuleUsage
type RuntimeSymbolUsage = model.RuntimeSymbolUsage
type SymbolUsage = model.SymbolUsage
type ImportUse = model.ImportUse
type SymbolRef = model.SymbolRef
type Location = model.Location

const (
	RuntimeCorrelationStaticOnly  = model.RuntimeCorrelationStaticOnly
	RuntimeCorrelationRuntimeOnly = model.RuntimeCorrelationRuntimeOnly
	RuntimeCorrelationOverlap     = model.RuntimeCorrelationOverlap
)

type ScopeMetadata = model.ScopeMetadata
type BaselineComparison = model.BaselineComparison
type SummaryDelta = model.SummaryDelta
type DependencyDeltaKind = model.DependencyDeltaKind
type DependencyDelta = model.DependencyDelta
type DeniedLicenseDelta = model.DeniedLicenseDelta
type CacheMetadata = model.CacheMetadata
type CacheInvalidation = model.CacheInvalidation
type EffectiveThresholds = model.EffectiveThresholds
type EffectivePolicy = model.EffectivePolicy
type Summary = model.Summary
type LicensePolicy = model.LicensePolicy
type UsageUncertainty = model.UsageUncertainty
type LanguageSummary = model.LanguageSummary

const (
	DependencyDeltaAdded   = model.DependencyDeltaAdded
	DependencyDeltaRemoved = model.DependencyDeltaRemoved
	DependencyDeltaChanged = model.DependencyDeltaChanged
)
