package runtime

import (
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
)

func Annotate(rep report.Report, trace Trace, opts AnnotateOptions) report.Report {
	seen := annotateExistingDependencies(&rep, trace)
	if opts.IncludeRuntimeOnlyRows {
		appendRuntimeOnlyDependencies(&rep, trace, seen)
		sortDependencies(rep.Dependencies)
	}
	return rep
}

func annotateExistingDependencies(rep *report.Report, trace Trace) map[string]struct{} {
	seen := make(map[string]struct{}, len(rep.Dependencies))
	for i := range rep.Dependencies {
		dep := &rep.Dependencies[i]
		if dep.Language != "" && dep.Language != "js-ts" {
			continue
		}
		seen[dep.Name] = struct{}{}
		annotateDependency(dep, trace)
	}
	return seen
}

func annotateDependency(dep *report.DependencyReport, trace Trace) {
	loads := trace.DependencyLoads[dep.Name]
	hasStatic := hasStaticEvidence(*dep)
	if loads == 0 && !hasStatic && dep.RuntimeUsage == nil {
		return
	}
	correlation := runtimeCorrelation(hasStatic, loads > 0)
	dep.RuntimeUsage = &report.RuntimeUsage{
		LoadCount:   loads,
		Correlation: correlation,
		RuntimeOnly: correlation == report.RuntimeCorrelationRuntimeOnly,
		Modules:     runtimeModules(trace.DependencyModules[dep.Name]),
		TopSymbols:  runtimeSymbols(trace.DependencySymbols[dep.Name]),
	}
}

func appendRuntimeOnlyDependencies(rep *report.Report, trace Trace, seen map[string]struct{}) {
	for dependency, loads := range trace.DependencyLoads {
		if loads == 0 {
			continue
		}
		if _, ok := seen[dependency]; ok {
			continue
		}
		rep.Dependencies = append(rep.Dependencies, report.DependencyReport{
			Language: "js-ts",
			Name:     dependency,
			RuntimeUsage: &report.RuntimeUsage{
				LoadCount:   loads,
				Correlation: report.RuntimeCorrelationRuntimeOnly,
				RuntimeOnly: true,
				Modules:     runtimeModules(trace.DependencyModules[dependency]),
				TopSymbols:  runtimeSymbols(trace.DependencySymbols[dependency]),
			},
		})
	}
}

func sortDependencies(dependencies []report.DependencyReport) {
	sort.Slice(dependencies, func(i, j int) bool {
		left := dependencies[i]
		right := dependencies[j]
		if left.Language == right.Language {
			return left.Name < right.Name
		}
		return left.Language < right.Language
	})
}

func hasStaticEvidence(dep report.DependencyReport) bool {
	return len(dep.UsedImports)+len(dep.UnusedImports) > 0
}

func runtimeCorrelation(hasStatic, hasRuntime bool) report.RuntimeCorrelation {
	switch {
	case hasStatic && hasRuntime:
		return report.RuntimeCorrelationOverlap
	case hasRuntime:
		return report.RuntimeCorrelationRuntimeOnly
	default:
		return report.RuntimeCorrelationStaticOnly
	}
}

func runtimeModules(values map[string]int) []report.RuntimeModuleUsage {
	if len(values) == 0 {
		return nil
	}
	items := make([]report.RuntimeModuleUsage, 0, len(values))
	for module, count := range values {
		items = append(items, report.RuntimeModuleUsage{Module: module, Count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count == items[j].Count {
			return items[i].Module < items[j].Module
		}
		return items[i].Count > items[j].Count
	})
	return items
}

func runtimeSymbols(values map[string]int) []report.RuntimeSymbolUsage {
	if len(values) == 0 {
		return nil
	}
	items := make([]report.RuntimeSymbolUsage, 0, len(values))
	for key, count := range values {
		parts := strings.SplitN(key, "\x00", 2)
		symbol := key
		module := ""
		if len(parts) == 2 {
			module = parts[0]
			symbol = parts[1]
		}
		items = append(items, report.RuntimeSymbolUsage{Symbol: symbol, Module: module, Count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count == items[j].Count {
			if items[i].Symbol == items[j].Symbol {
				return items[i].Module < items[j].Module
			}
			return items[i].Symbol < items[j].Symbol
		}
		return items[i].Count > items[j].Count
	})
	if len(items) > 5 {
		items = items[:5]
	}
	return items
}
