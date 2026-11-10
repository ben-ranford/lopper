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
		fmt.Fprintf(stderr, "unexpected arguments: %s\n", strings.Join(fs.Args(), " "))
		return 2
	}

	refs, err := listZeroLimitReads(*root)
	if err != nil {
		fmt.Fprintf(stderr, "scan zero-limit reads: %v\n", err)
		return 1
	}
	if *listOnly {
		for _, ref := range refs {
			fmt.Fprintln(stdout, ref.String())
		}
		return 0
	}

	findings, err := checkAllowlist(*allowlistPath, refs)
	if err != nil {
		fmt.Fprintf(stderr, "check zero-limit allowlist: %v\n", err)
		return 1
	}
	if len(findings) == 0 {
		fmt.Fprintf(stdout, "zero-limit safeio reads match %s (%d allowlisted)\n", *allowlistPath, len(refs))
		return 0
	}
	for _, finding := range findings {
		fmt.Fprintln(stderr, finding)
	}
	return 1
}
