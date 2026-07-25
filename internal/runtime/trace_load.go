package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	maxRuntimeTraceBytes        int64 = 8 * 1024 * 1024
	maxRuntimeTraceLines              = 600_000
	maxRuntimeTraceEvents             = 500_000
	maxRuntimeTraceObjectFields       = 16
	maxRuntimeTraceNameBytes          = 1_024
	maxRuntimeTraceLineBytes          = bufio.MaxScanTokenSize
)

const runtimeTraceOpenUnsupportedMessage = "runtime trace path opening unsupported: exact pinned no-follow regular-file opening is unavailable"

var ErrTraceOpenUnsupported = errors.New(runtimeTraceOpenUnsupportedMessage)

var runtimeTraceFieldNames = []string{
	"dependency",
	"entrypoint",
	"kind",
	"language",
	"module",
	"parent",
	"resolved",
}

var runtimeTraceEventParseHook func(string)

var (
	loadRuntimeTraceFile           = openRuntimeTraceFile
	runtimeTraceLstat              = os.Lstat
	runtimeTraceOpenFileNoFollow   = safeio.OpenFileNoFollow
	runtimeTraceOpenFileNoFollowOK = safeio.OpenFileNoFollowSupported
	runtimeTraceSameFile           = os.SameFile
	runtimeTraceBeforeOpen         func()
)

func Load(path string) (_ Trace, err error) {
	return LoadContext(context.Background(), path)
}

func LoadContext(ctx context.Context, path string) (_ Trace, err error) {
	if err := ctx.Err(); err != nil {
		return Trace{}, err
	}
	file, err := loadRuntimeTraceFile(path)
	if err != nil {
		return Trace{}, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	trace := newTrace()
	reader := bufio.NewReaderSize(newRuntimeTraceByteLimitReader(file, maxRuntimeTraceBytes), maxRuntimeTraceLineBytes+1)
	line := 0
	eventCount := 0
	for {
		if err := ctx.Err(); err != nil {
			return Trace{}, err
		}
		text, err := readRuntimeTraceLine(reader)
		switch {
		case errors.Is(err, safeio.ErrFileTooLarge):
			return Trace{}, safeio.ErrFileTooLarge
		case errors.Is(err, bufio.ErrTooLong):
			return Trace{}, bufio.ErrTooLong
		case errors.Is(err, io.EOF):
			if text == "" {
				return trace, nil
			}
		case err != nil:
			return Trace{}, err
		}
		line++
		if line > maxRuntimeTraceLines {
			return Trace{}, fmt.Errorf("runtime trace exceeds maximum line count of %d", maxRuntimeTraceLines)
		}
		text = strings.TrimSpace(text)
		if text == "" {
			if errors.Is(err, io.EOF) {
				return trace, nil
			}
			continue
		}
		eventCount++
		if eventCount > maxRuntimeTraceEvents {
			return Trace{}, fmt.Errorf("runtime trace exceeds maximum event count of %d", maxRuntimeTraceEvents)
		}
		event, err := parseRuntimeTraceEvent(ctx, []byte(text))
		if err != nil {
			return Trace{}, fmt.Errorf("parse runtime trace line %d: %w", line, err)
		}
		language := normalizeRuntimeLanguage(event.Language)
		dep := dependencyFromEventForLanguage(event, language)
		if dep == "" {
			continue
		}
		if len(dep) > maxRuntimeTraceNameBytes {
			return Trace{}, fmt.Errorf("runtime trace dependency name exceeds %d bytes", maxRuntimeTraceNameBytes)
		}
		module := runtimeModuleFromEventForLanguage(event, language, dep)
		if len(module) > maxRuntimeTraceNameBytes {
			return Trace{}, fmt.Errorf("runtime trace module name exceeds %d bytes", maxRuntimeTraceNameBytes)
		}
		symbol := runtimeSymbolFromModuleForLanguage(module, language, dep)
		addRuntimeEvent(&trace, language, dep, module, runtimeContextValue(event.Parent), runtimeContextValue(event.Entrypoint), symbol)
		if errors.Is(err, io.EOF) {
			return trace, nil
		}
	}
}

func readRuntimeTraceLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return "", bufio.ErrTooLong
	}
	return string(line), err
}

func openRuntimeTraceFile(path string) (io.ReadCloser, error) {
	if !runtimeTraceOpenFileNoFollowOK() {
		return nil, ErrTraceOpenUnsupported
	}
	info, err := runtimeTraceLstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("runtime trace path is a symlink: %s", path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("runtime trace path is not a regular file: %s", path)
	}
	if runtimeTraceBeforeOpen != nil {
		runtimeTraceBeforeOpen()
	}

	file, err := runtimeTraceOpenFileNoFollow(path)
	if err != nil {
		return nil, err
	}
	statFile, ok := file.(statReadCloser)
	if !ok {
		closeErr := file.Close()
		return nil, errors.Join(errors.New("runtime trace file does not support stat"), closeErr)
	}
	openedInfo, err := fileInfo(statFile)
	if err != nil {
		closeErr := file.Close()
		return nil, errors.Join(err, closeErr)
	}
	if !openedInfo.Mode().IsRegular() {
		closeErr := file.Close()
		return nil, errors.Join(fmt.Errorf("runtime trace path is not a regular file: %s", path), closeErr)
	}
	if !runtimeTraceSameFile(info, openedInfo) {
		closeErr := file.Close()
		return nil, errors.Join(fmt.Errorf("runtime trace path changed while opening: %s", path), closeErr)
	}
	return file, nil
}

type statReadCloser interface {
	io.ReadCloser
	Stat() (fs.FileInfo, error)
}

func fileInfo(file statReadCloser) (fs.FileInfo, error) {
	return file.Stat()
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

func parseRuntimeTraceEvent(ctx context.Context, data []byte) (Event, error) {
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return Event{}, err
	}
	if len(raw) > maxRuntimeTraceObjectFields {
		return Event{}, fmt.Errorf("runtime trace event exceeds maximum object entries of %d", maxRuntimeTraceObjectFields)
	}

	var event Event
	for _, field := range runtimeTraceFieldNames {
		if err := ctx.Err(); err != nil {
			return Event{}, err
		}
		if runtimeTraceEventParseHook != nil {
			runtimeTraceEventParseHook(field)
			if err := ctx.Err(); err != nil {
				return Event{}, err
			}
		}
		value, ok := raw[field]
		if !ok {
			continue
		}
		switch field {
		case "language":
			if err := json.Unmarshal(value, &event.Language); err != nil {
				return Event{}, fmt.Errorf("decode %s: %w", field, err)
			}
		case "dependency":
			if err := json.Unmarshal(value, &event.Dependency); err != nil {
				return Event{}, fmt.Errorf("decode %s: %w", field, err)
			}
		case "module":
			if err := json.Unmarshal(value, &event.Module); err != nil {
				return Event{}, fmt.Errorf("decode %s: %w", field, err)
			}
		case "resolved":
			if err := json.Unmarshal(value, &event.Resolved); err != nil {
				return Event{}, fmt.Errorf("decode %s: %w", field, err)
			}
		case "kind":
			if err := json.Unmarshal(value, &event.Kind); err != nil {
				return Event{}, fmt.Errorf("decode %s: %w", field, err)
			}
		case "parent":
			if err := json.Unmarshal(value, &event.Parent); err != nil {
				return Event{}, fmt.Errorf("decode %s: %w", field, err)
			}
		case "entrypoint":
			if err := json.Unmarshal(value, &event.Entrypoint); err != nil {
				return Event{}, fmt.Errorf("decode %s: %w", field, err)
			}
		}
	}
	return event, nil
}
