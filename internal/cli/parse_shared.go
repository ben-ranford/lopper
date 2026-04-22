package cli

import (
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/app"
)

func parseFlagSet(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ErrHelpRequested
		}
		return err
	}
	return nil
}

func resolveScopePatterns(visited map[string]bool, flagName string, cliValues []string, configValues []string) []string {
	if visited[flagName] {
		if len(cliValues) == 0 {
			return nil
		}
		return append([]string{}, cliValues...)
	}
	if len(configValues) == 0 {
		return nil
	}
	return append([]string{}, configValues...)
}

type patternListFlag struct {
	patterns []string
}

func newPatternListFlag(initial []string) *patternListFlag {
	if len(initial) == 0 {
		return &patternListFlag{}
	}
	return &patternListFlag{
		patterns: append([]string{}, initial...),
	}
}

func (f *patternListFlag) String() string {
	return strings.Join(f.patterns, ",")
}

func (f *patternListFlag) Set(value string) error {
	f.patterns = mergePatterns(f.patterns, splitPatternList(value))
	return nil
}

func (f *patternListFlag) Values() []string {
	if len(f.patterns) == 0 {
		return nil
	}
	return append([]string{}, f.patterns...)
}

func mergePatterns(existing, next []string) []string {
	if len(next) == 0 {
		return existing
	}
	seen := make(map[string]struct{}, len(existing)+len(next))
	merged := make([]string, 0, len(existing)+len(next))
	for _, pattern := range existing {
		if _, ok := seen[pattern]; ok {
			continue
		}
		seen[pattern] = struct{}{}
		merged = append(merged, pattern)
	}
	for _, pattern := range next {
		if _, ok := seen[pattern]; ok {
			continue
		}
		seen[pattern] = struct{}{}
		merged = append(merged, pattern)
	}
	return merged
}

func splitPatternList(value string) []string {
	parts := strings.Split(value, ",")
	seen := make(map[string]struct{}, len(parts))
	patterns := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		patterns = append(patterns, trimmed)
	}
	if len(patterns) == 0 {
		return nil
	}
	return patterns
}

func splitRepoList(value string) []string {
	parts := strings.Split(value, ",")
	repos := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		trimmedPath := strings.TrimSpace(part)
		if trimmedPath == "" {
			continue
		}
		normalizedPath := filepath.Clean(trimmedPath)
		if _, ok := seen[normalizedPath]; ok {
			continue
		}
		seen[normalizedPath] = struct{}{}
		repos = append(repos, normalizedPath)
	}
	if len(repos) == 0 {
		return nil
	}
	return repos
}

func isHelpArg(arg string) bool {
	switch arg {
	case "-h", "--help", "help":
		return true
	default:
		return false
	}
}

func isVersionArg(args []string) bool {
	return len(args) == 1 && strings.TrimSpace(args[0]) == "--version"
}

func normalizeArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}

	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, 1)

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			if flagNeedsValue(arg) && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		positionals = append(positionals, arg)
	}

	return append(flags, positionals...)
}

func flagNeedsValue(arg string) bool {
	if strings.Contains(arg, "=") {
		return false
	}
	switch arg {
	case "--repo", "--top", "--scope-mode", "--format", "--channel", "--release", "--cache-path", "--fail-on-increase", "--threshold-fail-on-increase", "--threshold-low-confidence-warning", "--threshold-min-usage-percent", "--threshold-max-uncertain-imports", "--score-weight-usage", "--score-weight-impact", "--score-weight-confidence", "--license-deny", "--language", "--runtime-profile", "--baseline", "--baseline-store", "--baseline-key", "--baseline-label", "--runtime-trace", "--runtime-test-command", "--config", "--enable-feature", "--disable-feature", "--include", "--exclude", "--lockfile-drift-policy", "--notify-on", "--notify-slack", "--notify-teams", "--snapshot", "--filter", "--sort", "--page-size", "--repos", "--output", "-o":
		return true
	default:
		return false
	}
}

func parseScopeMode(value string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(value))
	switch mode {
	case "", app.ScopeModePackage:
		return app.ScopeModePackage, nil
	case app.ScopeModeRepo, app.ScopeModeChangedPackages:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid --scope-mode: %s", value)
	}
}

func visitedFlags(fs *flag.FlagSet) map[string]bool {
	visited := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		visited[f.Name] = true
	})
	return visited
}
