package js

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
)

func buildNativeModuleRiskCue(depRoot string, pkg packageJSON) (*report.RiskCue, error) {
	isNative, details, err := detectNativeModuleIndicators(depRoot, pkg)
	if err != nil {
		return nil, err
	}
	if !isNative {
		return nil, nil
	}

	msg := "dependency appears to include native module indicators"
	if len(details) > 0 {
		msg = fmt.Sprintf("%s (%s)", msg, strings.Join(details, ", "))
	}

	return &report.RiskCue{
		Code:     riskCodeNativeModule,
		Severity: "high",
		Message:  msg,
	}, nil
}

func detectNativeModuleIndicators(depRoot string, pkg packageJSON) (bool, []string, error) {
	details := collectNativeMetadataIndicators(pkg)

	bindingDetails, err := detectBindingGyp(depRoot)
	if err != nil {
		return false, nil, err
	}
	details = append(details, bindingDetails...)

	nodeBinary, err := detectNodeBinary(depRoot)
	if err != nil {
		return false, nil, err
	}
	if nodeBinary != "" {
		details = append(details, nodeBinary)
	}

	return len(details) > 0, dedupeStrings(details), nil
}

func collectNativeMetadataIndicators(pkg packageJSON) []string {
	details := make([]string, 0, 3)
	if pkg.Gypfile {
		details = append(details, "package.json:gypfile")
	}
	for _, scriptName := range []string{"preinstall", "install", "postinstall"} {
		body := strings.ToLower(strings.TrimSpace(pkg.Scripts[scriptName]))
		if body == "" {
			continue
		}
		if strings.Contains(body, "node-gyp") || strings.Contains(body, "prebuild") || strings.Contains(body, "node-pre-gyp") || strings.Contains(body, "cmake-js") {
			details = append(details, fmt.Sprintf("scripts.%s", scriptName))
		}
	}
	return details
}

func detectBindingGyp(depRoot string) ([]string, error) {
	if _, err := os.Stat(filepath.Join(depRoot, "binding.gyp")); err == nil {
		return []string{"binding.gyp"}, nil
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return nil, nil
}

func detectNodeBinary(depRoot string) (string, error) {
	const maxVisited = 600
	scanner := nodeBinaryScanner{maxVisited: maxVisited}
	if err := filepath.WalkDir(depRoot, scanner.walk); err != nil && !errors.Is(err, fs.SkipAll) {
		return "", err
	}
	return scanner.found, nil
}

type nodeBinaryScanner struct {
	visited    int
	maxVisited int
	found      string
}

func (s *nodeBinaryScanner) walk(path string, entry fs.DirEntry, err error) error {
	if err != nil {
		return err
	}
	if entry.IsDir() {
		if entry.Name() == "node_modules" {
			return filepath.SkipDir
		}
		return nil
	}

	s.visited++
	if s.visited > s.maxVisited {
		return fs.SkipAll
	}
	if strings.EqualFold(filepath.Ext(entry.Name()), ".node") {
		s.found = filepath.Base(path)
		return fs.SkipAll
	}
	return nil
}

func dedupeStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := set[value]; ok {
			continue
		}
		set[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
