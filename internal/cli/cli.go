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
		return c.handleParseError(err)
	}

	output, runErr := c.Executor.Execute(ctx, req)
	writeErr := c.writeOutput(output)
	if writeErr != nil {
		return 1
	}

	if runErr != nil {
		writeErr = c.writeErrln(runErr.Error())
		if writeErr != nil {
			return 1
		}
		return exitCodeForRunError(runErr)
	}

	return 0
}

func (c *CommandLine) handleParseError(parseErr error) int {
	if errors.Is(parseErr, ErrHelpRequested) {
		if c.writeOut(Usage()) != nil {
			return 1
		}
		return 0
	}
	if c.writeErrf("error: %v\n\n", parseErr) != nil {
		return 1
	}
	if c.writeErr(Usage()) != nil {
		return 1
	}
	return 2
}

func (c *CommandLine) writeOutput(output string) error {
	if output == "" {
		return nil
	}
	err := c.writeOut(output)
	if err != nil {
		return err
	}
	if strings.HasSuffix(output, "\n") {
		return nil
	}
	err = c.writeOutln()
	if err != nil {
		return err
	}
	return nil
}

func exitCodeForRunError(runErr error) int {
	if errors.Is(runErr, app.ErrFailOnIncrease) {
		return 3
	}
	if errors.Is(runErr, app.ErrLockfileDrift) {
		return 4
	}
	return 1
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
