//go:build windows

package main

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
)

const proofBash = `C:\Program Files\Git\usr\bin\bash.exe`
const proofCygpath = `C:\Program Files\Git\usr\bin\cygpath.exe`
const discoveryProgram = `set -eu
for name in python python3; do
  location=$(command -v "$name")
  "C:/Program Files/Git/usr/bin/cygpath.exe" -aw -- "$location"
done
`

// The literal program is a location diagnostic, not an accepted user command.
// It uses the existing restricted native proof PATH and no shell startup state.
func verifyDiscovery(ctx context.Context, bin string) error {
	for _, tool := range []string{proofBash, proofCygpath} {
		if err := requireAMD64File(tool); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout := &limitedOutput{limit: 2*maxStringBytes + 4, cancel: cancel}
	stderr := &limitedOutput{limit: maxStreamBytes, cancel: cancel}
	cmd := exec.CommandContext(ctx, proofBash, "--noprofile", "--norc", "-c", discoveryProgram)
	environment, err := probeEnvironment()
	if err != nil {
		return err
	}
	cmd.Env = pinnedGoEnvironment(environment, filepath.Join(bin, "go.exe"))
	cmd.Dir = bin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	commandErr, joinErr := runJoined(cmd)
	if err := errors.Join(commandErr, joinErr, ctx.Err()); err != nil {
		return err
	}
	if len(stderr.bytes()) != 0 {
		return errors.New("native alias discovery emitted stderr")
	}
	return validateDiscovery(stdout.bytes(), []string{filepath.Join(bin, "python.exe"), filepath.Join(bin, "python3.exe")})
}
