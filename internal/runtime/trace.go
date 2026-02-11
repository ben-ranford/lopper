package runtime

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
)

type Event struct {
	Module   string `json:"module"`
	Resolved string `json:"resolved,omitempty"`
	Kind     string `json:"kind,omitempty"`
}

type Trace struct {
	DependencyLoads map[string]int
}

func Load(path string) (Trace, error) {
	// #nosec G304 -- caller intentionally selects the runtime trace file path.
	file, err := os.Open(path)
	if err != nil {
		return Trace{}, err
	}
	defer func() { _ = file.Close() }()

	trace := Trace{DependencyLoads: make(map[string]int)}
	scanner := bufio.NewScanner(file)
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
	}
	if err := scanner.Err(); err != nil {
		return Trace{}, err
	}

	return trace, nil
}

func Annotate(rep report.Report, trace Trace) report.Report {
	if len(trace.DependencyLoads) == 0 {
		return rep
	}

	for i := range rep.Dependencies {
		dep := &rep.Dependencies[i]
		if dep.Language != "" && dep.Language != "js-ts" {
			continue
		}
		loads := trace.DependencyLoads[dep.Name]
		if loads == 0 {
			continue
		}
		staticImports := len(dep.UsedImports) + len(dep.UnusedImports)
		dep.RuntimeUsage = &report.RuntimeUsage{
			LoadCount:   loads,
			RuntimeOnly: staticImports == 0,
		}
	}
	return rep
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
	if len(parts) == 0 {
		return ""
	}
	if strings.HasPrefix(parts[0], "@") {
		if len(parts) < 2 {
			return ""
		}
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}
