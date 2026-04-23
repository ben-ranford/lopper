package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/ben-ranford/lopper/internal/featureflags"
)

var releasePleasePRTitlePattern = regexp.MustCompile(`(?i)\brelease\s+v?([0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?)\b`)

func runReleasePRComment(args []string) error {
	fs := flag.NewFlagSet("release-pr-comment", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	prTitle := fs.String("pr-title", strings.TrimSpace(os.Getenv("PR_TITLE")), "release-please pull request title")
	release := fs.String("release", strings.TrimSpace(os.Getenv("RELEASE")), "release version")
	previousCatalog := fs.String("previous-catalog", strings.TrimSpace(os.Getenv("PREVIOUS_CATALOG")), "previous feature catalog path")
	workflowURL := fs.String("workflow-url", strings.TrimSpace(os.Getenv("WORKFLOW_URL")), "graduate-feature workflow URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("too many arguments for featureflag release-pr-comment")
	}

	resolvedRelease := normalizeReleaseVersion(*release)
	if resolvedRelease == "" {
		resolvedRelease = releasePleaseVersionFromTitle(*prTitle)
	}
	if resolvedRelease == "" {
		return fmt.Errorf("release version is required")
	}

	if err := validateDefaultRegistryFn(); err != nil {
		return err
	}
	if err := validateDefaultReleaseLocksFn(); err != nil {
		return err
	}

	registry := defaultRegistryFn()
	manifest, err := manifestEntriesFn(registry, featureflags.ChannelRelease, resolvedRelease)
	if err != nil {
		return err
	}
	previous, compared, err := readPreviousCatalog(*previousCatalog)
	if err != nil {
		return err
	}
	_, err = fmt.Print(formatReleasePRComment(resolvedRelease, registry.Flags(), manifest, previous, compared, strings.TrimSpace(*workflowURL)))
	return err
}

func normalizeReleaseVersion(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(value), "v") {
		return "v" + strings.TrimSpace(value[1:])
	}
	return "v" + value
}

func releasePleaseVersionFromTitle(title string) string {
	matches := releasePleasePRTitlePattern.FindStringSubmatch(strings.TrimSpace(title))
	if len(matches) < 2 {
		return ""
	}
	return normalizeReleaseVersion(matches[1])
}

func newlyAddedPreviewFlags(current, previous []featureflags.Flag, compared bool) []featureflags.Flag {
	if !compared {
		return nil
	}
	previousByCode := make(map[string]struct{}, len(previous))
	previousByName := make(map[string]struct{}, len(previous))
	for _, flag := range previous {
		previousByCode[flag.Code] = struct{}{}
		previousByName[flag.Name] = struct{}{}
	}
	added := make([]featureflags.Flag, 0)
	for _, flag := range current {
		if flag.Lifecycle != featureflags.LifecyclePreview {
			continue
		}
		_, seenCode := previousByCode[flag.Code]
		_, seenName := previousByName[flag.Name]
		if !seenCode && !seenName {
			added = append(added, flag)
		}
	}
	return added
}

func formatReleasePRComment(release string, current []featureflags.Flag, manifest []featureflags.ManifestEntry, previous []featureflags.Flag, compared bool, workflowURL string) string {
	var b strings.Builder
	b.WriteString("<!-- lopper-feature-flag-release-pr -->\n")
	b.WriteString("## Release feature flags\n\n")
	fmt.Fprintf(&b, "This release PR is preparing `%s`.\n\n", release)
	b.WriteString(formatReport(featureflags.ChannelRelease, release, current, manifest, previous, compared))
	b.WriteString("\n")
	b.WriteString("### Promotion options\n\n")
	fmt.Fprintf(&b, "- Ship a preview flag default-on only in `%s` by editing `internal/featureflags/release_locks.json`.\n", release)
	if workflowURL != "" {
		fmt.Fprintf(&b, "- Graduate a preview flag for future releases with [`graduate-feature.yml`](%s) using the feature code or name, then merge that PR before publishing `%s`.\n", workflowURL, release)
	} else {
		fmt.Fprintf(&b, "- Graduate a preview flag for future releases with `graduate-feature.yml` using the feature code or name, then merge that PR before publishing `%s`.\n", release)
	}

	candidates := newlyAddedPreviewFlags(current, previous, compared)
	if len(candidates) == 0 {
		b.WriteString("- No newly added preview flags were detected for this release candidate.\n")
		return b.String()
	}

	b.WriteString("\n### Graduation candidates\n\n")
	for _, flag := range candidates {
		fmt.Fprintf(&b, "- `%s` `%s`", flag.Code, flag.Name)
		if flag.Description != "" {
			fmt.Fprintf(&b, " - %s", flag.Description)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
