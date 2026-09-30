package dart

import "github.com/ben-ranford/lopper/internal/report"

// Declared dependency reviews pair a medium risk cue with the corresponding
// medium-priority action, resolving both messages under the same preview mode.
type dependencyReview struct {
	riskCode              string
	recommendationCode    string
	rationale             string
	riskMessage           func(dependencyInfo, bool) string
	recommendationMessage func(dependencyInfo, bool) string
}

var overrideReview = dependencyReview{
	riskCode:              "dependency-override",
	recommendationCode:    "review-dependency-override",
	rationale:             "Overrides can hide upstream changes and create drift over time.",
	riskMessage:           dependencyOverrideMessage,
	recommendationMessage: dependencyOverrideRecommendation,
}

var pluginReview = dependencyReview{
	riskCode:              "flutter-plugin-dependency",
	recommendationCode:    "audit-plugin-removal",
	rationale:             "Plugin dependencies can bind Android/iOS platform code beyond Dart call sites.",
	riskMessage:           pluginDependencyMessage,
	recommendationMessage: pluginRemovalRecommendation,
}

func (review dependencyReview) addTo(dep *report.DependencyReport, meta dependencyInfo, previewEnabled bool) {
	dep.RiskCues = append(dep.RiskCues, report.RiskCue{
		Code: review.riskCode, Severity: "medium", Message: review.riskMessage(meta, previewEnabled),
	})
	dep.Recommendations = append(dep.Recommendations, report.Recommendation{
		Code: review.recommendationCode, Priority: "medium",
		Message: review.recommendationMessage(meta, previewEnabled), Rationale: review.rationale,
	})
}
