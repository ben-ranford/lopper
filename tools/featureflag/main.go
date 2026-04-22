package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/safeio"
)

const catalogPath = "internal/featureflags/features.json"

var (
	exitFunc                  = os.Exit
	getwdFn                   = os.Getwd
	writeFileUnderFn          = safeio.WriteFileUnder
	validateDefaultRegistryFn = featureflags.ValidateDefaultRegistry
	defaultRegistryFn         = featureflags.DefaultRegistry
	newRegistryFn             = featureflags.NewRegistry
	manifestEntriesFn         = releaseManifest
	formatManifestFn          = featureflags.FormatManifest
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitFunc(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: featureflag add|validate|manifest")
	}
	switch args[0] {
	case "add":
		return runAdd(args[1:])
	case "validate":
		return runValidate()
	case "manifest":
		return runManifest()
	default:
		return fmt.Errorf("unknown featureflag command: %s", args[0])
	}
}

func runAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	name := fs.String("name", strings.TrimSpace(os.Getenv("NAME")), "feature flag name")
	description := fs.String("description", strings.TrimSpace(os.Getenv("DESCRIPTION")), "feature flag description")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("too many arguments for featureflag add")
	}
	if strings.TrimSpace(*name) == "" {
		return fmt.Errorf("feature flag name is required")
	}

	root, err := getwdFn()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	flags, err := readCatalog(root)
	if err != nil {
		return err
	}
	registry, err := newRegistryFn(flags)
	if err != nil {
		return err
	}
	code, err := registry.NextCode()
	if err != nil {
		return err
	}
	flags = append(flags, featureflags.Flag{
		Code:        code,
		Name:        strings.TrimSpace(*name),
		Description: strings.TrimSpace(*description),
		Lifecycle:   featureflags.LifecyclePreview,
	})
	data, err := featureflags.FormatCatalog(flags)
	if err != nil {
		return err
	}
	if err := writeFileUnderFn(root, filepath.Join(root, catalogPath), data, 0o644); err != nil {
		return fmt.Errorf("write feature catalog: %w", err)
	}
	_, err = fmt.Fprintf(os.Stdout, "added %s %s\n", code, strings.TrimSpace(*name))
	return err
}

func runValidate() error {
	if err := validateDefaultRegistryFn(); err != nil {
		return err
	}
	_, err := fmt.Fprintln(os.Stdout, "feature flag registry valid")
	return err
}

func runManifest() error {
	if err := validateDefaultRegistryFn(); err != nil {
		return err
	}
	manifest, err := manifestEntriesFn(defaultRegistryFn())
	if err != nil {
		return err
	}
	data, err := formatManifestFn(manifest)
	if err != nil {
		return err
	}
	_, err = fmt.Print(string(data))
	return err
}

func readCatalog(root string) ([]featureflags.Flag, error) {
	data, err := safeio.ReadFileUnder(root, filepath.Join(root, catalogPath))
	if err != nil {
		return nil, fmt.Errorf("read feature catalog: %w", err)
	}
	return featureflags.ParseCatalog(data)
}

func releaseManifest(registry *featureflags.Registry) ([]featureflags.ManifestEntry, error) {
	return registry.Manifest(featureflags.ResolveOptions{Channel: featureflags.ChannelRelease})
}
