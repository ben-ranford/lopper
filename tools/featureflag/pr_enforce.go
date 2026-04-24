package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/featureflags"
)

var featurePRTitlePattern = regexp.MustCompile(`(?i)^feat(?:\([^)]*\))?(?:!)?:\s+\S`)

type prEnforcementResult struct {
	RequireFlag       bool
	AddedFlags        []featureflags.Flag
	InvalidAddedFlags []featureflags.Flag
	CatalogViolations []string
}

func runPREnforce(args []string) error {
	fs := flag.NewFlagSet("pr-enforce", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	prTitle := fs.String("pr-title", strings.TrimSpace(os.Getenv("PR_TITLE")), "pull request title")
	previousCatalog := fs.String("previous-catalog", strings.TrimSpace(os.Getenv("PREVIOUS_CATALOG")), "previous feature catalog path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("too many arguments for featureflag pr-enforce")
	}
	if strings.TrimSpace(*previousCatalog) == "" {
		return fmt.Errorf("previous feature catalog is required")
	}

	root, err := getwdFn()
	if err != nil {
		return fmt.Errorf(resolveWorkingDirectoryError, err)
	}
	current, catalogViolations, err := readCurrentCatalogForPREnforcement(root)
	if err != nil {
		return err
	}
	previous, compared, err := readPreviousCatalog(*previousCatalog)
	if err != nil {
		return err
	}
	if !compared {
		return fmt.Errorf("previous feature catalog is required")
	}

	result := evaluatePREnforcement(strings.TrimSpace(*prTitle), current, previous, catalogViolations)
	if _, err := fmt.Print(formatPREnforcementReport(result)); err != nil {
		return err
	}
	return result.err()
}

func evaluatePREnforcement(prTitle string, current, previous []featureflags.Flag, catalogViolations []string) prEnforcementResult {
	addedFlags := newlyAddedFlags(current, previous)
	result := prEnforcementResult{
		RequireFlag:       isFeaturePRTitle(prTitle),
		AddedFlags:        addedFlags,
		CatalogViolations: catalogViolations,
	}
	for _, flag := range addedFlags {
		if flag.Lifecycle != featureflags.LifecyclePreview {
			result.InvalidAddedFlags = append(result.InvalidAddedFlags, flag)
		}
	}
	return result
}

func readCurrentCatalogForPREnforcement(root string) ([]featureflags.Flag, []string, error) {
	data, err := readCatalogData(root)
	if err != nil {
		return nil, nil, err
	}
	flags, err := featureflags.ParseCatalog(data)
	if err == nil {
		return flags, nil, nil
	}

	decodedFlags, decodeErr := decodeFeatureCatalog(data)
	if decodeErr != nil {
		return nil, nil, err
	}
	violations := duplicateFeatureFlagViolations(decodedFlags)
	if len(violations) == 0 {
		return nil, nil, err
	}
	return decodedFlags, violations, nil
}

func decodeFeatureCatalog(data []byte) ([]featureflags.Flag, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var flags []featureflags.Flag
	if err := decoder.Decode(&flags); err != nil {
		return nil, fmt.Errorf("invalid feature catalog JSON: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, fmt.Errorf("invalid feature catalog JSON: multiple JSON values")
	}
	return flags, nil
}

func duplicateFeatureFlagViolations(flags []featureflags.Flag) []string {
	codes := make(map[string][]string, len(flags))
	names := make(map[string][]string, len(flags))
	for _, flag := range flags {
		code := strings.TrimSpace(flag.Code)
		name := strings.TrimSpace(flag.Name)
		if code != "" {
			codes[code] = append(codes[code], name)
		}
		if name != "" {
			names[name] = append(names[name], code)
		}
	}

	violations := make([]string, 0, 2)
	for _, code := range sortedDuplicateKeys(codes) {
		violations = append(violations, fmt.Sprintf("Feature flag ids (`code`) must be unique: `%s` is used by %s.", code, formatDuplicateRefs(codes[code])))
	}
	for _, name := range sortedDuplicateKeys(names) {
		violations = append(violations, fmt.Sprintf("Feature flag names must be unique: `%s` is used by %s.", name, formatDuplicateRefs(names[name])))
	}
	return violations
}

func sortedDuplicateKeys(groups map[string][]string) []string {
	keys := make([]string, 0, len(groups))
	for key, refs := range groups {
		if len(refs) > 1 {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func formatDuplicateRefs(refs []string) string {
	parts := make([]string, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			ref = "<missing>"
		}
		parts = append(parts, fmt.Sprintf("`%s`", ref))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func isFeaturePRTitle(title string) bool {
	return featurePRTitlePattern.MatchString(strings.TrimSpace(title))
}

func newlyAddedFlags(current, previous []featureflags.Flag) []featureflags.Flag {
	previousByCode := make(map[string]struct{}, len(previous))
	for _, flag := range previous {
		previousByCode[flag.Code] = struct{}{}
	}

	added := make([]featureflags.Flag, 0)
	for _, flag := range current {
		_, seenCode := previousByCode[flag.Code]
		if !seenCode {
			added = append(added, flag)
		}
	}
	return added
}

func formatPREnforcementReport(result prEnforcementResult) string {
	violations := result.violations()
	status := "passed"
	if len(violations) > 0 {
		status = "failed"
	}

	var b strings.Builder
	b.WriteString("<!-- lopper-feature-flag-enforcement -->\n")
	b.WriteString("## Feature flag enforcement\n\n")
	if result.RequireFlag {
		b.WriteString("- Feature PR: yes (`feat` PR title)\n")
	} else {
		b.WriteString("- Feature PR: no\n")
	}
	fmt.Fprintf(&b, "- Check: %s\n", status)
	b.WriteString("- Rule: feature PRs must add a feature flag, new flags must start as `preview`, and feature flag ids and names must be unique.\n\n")

	b.WriteString("### New feature flags in this PR\n\n")
	if len(result.AddedFlags) == 0 {
		b.WriteString("None.\n\n")
	} else {
		for _, flag := range result.AddedFlags {
			fmt.Fprintf(&b, "- `%s` `%s` (`%s`)", flag.Code, flag.Name, flag.Lifecycle)
			if flag.Description != "" {
				fmt.Fprintf(&b, " - %s", flag.Description)
			}
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}

	if len(violations) == 0 {
		b.WriteString("### Result\n\n")
		switch {
		case result.RequireFlag:
			b.WriteString("Passed. This feature PR adds at least one new preview feature flag.\n")
		case len(result.AddedFlags) > 0:
			b.WriteString("Passed. Added feature flags all start as `preview`.\n")
		default:
			b.WriteString("Passed. No new feature flag was required for this PR.\n")
		}
		return b.String()
	}

	b.WriteString("### Violations\n\n")
	for _, violation := range violations {
		fmt.Fprintf(&b, "- %s\n", violation)
	}
	b.WriteByte('\n')
	return b.String()
}

func (r *prEnforcementResult) violations() []string {
	violations := make([]string, 0, 2+len(r.CatalogViolations))
	violations = append(violations, r.CatalogViolations...)
	if r.RequireFlag && len(r.AddedFlags) == 0 {
		violations = append(violations, "Feature PRs must add at least one new feature flag in `internal/featureflags/features.json`.")
	}
	if len(r.InvalidAddedFlags) > 0 {
		parts := make([]string, 0, len(r.InvalidAddedFlags))
		for _, flag := range r.InvalidAddedFlags {
			parts = append(parts, fmt.Sprintf("`%s` `%s` is `%s`", flag.Code, flag.Name, flag.Lifecycle))
		}
		violations = append(violations, "New feature flags must start as `preview`: "+strings.Join(parts, ", ")+".")
	}
	return violations
}

func (r *prEnforcementResult) err() error {
	violations := r.violations()
	if len(violations) == 0 {
		return nil
	}
	return errors.New(strings.Join(violations, " "))
}
