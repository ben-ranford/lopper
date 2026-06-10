package report

import (
	"fmt"
	"math"
	"slices"
)

var defaultRemovalCandidateWeights = RemovalCandidateWeights{
	Usage:      0.50,
	Impact:     0.30,
	Confidence: 0.20,
}

const (
	removalCandidateWeightUsageField      = "removal_candidate_weight_usage"
	removalCandidateWeightImpactField     = "removal_candidate_weight_impact"
	removalCandidateWeightConfidenceField = "removal_candidate_weight_confidence"
	removalCandidateImpactSaturation      = 100.0
)

func AnnotateRemovalCandidateScores(dependencies []DependencyReport) {
	AnnotateRemovalCandidateScoresWithWeights(dependencies, DefaultRemovalCandidateWeights())
}

func AnnotateRemovalCandidateScoresWithWeights(dependencies []DependencyReport, weights RemovalCandidateWeights) {
	if len(dependencies) == 0 {
		return
	}
	weights = NormalizeRemovalCandidateWeights(weights)

	for i := range dependencies {
		dependencies[i].RemovalCandidate = buildRemovalCandidate(dependencies[i], weights)
	}
}

func DefaultRemovalCandidateWeights() RemovalCandidateWeights {
	return defaultRemovalCandidateWeights
}

func NormalizeRemovalCandidateWeights(weights RemovalCandidateWeights) RemovalCandidateWeights {
	if err := ValidateRemovalCandidateWeightSet(weights); err != nil {
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

func validateRemovalCandidateWeight(name string, value float64) error {
	if !isFiniteWeight(value) {
		return fmt.Errorf("invalid threshold %s: %v (must be finite)", name, value)
	}
	if value < 0 {
		return fmt.Errorf("invalid threshold %s: %v (must be >= 0)", name, value)
	}
	return nil
}

func ValidateRemovalCandidateWeightSet(weights RemovalCandidateWeights) error {
	if err := validateRemovalCandidateWeight(removalCandidateWeightUsageField, weights.Usage); err != nil {
		return err
	}
	if err := validateRemovalCandidateWeight(removalCandidateWeightImpactField, weights.Impact); err != nil {
		return err
	}
	if err := validateRemovalCandidateWeight(removalCandidateWeightConfidenceField, weights.Confidence); err != nil {
		return err
	}
	if weights.Usage <= 0 && weights.Impact <= 0 && weights.Confidence <= 0 {
		return fmt.Errorf("invalid removal candidate weights: at least one weight must be greater than 0")
	}
	return nil
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

func buildRemovalCandidate(dep DependencyReport, weights RemovalCandidateWeights) *RemovalCandidate {
	usage, usageKnown := dependencyUsageSignal(dep)
	impact := dependencyImpactSignal(dep)
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

func dependencyImpactSignal(dep DependencyReport) float64 {
	impact := rawImpact(dep)
	if impact <= 0 {
		return 0
	}
	return clamp((math.Log1p(impact)/math.Log1p(removalCandidateImpactSaturation))*100, 0, 100)
}

func clamp(value, minValue, maxValue float64) float64 {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func roundTo(value float64, places int) float64 {
	scale := math.Pow(10, float64(places))
	return math.Round(value*scale) / scale
}
