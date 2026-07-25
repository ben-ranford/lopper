package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

const (
	artifactDirMode  = 0o750
	artifactFileMode = 0o600
)

type config struct {
	baseRef        string
	headRef        string
	summaryOut     string
	statusOut      string
	failureMessage string
}

func main() {
	os.Exit(runMain(os.Args[1:], ".", os.Stdout, os.Stderr))
}

func runMain(args []string, repoRoot string, stdout, stderr io.Writer) int {
	baseCommit, err := resolveBaseCommitFromArgs(args, repoRoot)
	if err != nil {
		if _, writeErr := fmt.Fprintln(stderr, err); writeErr != nil {
			return 2
		}
		return 2
	}

	if _, err := fmt.Fprintln(stdout, baseCommit); err != nil {
		return 2
	}
	return 0
}

func resolveBaseCommitFromArgs(args []string, repoRoot string) (string, error) {
	cfg, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(cfg.failureMessage) != "" {
		return "", writeFailure(cfg.summaryOut, cfg.statusOut, errors.New(cfg.failureMessage))
	}
	if strings.TrimSpace(cfg.baseRef) == "" {
		return "", writeFailure(cfg.summaryOut, cfg.statusOut, errors.New("-base-ref is required"))
	}

	baseCommit, err := resolveBaseCommit(repoRoot, cfg.baseRef, cfg.headRef)
	if err != nil {
		return "", writeFailure(cfg.summaryOut, cfg.statusOut, err)
	}

	return baseCommit, nil
}

func parseArgs(args []string) (config, error) {
	fs := flag.NewFlagSet("benchgate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	cfg := config{}
	fs.StringVar(&cfg.baseRef, "base-ref", "", "git ref to compare against HEAD")
	fs.StringVar(&cfg.headRef, "head-ref", "HEAD", "git ref to compare the base against")
	fs.StringVar(&cfg.summaryOut, "summary-out", "", "path to write the memory benchmark summary on failure")
	fs.StringVar(&cfg.statusOut, "status-out", "", "path to write the memory benchmark status on failure")
	fs.StringVar(&cfg.failureMessage, "failure-message", "", "failure message to write to artifacts and stderr")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	return cfg, nil
}

func resolveBaseCommit(repoRoot, baseRef, headRef string) (string, error) {
	if _, err := gitOutput(repoRoot, "rev-parse", "--verify", "-q", baseRef+"^{commit}"); err != nil {
		return "", fmt.Errorf("memory benchmark base ref %q does not resolve to a commit", baseRef)
	}

	baseCommit, err := gitOutput(repoRoot, "merge-base", baseRef, headRef)
	if err != nil {
		return "", fmt.Errorf("memory benchmark base ref %q is unrelated to %s", baseRef, headRef)
	}
	return baseCommit, nil
}

func gitOutput(repoRoot string, args ...string) (string, error) {
	gitPath, err := gitexec.ResolveBinaryPath()
	if err != nil {
		return "", err
	}
	cmd, err := gitexec.CommandContext(context.Background(), gitPath, args...)
	if err != nil {
		return "", err
	}
	cmd.Dir = repoRoot
	cmd.Env = gitexec.SanitizedEnv()
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func writeFailure(summaryOut, statusOut string, err error) error {
	if writeErr := writeFailureArtifacts(summaryOut, statusOut, err.Error()); writeErr != nil {
		return errors.Join(err, fmt.Errorf("write failure artifacts: %w", writeErr))
	}
	return err
}

func writeFailureArtifacts(summaryOut, statusOut, message string) error {
	var errs []error
	if summaryOut != "" {
		if err := writeSummaryArtifact(summaryOut, message); err != nil {
			errs = append(errs, fmt.Errorf("summary artifact: %w", err))
		}
	}
	if statusOut != "" {
		if err := writeStatusArtifact(statusOut); err != nil {
			errs = append(errs, fmt.Errorf("status artifact: %w", err))
		}
	}
	return errors.Join(errs...)
}

func writeSummaryArtifact(summaryOut, message string) error {
	summary := invalidComparisonSummary(message)
	return writeArtifactFile(summaryOut, []byte(summary))
}

func writeStatusArtifact(statusOut string) error {
	return writeArtifactFile(statusOut, []byte("2\n"))
}

func writeArtifactFile(path string, content []byte) (err error) {
	if err := ensureArtifactDir(path); err != nil {
		return err
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, artifactFileMode) // #nosec G304 -- benchgate writes explicit caller-provided artifact paths.
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	if err := file.Chmod(artifactFileMode); err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		return err
	}
	return file.Chmod(artifactFileMode)
}

func ensureArtifactDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, artifactDirMode); err != nil {
		return err
	}
	return os.Chmod(dir, artifactDirMode)
}

func invalidComparisonSummary(message string) string {
	return fmt.Sprintf("## Memory Benchmarks\n\nComparison could not be evaluated.\n\n%s\n", message)
}
