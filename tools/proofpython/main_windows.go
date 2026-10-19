//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Set once by the trusted bootstrap before receipt construction. No digest or
// mutable receipt is compiled into the aliases, avoiding a hash fixed point.
var transportExecutable string

func platformMain(name string, args []string) (int, error) {
	if strings.EqualFold(filepath.Base(name), "python.exe") || strings.EqualFold(filepath.Base(name), "python3.exe") {
		return transport(args)
	}
	if len(args) == 0 {
		return 2, errors.New("missing provider command")
	}
	if err := requireAMD64File(name); err != nil {
		return 2, err
	}
	err := providerCommand(context.Background(), args)
	if err != nil {
		return 2, err
	}
	return 0, nil
}

func transport(args []string) (int, error) {
	if transportExecutable == "" {
		return 2, errors.New("missing immutable transport target")
	}
	cmd, err := pythonCommand(context.Background(), transportExecutable, args, os.Environ(), os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		return 2, err
	}
	commandErr, joinErr := runJoined(cmd)
	if joinErr != nil {
		return 2, errors.Join(commandErr, joinErr)
	}
	var exit *exec.ExitError
	if errors.As(commandErr, &exit) {
		return exit.ExitCode(), nil
	}
	if commandErr != nil {
		return 2, commandErr
	}
	return 0, nil
}

func providerCommand(ctx context.Context, args []string) error {
	switch args[0] {
	case "cache":
		return cacheCommand(args[1:])
	case "copy":
		return copyCommand(ctx, args[1:])
	case "capture":
		return captureCommand(ctx, args[1:])
	case "verify":
		return verifyCommand(ctx, args[1:])
	case "joined-command":
		return joinedProof(ctx, args[1:])
	default:
		return errors.New("unsupported provider command")
	}
}

func cacheCommand(args []string) error {
	if len(args) != 2 {
		return errors.New("cache requires independently captured runner/agent roots")
	}
	root, err := cachedPython(args[0], args[1])
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, root)
	return err
}

func copyCommand(ctx context.Context, args []string) error {
	if len(args) != 3 {
		return errors.New("copy requires independent cache roots and destination")
	}
	if err := checkProviderLocation(args[2], filepath.Dir(args[2])); err != nil {
		return err
	}
	root, err := cachedPython(args[0], args[1])
	if err != nil {
		return err
	}
	source, err := inventory(ctx, root, true)
	if err != nil {
		return err
	}
	_, err = materialize(ctx, source, args[2])
	return err
}

func captureCommand(ctx context.Context, args []string) error {
	if len(args) != 3 {
		return errors.New("capture requires private root, Go bin and output")
	}
	captured, err := captureProvider(ctx, args[0], args[1])
	if err != nil {
		return err
	}
	data, err := encodeReceipt(captured)
	if err != nil {
		return err
	}
	return writeExclusive(args[2], data)
}

func verifyCommand(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return errors.New("verify requires receipt and independent digest")
	}
	expected, err := loadExpectedReceipt(args[0], args[1])
	if err != nil {
		return err
	}
	return verifyProvider(ctx, expected)
}

func writeExclusive(path string, data []byte) (returnErr error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	_, err = file.Write(data)
	return err
}

func joinedProof(ctx context.Context, args []string) error {
	if len(args) < 6 {
		return errors.New("joined-command requires receipt/digest/absolute Go/digest/cwd and fixed invocation")
	}
	expected, err := loadExpectedReceipt(args[0], args[1])
	if err != nil {
		return err
	}
	if err := proofArguments(args[5:]); err != nil {
		return err
	}
	goPath := args[2]
	if !filepath.IsAbs(goPath) || filepath.Base(goPath) != "go.exe" || filepath.Dir(goPath) != filepath.Dir(expected.Root) {
		return errors.New("captured Go does not match provider bin")
	}
	if err := authenticateFileContext(ctx, goPath, args[3]); err != nil {
		return err
	}
	if err := verifyProvider(ctx, expected); err != nil {
		return err
	}
	if err := verifyDiscovery(ctx, filepath.Dir(goPath)); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, goPath, args[5:]...)
	cmd.Dir = args[4]
	cmd.Env = pinnedGoEnvironment(os.Environ(), goPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	commandErr, joinErr := runJoined(cmd)
	if joinErr != nil {
		return errors.Join(commandErr, joinErr)
	}
	return errors.Join(commandErr, postProofVerification(ctx, goPath, args[3], expected))
}

func pinnedGoEnvironment(env []string, goPath string) []string {
	root := filepath.Dir(filepath.Dir(goPath))
	result := []string{"GOROOT=" + root, "GOTOOLCHAIN=local", "GOENV=off", "GOWORK=off", "GOFLAGS=", "GOCACHEPROG=", "GOAUTH=off", "REGRESSION_PROOF_GO_ROOT=" + root,
		"GOTELEMETRY=off", "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=1", `CC=C:\mingw64\bin\gcc.exe`, `CXX=C:\mingw64\bin\g++.exe`,
		"PATH=" + filepath.Dir(goPath) + `;C:\mingw64\bin;C:\Program Files\Git\cmd;C:\Program Files\Git\mingw64\bin;C:\Program Files\Git\usr\bin;C:\Windows\System32;C:\Windows`,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
	allowed := map[string]bool{"HOME": true, "USERPROFILE": true, "HOMEDRIVE": true, "HOMEPATH": true, "LOCALAPPDATA": true, "SYSTEMROOT": true, "WINDIR": true, "TMPDIR": true, "TMP": true, "TEMP": true, "GOCACHE": true, "GOMODCACHE": true, "GOPATH": true}
	seen := map[string]bool{}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		key = strings.ToUpper(key)
		if allowed[key] && !seen[key] && filepath.IsAbs(value) && !strings.ContainsAny(value, "\x00\r\n") {
			result = append(result, key+"="+value)
			seen[key] = true
		}
	}
	return result
}

func postProofVerification(ctx context.Context, goPath, digest string, expected receipt) error {
	if err := authenticateFileContext(ctx, goPath, digest); err != nil {
		return err
	}
	if err := verifyProvider(ctx, expected); err != nil {
		return err
	}
	return verifyDiscovery(ctx, filepath.Dir(goPath))
}
