package main

import (
	"context"
	"os"

	"github.com/ben-ranford/lopper/internal/app"
	"github.com/ben-ranford/lopper/internal/cli"
)

func main() {
	out := os.Stdout
	errOut := os.Stderr

	runner := app.New(out, os.Stdin)
	commandLine := cli.New(runner, out, errOut)

	code := commandLine.Run(context.Background(), os.Args[1:])
	os.Exit(code)
}
