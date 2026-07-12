package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/ben-ranford/lopper/internal/app"
)

func parseBaseline(args []string, req app.Request) (app.Request, error) {
	if len(args) == 0 || isHelpArg(args[0]) {
		return req, ErrHelpRequested
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "list":
		return parseBaselineList(args[1:], req)
	case "show":
		return parseBaselineShow(args[1:], req)
	default:
		return req, fmt.Errorf("unknown baseline command: %s", args[0])
	}
}

func parseBaselineList(args []string, req app.Request) (app.Request, error) {
	fs, values, err := newBaselineFlagSet("baseline list", args, req)
	if err != nil {
		return req, err
	}
	limit := fs.Int("limit", req.Baseline.Limit, "maximum number of snapshots to return")
	if err := parseFlagSet(fs, values.args); err != nil {
		return req, err
	}
	if len(fs.Args()) > 0 {
		return req, fmt.Errorf("too many arguments for baseline list")
	}
	if *limit <= 0 {
		return req, fmt.Errorf("--limit must be greater than zero")
	}
	return finishBaselineParse(req, "list", "", *limit, values)
}

func parseBaselineShow(args []string, req app.Request) (app.Request, error) {
	fs, values, err := newBaselineFlagSet("baseline show", args, req)
	if err != nil {
		return req, err
	}
	if err := parseFlagSet(fs, values.args); err != nil {
		return req, err
	}
	positionals := fs.Args()
	if len(positionals) == 0 {
		return req, fmt.Errorf("baseline show requires a snapshot key")
	}
	if len(positionals) > 1 {
		return req, fmt.Errorf("too many arguments for baseline show")
	}
	key := strings.TrimSpace(positionals[0])
	if key == "" {
		return req, fmt.Errorf("baseline show requires a snapshot key")
	}
	return finishBaselineParse(req, "show", key, req.Baseline.Limit, values)
}

type baselineFlagValues struct {
	args            []string
	format          *string
	store           *string
	baselineStore   *string
	enableFeatures  *patternListFlag
	disableFeatures *patternListFlag
}

func newBaselineFlagSet(name string, args []string, req app.Request) (*flag.FlagSet, baselineFlagValues, error) {
	normalizedArgs, err := normalizeArgs(args)
	if err != nil {
		return nil, baselineFlagValues{}, err
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	values := baselineFlagValues{
		args:            normalizedArgs,
		format:          fs.String("format", req.Baseline.Format, "output format (table or json)"),
		store:           fs.String("store", "", "baseline snapshot directory"),
		baselineStore:   fs.String("baseline-store", "", "baseline snapshot directory"),
		enableFeatures:  newPatternListFlag(nil),
		disableFeatures: newPatternListFlag(nil),
	}
	fs.Var(values.enableFeatures, "enable-feature", "comma-separated feature flag names to enable (repeatable)")
	fs.Var(values.disableFeatures, "disable-feature", "comma-separated feature flag names to disable (repeatable)")
	return fs, values, nil
}

func finishBaselineParse(req app.Request, action, key string, limit int, values baselineFlagValues) (app.Request, error) {
	storePath, err := resolveMatchingPath(*values.store, *values.baselineStore, "--store", "--baseline-store")
	if err != nil {
		return req, err
	}
	if storePath == "" {
		storePath = req.Baseline.StorePath
	}
	format := strings.ToLower(strings.TrimSpace(*values.format))
	if format != "table" && format != "json" {
		return req, fmt.Errorf("invalid baseline format: %s", *values.format)
	}
	features, err := resolveFeatureRefs(values.enableFeatures.Values(), values.disableFeatures.Values())
	if err != nil {
		return req, err
	}
	req.Mode = app.ModeBaseline
	req.Baseline = app.BaselineRequest{
		Action:    action,
		StorePath: strings.TrimSpace(storePath),
		Key:       key,
		Format:    format,
		Limit:     limit,
		Features:  features,
	}
	return req, nil
}
