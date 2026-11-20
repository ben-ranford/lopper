package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ben-ranford/lopper/internal/safeio"
)

const minRationaleRunes = 40

type allowEntry struct {
	Reference
	rationale string
	line      int
}

func checkAllowlist(path string, refs []Reference) ([]string, error) {
	entries, err := loadAllowlist(path)
	if err != nil {
		return nil, err
	}

	var findings []string
	seenRef := map[string]Reference{}
	for _, ref := range refs {
		key := formatReference(ref)
		if previous, ok := seenRef[key]; ok {
			findings = append(findings, fmt.Sprintf("%s duplicates scanned read %s", key, previous.Function))
			continue
		}
		seenRef[key] = ref
	}

	seenEntry := map[string]allowEntry{}
	seenRationale := map[string]int{}
	for _, entry := range entries {
		key := formatReference(entry.Reference)
		if previous, ok := seenEntry[key]; ok {
			findings = append(findings, fmt.Sprintf("%s:%d: duplicate allowlist entry for %s (first on line %d)", path, entry.line, key, previous.line))
			continue
		}
		seenEntry[key] = entry
		if previous, ok := seenRationale[entry.rationale]; ok {
			findings = append(findings, fmt.Sprintf("%s:%d: rationale duplicates line %d; each zero-limit read needs its own rationale", path, entry.line, previous))
			continue
		}
		seenRationale[entry.rationale] = entry.line
		if _, ok := seenRef[key]; !ok {
			findings = append(findings, fmt.Sprintf("%s:%d: stale allowlist entry %s", path, entry.line, key))
		}
	}
	for _, ref := range refs {
		if _, ok := seenEntry[formatReference(ref)]; ok {
			continue
		}
		findings = append(findings, fmt.Sprintf("unapproved zero-limit read %s", formatReference(ref)))
	}
	return findings, nil
}

func loadAllowlist(path string) ([]allowEntry, error) {
	data, err := safeio.ReadFileLimit(path, maxScanFileBytes)
	if err != nil {
		if errors.Is(err, safeio.ErrFileTooLarge) {
			return nil, fmt.Errorf("%s exceeds the %d byte allowlist limit", path, maxScanFileBytes)
		}
		return nil, err
	}

	var entries []allowEntry
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), maxScanFileBytes)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimRight(scanner.Text(), "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		entry, err := parseAllowlistLine(line, lineNo)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func parseAllowlistLine(line string, lineNo int) (allowEntry, error) {
	if !utf8.ValidString(line) {
		return allowEntry{}, fmt.Errorf("line is not valid UTF-8")
	}
	parts := strings.Split(line, "\t")
	if len(parts) != 5 {
		return allowEntry{}, fmt.Errorf("expected path, function, callee, index, and rationale separated by tabs")
	}
	path := parts[0]
	if path == "" || filepathIsUnsafe(path) {
		return allowEntry{}, fmt.Errorf("path %q must be a clean repository-relative slash path", path)
	}
	function := parts[1]
	callee := parts[2]
	if function == "" || strings.ContainsAny(function, " \t") {
		return allowEntry{}, fmt.Errorf("function %q is empty or contains whitespace", function)
	}
	if !validCallee(callee) {
		return allowEntry{}, fmt.Errorf("callee %q is not a safeio read", callee)
	}
	index, err := strconv.Atoi(parts[3])
	if err != nil || index < 1 {
		return allowEntry{}, fmt.Errorf("index %q must be a positive integer", parts[3])
	}
	rationale := strings.TrimSpace(parts[4])
	if err := validateRationale(rationale); err != nil {
		return allowEntry{}, err
	}
	return allowEntry{
		Reference: Reference{Path: path, Function: function, Callee: callee, Index: index},
		rationale: rationale,
		line:      lineNo,
	}, nil
}

func filepathIsUnsafe(path string) bool {
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\`) || strings.Contains(path, `\`) {
		return true
	}
	if path != strings.TrimSpace(path) || strings.Contains(path, "//") {
		return true
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return true
		}
	}
	return false
}

func validCallee(callee string) bool {
	name, ok := strings.CutPrefix(callee, "safeio.")
	if !ok || name == "" || strings.Contains(name, ".") {
		return false
	}
	switch name {
	case "ReadFile", "ReadFileUnder", "ReadFileWithinRoot":
		return true
	default:
		return isLimitedRead(name)
	}
}

func validateRationale(rationale string) error {
	if !utf8.ValidString(rationale) {
		return fmt.Errorf("rationale is not valid UTF-8")
	}
	count := utf8.RuneCountInString(rationale)
	if count < minRationaleRunes {
		return fmt.Errorf("rationale must be at least %d characters", minRationaleRunes)
	}
	letters := 0
	spaces := 0
	for _, r := range rationale {
		if unicode.IsControl(r) {
			return fmt.Errorf("rationale contains a control character")
		}
		if unicode.IsLetter(r) {
			letters++
		}
		if unicode.IsSpace(r) {
			spaces++
		}
	}
	if letters < 8 || spaces < 1 {
		return fmt.Errorf("rationale must explain the exception in words")
	}
	lower := strings.ToLower(rationale)
	if strings.HasPrefix(lower, "todo ") || strings.HasPrefix(lower, "tbd ") || strings.HasPrefix(lower, "fixme ") {
		return fmt.Errorf("rationale must explain why this read stays zero-limit")
	}
	return nil
}
