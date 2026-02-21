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
	penalty := 0.0
	rationale := make([]string, 0, 4)

	if dep.TotalExportsCount <= 0 {
		penalty += 35
	}
	if dep.RuntimeUsage != nil && dep.RuntimeUsage.RuntimeOnly {
		penalty += 20
		rationale = append(rationale, "runtime-only usage indicates lower static confidence")
	}
	if hasWildcardImport(dep.UsedImports) {
		penalty += 15
		rationale = append(rationale, "wildcard import usage reduces per-symbol confidence")
	}
	for _, cue := range dep.RiskCues {
		switch strings.ToLower(cue.Severity) {
		case "high":
			penalty += 20
		case "medium":
			penalty += 12
		case "low":
			penalty += 6
		}
	}
	return clamp(100-penalty, 0, 100), rationale
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
