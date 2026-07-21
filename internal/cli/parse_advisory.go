package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/ben-ranford/lopper/internal/advisory"
	"github.com/ben-ranford/lopper/internal/app"
)

func parseAdvisory(args []string, req app.Request) (app.Request, error) {
	if len(args) == 0 {
		return req, fmt.Errorf("advisory requires sync or status")
	}
	switch args[0] {
	case "sync":
		return parseAdvisorySync(args[1:], req)
	case "status":
		return parseAdvisoryStatus(args[1:], req)
	default:
		return req, fmt.Errorf("unknown advisory command: %s", args[0])
	}
}

func parseAdvisorySync(args []string, req app.Request) (app.Request, error) {
	if len(args) == 0 || strings.TrimSpace(args[0]) != "osv" {
		return req, fmt.Errorf("advisory sync requires provider osv")
	}
	fs, values := newAdvisoryFlagSet(req)
	sourceURL := fs.String("source-url", advisory.DefaultOSVSourceURL, "OSV advisory snapshot URL")
	if err := parseFlagSet(fs, args[1:]); err != nil {
		return req, err
	}
	if fs.NArg() > 0 {
		return req, fmt.Errorf("unexpected arguments for advisory sync")
	}
	return buildAdvisoryRequest(req, "sync", "osv", *sourceURL, values)
}

func parseAdvisoryStatus(args []string, req app.Request) (app.Request, error) {
	fs, values := newAdvisoryFlagSet(req)
	if err := parseFlagSet(fs, args); err != nil {
		return req, err
	}
	if fs.NArg() > 0 {
		return req, fmt.Errorf("unexpected arguments for advisory status")
	}
	return buildAdvisoryRequest(req, "status", "osv", "", values)
}

type advisoryFlagValues struct {
	cachePath       *string
	outputFlag      *string
	outputShortFlag *string
	enableFeatures  *patternListFlag
	disableFeatures *patternListFlag
}

func newAdvisoryFlagSet(req app.Request) (*flag.FlagSet, advisoryFlagValues) {
	fs := flag.NewFlagSet("advisory", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	enableFeatures := newPatternListFlag(nil)
	disableFeatures := newPatternListFlag(nil)
	values := advisoryFlagValues{
		cachePath:       fs.String("cache-path", req.Advisory.CachePath, "advisory cache directory"),
		outputFlag:      fs.String("output", req.Advisory.OutputPath, "output file path"),
		outputShortFlag: fs.String("o", req.Advisory.OutputPath, "output file path"),
		enableFeatures:  enableFeatures,
		disableFeatures: disableFeatures,
	}
	fs.Var(enableFeatures, "enable-feature", "comma-separated feature flag names to enable (repeatable)")
	fs.Var(disableFeatures, "disable-feature", "comma-separated feature flag names to disable (repeatable)")
	return fs, values
}

func buildAdvisoryRequest(req app.Request, command, provider, sourceURL string, values advisoryFlagValues) (app.Request, error) {
	outputPath, err := resolveOutputPath(*values.outputFlag, *values.outputShortFlag)
	if err != nil {
		return req, err
	}
	features, err := resolveFeatureRefs(values.enableFeatures.Values(), values.disableFeatures.Values())
	if err != nil {
		return req, err
	}
	cachePath := strings.TrimSpace(*values.cachePath)
	if cachePath == "" {
		return req, fmt.Errorf("--cache-path is required for advisory %s", command)
	}
	req.Mode = app.ModeAdvisory
	req.Advisory = app.AdvisoryRequest{
		Command:    command,
		Provider:   provider,
		CachePath:  cachePath,
		SourceURL:  strings.TrimSpace(sourceURL),
		OutputPath: outputPath,
		Features:   features,
	}
	return req, nil
}
