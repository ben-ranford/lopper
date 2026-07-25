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
	"github.com/ben-ranford/lopper/internal/safeio"
)

func buildNativeModuleRiskCueWithinRoot(root safeio.Root, depRoot string, pkg packageJSON) (*report.RiskCue, error) {
	isNative, details, err := detectNativeModuleIndicatorsWithinRoot(root, depRoot, pkg)
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

func detectNativeModuleIndicators(depRoot string, pkg packageJSON) (native bool, details []string, err error) {
	root, validatedDepRoot, err := openValidatedRootNoFollow(depRoot)
	if err != nil {
		return false, nil, err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()
	return detectNativeModuleIndicatorsWithinRoot(root, validatedDepRoot, pkg)
}

func detectNativeModuleIndicatorsWithinRoot(root safeio.Root, depRoot string, pkg packageJSON) (bool, []string, error) {
	details := collectNativeMetadataIndicators(pkg)

	bindingDetails, err := detectBindingGypWithinRoot(root)
	if err != nil {
		return false, nil, err
	}
	details = append(details, bindingDetails...)

	nodeBinary, err := detectNodeBinaryWithinRoot(root, depRoot)
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

func detectBindingGyp(depRoot string) (details []string, err error) {
	root, _, err := openValidatedRootNoFollow(depRoot)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()
	return detectBindingGypWithinRoot(root)
}

func detectBindingGypWithinRoot(root safeio.Root) ([]string, error) {
	if info, err := root.Lstat("binding.gyp"); err == nil && info.Mode().IsRegular() {
		return []string{"binding.gyp"}, nil
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return nil, nil
}

func detectNodeBinary(depRoot string) (binary string, err error) {
	root, validatedDepRoot, err := openValidatedRootNoFollow(depRoot)
	if err != nil {
		return "", err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()
	return detectNodeBinaryWithinRoot(root, validatedDepRoot)
}

func detectNodeBinaryWithinRoot(root safeio.Root, depRoot string) (string, error) {
	const maxVisited = 600
	scanner := nodeBinaryScanner{maxVisited: maxVisited}
	if err := walkRootNoFollow(root, func(relPath string, info fs.FileInfo) (bool, bool, error) {
		return scanner.walkInfo(depRoot, relPath, info)
	}); err != nil && !errors.Is(err, fs.SkipAll) {
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
	info, err := entry.Info()
	if err != nil {
		return err
	}
	skipDir, stop, err := s.walkInfo(path, path, info)
	if skipDir {
		return filepath.SkipDir
	}
	if stop {
		return err
	}
	return err
}

func (s *nodeBinaryScanner) walkInfo(depRoot, relPath string, info fs.FileInfo) (bool, bool, error) {
	if info.IsDir() {
		return filepath.Base(relPath) == "node_modules", false, nil
	}

	s.visited++
	if s.visited > s.maxVisited {
		return false, true, fs.SkipAll
	}
	if strings.EqualFold(filepath.Ext(relPath), ".node") {
		s.found = filepath.Base(filepath.Join(depRoot, relPath))
		return false, true, fs.SkipAll
	}
	return false, false, nil
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
