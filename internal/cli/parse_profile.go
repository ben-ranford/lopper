package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/ben-ranford/lopper/internal/app"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

func parseProfile(args []string, req app.Request) (app.Request, error) {
	if len(args) == 0 || isHelpArg(args[0]) {
		return req, ErrHelpRequested
	}

	switch strings.TrimSpace(args[0]) {
	case "apply":
		return parseProfileApply(args[1:], req)
	default:
		return req, fmt.Errorf("unknown profile command: %s", args[0])
	}
}

func parseProfileApply(args []string, req app.Request) (app.Request, error) {
	normalizedArgs, err := normalizeArgs(args)
	if err != nil {
		return req, err
	}
	args = normalizedArgs

	fs := flag.NewFlagSet("profile apply", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	outputFlag := fs.String("output", req.Profile.OutputPath, "output file path")
	outputShortFlag := fs.String("o", req.Profile.OutputPath, "output file path")
	force := fs.Bool("force", req.Profile.Force, "overwrite an existing output file")
	enableFeatures := newPatternListFlag(nil)
	disableFeatures := newPatternListFlag(nil)
	fs.Var(enableFeatures, "enable-feature", "comma-separated feature flag names to enable (repeatable)")
	fs.Var(disableFeatures, "disable-feature", "comma-separated feature flag names to disable (repeatable)")
	if err := parseFlagSet(fs, args); err != nil {
		return req, err
	}

	positionals := fs.Args()
	if len(positionals) == 0 {
		return req, fmt.Errorf("profile apply requires a profile name (%s)", strings.Join(thresholds.ProfileNames(), ", "))
	}
	if len(positionals) > 1 {
		return req, fmt.Errorf("too many arguments for profile apply")
	}
	profileName := strings.TrimSpace(positionals[0])
	if _, ok := thresholds.LookupProfile(profileName); !ok {
		return req, fmt.Errorf("unknown threshold profile %q (available: %s)", profileName, strings.Join(thresholds.ProfileNames(), ", "))
	}
	outputPath, err := resolveOutputPath(*outputFlag, *outputShortFlag)
	if err != nil {
		return req, err
	}
	features, err := resolveFeatureRefs(enableFeatures.Values(), disableFeatures.Values())
	if err != nil {
		return req, err
	}

	req.Mode = app.ModeProfile
	req.Profile.Name = profileName
	req.Profile.OutputPath = outputPath
	req.Profile.Force = *force
	req.Profile.Features = features
	return req, nil
}
