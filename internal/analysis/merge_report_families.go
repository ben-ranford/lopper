package analysis

import (
	"sort"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
)

type reportFamilyMerger interface {
	merge(current report.Report)
	finalize(result *report.Report)
}

func mergeReports(repoPath string, reports []report.Report) report.Report {
	result := report.Report{
		RepoPath: repoPath,
	}

	families := []reportFamilyMerger{
		&warningsReportFamilyMerger{},
		&usageUncertaintyReportFamilyMerger{},
		&generatedAtReportFamilyMerger{},
		newDependencyReportFamilyMerger(),
	}

	for _, current := range reports {
		for _, family := range families {
			family.merge(current)
		}
	}

	for _, family := range families {
		family.finalize(&result)
	}
	return result
}

type warningsReportFamilyMerger struct {
	warnings []string
}

func (m *warningsReportFamilyMerger) merge(current report.Report) {
	m.warnings = append(m.warnings, current.Warnings...)
}

func (m *warningsReportFamilyMerger) finalize(result *report.Report) {
	result.Warnings = append([]string(nil), m.warnings...)
}

type usageUncertaintyReportFamilyMerger struct {
	usageUncertainty *report.UsageUncertainty
}

func (m *usageUncertaintyReportFamilyMerger) merge(current report.Report) {
	m.usageUncertainty = mergeUsageUncertainty(m.usageUncertainty, current.UsageUncertainty)
}

func (m *usageUncertaintyReportFamilyMerger) finalize(result *report.Report) {
	result.UsageUncertainty = m.usageUncertainty
}

type generatedAtReportFamilyMerger struct {
	generatedAt time.Time
}

func (m *generatedAtReportFamilyMerger) merge(current report.Report) {
	if current.GeneratedAt.After(m.generatedAt) {
		m.generatedAt = current.GeneratedAt
	}
}

func (m *generatedAtReportFamilyMerger) finalize(result *report.Report) {
	result.GeneratedAt = m.generatedAt
}

type dependencyReportFamilyMerger struct {
	mergedByKey map[string]report.DependencyReport
}

func newDependencyReportFamilyMerger() *dependencyReportFamilyMerger {
	return &dependencyReportFamilyMerger{
		mergedByKey: make(map[string]report.DependencyReport),
	}
}

func (m *dependencyReportFamilyMerger) merge(current report.Report) {
	for _, dep := range current.Dependencies {
		key := dependencyMergeKey(dep)
		if existing, ok := m.mergedByKey[key]; ok {
			m.mergedByKey[key] = mergeDependency(existing, dep)
			continue
		}
		m.mergedByKey[key] = dep
	}
}

func (m *dependencyReportFamilyMerger) finalize(result *report.Report) {
	orderedKeys := make([]string, 0, len(m.mergedByKey))
	for key := range m.mergedByKey {
		orderedKeys = append(orderedKeys, key)
	}
	// Preserve deterministic order in the merged report itself.
	sort.Strings(orderedKeys)
	result.Dependencies = make([]report.DependencyReport, 0, len(orderedKeys))
	for _, key := range orderedKeys {
		result.Dependencies = append(result.Dependencies, m.mergedByKey[key])
	}
}

func dependencyMergeKey(dep report.DependencyReport) string {
	return dep.Language + "\x00" + dep.Name
}
