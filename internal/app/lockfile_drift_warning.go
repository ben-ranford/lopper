package app

import (
	"fmt"
	"strings"
)

const lockfileDriftWarningPrefix = "lockfile drift detected: "

func buildLockfileDriftWarnings(findings []lockfileDriftFinding) []string {
	if len(findings) == 0 {
		return nil
	}
	warnings := make([]string, 0, len(findings))
	for _, finding := range findings {
		warnings = append(warnings, buildLockfileDriftWarning(finding))
	}
	return warnings
}

func buildLockfileDriftWarning(finding lockfileDriftFinding) string {
	manifest := manifestNameForFinding(finding)
	switch finding.kind {
	case lockfileDriftMissingLockfile:
		return fmt.Sprintf("%s%s in %s: %s exists but no matching lockfile (%s) was found; %s", lockfileDriftWarningPrefix, finding.rule.manager, finding.relDir, manifest, strings.Join(finding.rule.lockfiles, ", "), finding.rule.remedy)
	case lockfileDriftStaleLockfile:
		return fmt.Sprintf("%s%s in %s: %s exists without %s; remove stale lockfile or restore the manifest", lockfileDriftWarningPrefix, finding.rule.manager, finding.relDir, finding.lockfiles[0].name, manifestDescription(finding.rule))
	case lockfileDriftManifestChange:
		return fmt.Sprintf("%s%s in %s: %s changed while no matching lockfile changed; %s", lockfileDriftWarningPrefix, finding.rule.manager, finding.relDir, manifest, finding.rule.remedy)
	default:
		return fmt.Sprintf("%s%s in %s: unable to classify lockfile drift for %s", lockfileDriftWarningPrefix, finding.rule.manager, finding.relDir, manifest)
	}
}

func manifestNameForFinding(finding lockfileDriftFinding) string {
	if strings.TrimSpace(finding.manifest) != "" {
		return finding.manifest
	}
	return finding.rule.manifest
}

func formatLockfileDriftError(driftWarnings []string) error {
	if len(driftWarnings) == 0 {
		return ErrLockfileDrift
	}
	cleaned := make([]string, 0, len(driftWarnings))
	for _, warning := range driftWarnings {
		cleaned = append(cleaned, strings.TrimPrefix(warning, lockfileDriftWarningPrefix))
	}
	return fmt.Errorf("%w: %s", ErrLockfileDrift, strings.Join(cleaned, "; "))
}
