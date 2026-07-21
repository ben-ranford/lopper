package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
)

const (
	remediationCategoryDuplicateDependency = "duplicate_dependency"
	remediationCategoryLicense             = "license"
	remediationCategoryRecommendation      = "recommendation"
	remediationCategoryRepoError           = "repo_error"
	remediationCategoryRisk                = "risk"
	remediationCategoryRuntimeRegression   = "runtime_regression"
	remediationCategoryVulnerability       = "vulnerability"
	remediationCategoryWaste               = "waste"
)

func repoRemediationItems(analysis RepoAnalysis, repoNameCounts map[string]int) []RemediationItem {
	repoLabel := crossRepoRepositoryLabel(analysis.Input, repoNameCounts)
	repoPath := strings.TrimSpace(analysis.Input.Path)
	repoID := repoIdentity(analysis.Input)
	items := make([]RemediationItem, 0)

	for _, dependency := range analysis.Report.Dependencies {
		dependencyName := strings.TrimSpace(dependency.Name)
		if dependencyName == "" {
			continue
		}
		items = append(items, vulnerabilityRemediationItems(repoID, repoLabel, repoPath, dependencyName, dependency.Vulnerabilities)...)
		if item, ok := licenseRemediationItem(repoID, repoLabel, repoPath, dependencyName, dependency.License); ok {
			items = append(items, item)
		}
		items = append(items, recommendationRemediationItems(repoID, repoLabel, repoPath, dependencyName, dependency.Recommendations)...)
		items = append(items, riskRemediationItems(repoID, repoLabel, repoPath, dependencyName, dependency.RiskCues)...)
	}
	items = append(items, runtimeRegressionRemediationItems(repoID, repoLabel, repoPath, analysis.Report.BaselineComparison)...)

	return items
}

func repoErrorRemediationItem(input RepoInput, repoNameCounts map[string]int, err error) RemediationItem {
	repoLabel := crossRepoRepositoryLabel(input, repoNameCounts)
	repoID := repoIdentity(input)
	return RemediationItem{
		ID:              stableRemediationID(repoID, remediationCategoryRepoError),
		Repo:            repoLabel,
		RepoPath:        strings.TrimSpace(input.Path),
		Category:        remediationCategoryRepoError,
		Severity:        report.VulnerabilityPriorityCritical,
		Priority:        report.VulnerabilityPriorityCritical,
		Evidence:        []string{strings.TrimSpace(err.Error())},
		SuggestedAction: "Fix the dashboard analysis failure for this repository and rerun the dashboard.",
	}
}

func vulnerabilityRemediationItems(repoID, repoLabel, repoPath, dependencyName string, findings []report.VulnerabilityFinding) []RemediationItem {
	items := make([]RemediationItem, 0, len(findings))
	for _, finding := range findings {
		if report.FindingSuppressedByException(finding) {
			continue
		}
		advisoryID := strings.TrimSpace(finding.AdvisoryID)
		priority := normalizeRemediationLevel(finding.Priority)
		severity := normalizeRemediationLevel(finding.Severity)
		if priority == "" {
			priority = severity
		}
		if severity == "" {
			severity = priority
		}
		var evidence []string
		if advisoryID != "" {
			evidence = append(evidence, "advisory: "+advisoryID)
		}
		if finding.Source != "" {
			evidence = append(evidence, "source: "+strings.TrimSpace(finding.Source))
		}
		if finding.Reachable {
			evidence = append(evidence, "reachable: true")
		}
		evidence = append(evidence, finding.Evidence...)

		items = append(items, RemediationItem{
			ID:              stableRemediationID(repoID, dependencyName, remediationCategoryVulnerability, advisoryID, finding.Package),
			Repo:            repoLabel,
			RepoPath:        repoPath,
			Dependency:      dependencyName,
			Category:        remediationCategoryVulnerability,
			Severity:        severity,
			Priority:        priority,
			Evidence:        compactEvidence(evidence),
			SuggestedAction: vulnerabilitySuggestedAction(dependencyName, finding),
		})
	}
	return items
}

func vulnerabilitySuggestedAction(dependencyName string, finding report.VulnerabilityFinding) string {
	advisoryID := strings.TrimSpace(finding.AdvisoryID)
	if advisoryID == "" {
		advisoryID = "the advisory"
	}
	fixedVersion := strings.TrimSpace(finding.FixedVersion)
	if fixedVersion != "" {
		return fmt.Sprintf("Upgrade %s to %s or later to address %s.", dependencyName, fixedVersion, advisoryID)
	}
	return fmt.Sprintf("Triage %s for %s and apply the vendor remediation or document accepted risk.", advisoryID, dependencyName)
}

func licenseRemediationItem(repoID, repoLabel, repoPath, dependencyName string, license *report.DependencyLicense) (RemediationItem, bool) {
	if license == nil || !license.Denied {
		return RemediationItem{}, false
	}
	label := firstNonBlank(license.SPDX, license.Raw, "denied license")
	evidence := []string{"license: " + label}
	if license.Source != "" {
		evidence = append(evidence, "source: "+strings.TrimSpace(license.Source))
	}
	evidence = append(evidence, license.Evidence...)
	return RemediationItem{
		ID:              stableRemediationID(repoID, dependencyName, remediationCategoryLicense, label),
		Repo:            repoLabel,
		RepoPath:        repoPath,
		Dependency:      dependencyName,
		Category:        remediationCategoryLicense,
		Severity:        report.VulnerabilityPriorityHigh,
		Priority:        report.VulnerabilityPriorityHigh,
		Evidence:        compactEvidence(evidence),
		SuggestedAction: fmt.Sprintf("Replace %s or add an explicit policy approval for denied license %s.", dependencyName, label),
	}, true
}

func recommendationRemediationItems(repoID, repoLabel, repoPath, dependencyName string, recommendations []report.Recommendation) []RemediationItem {
	items := make([]RemediationItem, 0, len(recommendations))
	for _, recommendation := range recommendations {
		code := strings.TrimSpace(recommendation.Code)
		message := strings.TrimSpace(recommendation.Message)
		if code == "" && message == "" {
			continue
		}
		category := remediationCategoryRecommendation
		if isWasteRecommendationCode(code) {
			category = remediationCategoryWaste
		}
		priority := normalizeRemediationLevel(recommendation.Priority)
		if priority == "" {
			priority = report.VulnerabilityPriorityMedium
		}
		var evidence []string
		if code != "" {
			evidence = append(evidence, "recommendation: "+code)
		}
		if recommendation.Rationale != "" {
			evidence = append(evidence, strings.TrimSpace(recommendation.Rationale))
		}
		evidence = append(evidence, recommendation.ConfidenceReasonCodes...)
		action := message
		if action == "" {
			action = fmt.Sprintf("Review recommendation %s for %s and apply or document the outcome.", code, dependencyName)
		}
		items = append(items, RemediationItem{
			ID:              stableRemediationID(repoID, dependencyName, category, code, message),
			Repo:            repoLabel,
			RepoPath:        repoPath,
			Dependency:      dependencyName,
			Category:        category,
			Severity:        priority,
			Priority:        priority,
			Evidence:        compactEvidence(evidence),
			SuggestedAction: action,
		})
	}
	return items
}

func riskRemediationItems(repoID, repoLabel, repoPath, dependencyName string, cues []report.RiskCue) []RemediationItem {
	items := make([]RemediationItem, 0, len(cues))
	for _, cue := range cues {
		code := strings.TrimSpace(cue.Code)
		message := strings.TrimSpace(cue.Message)
		if code == "" && message == "" {
			continue
		}
		severity := normalizeRemediationLevel(cue.Severity)
		if severity == "" {
			severity = report.VulnerabilityPriorityLow
		}
		var evidence []string
		if code != "" {
			evidence = append(evidence, "risk: "+code)
		}
		if message != "" {
			evidence = append(evidence, message)
		}
		evidence = append(evidence, cue.ConfidenceReasonCodes...)
		items = append(items, RemediationItem{
			ID:              stableRemediationID(repoID, dependencyName, remediationCategoryRisk, code, message),
			Repo:            repoLabel,
			RepoPath:        repoPath,
			Dependency:      dependencyName,
			Category:        remediationCategoryRisk,
			Severity:        severity,
			Priority:        severity,
			Evidence:        compactEvidence(evidence),
			SuggestedAction: fmt.Sprintf("Investigate the %s risk cue for %s and upgrade, replace, or document acceptance.", firstNonBlank(code, "reported"), dependencyName),
		})
	}
	return items
}

func runtimeRegressionRemediationItems(repoID, repoLabel, repoPath string, comparison *report.BaselineComparison) []RemediationItem {
	if comparison == nil || len(comparison.RuntimeRegressions) == 0 {
		return nil
	}
	items := make([]RemediationItem, 0, len(comparison.RuntimeRegressions))
	for _, regression := range comparison.RuntimeRegressions {
		dependencyName := strings.TrimSpace(regression.Name)
		if dependencyName == "" {
			continue
		}
		items = append(items, RemediationItem{
			ID:              stableRemediationID(repoID, dependencyName, remediationCategoryRuntimeRegression),
			Repo:            repoLabel,
			RepoPath:        repoPath,
			Dependency:      dependencyName,
			Category:        remediationCategoryRuntimeRegression,
			Severity:        report.VulnerabilityPriorityHigh,
			Priority:        report.VulnerabilityPriorityHigh,
			Evidence:        runtimeRegressionEvidence(regression),
			SuggestedAction: fmt.Sprintf("Inspect new runtime usage for %s and update code, dependency ownership, or the baseline intentionally.", dependencyName),
		})
	}
	return items
}

func runtimeRegressionEvidence(regression report.DependencyDelta) []string {
	evidence := []string{"runtime regression detected"}
	if regression.Language != "" {
		evidence = append(evidence, "language: "+strings.TrimSpace(regression.Language))
	}
	if regression.RuntimeDelta != nil {
		if regression.RuntimeDelta.NewRuntimeLoads {
			evidence = append(evidence, "new runtime loads")
		}
		if regression.RuntimeDelta.RuntimeOnlyRegression {
			evidence = append(evidence, "runtime-only regression")
		}
		if regression.RuntimeDelta.LoadCountDelta != nil && *regression.RuntimeDelta.LoadCountDelta != 0 {
			evidence = append(evidence, fmt.Sprintf("load_count_delta: %+d", *regression.RuntimeDelta.LoadCountDelta))
		}
	}
	return compactEvidence(evidence)
}

func crossRepoRemediationItems(dependencies []CrossRepoDependency) []RemediationItem {
	items := make([]RemediationItem, 0, len(dependencies))
	for _, dependency := range dependencies {
		dependencyName := strings.TrimSpace(dependency.Name)
		if dependencyName == "" {
			continue
		}
		repoList := strings.Join(dependency.Repositories, ", ")
		items = append(items, RemediationItem{
			ID:              stableRemediationID("org", dependencyName, remediationCategoryDuplicateDependency),
			Repo:            repoList,
			Dependency:      dependencyName,
			Category:        remediationCategoryDuplicateDependency,
			Severity:        report.VulnerabilityPriorityLow,
			Priority:        report.VulnerabilityPriorityLow,
			Evidence:        []string{fmt.Sprintf("dependency appears in %d repositories: %s", dependency.Count, repoList)},
			SuggestedAction: fmt.Sprintf("Review shared ownership for %s and decide whether to consolidate, pin, or govern it centrally.", dependencyName),
		})
	}
	return items
}

func compareRemediationItems(current, baseline []RemediationItem) ([]RemediationItemDelta, []RemediationItemDelta, []RemediationItemDelta, []RemediationItemDelta, []RemediationItemDelta) {
	currentByID := remediationItemsByID(current)
	baselineByID := remediationItemsByID(baseline)
	keys := make([]string, 0, len(currentByID)+len(baselineByID))
	seen := make(map[string]struct{}, len(currentByID)+len(baselineByID))
	for key := range currentByID {
		keys = append(keys, key)
		seen[key] = struct{}{}
	}
	for key := range baselineByID {
		if _, ok := seen[key]; ok {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	deltas := make([]RemediationItemDelta, 0, len(keys))
	newItems := make([]RemediationItemDelta, 0)
	regressedItems := make([]RemediationItemDelta, 0)
	existingItems := make([]RemediationItemDelta, 0)
	removedItems := make([]RemediationItemDelta, 0)
	for _, key := range keys {
		currentItem, hasCurrent := currentByID[key]
		baselineItem, hasBaseline := baselineByID[key]
		var delta RemediationItemDelta
		switch {
		case hasCurrent && !hasBaseline:
			delta = remediationItemDelta(RemediationItemNew, currentItem, RemediationItem{})
			newItems = append(newItems, delta)
		case hasCurrent && hasBaseline:
			if remediationItemRank(currentItem) > remediationItemRank(baselineItem) {
				delta = remediationItemDelta(RemediationItemRegressed, currentItem, baselineItem)
				regressedItems = append(regressedItems, delta)
			} else {
				delta = remediationItemDelta(RemediationItemExisting, currentItem, baselineItem)
				existingItems = append(existingItems, delta)
			}
		case !hasCurrent && hasBaseline:
			delta = remediationItemDelta(RemediationItemRemoved, baselineItem, baselineItem)
			removedItems = append(removedItems, delta)
		}
		deltas = append(deltas, delta)
	}
	sortRemediationItemDeltas(deltas)
	sortRemediationItemDeltas(newItems)
	sortRemediationItemDeltas(regressedItems)
	sortRemediationItemDeltas(existingItems)
	sortRemediationItemDeltas(removedItems)
	return deltas, newItems, regressedItems, existingItems, removedItems
}

func annotateRemediationItems(items []RemediationItem, comparison BaselineComparison) []RemediationItem {
	if len(items) == 0 {
		return items
	}
	statusByID := make(map[string]string, len(comparison.NewRemediationItems)+len(comparison.RegressedRemediationItems)+len(comparison.ExistingRemediationItems))
	for _, delta := range comparison.NewRemediationItems {
		statusByID[delta.ID] = string(RemediationItemNew)
	}
	for _, delta := range comparison.RegressedRemediationItems {
		statusByID[delta.ID] = string(RemediationItemRegressed)
	}
	for _, delta := range comparison.ExistingRemediationItems {
		statusByID[delta.ID] = string(RemediationItemExisting)
	}
	annotated := append([]RemediationItem(nil), items...)
	for index := range annotated {
		annotated[index].BaselineStatus = statusByID[annotated[index].ID]
	}
	return annotated
}

func remediationItemDelta(kind RemediationItemDeltaKind, item, baseline RemediationItem) RemediationItemDelta {
	return RemediationItemDelta{
		Kind:             kind,
		ID:               item.ID,
		Repo:             item.Repo,
		RepoPath:         item.RepoPath,
		Dependency:       item.Dependency,
		Category:         item.Category,
		Owner:            item.Owner,
		Team:             item.Team,
		Due:              item.Due,
		Status:           item.Status,
		RoutingSource:    item.RoutingSource,
		Severity:         item.Severity,
		Priority:         item.Priority,
		BaselineSeverity: baseline.Severity,
		BaselinePriority: baseline.Priority,
		Evidence:         append([]string(nil), item.Evidence...),
		SuggestedAction:  item.SuggestedAction,
	}
}

func remediationItemsByID(items []RemediationItem) map[string]RemediationItem {
	byID := make(map[string]RemediationItem, len(items))
	for _, item := range dedupeAndSortRemediationItems(items) {
		if item.ID == "" {
			continue
		}
		byID[item.ID] = item
	}
	return byID
}

func dedupeAndSortRemediationItems(items []RemediationItem) []RemediationItem {
	byID := make(map[string]RemediationItem, len(items))
	for _, item := range items {
		item = normalizeRemediationItem(item)
		if item.ID == "" {
			continue
		}
		existing, ok := byID[item.ID]
		if !ok {
			byID[item.ID] = item
			continue
		}
		merged := existing
		if remediationItemRank(item) > remediationItemRank(existing) {
			merged = item
		}
		merged.Evidence = compactEvidence(append(existing.Evidence, item.Evidence...))
		byID[item.ID] = merged
	}
	deduped := make([]RemediationItem, 0, len(byID))
	for _, item := range byID {
		deduped = append(deduped, item)
	}
	sortRemediationItems(deduped)
	return deduped
}

func normalizeRemediationItem(item RemediationItem) RemediationItem {
	item.ID = strings.TrimSpace(item.ID)
	item.Repo = strings.TrimSpace(item.Repo)
	item.RepoPath = strings.TrimSpace(item.RepoPath)
	item.Dependency = strings.TrimSpace(item.Dependency)
	item.Category = strings.TrimSpace(item.Category)
	item.Owner = strings.TrimSpace(item.Owner)
	item.Team = strings.TrimSpace(item.Team)
	item.Due = strings.TrimSpace(item.Due)
	item.Status = strings.TrimSpace(item.Status)
	item.RoutingSource = strings.TrimSpace(item.RoutingSource)
	item.Severity = normalizeRemediationLevel(item.Severity)
	item.Priority = normalizeRemediationLevel(item.Priority)
	item.Evidence = compactEvidence(item.Evidence)
	item.SuggestedAction = strings.TrimSpace(item.SuggestedAction)
	item.BaselineStatus = strings.TrimSpace(item.BaselineStatus)
	return item
}

func sortRemediationItems(items []RemediationItem) {
	sort.Slice(items, func(i, j int) bool {
		leftRank := remediationItemRank(items[i])
		rightRank := remediationItemRank(items[j])
		if leftRank != rightRank {
			return leftRank > rightRank
		}
		if categoryRank(items[i].Category) != categoryRank(items[j].Category) {
			return categoryRank(items[i].Category) < categoryRank(items[j].Category)
		}
		if items[i].Repo != items[j].Repo {
			return items[i].Repo < items[j].Repo
		}
		if items[i].Dependency != items[j].Dependency {
			return items[i].Dependency < items[j].Dependency
		}
		if items[i].Category != items[j].Category {
			return items[i].Category < items[j].Category
		}
		return items[i].ID < items[j].ID
	})
}

func sortRemediationItemDeltas(items []RemediationItemDelta) {
	sort.Slice(items, func(i, j int) bool {
		leftRank := remediationDeltaRank(items[i])
		rightRank := remediationDeltaRank(items[j])
		if leftRank != rightRank {
			return leftRank > rightRank
		}
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		if items[i].Repo != items[j].Repo {
			return items[i].Repo < items[j].Repo
		}
		if items[i].Dependency != items[j].Dependency {
			return items[i].Dependency < items[j].Dependency
		}
		return items[i].ID < items[j].ID
	})
}

func remediationDeltaRank(item RemediationItemDelta) int {
	return maxInt(remediationLevelRank(item.Priority), remediationLevelRank(item.Severity))
}

func remediationItemRank(item RemediationItem) int {
	return maxInt(remediationLevelRank(item.Priority), remediationLevelRank(item.Severity))
}

func remediationLevelRank(value string) int {
	switch normalizeRemediationLevel(value) {
	case report.VulnerabilityPriorityCritical:
		return 4
	case report.VulnerabilityPriorityHigh:
		return 3
	case report.VulnerabilityPriorityMedium:
		return 2
	case report.VulnerabilityPriorityLow:
		return 1
	default:
		return 0
	}
}

func normalizeRemediationLevel(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case report.VulnerabilityPriorityCritical:
		return report.VulnerabilityPriorityCritical
	case report.VulnerabilityPriorityHigh:
		return report.VulnerabilityPriorityHigh
	case "moderate", report.VulnerabilityPriorityMedium:
		return report.VulnerabilityPriorityMedium
	case report.VulnerabilityPriorityLow:
		return report.VulnerabilityPriorityLow
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func categoryRank(category string) int {
	switch category {
	case remediationCategoryRepoError:
		return 0
	case remediationCategoryVulnerability:
		return 1
	case remediationCategoryLicense:
		return 2
	case remediationCategoryRuntimeRegression:
		return 3
	case remediationCategoryRisk:
		return 4
	case remediationCategoryWaste:
		return 5
	case remediationCategoryRecommendation:
		return 6
	case remediationCategoryDuplicateDependency:
		return 7
	default:
		return 8
	}
}

func stableRemediationID(parts ...string) string {
	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.ToLower(strings.TrimSpace(part))
		if trimmed != "" {
			normalized = append(normalized, trimmed)
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(normalized, "\x00")))
	return "rqi-" + hex.EncodeToString(sum[:8])
}

func compactEvidence(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func isWasteRecommendationCode(code string) bool {
	code = strings.ToLower(strings.TrimSpace(code))
	return strings.HasPrefix(code, "remove-") || strings.Contains(code, "low-usage")
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
