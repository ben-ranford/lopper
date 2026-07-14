package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/safeio"
)

type releaseDelta struct {
	Release string               `json:"release"`
	Updates []releaseDeltaUpdate `json:"updates"`
}

type releaseDeltaUpdate struct {
	Code               string `json:"code"`
	FirstStableRelease string `json:"firstStableRelease"`
}

func runExportReleaseDelta(args []string) error {
	fs := flag.NewFlagSet("export-release-delta", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	release := fs.String("release", strings.TrimSpace(os.Getenv("RELEASE")), "stable release version to export")
	output := fs.String("output", strings.TrimSpace(os.Getenv("OUTPUT")), "output path for the release delta JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("too many arguments for featureflag export-release-delta")
	}

	resolvedRelease := normalizeReleaseVersion(*release)
	if resolvedRelease == "" {
		return fmt.Errorf("release version is required")
	}
	outputPath := strings.TrimSpace(*output)
	if outputPath == "" {
		return fmt.Errorf("release delta output path is required")
	}

	root, err := getwdFn()
	if err != nil {
		return fmt.Errorf(resolveWorkingDirectoryError, err)
	}
	flags, err := readCatalog(root)
	if err != nil {
		return err
	}

	delta := releaseDelta{Release: resolvedRelease}
	for _, flag := range flags {
		if flag.FirstStableRelease != "" {
			continue
		}
		delta.Updates = append(delta.Updates, releaseDeltaUpdate{
			Code:               flag.Code,
			FirstStableRelease: resolvedRelease,
		})
	}

	data, err := formatReleaseDelta(delta)
	if err != nil {
		return err
	}
	absoluteOutput := filepath.Join(root, outputPath)
	if err := os.MkdirAll(filepath.Dir(absoluteOutput), 0o750); err != nil {
		return fmt.Errorf("create release delta output directory: %w", err)
	}
	if err := writeFileUnderFn(root, absoluteOutput, data, 0o644); err != nil {
		return fmt.Errorf("write release delta: %w", err)
	}

	_, err = fmt.Fprintf(os.Stdout, "exported %d feature release delta update(s) for %s\n", len(delta.Updates), resolvedRelease)
	return err
}

func runApplyReleaseDelta(args []string) error {
	fs := flag.NewFlagSet("apply-release-delta", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	deltaPath := fs.String("delta", strings.TrimSpace(os.Getenv("DELTA")), "release delta JSON path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("too many arguments for featureflag apply-release-delta")
	}

	resolvedDeltaPath := strings.TrimSpace(*deltaPath)
	if resolvedDeltaPath == "" {
		return fmt.Errorf("release delta path is required")
	}

	root, err := getwdFn()
	if err != nil {
		return fmt.Errorf(resolveWorkingDirectoryError, err)
	}
	flags, err := readCatalog(root)
	if err != nil {
		return err
	}
	delta, err := readReleaseDelta(root, resolvedDeltaPath)
	if err != nil {
		return err
	}

	updatesByCode := make(map[string]releaseDeltaUpdate, len(delta.Updates))
	for _, update := range delta.Updates {
		if _, exists := updatesByCode[update.Code]; exists {
			return fmt.Errorf("duplicate release delta update for feature %s", update.Code)
		}
		updatesByCode[update.Code] = update
	}

	applied := 0
	for i := range flags {
		update, ok := updatesByCode[flags[i].Code]
		if !ok {
			continue
		}
		switch flags[i].FirstStableRelease {
		case "":
			flags[i].FirstStableRelease = update.FirstStableRelease
			applied++
		case update.FirstStableRelease:
		default:
			return fmt.Errorf("feature %s already records first stable release %s; cannot apply release delta %s", flags[i].Code, flags[i].FirstStableRelease, update.FirstStableRelease)
		}
		delete(updatesByCode, flags[i].Code)
	}

	if len(updatesByCode) > 0 {
		missing := make([]string, 0, len(updatesByCode))
		for code := range updatesByCode {
			missing = append(missing, code)
		}
		slices.Sort(missing)
		return fmt.Errorf("release delta references missing features: %s", strings.Join(missing, ", "))
	}

	if applied == 0 {
		_, err = fmt.Fprintf(os.Stdout, "no feature release delta updates to apply from %s\n", resolvedDeltaPath)
		return err
	}

	data, err := featureflags.FormatCatalog(flags)
	if err != nil {
		return err
	}
	if err := writeFileUnderFn(root, filepath.Join(root, catalogPath), data, 0o644); err != nil {
		return fmt.Errorf("write feature catalog: %w", err)
	}

	_, err = fmt.Fprintf(os.Stdout, "applied %d feature release delta update(s) from %s\n", applied, resolvedDeltaPath)
	return err
}

func readReleaseDelta(root string, path string) (releaseDelta, error) {
	data, err := safeio.ReadFileUnder(root, filepath.Join(root, path))
	if err != nil {
		return releaseDelta{}, fmt.Errorf("read release delta: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var delta releaseDelta
	if err := decoder.Decode(&delta); err != nil {
		return releaseDelta{}, fmt.Errorf("parse release delta: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return releaseDelta{}, fmt.Errorf("parse release delta: multiple JSON values")
	}

	delta.Release = normalizeReleaseVersion(delta.Release)
	for i := range delta.Updates {
		delta.Updates[i].Code = strings.TrimSpace(delta.Updates[i].Code)
		delta.Updates[i].FirstStableRelease = normalizeReleaseVersion(delta.Updates[i].FirstStableRelease)
		if delta.Updates[i].Code == "" {
			return releaseDelta{}, fmt.Errorf("parse release delta: update %d feature code is required", i)
		}
		if delta.Updates[i].FirstStableRelease == "" {
			return releaseDelta{}, fmt.Errorf("parse release delta: update %d first stable release is required", i)
		}
	}
	if len(delta.Updates) > 0 && delta.Release == "" {
		return releaseDelta{}, fmt.Errorf("parse release delta: release is required when updates are present")
	}
	return delta, nil
}

func formatReleaseDelta(delta releaseDelta) ([]byte, error) {
	data, err := json.MarshalIndent(delta, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal release delta: %w", err)
	}
	return append(data, '\n'), nil
}
