package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ben-ranford/lopper/internal/app"
)

type Executor interface {
	Execute(ctx context.Context, req app.Request) (string, error)
}

type CommandLine struct {
	Executor Executor
	Out      io.Writer
	Err      io.Writer
}

func New(executor Executor, out io.Writer, errOut io.Writer) *CommandLine {
	return &CommandLine{
		Executor: executor,
		Out:      out,
		Err:      errOut,
	}
}

func (c *CommandLine) Run(ctx context.Context, args []string) int {
	req, err := ParseArgs(args)
	if err != nil {
		if errors.Is(err, ErrHelpRequested) {
			if writeErr := c.writeOut(Usage()); writeErr != nil {
				return 1
			}
			return 0
		}
		if writeErr := c.writeErrf("error: %v\n\n", err); writeErr != nil {
			return 1
		}
		if writeErr := c.writeErr(Usage()); writeErr != nil {
			return 1
		}
		return 2
	}

	output, runErr := c.Executor.Execute(ctx, req)
	if output != "" {
		if writeErr := c.writeOut(output); writeErr != nil {
			return 1
		}
		if !strings.HasSuffix(output, "\n") {
			if writeErr := c.writeOutln(); writeErr != nil {
				return 1
			}
		}
	}

	if runErr != nil {
		if errors.Is(runErr, app.ErrFailOnIncrease) {
			if writeErr := c.writeErrln(runErr.Error()); writeErr != nil {
				return 1
			}
			return 3
		}
		if writeErr := c.writeErrln(runErr.Error()); writeErr != nil {
			return 1
		}
		return 1
	}

	return 0
}

func (c *CommandLine) writeOut(value string) error {
	_, err := fmt.Fprint(c.Out, value)
	return err
}

func (c *CommandLine) writeErr(value string) error {
	_, err := fmt.Fprint(c.Err, value)
	return err
}

func (c *CommandLine) writeErrf(format string, args ...any) error {
	_, err := fmt.Fprintf(c.Err, format, args...)
	return err
}

func (c *CommandLine) writeErrln(args ...any) error {
	_, err := fmt.Fprintln(c.Err, args...)
	return err
}

func (c *CommandLine) writeOutln(args ...any) error {
	_, err := fmt.Fprintln(c.Out, args...)
	return err
}
