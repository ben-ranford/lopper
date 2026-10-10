package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	process "github.com/ben-ranford/lopper/internal/runtime"
)

// Completion is certified only after the existing owner reports an empty job.
// A parent Wait result alone must never authorise verification or deletion.
func runJoined(cmd *exec.Cmd) (commandErr, joinErr error) {
	process.ConfigureCommandCancellation(cmd)
	cleanup, err := process.StartCommand(cmd)
	if err != nil {
		return err, err
	}
	commandErr = cmd.Wait()
	joinErr = cleanup()
	return commandErr, joinErr
}

func proofArguments(args []string) error {
	if len(args) < 2 {
		return errors.New("missing fixed proof invocation")
	}
	if args[0] == "test" && args[len(args)-1] == "./internal/githubaction" {
		return ordinaryArguments(args[1 : len(args)-1])
	}
	if args[0] == "run" && args[1] == "./tools/regressionproof" {
		return formalArguments(args[2:])
	}
	return errors.New("unsupported proof command family")
}

func ordinaryArguments(args []string) error {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-count=1" || arg == "-race" || arg == "-json" || arg == "-p=1" || arg == "-timeout=20m":
		case arg == "-run" && i+1 < len(args):
			i++
		default:
			return errors.New("unsupported ordinary proof argument")
		}
	}
	return nil
}

func formalArguments(args []string) error {
	allowed := map[string]bool{"--repo": true, "--body-file": true, "--title": true, "--base-sha": true, "--regression-exempt-label": true, "--target-os": true}
	seen := map[string]bool{}
	for i := 0; i < len(args); i += 2 {
		if i+1 >= len(args) || !allowed[args[i]] || seen[args[i]] {
			return errors.New("unsupported formal proof argument")
		}
		if args[i] == "--target-os" && args[i+1] != "windows" {
			return errors.New("formal proof must use native Windows target")
		}
		seen[args[i]] = true
	}
	if !seen["--target-os"] {
		return errors.New("missing native proof target")
	}
	return nil
}

func pythonEnvironment(env []string) []string {
	result := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		key = strings.ToUpper(key)
		if strings.HasPrefix(key, "PYTHON") || strings.HasPrefix(key, "LD_") || strings.HasPrefix(key, "DYLD_") {
			continue
		}
		result = append(result, entry)
	}
	return result
}

func pythonCommand(ctx context.Context, executable string, args, env []string, stdin io.Reader, stdout, stderr io.Writer) (*exec.Cmd, error) {
	if !filepath.IsAbs(executable) {
		return nil, errors.New("python transport requires absolute compiled runtime")
	}
	if err := canonicalRegular(executable); err != nil {
		return nil, err
	}
	info, err := os.Lstat(executable)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("python transport requires a regular executable")
	}
	cmd := exec.CommandContext(ctx, executable, "-I", "-S", "-B")
	cmd.Args = append(cmd.Args, args...)
	cmd.Env = pythonEnvironment(env)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = time.Second
	return cmd, nil
}
