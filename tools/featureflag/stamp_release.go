package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/featureflags"
)

func runStampRelease(args []string) error {
	fs := flag.NewFlagSet("stamp-release", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	release := fs.String("release", strings.TrimSpace(os.Getenv("RELEASE")), "stable release version to stamp")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("too many arguments for featureflag stamp-release")
	}

	resolvedRelease := normalizeReleaseVersion(*release)
	if resolvedRelease == "" {
		return fmt.Errorf("release version is required")
	}

	root, err := getwdFn()
	if err != nil {
		return fmt.Errorf(resolveWorkingDirectoryError, err)
	}
	flags, err := readCatalog(root)
	if err != nil {
		return err
	}

	stamped := 0
	for i := range flags {
		if flags[i].FirstStableRelease != "" {
			continue
		}
		flags[i].FirstStableRelease = resolvedRelease
		stamped++
	}

	if stamped == 0 {
		_, err = fmt.Fprintf(os.Stdout, "no feature release stamps to update for %s\n", resolvedRelease)
		return err
	}

	data, err := featureflags.FormatCatalog(flags)
	if err != nil {
		return err
	}
	if err := writeFileUnderFn(root, filepath.Join(root, catalogPath), data, 0o644); err != nil {
		return fmt.Errorf("write feature catalog: %w", err)
	}

	_, err = fmt.Fprintf(os.Stdout, "stamped %d feature(s) with first stable release %s\n", stamped, resolvedRelease)
	return err
}
