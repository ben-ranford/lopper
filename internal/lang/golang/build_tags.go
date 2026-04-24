package golang

import (
	"go/build/constraint"
	"os"
	"runtime"
	"strings"
)

func isGeneratedGoFile(content []byte) bool {
	lines := strings.Split(string(content), "\n")
	maxLines := minInt(len(lines), 20)
	for i := 0; i < maxLines; i++ {
		line := strings.ToLower(strings.TrimSpace(lines[i]))
		if strings.Contains(line, "code generated") && strings.Contains(line, "do not edit") {
			return true
		}
	}
	return false
}

func matchesActiveBuild(content []byte) bool {
	goBuildExpr, plusBuildExprs := extractBuildConstraintExpressions(content)
	switch {
	case goBuildExpr != nil:
		return goBuildExpr.Eval(isActiveBuildTag)
	case len(plusBuildExprs) > 0:
		for _, expr := range plusBuildExprs {
			if !expr.Eval(isActiveBuildTag) {
				return false
			}
		}
		return true
	default:
		return true
	}
}

func extractBuildConstraintExpressions(content []byte) (constraint.Expr, []constraint.Expr) {
	lines := strings.Split(string(content), "\n")
	maxLines := minInt(len(lines), maxGoBuildHeaderLine)
	plusBuildExprs := make([]constraint.Expr, 0)
	var goBuildExpr constraint.Expr

	for i := 0; i < maxLines; i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if shouldStopBuildConstraintScan(line) {
			break
		}
		expr, kind := parseBuildConstraintComment(line)
		switch kind {
		case "go":
			if expr != nil {
				goBuildExpr = expr
			}
		case "plus":
			if expr != nil {
				plusBuildExprs = append(plusBuildExprs, expr)
			}
		}
	}
	return goBuildExpr, plusBuildExprs
}

func shouldStopBuildConstraintScan(line string) bool {
	if strings.HasPrefix(line, "package ") {
		return true
	}
	return !strings.HasPrefix(line, "//")
}

func parseBuildConstraintComment(line string) (constraint.Expr, string) {
	switch {
	case strings.HasPrefix(line, "//go:build "):
		expr, err := constraint.Parse(line)
		if err != nil {
			return nil, "go"
		}
		return expr, "go"
	case strings.HasPrefix(line, "// +build "):
		expr, err := constraint.Parse(line)
		if err != nil {
			return nil, "plus"
		}
		return expr, "plus"
	default:
		return nil, ""
	}
}

func isActiveBuildTag(tag string) bool {
	tag = strings.TrimSpace(strings.ToLower(tag))
	if tag == "" {
		return false
	}
	if tag == runtime.GOOS || tag == runtime.GOARCH {
		return true
	}
	if tag == "unix" {
		switch runtime.GOOS {
		case "android", "darwin", "dragonfly", "freebsd", "illumos", "ios", "linux", "netbsd", "openbsd", "solaris":
			return true
		}
	}
	if tag == "cgo" {
		return strings.EqualFold(os.Getenv("CGO_ENABLED"), "1")
	}
	if strings.HasPrefix(tag, "go1.") {
		return isSupportedGoReleaseTag(tag)
	}
	// Unknown tags are treated as disabled unless set explicitly.
	return false
}

func isSupportedGoReleaseTag(tag string) bool {
	minorCurrent, ok := goVersionMinor(runtime.Version())
	if !ok {
		return false
	}
	if !strings.HasPrefix(tag, "go1.") {
		return false
	}
	minorTag, ok := leadingInt(strings.TrimPrefix(tag, "go1."))
	if !ok {
		return false
	}
	return minorTag <= minorCurrent
}

func goVersionMinor(version string) (int, bool) {
	normalized := strings.TrimSpace(version)
	normalized = strings.TrimPrefix(normalized, "devel ")
	goIndex := strings.Index(normalized, "go")
	if goIndex < 0 {
		return 0, false
	}
	normalized = strings.TrimPrefix(normalized[goIndex:], "go")
	normalized = strings.SplitN(normalized, " ", 2)[0]
	normalized = strings.SplitN(normalized, "-", 2)[0]

	versionParts := strings.Split(normalized, ".")
	if len(versionParts) < 2 || versionParts[0] != "1" {
		return 0, false
	}
	return leadingInt(versionParts[1])
}

func leadingInt(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	n := 0
	seen := false
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			break
		}
		seen = true
		n = (n * 10) + int(value[i]-'0')
	}
	if !seen {
		return 0, false
	}
	return n, true
}

func parseIntDefault(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	n := 0
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return fallback
		}
		n = (n * 10) + int(value[i]-'0')
	}
	return n
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
