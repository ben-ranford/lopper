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

const (
	catalogPath                  = "internal/featureflags/features.json"
	resolveWorkingDirectoryError = "resolve working directory: %w"
)

var (
	exitFunc                      = os.Exit
	getwdFn                       = os.Getwd
	writeFileUnderFn              = safeio.WriteFileUnder
	validateDefaultRegistryFn     = featureflags.ValidateDefaultRegistry
	validateDefaultReleaseLocksFn = featureflags.ValidateDefaultReleaseLocks
	defaultRegistryFn             = featureflags.DefaultRegistry
	newRegistryFn                 = featureflags.NewRegistry
	manifestEntriesFn             = manifestEntries
	formatManifestFn              = featureflags.FormatManifest
	releaseLockProviderFn         = featureflags.DefaultReleaseLock
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		exitFunc(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: featureflag add|graduate|validate|manifest|report|pr-enforce|release-pr-comment")
	}
	switch args[0] {
	case "add":
		return runAdd(args[1:])
	case "graduate":
		return runGraduate(args[1:])
	case "validate":
		return runValidate()
	case "manifest":
		return runManifest(args[1:])
	case "report":
		return runReport(args[1:])
	case "pr-enforce":
		return runPREnforce(args[1:])
	case "release-pr-comment":
		return runReleasePRComment(args[1:])
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
		return fmt.Errorf(resolveWorkingDirectoryError, err)
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

func runGraduate(args []string) error {
	fs := flag.NewFlagSet("graduate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	feature := fs.String("feature", strings.TrimSpace(os.Getenv("FEATURE")), "feature code or name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("too many arguments for featureflag graduate")
	}
	ref := strings.TrimSpace(*feature)
	if ref == "" {
		return fmt.Errorf("feature code or name is required")
	}

	root, err := getwdFn()
	if err != nil {
		return fmt.Errorf(resolveWorkingDirectoryError, err)
	}
	flags, err := readCatalog(root)
	if err != nil {
		return err
	}
	registry, err := newRegistryFn(flags)
	if err != nil {
		return err
	}
	target, ok := registry.Lookup(ref)
	if !ok {
		return fmt.Errorf("unknown feature: %s", ref)
	}
	if target.Lifecycle == featureflags.LifecycleStable {
		return fmt.Errorf("feature %s is already stable", target.Code)
	}
	updated := false
	for i := range flags {
		if flags[i].Code == target.Code {
			flags[i].Lifecycle = featureflags.LifecycleStable
			updated = true
			break
		}
	}
	if !updated {
		return fmt.Errorf("feature %s is missing from the catalog", target.Code)
	}
	data, err := featureflags.FormatCatalog(flags)
	if err != nil {
		return err
	}
	if err := writeFileUnderFn(root, filepath.Join(root, catalogPath), data, 0o644); err != nil {
		return fmt.Errorf("write feature catalog: %w", err)
	}
	_, err = fmt.Fprintf(os.Stdout, "graduated %s %s to stable\n", target.Code, target.Name)
	return err
}

func runValidate() error {
	if err := validateDefaultRegistryFn(); err != nil {
		return err
	}
	if err := validateDefaultReleaseLocksFn(); err != nil {
		return err
	}
	_, err := fmt.Fprintln(os.Stdout, "feature flag registry valid")
	return err
}

func runManifest(args []string) error {
	channel, release, _, err := parseManifestArgs("manifest", args)
	if err != nil {
		return err
	}
	if err := validateDefaultRegistryFn(); err != nil {
		return err
	}
	if err := validateDefaultReleaseLocksFn(); err != nil {
		return err
	}
	manifest, err := manifestEntriesFn(defaultRegistryFn(), channel, release)
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

func runReport(args []string) error {
	channel, release, previousCatalog, err := parseManifestArgs("report", args)
	if err != nil {
		return err
	}
	if err := validateDefaultRegistryFn(); err != nil {
		return err
	}
	if err := validateDefaultReleaseLocksFn(); err != nil {
		return err
	}
	registry := defaultRegistryFn()
	manifest, err := manifestEntriesFn(registry, channel, release)
	if err != nil {
		return err
	}
	previousFlags, compared, err := readPreviousCatalog(previousCatalog)
	if err != nil {
		return err
	}
	_, err = fmt.Print(formatReport(channel, release, registry.Flags(), manifest, previousFlags, compared))
	return err
}

func readCatalog(root string) ([]featureflags.Flag, error) {
	data, err := safeio.ReadFileUnder(root, filepath.Join(root, catalogPath))
	if err != nil {
		return nil, fmt.Errorf("read feature catalog: %w", err)
	}
	return featureflags.ParseCatalog(data)
}

func parseManifestArgs(name string, args []string) (featureflags.Channel, string, string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	channelValue := fs.String("channel", string(featureflags.ChannelRelease), "feature build channel")
	release := fs.String("release", "", "release version for release locks")
	previousCatalog := fs.String("previous-catalog", "", "previous feature catalog path")
	if err := fs.Parse(args); err != nil {
		return "", "", "", err
	}
	if len(fs.Args()) > 0 {
		return "", "", "", fmt.Errorf("too many arguments for featureflag %s", name)
	}
	channel, err := featureflags.NormalizeChannel(*channelValue)
	if err != nil {
		return "", "", "", err
	}
	return channel, strings.TrimSpace(*release), strings.TrimSpace(*previousCatalog), nil
}

func manifestEntries(registry *featureflags.Registry, channel featureflags.Channel, release string) ([]featureflags.ManifestEntry, error) {
	var lock *featureflags.ReleaseLock
	var err error
	if channel == featureflags.ChannelRelease {
		lock, err = releaseLockProviderFn(release)
		if err != nil {
			return nil, err
		}
	}
	return registry.Manifest(featureflags.ResolveOptions{Channel: channel, Lock: lock})
}

func readPreviousCatalog(path string) ([]featureflags.Flag, bool, error) {
	if path == "" {
		return nil, false, nil
	}
	root, err := getwdFn()
	if err != nil {
		return nil, false, fmt.Errorf(resolveWorkingDirectoryError, err)
	}
	data, err := safeio.ReadFileUnder(root, filepath.Join(root, path))
	if err != nil {
		return nil, false, fmt.Errorf("read previous feature catalog: %w", err)
	}
	flags, err := featureflags.ParseCatalog(data)
	if err != nil {
		return nil, false, fmt.Errorf("parse previous feature catalog: %w", err)
	}
	return flags, true, nil
}

func formatReport(channel featureflags.Channel, release string, current []featureflags.Flag, manifest []featureflags.ManifestEntry, previous []featureflags.Flag, compared bool) string {
	enabledByCode := make(map[string]bool, len(manifest))
	for _, entry := range manifest {
		enabledByCode[entry.Code] = entry.EnabledByDefault
	}

	var stableDefault, previewOptIn, previewDefault []featureflags.Flag
	for _, flag := range current {
		enabled := enabledByCode[flag.Code]
		switch {
		case flag.Lifecycle == featureflags.LifecycleStable && enabled:
			stableDefault = append(stableDefault, flag)
		case flag.Lifecycle == featureflags.LifecyclePreview && enabled:
			previewDefault = append(previewDefault, flag)
		case flag.Lifecycle == featureflags.LifecyclePreview:
			previewOptIn = append(previewOptIn, flag)
		}
	}

	var b strings.Builder
	b.WriteString("<!-- lopper-feature-flags:start -->\n")
	b.WriteString("## Feature flags\n\n")
	fmt.Fprintf(&b, "- Channel: `%s`\n", channel)
	if release != "" {
		fmt.Fprintf(&b, "- Release: `%s`\n", release)
	}
	b.WriteString("\n")
	writeFlagSection(&b, "Stable by default", stableDefault)
	writeFlagSection(&b, "Preview available by opt-in", previewOptIn)
	if channel == featureflags.ChannelRolling {
		writeFlagSection(&b, "Preview enabled by rolling channel", previewDefault)
	} else {
		writeFlagSection(&b, "Preview locked default-on for this release", previewDefault)
	}
	writeNewPreviewSection(&b, current, previous, compared)
	b.WriteString("<!-- lopper-feature-flags:end -->\n")
	return b.String()
}

func writeFlagSection(b *strings.Builder, title string, flags []featureflags.Flag) {
	fmt.Fprintf(b, "### %s\n\n", title)
	if len(flags) == 0 {
		b.WriteString("None.\n\n")
		return
	}
	for _, flag := range flags {
		fmt.Fprintf(b, "- `%s` `%s`", flag.Code, flag.Name)
		if flag.Description != "" {
			fmt.Fprintf(b, " - %s", flag.Description)
		}
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
}

func writeNewPreviewSection(b *strings.Builder, current []featureflags.Flag, previous []featureflags.Flag, compared bool) {
	b.WriteString("### Newly added preview flags since previous release\n\n")
	if !compared {
		b.WriteString("Not compared; no previous feature catalog was provided.\n\n")
		return
	}
	added := newlyAddedPreviewFlags(current, previous, compared)
	if len(added) == 0 {
		b.WriteString("None.\n\n")
		return
	}
	for _, flag := range added {
		fmt.Fprintf(b, "- `%s` `%s`\n", flag.Code, flag.Name)
	}
	b.WriteByte('\n')
}
