package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

var titlePattern = regexp.MustCompile(`^(feat|fix|perf|docs|refactor|revert|test|ci|build|chore)(\([a-z0-9][a-z0-9._/-]*\))?!?: [^\s].+$`)

var placeholderTexts = []string{
	"Describe the problem and the intent of this change. Use `N/A` only when the section truly does not apply.",
	"Describe the problem and the intent of this change.",
	"Commands and checks run:",
	"Additional manual validation:",
}

var requiredHeadings = []string{
	"Summary",
	"Changes",
	"Validation",
	"Risk and compatibility",
	"Checklist",
}

var requiredRiskFields = []string{
	"Breaking changes",
	"Migration required",
	"Performance impact",
	"Memory benchmark impact",
}

var requiredChecklistItems = []string{
	"Tests added/updated for behavior changes",
	"Docs updated (README/docs/schema) if needed",
	"`memory-approved` requested/applied if intentional memory benchmark regressions exceed CI thresholds",
	"No unrelated changes included",
	"Ready for review",
}

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stderr))
}

func run(args []string, getenv func(string) string, stderr io.Writer) int {
	fs := flag.NewFlagSet("prcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	title := fs.String("title", strings.TrimSpace(getenv("PR_TITLE")), "pull request title")
	headRef := fs.String("head-ref", strings.TrimSpace(getenv("PR_HEAD_REF")), "pull request head ref")
	bodyFile := fs.String("body-file", "", "path to a file containing the pull request body")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	body := strings.TrimSpace(getenv("PR_BODY"))
	if *bodyFile != "" {
		data, err := os.ReadFile(*bodyFile)
		if err != nil {
			if _, writeErr := fmt.Fprintf(stderr, "read PR body: %v\n", err); writeErr != nil {
				return 1
			}
			return 1
		}
		body = string(data)
	}

	if err := validate(*title, *headRef, body); err != nil {
		if _, writeErr := fmt.Fprintf(stderr, "%v\n", err); writeErr != nil {
			return 1
		}
		return 1
	}
	return 0
}

func validate(title, headRef, body string) error {
	var failures []string
	title = strings.TrimSpace(title)
	if !titlePattern.MatchString(title) {
		failures = append(failures, "PR title must be a Conventional Commit title using one of feat, fix, perf, docs, refactor, revert, test, ci, build, or chore; use fix(scope): ... for bug fixes instead of bug: ...")
	}

	if isReleasePleasePR(headRef, title) {
		if len(failures) > 0 {
			return errors.New(strings.Join(failures, "\n"))
		}
		return nil
	}

	sections := parseSections(body)
	for _, heading := range requiredHeadings {
		if _, ok := sections[heading]; !ok {
			failures = append(failures, fmt.Sprintf("PR body is missing required template section %q", heading))
		}
	}

	for _, heading := range []string{"Summary", "Changes", "Validation"} {
		content, ok := sections[heading]
		if !ok {
			continue
		}
		if !hasMeaningfulContent(content) {
			failures = append(failures, fmt.Sprintf("PR section %q must be completed; keep the heading and replace placeholder text with real content or N/A", heading))
		}
	}

	if content, ok := sections["Risk and compatibility"]; ok {
		for _, field := range requiredRiskFields {
			if !fieldHasValue(content, field) {
				failures = append(failures, fmt.Sprintf("Risk and compatibility field %q must be present and filled, using N/A or None when there is no impact", field))
			}
		}
	}

	if content, ok := sections["Checklist"]; ok {
		for _, item := range requiredChecklistItems {
			if !checkedChecklistItem(content, item) {
				failures = append(failures, fmt.Sprintf("Checklist item %q must be present and checked", item))
			}
		}
	}

	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "\n"))
	}
	return nil
}

func isReleasePleasePR(headRef, title string) bool {
	return strings.HasPrefix(strings.TrimSpace(headRef), "release-please--branches--") &&
		regexp.MustCompile(`^chore\(main\): release [0-9]+\.[0-9]+\.[0-9]+$`).MatchString(strings.TrimSpace(title))
}

func parseSections(body string) map[string]string {
	sections := make(map[string]string)
	var current string
	var b strings.Builder

	flush := func() {
		if current == "" {
			return
		}
		sections[current] = strings.TrimSpace(b.String())
		b.Reset()
	}

	for _, line := range strings.Split(body, "\n") {
		if heading, ok := parseH2(line); ok {
			flush()
			current = heading
			continue
		}
		if current != "" {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	flush()

	return sections
}

func parseH2(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, "## ")), true
}

func hasMeaningfulContent(content string) bool {
	content = stripCodeFences(content)
	for _, placeholder := range placeholderTexts {
		content = strings.ReplaceAll(content, placeholder, "")
	}
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == "-" {
			continue
		}
		if strings.HasPrefix(trimmed, "<!--") && strings.HasSuffix(trimmed, "-->") {
			continue
		}
		return true
	}
	return false
}

func stripCodeFences(content string) string {
	var kept []string
	inFence := false
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func fieldHasValue(content, field string) bool {
	pattern := regexp.MustCompile(`(?m)^-\s*` + regexp.QuoteMeta(field) + `:\s*(.+)$`)
	match := pattern.FindStringSubmatch(content)
	if len(match) != 2 {
		return false
	}
	value := strings.TrimSpace(match[1])
	return value != ""
}

func checkedChecklistItem(content, item string) bool {
	pattern := regexp.MustCompile(`(?mi)^-\s*\[[x]\]\s*` + regexp.QuoteMeta(item) + `\s*$`)
	return pattern.MatchString(content)
}
