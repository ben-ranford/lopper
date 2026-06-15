package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/ben-ranford/lopper/internal/app"
)

func parseFeatures(args []string, req app.Request) (app.Request, error) {
	normalizedArgs, err := normalizeArgs(args)
	if err != nil {
		return req, err
	}
	args = normalizedArgs

	fs := flag.NewFlagSet("features", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	format := fs.String("format", req.Features.Format, "output format")
	outputFlag := fs.String("output", req.Features.OutputPath, "output file path")
	outputShortFlag := fs.String("o", req.Features.OutputPath, "output file path")
	channel := fs.String("channel", req.Features.Channel, "feature build channel")
	release := fs.String("release", req.Features.Release, "release version for release locks")
	if err := parseFlagSet(fs, args); err != nil {
		return req, err
	}
	outputPath, err := resolveOutputPath(*outputFlag, *outputShortFlag)
	if err != nil {
		return req, err
	}
	if len(fs.Args()) > 0 {
		return req, fmt.Errorf("too many arguments for features")
	}

	req.Mode = app.ModeFeatures
	req.Features.Format = strings.TrimSpace(*format)
	req.Features.OutputPath = outputPath
	req.Features.Channel = strings.TrimSpace(*channel)
	req.Features.Release = strings.TrimSpace(*release)
	return req, nil
}
