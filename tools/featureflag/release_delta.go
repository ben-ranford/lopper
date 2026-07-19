package main

import (
	"bytes"
	"encoding/json"
	"errors"
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

type exportReleaseDeltaOptions struct {
	release string
	output  string
	catalog string
}

type applyReleaseDeltaOptions struct {
	release string
	delta   string
}

type releaseDeltaSource struct {
	root string
	path string
}

func runExportReleaseDelta(args []string) error {
	options, err := parseExportReleaseDeltaArgs(args)
	if err != nil {
		return err
	}

	root, err := getwdFn()
	if err != nil {
		return fmt.Errorf(resolveWorkingDirectoryError, err)
	}
	flags, err := readCatalogFromPath(catalogSource{root: root, path: options.catalog})
	if err != nil {
		return err
	}

	delta := buildReleaseDelta(options.release, flags)
	data, err := formatReleaseDelta(delta)
	if err != nil {
		return err
	}
	absoluteOutput := filepath.Join(root, options.output)
	if err := func() error {
		outputRoot, err := os.OpenRoot(root)
		if err != nil {
			return err
		}
		mkdirErr := outputRoot.MkdirAll(filepath.Dir(filepath.Clean(options.output)), 0o750)
		return errors.Join(mkdirErr, outputRoot.Close())
	}(); err != nil {
		return fmt.Errorf("create release delta output directory: %w", err)
	}
	if err := writeFileUnderFn(root, absoluteOutput, data, 0o644); err != nil {
		return fmt.Errorf("write release delta: %w", err)
	}

	_, err = fmt.Fprintf(os.Stdout, "exported %d feature release delta update(s) for %s\n", len(delta.Updates), options.release)
	return err
}

func parseExportReleaseDeltaArgs(args []string) (exportReleaseDeltaOptions, error) {
	fs := flag.NewFlagSet("export-release-delta", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	release := fs.String("release", strings.TrimSpace(os.Getenv("RELEASE")), "stable release version to export")
	output := fs.String("output", strings.TrimSpace(os.Getenv("OUTPUT")), "output path for the release delta JSON")
	catalog := fs.String("catalog", strings.TrimSpace(os.Getenv("CATALOG")), "feature catalog path to derive the release delta from")
	if err := fs.Parse(args); err != nil {
		return exportReleaseDeltaOptions{}, err
	}
	if len(fs.Args()) > 0 {
		return exportReleaseDeltaOptions{}, fmt.Errorf("too many arguments for featureflag export-release-delta")
	}

	resolvedRelease := normalizeReleaseVersion(*release)
	if resolvedRelease == "" {
		return exportReleaseDeltaOptions{}, fmt.Errorf("release version is required")
	}
	outputPath := strings.TrimSpace(*output)
	if outputPath == "" {
		return exportReleaseDeltaOptions{}, fmt.Errorf("release delta output path is required")
	}
	catalogPathValue := strings.TrimSpace(*catalog)
	if catalogPathValue == "" {
		catalogPathValue = catalogPath
	}
	return exportReleaseDeltaOptions{
		release: resolvedRelease,
		output:  outputPath,
		catalog: catalogPathValue,
	}, nil
}

func buildReleaseDelta(release string, flags []featureflags.Flag) releaseDelta {
	delta := releaseDelta{Release: release}
	for _, flag := range flags {
		if flag.FirstStableRelease != "" {
			continue
		}
		delta.Updates = append(delta.Updates, releaseDeltaUpdate{
			Code:               flag.Code,
			FirstStableRelease: release,
		})
	}
	return delta
}

func runApplyReleaseDelta(args []string) error {
	options, err := parseApplyReleaseDeltaArgs(args)
	if err != nil {
		return err
	}

	root, err := getwdFn()
	if err != nil {
		return fmt.Errorf(resolveWorkingDirectoryError, err)
	}
	flags, err := readCatalog(root)
	if err != nil {
		return err
	}
	delta, err := readReleaseDelta(releaseDeltaSource{root: root, path: options.delta})
	if err != nil {
		return err
	}
	if err := validateReleaseDeltaRelease(delta, options.release); err != nil {
		return err
	}
	applied, err := applyReleaseDeltaUpdates(flags, delta)
	if err != nil {
		return err
	}

	if applied == 0 {
		_, err = fmt.Fprintf(os.Stdout, "no feature release delta updates to apply from %s\n", options.delta)
		return err
	}

	data, err := featureflags.FormatCatalog(flags)
	if err != nil {
		return err
	}
	if err := writeFileUnderFn(root, filepath.Join(root, catalogPath), data, 0o644); err != nil {
		return fmt.Errorf("write feature catalog: %w", err)
	}

	_, err = fmt.Fprintf(os.Stdout, "applied %d feature release delta update(s) from %s\n", applied, options.delta)
	return err
}

func parseApplyReleaseDeltaArgs(args []string) (applyReleaseDeltaOptions, error) {
	fs := flag.NewFlagSet("apply-release-delta", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	release := fs.String("release", strings.TrimSpace(os.Getenv("RELEASE")), "stable release version expected in the release delta")
	deltaPath := fs.String("delta", strings.TrimSpace(os.Getenv("DELTA")), "release delta JSON path")
	if err := fs.Parse(args); err != nil {
		return applyReleaseDeltaOptions{}, err
	}
	if len(fs.Args()) > 0 {
		return applyReleaseDeltaOptions{}, fmt.Errorf("too many arguments for featureflag apply-release-delta")
	}

	resolvedRelease := normalizeReleaseVersion(*release)
	if resolvedRelease == "" {
		return applyReleaseDeltaOptions{}, fmt.Errorf("release version is required")
	}

	resolvedDeltaPath := strings.TrimSpace(*deltaPath)
	if resolvedDeltaPath == "" {
		return applyReleaseDeltaOptions{}, fmt.Errorf("release delta path is required")
	}
	return applyReleaseDeltaOptions{release: resolvedRelease, delta: resolvedDeltaPath}, nil
}

func validateReleaseDeltaRelease(delta releaseDelta, expectedRelease string) error {
	if delta.Release != expectedRelease {
		return fmt.Errorf("release delta targets %s; expected %s", delta.Release, expectedRelease)
	}
	for _, update := range delta.Updates {
		if update.FirstStableRelease != expectedRelease {
			return fmt.Errorf("release delta update for feature %s targets %s; expected %s", update.Code, update.FirstStableRelease, expectedRelease)
		}
	}
	return nil
}

func applyReleaseDeltaUpdates(flags []featureflags.Flag, delta releaseDelta) (int, error) {
	updatesByCode, err := indexReleaseDeltaUpdates(delta.Updates)
	if err != nil {
		return 0, err
	}

	applied, err := applyIndexedReleaseDeltaUpdates(flags, updatesByCode)
	if err != nil {
		return 0, err
	}
	if err := ensureAllReleaseDeltaUpdatesApplied(updatesByCode); err != nil {
		return 0, err
	}
	return applied, nil
}

func indexReleaseDeltaUpdates(updates []releaseDeltaUpdate) (map[string]releaseDeltaUpdate, error) {
	updatesByCode := make(map[string]releaseDeltaUpdate, len(updates))
	for _, update := range updates {
		if _, exists := updatesByCode[update.Code]; exists {
			return nil, fmt.Errorf("duplicate release delta update for feature %s", update.Code)
		}
		updatesByCode[update.Code] = update
	}
	return updatesByCode, nil
}

func applyIndexedReleaseDeltaUpdates(flags []featureflags.Flag, updatesByCode map[string]releaseDeltaUpdate) (int, error) {
	applied := 0
	for i := range flags {
		update, ok := updatesByCode[flags[i].Code]
		if !ok {
			continue
		}
		stamped, err := applyIndexedReleaseDeltaUpdate(&flags[i], update)
		if err != nil {
			return 0, err
		}
		if stamped {
			applied++
		}
		delete(updatesByCode, flags[i].Code)
	}
	return applied, nil
}

func applyIndexedReleaseDeltaUpdate(flag *featureflags.Flag, update releaseDeltaUpdate) (bool, error) {
	switch flag.FirstStableRelease {
	case "":
		flag.FirstStableRelease = update.FirstStableRelease
		return true, nil
	case update.FirstStableRelease:
		return false, nil
	default:
		return false, fmt.Errorf("feature %s already records first stable release %s; cannot apply release delta %s", flag.Code, flag.FirstStableRelease, update.FirstStableRelease)
	}
}

func ensureAllReleaseDeltaUpdatesApplied(updatesByCode map[string]releaseDeltaUpdate) error {
	if len(updatesByCode) == 0 {
		return nil
	}

	missing := make([]string, 0, len(updatesByCode))
	for code := range updatesByCode {
		missing = append(missing, code)
	}
	slices.Sort(missing)
	return fmt.Errorf("release delta references missing features: %s", strings.Join(missing, ", "))
}

func readReleaseDelta(source releaseDeltaSource) (releaseDelta, error) {
	data, err := safeio.ReadFileUnder(source.root, filepath.Join(source.root, source.path))
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
