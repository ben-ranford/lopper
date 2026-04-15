package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

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
