package dashboard

import (
	"errors"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

type RoutingOptions struct {
	DefaultOwner  string
	DefaultTeam   string
	DefaultDue    string
	DefaultStatus string
	Rules         []RoutingRule
	Codeowners    []CodeownerRule
}

type RoutingRule struct {
	Repo       string
	PathPrefix string
	Category   string
	Dependency string
	Owner      string
	Team       string
	Due        string
	Status     string
}

type CodeownerRule struct {
	Pattern string
	Owners  []string
	Source  string
}

func ApplyRouting(items []RemediationItem, options RoutingOptions) []RemediationItem {
	if len(items) == 0 {
		return items
	}
	routed := append([]RemediationItem{}, items...)
	for index := range routed {
		routed[index] = routeRemediationItem(routed[index], options)
	}
	return routed
}

func routeRemediationItem(item RemediationItem, options RoutingOptions) RemediationItem {
	for _, rule := range options.Rules {
		if !routingRuleMatches(rule, item) {
			continue
		}
		return applyRoutingAssignment(item, rule.Owner, rule.Team, rule.Due, rule.Status, "config")
	}
	for index := len(options.Codeowners) - 1; index >= 0; index-- {
		rule := options.Codeowners[index]
		if !codeownerRuleMatches(rule, item) {
			continue
		}
		owner := ""
		if len(rule.Owners) > 0 {
			owner = strings.Join(rule.Owners, ",")
		}
		return applyRoutingAssignment(item, owner, "", options.DefaultDue, options.DefaultStatus, rule.Source)
	}
	return applyRoutingAssignment(item, options.DefaultOwner, options.DefaultTeam, options.DefaultDue, options.DefaultStatus, "default")
}

func routingRuleMatches(rule RoutingRule, item RemediationItem) bool {
	if rule.Repo != "" && !strings.EqualFold(strings.TrimSpace(rule.Repo), strings.TrimSpace(item.Repo)) {
		return false
	}
	if rule.Category != "" && !strings.EqualFold(strings.TrimSpace(rule.Category), strings.TrimSpace(item.Category)) {
		return false
	}
	if rule.Dependency != "" && !strings.EqualFold(strings.TrimSpace(rule.Dependency), strings.TrimSpace(item.Dependency)) {
		return false
	}
	if pathPrefix := filepath.ToSlash(strings.TrimSpace(rule.PathPrefix)); pathPrefix != "" {
		repoPath := filepath.ToSlash(item.RepoPath)
		if strings.HasSuffix(pathPrefix, "/") {
			if !strings.HasPrefix(repoPath, pathPrefix) {
				return false
			}
		} else if repoPath != pathPrefix && !strings.HasPrefix(repoPath, pathPrefix+"/") {
			return false
		}
	}
	return true
}

func codeownerRuleMatches(rule CodeownerRule, item RemediationItem) bool {
	pattern := strings.TrimSpace(rule.Pattern)
	if pattern == "" {
		return false
	}
	for _, target := range codeownerRuleTargets(item) {
		matched, err := codeownerPathMatch(pattern, target)
		if err == nil && matched {
			return true
		}
	}
	return false
}

func codeownerPathMatch(pattern, target string) (bool, error) {
	expr, err := codeownerGlobRegexp(pattern)
	if err != nil {
		return false, err
	}
	return expr.MatchString(strings.TrimPrefix(filepath.ToSlash(target), "/")), nil
}

func codeownerGlobRegexp(pattern string) (*regexp.Regexp, error) {
	pattern = strings.TrimSpace(filepath.ToSlash(pattern))
	if pattern == "" || isUnsupportedCodeownerPattern(pattern) {
		return nil, errInvalidCodeownerPattern
	}
	rootlessPattern := strings.Trim(pattern, "/")
	anchored := strings.HasPrefix(pattern, "/") || strings.Contains(rootlessPattern, "/")
	if anchored {
		pattern = strings.TrimPrefix(pattern, "/")
	}
	directoryOnly := strings.HasSuffix(pattern, "/")
	pattern = strings.TrimSuffix(pattern, "/")

	var builder strings.Builder
	if anchored {
		builder.WriteString("^")
	} else {
		builder.WriteString(`(?:^|.*/)`)
	}
	for i := 0; i < len(pattern); {
		i = appendCodeownerGlobToken(&builder, pattern, i)
	}
	if directoryOnly || codeownerTerminalLiteralSegment(pattern) {
		builder.WriteString(`(?:/.*)?`)
	}
	builder.WriteString("$")
	return regexp.Compile(builder.String())
}

func appendCodeownerGlobToken(builder *strings.Builder, pattern string, index int) int {
	switch pattern[index] {
	case '*':
		return appendCodeownerGlobStar(builder, pattern, index)
	case '?':
		builder.WriteString(`[^/]`)
	default:
		builder.WriteString(regexp.QuoteMeta(pattern[index : index+1]))
	}
	return index + 1
}

func appendCodeownerGlobStar(builder *strings.Builder, pattern string, index int) int {
	if index+1 < len(pattern) && pattern[index+1] == '*' {
		if index+2 < len(pattern) && pattern[index+2] == '/' {
			builder.WriteString(`(?:[^/]+/)*`)
			return index + 3
		}
		builder.WriteString(".*")
		return index + 2
	}
	builder.WriteString(`[^/]*`)
	return index + 1
}

func codeownerTerminalLiteralSegment(pattern string) bool {
	segment := pattern
	if index := strings.LastIndex(segment, "/"); index >= 0 {
		segment = segment[index+1:]
	}
	return segment != "" && !strings.ContainsAny(segment, "*?")
}

func codeownerRuleTargets(item RemediationItem) []string {
	if targets := codeownerEvidenceTargets(item.Evidence); len(targets) > 0 {
		return targets
	}
	return codeownerFallbackTargets(item)
}

func codeownerEvidenceTargets(evidence []string) []string {
	targets := make([]string, 0, len(evidence))
	seen := map[string]struct{}{}
	for _, value := range evidence {
		target := codeownerEvidenceTarget(value)
		if target == "" {
			continue
		}
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		targets = append(targets, target)
	}
	return targets
}

func codeownerEvidenceTarget(value string) string {
	target := strings.TrimSpace(value)
	if target == "" {
		return ""
	}
	staticLocation := strings.HasPrefix(target, "static_location:")
	if staticLocation {
		target = strings.TrimSpace(strings.TrimPrefix(target, "static_location:"))
		if index := strings.LastIndex(target, ":"); index > strings.LastIndex(target, "/") && allDigits(target[index+1:]) {
			target = target[:index]
		}
	}
	target = strings.TrimSpace(strings.ReplaceAll(target, "\\", "/"))
	for strings.HasPrefix(target, "./") {
		target = strings.TrimPrefix(target, "./")
	}
	if target == "" || target == "." || strings.HasPrefix(target, "/") || codeownerWindowsDrivePath(target) {
		return ""
	}
	for _, segment := range strings.Split(target, "/") {
		if segment == ".." {
			return ""
		}
	}
	if strings.ContainsAny(target, " \t\r\n") || !staticLocation && !strings.Contains(target, "/") {
		return ""
	}
	return target
}

func codeownerWindowsDrivePath(target string) bool {
	return len(target) >= 2 && target[1] == ':' &&
		(target[0] >= 'A' && target[0] <= 'Z' || target[0] >= 'a' && target[0] <= 'z')
}

func codeownerFallbackTargets(item RemediationItem) []string {
	targets := make([]string, 0, 2)
	seen := map[string]struct{}{}
	for _, value := range []string{item.RepoPath, item.Repo} {
		target := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(value)), "/")
		if target == "" {
			continue
		}
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		targets = append(targets, target)
	}
	return targets
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func applyRoutingAssignment(item RemediationItem, owner, team, due, status, source string) RemediationItem {
	item.Owner = strings.TrimSpace(owner)
	item.Team = strings.TrimSpace(team)
	item.Due = strings.TrimSpace(due)
	item.Status = strings.TrimSpace(status)
	if item.Status == "" {
		item.Status = "open"
	}
	item.RoutingSource = strings.TrimSpace(source)
	if item.Owner == "" && item.Team == "" {
		item.Owner = "unassigned"
		if item.RoutingSource == "" || item.RoutingSource == "default" {
			item.RoutingSource = "unassigned"
		}
	}
	return item
}

func LoadCodeowners(repoPath string) []CodeownerRule {
	for _, candidate := range []string{
		filepath.Join(repoPath, ".github", "CODEOWNERS"),
		filepath.Join(repoPath, "CODEOWNERS"),
		filepath.Join(repoPath, "docs", "CODEOWNERS"),
	} {
		data, err := safeio.ReadFileUnder(repoPath, candidate)
		if err != nil {
			continue
		}
		return parseCodeowners(string(data), relativeRoutingSource(repoPath, candidate))
	}
	return nil
}

func parseCodeowners(content, source string) []CodeownerRule {
	rules := make([]CodeownerRule, 0)
	for _, line := range strings.Split(content, "\n") {
		rule, ok := parseCodeownersLine(line, source)
		if !ok {
			continue
		}
		rules = append(rules, rule)
	}
	return rules
}

var errInvalidCodeownerPattern = errors.New("invalid codeowner pattern")
var (
	codeownerEmailLocalRegexp = regexp.MustCompile(`^[A-Za-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+$`)
)

func parseCodeownersLine(line, source string) (CodeownerRule, bool) {
	line = strings.TrimSpace(stripCodeownersComment(line))
	if line == "" {
		return CodeownerRule{}, false
	}
	fields := strings.Fields(line)
	if len(fields) == 0 || isUnsupportedCodeownerPattern(fields[0]) {
		return CodeownerRule{}, false
	}
	owners := append([]string{}, fields[1:]...)
	if !codeownerOwnersValid(owners) {
		return CodeownerRule{}, false
	}
	sort.Strings(owners)
	return CodeownerRule{Pattern: fields[0], Owners: owners, Source: source}, true
}

func stripCodeownersComment(line string) string {
	escaped := false
	for index := 0; index < len(line); index++ {
		switch line[index] {
		case '#':
			if !escaped {
				return line[:index]
			}
			escaped = false
		case '\\':
			escaped = !escaped
		default:
			escaped = false
		}
	}
	return line
}

func isUnsupportedCodeownerPattern(pattern string) bool {
	pattern = strings.TrimSpace(pattern)
	return pattern == "" ||
		strings.HasPrefix(pattern, "!") ||
		strings.HasPrefix(pattern, `\#`) ||
		strings.ContainsAny(pattern, "[]") ||
		codeownerPatternHasInvalidStars(pattern)
}

func codeownerPatternHasInvalidStars(pattern string) bool {
	for index := 0; index < len(pattern); {
		if pattern[index] != '*' {
			index++
			continue
		}
		next := index
		for next < len(pattern) && pattern[next] == '*' {
			next++
		}
		if next-index > 2 {
			return true
		}
		if next-index == 2 && (index > 0 && pattern[index-1] != '/' || next < len(pattern) && pattern[next] != '/') {
			return true
		}
		index = next
	}
	return false
}

func codeownerOwnersValid(owners []string) bool {
	for _, owner := range owners {
		if !codeownerOwnerValid(owner) {
			return false
		}
	}
	return true
}

func codeownerOwnerValid(owner string) bool {
	if owner == "" {
		return false
	}
	if strings.HasPrefix(owner, "@") {
		return codeownerMentionValid(strings.TrimPrefix(owner, "@"))
	}
	return codeownerEmailValid(owner)
}

func codeownerMentionValid(mention string) bool {
	parts := strings.Split(mention, "/")
	switch len(parts) {
	case 1:
		return codeownerAccountValid(parts[0], false)
	case 2:
		return codeownerAccountValid(parts[0], false) && codeownerAccountValid(parts[1], true)
	default:
		return false
	}
}

func codeownerAccountValid(account string, allowDot bool) bool {
	if account == "" || !allowDot && len(account) > 39 {
		return false
	}
	if account[0] == '-' || account[len(account)-1] == '-' || strings.Contains(account, "--") {
		return false
	}
	for _, char := range account {
		switch {
		case char >= 'A' && char <= 'Z', char >= 'a' && char <= 'z', char >= '0' && char <= '9', char == '_', char == '-':
		case allowDot && char == '.':
		default:
			return false
		}
	}
	return true
}

func codeownerEmailValid(owner string) bool {
	local, domain, ok := strings.Cut(owner, "@")
	if !ok || local == "" || domain == "" || !codeownerEmailLocalRegexp.MatchString(local) {
		return false
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if !codeownerEmailDomainLabelValid(label) {
			return false
		}
	}
	return true
}

func codeownerEmailDomainLabelValid(label string) bool {
	if label == "" || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for _, char := range label {
		if char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func relativeRoutingSource(repoPath, path string) string {
	rel, err := filepath.Rel(repoPath, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}
