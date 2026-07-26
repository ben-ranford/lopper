package runtime

import (
	"bufio"
	"bytes"
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

var runtimeTraceEventParseHook func(string)

var (
	loadRuntimeTraceFile         = openRuntimeTraceFile
	runtimeTraceLstat            = os.Lstat
	runtimeTraceOpenFileNoFollow = safeio.OpenFileNoFollow
	runtimeTraceSameFile         = os.SameFile
	runtimeTraceBeforeOpen       func()
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
	state := runtimeTraceLoadState{trace: &trace}
	for {
		if err := ctx.Err(); err != nil {
			return Trace{}, err
		}
		text, readErr := readRuntimeTraceLine(reader)
		done, err := state.consumeLine(ctx, text, readErr)
		if err != nil {
			return Trace{}, err
		}
		if done {
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

type runtimeTraceLoadState struct {
	trace      *Trace
	line       int
	eventCount int
}

func (s *runtimeTraceLoadState) consumeLine(ctx context.Context, text string, readErr error) (bool, error) {
	done, err := runtimeTraceReadStatus(text, readErr)
	if err != nil {
		return false, err
	}
	if done && text == "" {
		return true, nil
	}

	s.line++
	if s.line > maxRuntimeTraceLines {
		return false, fmt.Errorf("runtime trace exceeds maximum line count of %d", maxRuntimeTraceLines)
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return done, nil
	}

	s.eventCount++
	if s.eventCount > maxRuntimeTraceEvents {
		return false, fmt.Errorf("runtime trace exceeds maximum event count of %d", maxRuntimeTraceEvents)
	}

	if err := s.addEvent(ctx, []byte(text)); err != nil {
		return false, err
	}

	return done, nil
}

func runtimeTraceReadStatus(text string, err error) (bool, error) {
	switch {
	case errors.Is(err, safeio.ErrFileTooLarge):
		return false, safeio.ErrFileTooLarge
	case errors.Is(err, bufio.ErrTooLong):
		return false, bufio.ErrTooLong
	case errors.Is(err, io.EOF):
		return text == "", nil
	case err != nil:
		return false, err
	default:
		return false, nil
	}
}

func (s *runtimeTraceLoadState) addEvent(ctx context.Context, data []byte) error {
	event, err := parseRuntimeTraceEvent(ctx, data)
	if err != nil {
		return fmt.Errorf("parse runtime trace line %d: %w", s.line, err)
	}

	language := normalizeRuntimeLanguage(event.Language)
	dependency := dependencyFromEventForLanguage(event, language)
	if dependency == "" {
		return nil
	}
	if len(dependency) > maxRuntimeTraceNameBytes {
		return fmt.Errorf("runtime trace dependency name exceeds %d bytes", maxRuntimeTraceNameBytes)
	}

	module := runtimeModuleFromEventForLanguage(event, language, dependency)
	if len(module) > maxRuntimeTraceNameBytes {
		return fmt.Errorf("runtime trace module name exceeds %d bytes", maxRuntimeTraceNameBytes)
	}

	parent := runtimeContextValue(event.Parent)
	entrypoint := runtimeContextValue(event.Entrypoint)
	symbol := runtimeSymbolFromModuleForLanguage(module, language, dependency)
	addRuntimeEvent(s.trace, language, dependency, module, parent, entrypoint, symbol)
	return nil
}

func openRuntimeTraceFile(path string) (io.ReadCloser, error) {
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
		if errors.Is(err, safeio.ErrOpenFileNoFollowUnsupported) {
			return nil, ErrTraceOpenUnsupported
		}
		return nil, err
	}
	statFile, ok := file.(statReadCloser)
	if !ok {
		return nil, closeRuntimeTraceFileWithError(file, errors.New("runtime trace file does not support stat"))
	}
	openedInfo, err := fileInfo(statFile)
	if err != nil {
		return nil, closeRuntimeTraceFileWithError(file, err)
	}
	if !openedInfo.Mode().IsRegular() {
		return nil, closeRuntimeTraceFileWithError(file, fmt.Errorf("runtime trace path is not a regular file: %s", path))
	}
	if !runtimeTraceSameFile(info, openedInfo) {
		return nil, closeRuntimeTraceFileWithError(file, fmt.Errorf("runtime trace path changed while opening: %s", path))
	}
	return file, nil
}

func closeRuntimeTraceFileWithError(file io.Closer, err error) error {
	return errors.Join(err, file.Close())
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
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := requireRuntimeTraceObjectStart(decoder); err != nil {
		return Event{}, err
	}

	eventDecoder := newRuntimeTraceObjectDecoder()
	if err := eventDecoder.decodeFields(ctx, decoder); err != nil {
		return Event{}, err
	}
	if err := requireRuntimeTraceObjectEnd(decoder); err != nil {
		return Event{}, err
	}
	return eventDecoder.event, nil
}

type runtimeTraceObjectDecoder struct {
	event      Event
	seenFields map[string]struct{}
	fieldCount int
}

func newRuntimeTraceObjectDecoder() *runtimeTraceObjectDecoder {
	return &runtimeTraceObjectDecoder{
		seenFields: make(map[string]struct{}, maxRuntimeTraceObjectFields),
	}
}

func (d *runtimeTraceObjectDecoder) decodeFields(ctx context.Context, decoder *json.Decoder) error {
	for decoder.More() {
		if err := d.decodeField(ctx, decoder); err != nil {
			return err
		}
	}
	return nil
}

func (d *runtimeTraceObjectDecoder) decodeField(ctx context.Context, decoder *json.Decoder) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	field, err := readRuntimeTraceFieldName(decoder)
	if err != nil {
		return err
	}
	if err := d.recordField(field); err != nil {
		return err
	}
	if err := runRuntimeTraceEventParseHook(ctx, field); err != nil {
		return err
	}
	return decodeRuntimeTraceFieldFromDecoder(field, decoder, &d.event)
}

func (d *runtimeTraceObjectDecoder) recordField(field string) error {
	d.fieldCount++
	if d.fieldCount > maxRuntimeTraceObjectFields {
		return fmt.Errorf("runtime trace event exceeds maximum object entries of %d", maxRuntimeTraceObjectFields)
	}
	if _, duplicate := d.seenFields[field]; duplicate {
		return fmt.Errorf("runtime trace event contains duplicate field %q", field)
	}
	d.seenFields[field] = struct{}{}
	return nil
}

func requireRuntimeTraceObjectStart(decoder *json.Decoder) error {
	start, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := start.(json.Delim)
	if !ok || delimiter != '{' {
		return fmt.Errorf("runtime trace event must be a JSON object")
	}
	return nil
}

func readRuntimeTraceFieldName(decoder *json.Decoder) (string, error) {
	token, err := decoder.Token()
	if err != nil {
		return "", err
	}
	field, ok := token.(string)
	if !ok {
		return "", fmt.Errorf("runtime trace event field name is not a string")
	}
	return field, nil
}

func runRuntimeTraceEventParseHook(ctx context.Context, field string) error {
	if runtimeTraceEventParseHook != nil {
		runtimeTraceEventParseHook(field)
	}
	return ctx.Err()
}

func requireRuntimeTraceObjectEnd(decoder *json.Decoder) error {
	end, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := end.(json.Delim)
	if !ok || delimiter != '}' {
		return fmt.Errorf("runtime trace event must end with a JSON object")
	}

	_, err = decoder.Token()
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("runtime trace event must contain exactly one JSON object")
}

func decodeRuntimeTraceFieldFromDecoder(field string, decoder *json.Decoder, event *Event) error {
	switch field {
	case "language":
		return decodeRuntimeTraceStringField(field, decoder, &event.Language)
	case "dependency":
		return decodeRuntimeTraceStringField(field, decoder, &event.Dependency)
	case "module":
		return decodeRuntimeTraceStringField(field, decoder, &event.Module)
	case "resolved":
		return decodeRuntimeTraceStringField(field, decoder, &event.Resolved)
	case "kind":
		return decodeRuntimeTraceStringField(field, decoder, &event.Kind)
	case "parent":
		return decodeRuntimeTraceStringField(field, decoder, &event.Parent)
	case "entrypoint":
		return decodeRuntimeTraceStringField(field, decoder, &event.Entrypoint)
	default:
		var discard json.RawMessage
		return decoder.Decode(&discard)
	}
}

func decodeRuntimeTraceStringField(field string, decoder *json.Decoder, target *string) error {
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", field, err)
	}
	return nil
}
