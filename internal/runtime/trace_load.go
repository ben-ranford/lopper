package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func Load(path string) (_ Trace, err error) {
	file, err := safeio.OpenFile(path)
	if err != nil {
		return Trace{}, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	return loadRuntimeTrace(file)
}

// LoadValidatedTrace parses bytes retained from a stable, no-follow file snapshot.
func LoadValidatedTrace(path string) (Trace, error) {
	snapshot, err := stableRuntimeTraceFileSnapshot(path)
	if err != nil {
		return Trace{}, err
	}
	return loadRuntimeTrace(bytes.NewReader(snapshot.data))
}

// Load parses the exact bytes retained by a validated capture snapshot.
func (s *ValidatedTraceSnapshot) Load() (Trace, error) {
	if s == nil {
		return Trace{}, errors.New("validated runtime trace snapshot is nil")
	}
	return loadRuntimeTrace(bytes.NewReader(s.data))
}

func loadRuntimeTrace(reader io.Reader) (Trace, error) {
	trace := newTrace()
	scanner := bufio.NewScanner(newRuntimeTraceByteLimitReader(reader, maxRuntimeTraceBytes))
	line := 0
	for scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return Trace{}, err
		}
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

type runtimeTraceByteLimitReader struct {
	reader    io.Reader
	remaining int64
}

func newRuntimeTraceByteLimitReader(reader io.Reader, maxBytes int64) io.Reader {
	return &runtimeTraceByteLimitReader{reader: reader, remaining: maxBytes}
}

func (r *runtimeTraceByteLimitReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	limit := r.remaining + 1
	if limit <= 0 {
		limit = 1
	}
	if int64(len(p)) > limit {
		p = p[:limit]
	}

	n, err := r.reader.Read(p)
	if int64(n) <= r.remaining {
		r.remaining -= int64(n)
		return n, err
	}

	allowed := int(r.remaining)
	if allowed < 0 {
		allowed = 0
	}
	r.remaining = 0
	return allowed, safeio.ErrFileTooLarge
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
	value = strings.TrimPrefix(value, fileURLPrefix)
	return filepath.ToSlash(value)
}
