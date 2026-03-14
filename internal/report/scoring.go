package report

import (
	"math"
	"slices"
	"strings"
)

var defaultRemovalCandidateWeights = RemovalCandidateWeights{
	Usage:      0.50,
	Impact:     0.30,
	Confidence: 0.20,
}

const (
	reachabilityConfidenceModelV2 = "reachability-v2"

	reachabilityWeightRuntimeCorrelation = 0.20
	reachabilityWeightExportInventory    = 0.30
	reachabilityWeightImportPrecision    = 0.20
	reachabilityWeightUsageUncertainty   = 0.15
	reachabilityWeightDynamicLoader      = 0.10
	reachabilityWeightRiskSeverity       = 0.05

	confidenceReasonRuntimeOverlap          = "runtime-overlap"
	confidenceReasonRuntimeStaticOnly       = "runtime-static-only"
	confidenceReasonRuntimeEvidenceAbsent   = "runtime-evidence-absent"
	confidenceReasonRuntimeOnlyUsage        = "runtime-only-usage"
	confidenceReasonExportInventoryKnown    = "export-inventory-known"
	confidenceReasonMissingExportInventory  = "missing-export-inventory"
	confidenceReasonPreciseStaticImports    = "precise-static-imports"
	confidenceReasonLimitedImportEvidence   = "limited-static-import-evidence"
	confidenceReasonWildcardImport          = "wildcard-import"
	confidenceReasonUsageUncertaintyClear   = "usage-uncertainty-clear"
	confidenceReasonRepoUsageUncertainty    = "repo-usage-uncertainty"
	confidenceReasonEntryPointsStatic       = "dependency-entrypoints-static"
	confidenceReasonDependencyDynamicLoader = "dependency-dynamic-loader"
	confidenceReasonNoRiskCues              = "no-risk-cues"
	confidenceReasonRiskLow                 = "risk-low"
	confidenceReasonRiskMedium              = "risk-medium"
	confidenceReasonRiskHigh                = "risk-high"
)

var orderedConfidenceReasonCodeValues = [...]string{
	confidenceReasonRuntimeOverlap,
	confidenceReasonRuntimeStaticOnly,
	confidenceReasonRuntimeEvidenceAbsent,
	confidenceReasonRuntimeOnlyUsage,
	confidenceReasonExportInventoryKnown,
	confidenceReasonMissingExportInventory,
	confidenceReasonPreciseStaticImports,
	confidenceReasonLimitedImportEvidence,
	confidenceReasonWildcardImport,
	confidenceReasonUsageUncertaintyClear,
	confidenceReasonRepoUsageUncertainty,
	confidenceReasonEntryPointsStatic,
	confidenceReasonDependencyDynamicLoader,
	confidenceReasonNoRiskCues,
	confidenceReasonRiskHigh,
	confidenceReasonRiskMedium,
	confidenceReasonRiskLow,
}

type evaluatedReachabilitySignal struct {
	signal  ReachabilitySignal
	summary string
}

func AnnotateReachabilityConfidence(reportData *Report) {
	if reportData == nil {
		return
	}
	for depIndex := range reportData.Dependencies {
		reportData.Dependencies[depIndex].ReachabilityConfidence = buildReachabilityConfidence(reportData.Dependencies[depIndex], reportData.UsageUncertainty)
	}
}

func AnnotateRemovalCandidateScores(dependencies []DependencyReport) {
	AnnotateRemovalCandidateScoresWithWeights(dependencies, DefaultRemovalCandidateWeights())
}

func AnnotateRemovalCandidateScoresWithWeights(dependencies []DependencyReport, weights RemovalCandidateWeights) {
	if len(dependencies) == 0 {
		return
	}
	weights = NormalizeRemovalCandidateWeights(weights)
	maxImpactRaw := 0.0
	for _, dep := range dependencies {
		impactRaw := rawImpact(dep)
		if impactRaw > maxImpactRaw {
			maxImpactRaw = impactRaw
		}
	}

	for i := range dependencies {
		dependencies[i].RemovalCandidate = buildRemovalCandidate(dependencies[i], maxImpactRaw, weights)
	}
}

func DefaultRemovalCandidateWeights() RemovalCandidateWeights {
	return defaultRemovalCandidateWeights
}

func NormalizeRemovalCandidateWeights(weights RemovalCandidateWeights) RemovalCandidateWeights {
	if !isFiniteWeight(weights.Usage) || !isFiniteWeight(weights.Impact) || !isFiniteWeight(weights.Confidence) {
		return defaultRemovalCandidateWeights
	}
	if weights.Usage < 0 || weights.Impact < 0 || weights.Confidence < 0 {
		return defaultRemovalCandidateWeights
	}
	total := weights.Usage + weights.Impact + weights.Confidence
	if !isFiniteWeight(total) {
		return defaultRemovalCandidateWeights
	}
	if total <= 0 {
		return defaultRemovalCandidateWeights
	}
	return RemovalCandidateWeights{
		Usage:      weights.Usage / total,
		Impact:     weights.Impact / total,
		Confidence: weights.Confidence / total,
	}
}

func isFiniteWeight(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func RemovalCandidateScore(dep DependencyReport) (float64, bool) {
	if dep.RemovalCandidate == nil {
		return 0, false
	}
	return dep.RemovalCandidate.Score, true
}

func buildRemovalCandidate(dep DependencyReport, maxImpactRaw float64, weights RemovalCandidateWeights) *RemovalCandidate {
	usage, usageKnown := dependencyUsageSignal(dep)
	impact := dependencyImpactSignal(dep, maxImpactRaw)
	confidence, rationale := dependencyConfidenceSignal(dep)

	if !usageKnown {
		rationale = append(rationale, "usage coverage unknown because total exports are unavailable")
	}

	score := (usage * weights.Usage) + (impact * weights.Impact) + (confidence * weights.Confidence)
	score = roundTo(score, 1)

	return &RemovalCandidate{
		Score:      score,
		Usage:      roundTo(usage, 1),
		Impact:     roundTo(impact, 1),
		Confidence: roundTo(confidence, 1),
		Weights:    weights,
		Rationale:  slices.Clip(rationale),
	}
}

func dependencyUsageSignal(dep DependencyReport) (float64, bool) {
	if dep.TotalExportsCount <= 0 {
		return 0, false
	}
	usedPercent := dep.UsedPercent
	if usedPercent <= 0 {
		usedPercent = (float64(dep.UsedExportsCount) / float64(dep.TotalExportsCount)) * 100
	}
	return clamp(100-usedPercent, 0, 100), true
}

func rawImpact(dep DependencyReport) float64 {
	if dep.TotalExportsCount <= 0 {
		return 0
	}
	unused := dep.TotalExportsCount - dep.UsedExportsCount
	if unused < 0 {
		unused = 0
	}
	return float64(unused)
}

func dependencyImpactSignal(dep DependencyReport, maxImpactRaw float64) float64 {
	if maxImpactRaw <= 0 {
		return 0
	}
	return clamp((rawImpact(dep)/maxImpactRaw)*100, 0, 100)
}

func dependencyConfidenceSignal(dep DependencyReport) (float64, []string) {
	confidence := resolveReachabilityConfidence(dep)
	if confidence == nil {
		return 0, nil
	}
	return confidence.Score, reachabilityRationale(confidence)
}

func resolveReachabilityConfidence(dep DependencyReport) *ReachabilityConfidence {
	if dep.ReachabilityConfidence != nil {
		return dep.ReachabilityConfidence
	}
	return buildReachabilityConfidence(dep, nil)
}

func buildReachabilityConfidence(dep DependencyReport, usage *UsageUncertainty) *ReachabilityConfidence {
	evaluated := []evaluatedReachabilitySignal{
		runtimeCorrelationConfidenceSignal(dep.RuntimeUsage),
		exportInventoryConfidenceSignal(dep),
		importPrecisionConfidenceSignal(dep),
		usageUncertaintyConfidenceSignal(usage),
		dynamicLoaderConfidenceSignal(dep.RiskCues),
		riskSeverityConfidenceSignal(dep.RiskCues),
	}

	signals := make([]ReachabilitySignal, 0, len(evaluated))
	reasonCodes := make([]string, 0, len(evaluated))
	score := 0.0
	for _, item := range evaluated {
		signal := item.signal
		signal.Score = roundTo(clamp(signal.Score, 0, 100), 1)
		signal.Weight = roundTo(signal.Weight, 3)
		contribution := signal.Score * signal.Weight
		signal.Contribution = roundTo(contribution, 1)
		score += contribution
		signals = append(signals, signal)
		if signal.Code != "" {
			reasonCodes = append(reasonCodes, signal.Code)
		}
	}

	return &ReachabilityConfidence{
		Model:          reachabilityConfidenceModelV2,
		Score:          roundTo(clamp(score, 0, 100), 1),
		Summary:        summarizeReachabilityConfidence(evaluated),
		RationaleCodes: orderedConfidenceReasonCodes(reasonCodes),
		Signals:        signals,
	}
}

func runtimeCorrelationConfidenceSignal(usage *RuntimeUsage) evaluatedReachabilitySignal {
	switch runtimeUsageCorrelation(usage) {
	case RuntimeCorrelationOverlap:
		return evaluatedReachabilitySignal{
			signal: ReachabilitySignal{
				Code:      confidenceReasonRuntimeOverlap,
				Score:     100,
				Weight:    reachabilityWeightRuntimeCorrelation,
				Rationale: "runtime and static evidence overlap for this dependency",
			},
			summary: "runtime overlap",
		}
	case RuntimeCorrelationStaticOnly:
		return evaluatedReachabilitySignal{
			signal: ReachabilitySignal{
				Code:      confidenceReasonRuntimeStaticOnly,
				Score:     70,
				Weight:    reachabilityWeightRuntimeCorrelation,
				Rationale: "runtime trace did not load the dependency, so confidence relies on static evidence",
			},
			summary: "static-only runtime",
		}
	case RuntimeCorrelationRuntimeOnly:
		return evaluatedReachabilitySignal{
			signal: ReachabilitySignal{
				Code:      confidenceReasonRuntimeOnlyUsage,
				Score:     15,
				Weight:    reachabilityWeightRuntimeCorrelation,
				Rationale: "runtime-only usage indicates weaker static reachability evidence",
			},
			summary: "runtime-only",
		}
	default:
		return evaluatedReachabilitySignal{
			signal: ReachabilitySignal{
				Code:      confidenceReasonRuntimeEvidenceAbsent,
				Score:     65,
				Weight:    reachabilityWeightRuntimeCorrelation,
				Rationale: "no runtime trace evidence was available for this dependency",
			},
			summary: "no runtime trace",
		}
	}
}

func exportInventoryConfidenceSignal(dep DependencyReport) evaluatedReachabilitySignal {
	if dep.TotalExportsCount > 0 {
		return evaluatedReachabilitySignal{
			signal: ReachabilitySignal{
				Code:      confidenceReasonExportInventoryKnown,
				Score:     100,
				Weight:    reachabilityWeightExportInventory,
				Rationale: "export inventory is available for deterministic symbol coverage",
			},
			summary: "export inventory",
		}
	}
	score := 20.0
	rationale := "export inventory is unavailable, so coverage is estimated from partial evidence"
	if hasStaticImportEvidence(dep) {
		score = 35
		rationale = "export inventory is unavailable, but static import evidence still anchors coverage estimates"
	}
	return evaluatedReachabilitySignal{
		signal: ReachabilitySignal{
			Code:      confidenceReasonMissingExportInventory,
			Score:     score,
			Weight:    reachabilityWeightExportInventory,
			Rationale: rationale,
		},
		summary: "no export inventory",
	}
}

func importPrecisionConfidenceSignal(dep DependencyReport) evaluatedReachabilitySignal {
	switch {
	case hasWildcardImport(dep.UsedImports) || hasWildcardImport(dep.UnusedImports):
		return evaluatedReachabilitySignal{
			signal: ReachabilitySignal{
				Code:      confidenceReasonWildcardImport,
				Score:     35,
				Weight:    reachabilityWeightImportPrecision,
				Rationale: "wildcard or namespace imports reduce per-symbol reachability precision",
			},
			summary: "wildcard import",
		}
	case hasStaticImportEvidence(dep):
		return evaluatedReachabilitySignal{
			signal: ReachabilitySignal{
				Code:      confidenceReasonPreciseStaticImports,
				Score:     100,
				Weight:    reachabilityWeightImportPrecision,
				Rationale: "explicit static imports provide precise symbol attribution",
			},
			summary: "precise imports",
		}
	default:
		return evaluatedReachabilitySignal{
			signal: ReachabilitySignal{
				Code:      confidenceReasonLimitedImportEvidence,
				Score:     65,
				Weight:    reachabilityWeightImportPrecision,
				Rationale: "static import evidence is limited, so symbol attribution is less precise",
			},
			summary: "limited import evidence",
		}
	}
}

func usageUncertaintyConfidenceSignal(usage *UsageUncertainty) evaluatedReachabilitySignal {
	if usage == nil || usage.UncertainImportUses <= 0 {
		return evaluatedReachabilitySignal{
			signal: ReachabilitySignal{
				Code:      confidenceReasonUsageUncertaintyClear,
				Score:     100,
				Weight:    reachabilityWeightUsageUncertainty,
				Rationale: "no unresolved dynamic import or require usage was observed",
			},
		}
	}

	total := usage.ConfirmedImportUses + usage.UncertainImportUses
	if total <= 0 {
		total = usage.UncertainImportUses
	}
	ratio := float64(usage.UncertainImportUses) / float64(total)
	penalty := math.Min(40, ratio*60)
	score := 100 - penalty
	return evaluatedReachabilitySignal{
		signal: ReachabilitySignal{
			Code:      confidenceReasonRepoUsageUncertainty,
			Score:     score,
			Weight:    reachabilityWeightUsageUncertainty,
			Rationale: "unresolved dynamic import or require usage reduces confidence with a bounded repo-level penalty",
		},
		summary: "repo uncertainty",
	}
}

func dynamicLoaderConfidenceSignal(cues []RiskCue) evaluatedReachabilitySignal {
	if hasRiskCode(cues, "dynamic-loader") {
		return evaluatedReachabilitySignal{
			signal: ReachabilitySignal{
				Code:      confidenceReasonDependencyDynamicLoader,
				Score:     35,
				Weight:    reachabilityWeightDynamicLoader,
				Rationale: "dependency entrypoints use dynamic loading, which weakens deterministic reachability scoring",
			},
			summary: "dynamic entrypoints",
		}
	}
	return evaluatedReachabilitySignal{
		signal: ReachabilitySignal{
			Code:      confidenceReasonEntryPointsStatic,
			Score:     100,
			Weight:    reachabilityWeightDynamicLoader,
			Rationale: "dependency entrypoints appear statically declared",
		},
	}
}

func riskSeverityConfidenceSignal(cues []RiskCue) evaluatedReachabilitySignal {
	switch highestRiskSeverity(cues) {
	case "high":
		return evaluatedReachabilitySignal{
			signal: ReachabilitySignal{
				Code:      confidenceReasonRiskHigh,
				Score:     40,
				Weight:    reachabilityWeightRiskSeverity,
				Rationale: "high-severity dependency risk cues limit reachability confidence",
			},
			summary: "risk high",
		}
	case "medium":
		return evaluatedReachabilitySignal{
			signal: ReachabilitySignal{
				Code:      confidenceReasonRiskMedium,
				Score:     65,
				Weight:    reachabilityWeightRiskSeverity,
				Rationale: "medium-severity dependency risk cues reduce reachability confidence",
			},
			summary: "risk medium",
		}
	case "low":
		return evaluatedReachabilitySignal{
			signal: ReachabilitySignal{
				Code:      confidenceReasonRiskLow,
				Score:     85,
				Weight:    reachabilityWeightRiskSeverity,
				Rationale: "low-severity dependency risk cues slightly reduce reachability confidence",
			},
			summary: "risk low",
		}
	default:
		return evaluatedReachabilitySignal{
			signal: ReachabilitySignal{
				Code:      confidenceReasonNoRiskCues,
				Score:     100,
				Weight:    reachabilityWeightRiskSeverity,
				Rationale: "no additional dependency risk cues reduced reachability confidence",
			},
		}
	}
}

func runtimeUsageCorrelation(usage *RuntimeUsage) RuntimeCorrelation {
	if usage == nil {
		return ""
	}
	if usage.Correlation != "" {
		return usage.Correlation
	}
	if usage.RuntimeOnly {
		return RuntimeCorrelationRuntimeOnly
	}
	if usage.LoadCount > 0 {
		return RuntimeCorrelationOverlap
	}
	return RuntimeCorrelationStaticOnly
}

func hasStaticImportEvidence(dep DependencyReport) bool {
	return len(dep.UsedImports)+len(dep.UnusedImports) > 0
}

func hasRiskCode(cues []RiskCue, code string) bool {
	for _, cue := range cues {
		if strings.EqualFold(strings.TrimSpace(cue.Code), code) {
			return true
		}
	}
	return false
}

func highestRiskSeverity(cues []RiskCue) string {
	highest := ""
	weight := 0
	for _, cue := range cues {
		currentWeight := riskSeverityWeight(cue.Severity)
		if currentWeight <= weight {
			continue
		}
		weight = currentWeight
		highest = strings.ToLower(strings.TrimSpace(cue.Severity))
	}
	return highest
}

func riskSeverityWeight(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

func summarizeReachabilityConfidence(signals []evaluatedReachabilitySignal) string {
	parts := make([]string, 0, 3)
	for _, signal := range signals {
		if signal.summary == "" {
			continue
		}
		if signal.signal.Score >= 100 && !alwaysIncludeReachabilitySignal(signal.signal.Code) {
			continue
		}
		parts = append(parts, signal.summary)
		if len(parts) == 3 {
			break
		}
	}
	return strings.Join(parts, "; ")
}

func reachabilityRationale(confidence *ReachabilityConfidence) []string {
	if confidence == nil {
		return nil
	}
	rationale := make([]string, 0, len(confidence.Signals))
	for _, signal := range confidence.Signals {
		if strings.TrimSpace(signal.Rationale) == "" {
			continue
		}
		if signal.Score >= 100 && !alwaysIncludeReachabilitySignal(signal.Code) {
			continue
		}
		rationale = append(rationale, signal.Rationale)
	}
	if len(rationale) == 0 && confidence.Summary != "" {
		rationale = append(rationale, confidence.Summary)
	}
	return rationale
}

func alwaysIncludeReachabilitySignal(code string) bool {
	switch code {
	case confidenceReasonRuntimeOverlap, confidenceReasonExportInventoryKnown, confidenceReasonPreciseStaticImports:
		return true
	default:
		return false
	}
}

func AnnotateFindingConfidence(dependencies []DependencyReport) {
	for depIndex := range dependencies {
		confidence := resolveReachabilityConfidence(dependencies[depIndex])
		if confidence == nil {
			continue
		}
		score := roundTo(confidence.Score, 1)
		reasonCodes := orderedConfidenceReasonCodes(confidence.RationaleCodes)

		for findingIndex := range dependencies[depIndex].UnusedExports {
			dependencies[depIndex].UnusedExports[findingIndex].ConfidenceScore = score
			dependencies[depIndex].UnusedExports[findingIndex].ConfidenceReasonCodes = reasonCodes
		}
		for findingIndex := range dependencies[depIndex].UnusedImports {
			dependencies[depIndex].UnusedImports[findingIndex].ConfidenceScore = score
			dependencies[depIndex].UnusedImports[findingIndex].ConfidenceReasonCodes = reasonCodes
		}
		for findingIndex := range dependencies[depIndex].Recommendations {
			dependencies[depIndex].Recommendations[findingIndex].ConfidenceScore = score
			dependencies[depIndex].Recommendations[findingIndex].ConfidenceReasonCodes = reasonCodes
		}
		for findingIndex := range dependencies[depIndex].RiskCues {
			dependencies[depIndex].RiskCues[findingIndex].ConfidenceScore = score
			dependencies[depIndex].RiskCues[findingIndex].ConfidenceReasonCodes = reasonCodes
		}
	}
}

func FilterFindingsByConfidence(dependencies []DependencyReport, minConfidence float64) {
	if minConfidence <= 0 {
		return
	}
	for depIndex := range dependencies {
		dependencies[depIndex].UnusedExports = filterByConfidenceScore(dependencies[depIndex].UnusedExports, minConfidence, func(item SymbolRef) float64 {
			return item.ConfidenceScore
		})
		dependencies[depIndex].UnusedImports = filterByConfidenceScore(dependencies[depIndex].UnusedImports, minConfidence, func(item ImportUse) float64 {
			return item.ConfidenceScore
		})
		dependencies[depIndex].Recommendations = filterByConfidenceScore(dependencies[depIndex].Recommendations, minConfidence, func(item Recommendation) float64 {
			return item.ConfidenceScore
		})
		dependencies[depIndex].RiskCues = filterByConfidenceScore(dependencies[depIndex].RiskCues, minConfidence, func(item RiskCue) float64 {
			return item.ConfidenceScore
		})
	}
}

func orderedConfidenceReasonCodes(reasonCodes []string) []string {
	result := make([]string, 0, len(orderedConfidenceReasonCodeValues))
	for _, candidate := range orderedConfidenceReasonCodeValues {
		if !slices.Contains(reasonCodes, candidate) {
			continue
		}
		result = append(result, candidate)
	}
	return result
}

func filterByConfidenceScore[T any](values []T, minConfidence float64, score func(item T) float64) []T {
	filtered := make([]T, 0, len(values))
	for _, value := range values {
		if score(value) < minConfidence {
			continue
		}
		filtered = append(filtered, value)
	}
	return filtered
}

func hasWildcardImport(imports []ImportUse) bool {
	for _, imp := range imports {
		if imp.Name == "*" {
			return true
		}
	}
	return false
}

func clamp(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func roundTo(value float64, places int) float64 {
	scale := math.Pow(10, float64(places))
	return math.Round(value*scale) / scale
}
