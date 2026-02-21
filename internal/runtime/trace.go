package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

type Event struct {
	Module   string `json:"module"`
	Resolved string `json:"resolved,omitempty"`
	Kind     string `json:"kind,omitempty"`
}

type Trace struct {
	DependencyLoads   map[string]int
	DependencyModules map[string]map[string]int
	DependencySymbols map[string]map[string]int
}

type AnnotateOptions struct {
	IncludeRuntimeOnlyRows bool
}

func Load(path string) (Trace, error) {
	content, err := safeio.ReadFile(path)
	if err != nil {
		return Trace{}, err
	}

	trace := Trace{
		DependencyLoads:   make(map[string]int),
		DependencyModules: make(map[string]map[string]int),
		DependencySymbols: make(map[string]map[string]int),
	}
	scanner := bufio.NewScanner(bytes.NewReader(content))
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(text), &event); err != nil {
			return Trace{}, fmt.Errorf("parse runtime trace line %d: %w", line, err)
		}
		dep := dependencyFromEvent(event)
		if dep == "" {
			continue
		}
		trace.DependencyLoads[dep]++
		module := runtimeModuleFromEvent(event, dep)
		addCount(trace.DependencyModules, dep, module)
		symbol := runtimeSymbolFromModule(module, dep)
		addSymbolCount(trace.DependencySymbols, dep, module, symbol)
	}
	if err := scanner.Err(); err != nil {
		return Trace{}, err
	}

	return trace, nil
}

func Annotate(rep report.Report, trace Trace, opts AnnotateOptions) report.Report {
	seen := make(map[string]struct{}, len(rep.Dependencies))
	for i := range rep.Dependencies {
		dep := &rep.Dependencies[i]
		if dep.Language != "" && dep.Language != "js-ts" {
			continue
		}
		seen[dep.Name] = struct{}{}
		loads := trace.DependencyLoads[dep.Name]
		hasStatic := hasStaticEvidence(*dep)
		if loads == 0 && !hasStatic && dep.RuntimeUsage == nil {
			continue
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

	if opts.IncludeRuntimeOnlyRows {
		for dep, loads := range trace.DependencyLoads {
			if loads == 0 {
				continue
			}
			if _, ok := seen[dep]; ok {
				continue
			}
			rep.Dependencies = append(rep.Dependencies, report.DependencyReport{
				Language: "js-ts",
				Name:     dep,
				RuntimeUsage: &report.RuntimeUsage{
					LoadCount:   loads,
					Correlation: report.RuntimeCorrelationRuntimeOnly,
					RuntimeOnly: true,
					Modules:     runtimeModules(trace.DependencyModules[dep]),
					TopSymbols:  runtimeSymbols(trace.DependencySymbols[dep]),
				},
			})
		}
		sort.Slice(rep.Dependencies, func(i, j int) bool {
			left := rep.Dependencies[i]
			right := rep.Dependencies[j]
			if left.Language == right.Language {
				return left.Name < right.Name
			}
			return left.Language < right.Language
		})
	}

	return rep
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

func addCount(target map[string]map[string]int, dependency string, value string) {
	if dependency == "" || value == "" {
		return
	}
	items, ok := target[dependency]
	if !ok {
		items = make(map[string]int)
		target[dependency] = items
	}
	items[value]++
}

func addSymbolCount(target map[string]map[string]int, dependency string, module string, symbol string) {
	if dependency == "" || symbol == "" {
		return
	}
	items, ok := target[dependency]
	if !ok {
		items = make(map[string]int)
		target[dependency] = items
	}
	items[module+"\x00"+symbol]++
}

func runtimeModuleFromEvent(event Event, dependency string) string {
	if module := runtimeModuleFromSpecifier(event.Module, dependency); module != "" {
		return module
	}
	if module := runtimeModuleFromResolvedPath(event.Resolved, dependency); module != "" {
		return module
	}
	return dependency
}

func runtimeModuleFromSpecifier(specifier string, dependency string) string {
	specifier = strings.TrimSpace(specifier)
	if specifier == "" {
		return ""
	}
	if dependencyFromSpecifier(specifier) != dependency {
		return ""
	}
	return specifier
}

func runtimeModuleFromResolvedPath(value string, dependency string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.TrimPrefix(value, "file://")
	value = filepath.ToSlash(value)

	marker := "/node_modules/"
	pos := strings.LastIndex(value, marker)
	if pos < 0 {
		return ""
	}
	rest := value[pos+len(marker):]
	if rest == "" {
		return ""
	}
	parts := strings.Split(rest, "/")
	if strings.HasPrefix(parts[0], "@") {
		if len(parts) < 2 {
			return ""
		}
		if dependency != parts[0]+"/"+parts[1] {
			return ""
		}
		if len(parts) == 2 {
			return dependency
		}
		return dependency + "/" + strings.Join(parts[2:], "/")
	}
	if dependency != parts[0] {
		return ""
	}
	if len(parts) == 1 {
		return dependency
	}
	return dependency + "/" + strings.Join(parts[1:], "/")
}

func runtimeSymbolFromModule(module string, dependency string) string {
	if module == "" || dependency == "" {
		return ""
	}
	subpath := strings.TrimPrefix(module, dependency)
	subpath = strings.TrimPrefix(subpath, "/")
	if subpath == "" {
		return ""
	}
	base := path.Base(subpath)
	ext := path.Ext(base)
	name := strings.TrimSuffix(base, ext)
	if name == "" || name == "." {
		return ""
	}
	if name == "index" {
		dir := path.Base(path.Dir(subpath))
		if dir != "." && dir != "/" {
			return dir
		}
	}
	return name
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

func dependencyFromEvent(event Event) string {
	if dep := dependencyFromSpecifier(event.Module); dep != "" {
		return dep
	}
	return dependencyFromResolvedPath(event.Resolved)
}

func dependencyFromSpecifier(specifier string) string {
	specifier = strings.TrimSpace(specifier)
	if specifier == "" {
		return ""
	}
	if strings.HasPrefix(specifier, ".") || strings.HasPrefix(specifier, "/") || strings.Contains(specifier, ":") {
		return ""
	}
	if strings.HasPrefix(specifier, "@") {
		parts := strings.SplitN(specifier, "/", 3)
		if len(parts) < 2 {
			return ""
		}
		return parts[0] + "/" + parts[1]
	}
	parts := strings.SplitN(specifier, "/", 2)
	return parts[0]
}

func dependencyFromResolvedPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.TrimPrefix(value, "file://")
	value = filepath.ToSlash(value)

	marker := "/node_modules/"
	pos := strings.LastIndex(value, marker)
	if pos < 0 {
		return ""
	}
	rest := value[pos+len(marker):]
	if rest == "" {
		return ""
	}
	parts := strings.Split(rest, "/")
	if strings.HasPrefix(parts[0], "@") {
		if len(parts) < 2 {
			return ""
		}
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}
