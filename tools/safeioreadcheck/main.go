// Command safeioreadcheck fails when a production Go file introduces a
// zero-limit safeio parser read that is not present in the documented
// allowlist.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("safeioreadcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "repository root to scan")
	allowlistPath := fs.String("allowlist", ".github/safeio-zero-limit-allowlist.txt", "rationale-per-entry allowlist")
	listOnly := fs.Bool("list", false, "print zero-limit reads and skip the allowlist")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return writeStatus(stderr, 2, "unexpected arguments: %s\n", strings.Join(fs.Args(), " "))
	}

	refs, err := listZeroLimitReads(*root)
	if err != nil {
		return writeStatus(stderr, 1, "scan zero-limit reads: %v\n", err)
	}
	if *listOnly {
		for _, ref := range refs {
			if err := writeLine(stdout, formatReference(ref)); err != nil {
				return 1
			}
		}
		return 0
	}

	findings, err := checkAllowlist(*allowlistPath, refs)
	if err != nil {
		return writeStatus(stderr, 1, "check zero-limit allowlist: %v\n", err)
	}
	if len(findings) == 0 {
		return writeStatus(stdout, 0, "zero-limit safeio reads match %s (%d allowlisted)\n", *allowlistPath, len(refs))
	}
	for _, finding := range findings {
		if err := writeLine(stderr, finding); err != nil {
			return 1
		}
	}
	return 1
}

func writeStatus(w io.Writer, code int, format string, args ...any) int {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		return 1
	}
	return code
}

func writeLine(w io.Writer, line string) error {
	_, err := fmt.Fprintln(w, line)
	return err
}
