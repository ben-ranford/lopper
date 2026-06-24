package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func Load(path string) (Trace, error) {
	content, err := safeio.ReadFile(path)
	if err != nil {
		return Trace{}, err
	}

	trace := newTrace()
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
		language := normalizeRuntimeLanguage(event.Language)
		dep := dependencyFromEventForLanguage(event, language)
		if dep == "" {
			continue
		}
		module := runtimeModuleFromEventForLanguage(event, language, dep)
		symbol := runtimeSymbolFromModuleForLanguage(module, language, dep)
		addRuntimeEvent(&trace, language, dep, module, runtimeContextValue(event.Parent), runtimeContextValue(event.Entrypoint), symbol)
	}
	if err := scanner.Err(); err != nil {
		return Trace{}, err
	}

	return trace, nil
}

func newTrace() Trace {
	return Trace{
		DependencyLoads:                 make(map[string]int),
		DependencyModules:               make(map[string]map[string]int),
		DependencyParents:               make(map[string]map[string]int),
		DependencyEntrypoints:           make(map[string]map[string]int),
		DependencySymbols:               make(map[string]map[string]int),
		DependencyLoadsByLanguage:       make(map[DependencyKey]int),
		DependencyModulesByLanguage:     make(map[DependencyKey]map[string]int),
		DependencyParentsByLanguage:     make(map[DependencyKey]map[string]int),
		DependencyEntrypointsByLanguage: make(map[DependencyKey]map[string]int),
		DependencySymbolsByLanguage:     make(map[DependencyKey]map[string]int),
	}
}

func addRuntimeEvent(trace *Trace, language, dependency, module, parent, entrypoint, symbol string) {
	key := DependencyKey{Language: normalizeRuntimeLanguage(language), Name: dependency}
	trace.DependencyLoadsByLanguage[key]++
	addCountByKey(trace.DependencyModulesByLanguage, key, module)
	addCountByKey(trace.DependencyParentsByLanguage, key, parent)
	addCountByKey(trace.DependencyEntrypointsByLanguage, key, entrypoint)
	addSymbolCountByKey(trace.DependencySymbolsByLanguage, key, module, symbol)
	if key.Language != runtimeLanguageJSTS {
		return
	}
	trace.DependencyLoads[dependency]++
	addCount(trace.DependencyModules, dependency, module)
	addCount(trace.DependencyParents, dependency, parent)
	addCount(trace.DependencyEntrypoints, dependency, entrypoint)
	addSymbolCount(trace.DependencySymbols, dependency, module, symbol)
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

func addCountByKey(target map[DependencyKey]map[string]int, key DependencyKey, value string) {
	if key.Name == "" || value == "" {
		return
	}
	items, ok := target[key]
	if !ok {
		items = make(map[string]int)
		target[key] = items
	}
	items[value]++
}

func addSymbolCountByKey(target map[DependencyKey]map[string]int, key DependencyKey, module string, symbol string) {
	if key.Name == "" || symbol == "" {
		return
	}
	items, ok := target[key]
	if !ok {
		items = make(map[string]int)
		target[key] = items
	}
	items[module+"\x00"+symbol]++
}

func runtimeContextValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.TrimPrefix(value, "file://")
	return filepath.ToSlash(value)
}
