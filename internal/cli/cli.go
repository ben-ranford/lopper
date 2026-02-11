package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ben-ranford/lopper/internal/app"
)

type Runner interface {
	Execute(ctx context.Context, req app.Request) (string, error)
}

type CLI struct {
	Runner Runner
	Out    io.Writer
	Err    io.Writer
}

func New(runner Runner, out io.Writer, errOut io.Writer) *CLI {
	return &CLI{
		Runner: runner,
		Out:    out,
		Err:    errOut,
	}
}

func (c *CLI) Run(ctx context.Context, args []string) int {
	req, err := ParseArgs(args)
	if err != nil {
		if errors.Is(err, ErrHelpRequested) {
			_, _ = fmt.Fprint(c.Out, Usage())
			return 0
		}
		_, _ = fmt.Fprintf(c.Err, "error: %v\n\n", err)
		_, _ = fmt.Fprint(c.Err, Usage())
		return 2
	}

	output, runErr := c.Runner.Execute(ctx, req)
	if output != "" {
		_, _ = fmt.Fprint(c.Out, output)
		if !strings.HasSuffix(output, "\n") {
			_, _ = fmt.Fprintln(c.Out)
		}
	}

	if runErr != nil {
		if errors.Is(runErr, app.ErrFailOnIncrease) {
			_, _ = fmt.Fprintln(c.Err, runErr.Error())
			return 3
		}
		_, _ = fmt.Fprintln(c.Err, runErr.Error())
		return 1
	}

	return 0
}
