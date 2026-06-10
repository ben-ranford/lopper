package analysis

import "github.com/ben-ranford/lopper/internal/report"

func annotateDerivedDependencyMetrics(dependencies []report.DependencyReport) {
	for index := range dependencies {
		annotateDependencyUsedPercent(&dependencies[index])
		annotateRuntimeCorrelation(dependencies[index].RuntimeUsage)
	}
}

func annotateDependencyUsedPercent(dep *report.DependencyReport) {
	if dep == nil || dep.UsedPercent > 0 || dep.TotalExportsCount <= 0 {
		return
	}
	dep.UsedPercent = (float64(dep.UsedExportsCount) / float64(dep.TotalExportsCount)) * 100
}

func annotateRuntimeCorrelation(usage *report.RuntimeUsage) {
	if usage == nil || usage.Correlation != "" {
		return
	}
	switch {
	case usage.RuntimeOnly:
		usage.Correlation = report.RuntimeCorrelationRuntimeOnly
	case usage.LoadCount > 0:
		usage.Correlation = report.RuntimeCorrelationOverlap
	default:
		usage.Correlation = report.RuntimeCorrelationStaticOnly
	}
}
