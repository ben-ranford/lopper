package cli

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/featureflags"
)

func TestNormalizeArgsPreservesTerminator(t *testing.T) {
	for _, testCase := range []struct {
		name              string
		args, positionals []string
		format            string
	}{
		{"first", []string{"--", "--format", "json"}, []string{"--format", "json"}, "table"},
		{"reordered", []string{"first", "--format", "json", "--", "--top", "4"}, []string{"first", "--top", "4"}, "json"},
		{"flags around positional", []string{"--format", "json", "first", "--top", "4", "--", "--format=csv"}, []string{"first", "--format=csv"}, "json"},
		{"empty", []string{"--"}, []string{}, "table"},
		{"duplicate", []string{"--", "--", "--format=json"}, []string{"--", "--format=json"}, "table"},
		{"literal missing value", []string{"--", "--format"}, []string{"--format"}, "table"},
		{"no terminator", []string{"first", "--format=json"}, []string{"first"}, "json"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			args, err := normalizeArgs(testCase.args)
			if err != nil {
				t.Fatal(err)
			}
			fs := flag.NewFlagSet("terminator", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			format := fs.String("format", "table", "")
			fs.Int("top", 0, "")
			if err := fs.Parse(args); err != nil {
				t.Fatal(err)
			}
			if *format != testCase.format || !reflect.DeepEqual(fs.Args(), testCase.positionals) {
				t.Fatalf("normalized %q: format=%q, positionals=%q; want format=%q, positionals=%q", args, *format, fs.Args(), testCase.format, testCase.positionals)
			}
		})
	}
	if _, err := normalizeArgs([]string{"--format", "--", "json"}); err == nil {
		t.Fatal("a terminator cannot provide a missing pre-terminator flag value")
	}
}

func TestRunPreservesFlagTerminator(t *testing.T) {
	withFeatureRegistry(t, featureflags.ChannelRelease, nil)
	for _, args := range [][]string{
		{"features", "--", "--output", "features.json"},
		{"features", "--", "--format=json"},
		{"features", "--", "--help"},
		{"mcp", "--", "--enable-feature", "preview-flag"},
		{"mcp", "--", "--disable-feature", "stable-flag"},
		{"mcp", "--", "--enable-feature", "missing"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			runner := &fakeRunner{output: "executor was called"}
			var out, errOut bytes.Buffer
			code := New(runner, &out, &errOut).Run(t.Context(), args)
			if code != 2 || out.Len() != 0 || !strings.Contains(errOut.String(), "too many arguments") {
				t.Fatalf("terminated arguments must be rejected before dispatch: code=%d stdout=%q stderr=%q", code, strings.SplitN(out.String(), "\n", 2)[0], strings.SplitN(errOut.String(), "\n", 2)[0])
			}
		})
	}
}

func TestParseTerminatorAcrossNormalizers(t *testing.T) {
	for _, prefix := range [][]string{
		{"analyse"}, {"tui"}, {"dashboard"}, {"baseline", "list"},
		{"baseline", "show"}, {"pr-review"}, {"features"}, {"profile", "apply"}, {"mcp"},
	} {
		t.Run(strings.Join(prefix, " "), func(t *testing.T) {
			assertTerminatedHelp(t, prefix)
		})
	}
	for _, command := range []string{"features", "mcp"} {
		if _, err := ParseArgs([]string{command, "--"}); err != nil {
			t.Fatalf("empty terminator for %s: %v", command, err)
		}
	}
}

func assertTerminatedHelp(t *testing.T, prefix []string) {
	t.Helper()
	args := append(append([]string{}, prefix...), "--", "--help")
	req, err := ParseArgs(args)
	if errors.Is(err, ErrHelpRequested) {
		t.Fatal("post-terminator help was executed as a flag")
	}
	switch strings.Join(prefix, " ") {
	case "analyse":
		if err != nil || req.Analyse.Dependency != "--help" {
			t.Fatalf("literal dependency: request=%+v err=%v", req.Analyse, err)
		}
	case "baseline show":
		if err != nil || req.Baseline.Key != "--help" {
			t.Fatalf("literal snapshot key: request=%+v err=%v", req.Baseline, err)
		}
	default:
		if err == nil {
			t.Fatal("unexpected positional argument was accepted")
		}
	}
}
