package cli

import (
	"errors"
	"fmt"

	"github.com/ben-ranford/lopper/internal/app"
)

var (
	ErrHelpRequested      = errors.New("help requested")
	ErrVersionRequested   = errors.New("version requested")
	ErrConflictingTargets = errors.New("cannot use both dependency and --top")
)

func ParseArgs(args []string) (app.Request, error) {
	req := app.DefaultRequest()
	if len(args) == 0 {
		return req, nil
	}

	if isHelpArg(args[0]) {
		return req, ErrHelpRequested
	}
	if isVersionArg(args) {
		return req, ErrVersionRequested
	}

	switch args[0] {
	case "tui":
		return parseTUI(args[1:], req)
	case "analyse":
		return parseAnalyse(args[1:], req)
	case "dashboard":
		return parseDashboard(args[1:], req)
	case "features":
		return parseFeatures(args[1:], req)
	case "mcp":
		return parseMCP(args[1:], req)
	default:
		return req, fmt.Errorf("unknown command: %s", args[0])
	}
}
