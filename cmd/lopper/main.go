package main

import (
	"context"
	"io"
	"os"

	"github.com/ben-ranford/lopper/internal/app"
	"github.com/ben-ranford/lopper/internal/cli"
)

var exitFunc = os.Exit

func run(args []string, in io.Reader, out io.Writer, errOut io.Writer) int {
	runner := app.New(out, in)
	commandLine := cli.New(runner, out, errOut)
	return commandLine.Run(context.Background(), args)
}

func main() {
	exitFunc(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
