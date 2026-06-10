package cli

import (
	"flag"
	"fmt"
	"io"

	"github.com/ben-ranford/lopper/internal/app"
)

func parseMCP(args []string, req app.Request) (app.Request, error) {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	enableFeatures := newPatternListFlag(nil)
	disableFeatures := newPatternListFlag(nil)
	fs.Var(enableFeatures, "enable-feature", "comma-separated feature flag names to enable (repeatable)")
	fs.Var(disableFeatures, "disable-feature", "comma-separated feature flag names to disable (repeatable)")
	if err := parseFlagSet(fs, args); err != nil {
		return req, err
	}
	if len(fs.Args()) > 0 {
		return req, fmt.Errorf("too many arguments for mcp")
	}
	features, err := resolveFeatureRefs(enableFeatures.Values(), disableFeatures.Values())
	if err != nil {
		return req, err
	}
	req.Mode = app.ModeMCP
	req.MCP.Features = features
	return req, nil
}
