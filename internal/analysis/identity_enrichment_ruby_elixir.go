package analysis

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"

	elixirlang "github.com/ben-ranford/lopper/internal/lang/elixir"
	rubylang "github.com/ben-ranford/lopper/internal/lang/ruby"
	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	rubyIdentityLockName       = "Gemfile.lock"
	elixirIdentityManifestName = "mix.exs"
	elixirIdentityLockName     = "mix.lock"
	rubyIdentitySourceGem      = "gem"
	rubyIdentitySourceGit      = "git"
	rubyIdentitySourcePath     = "path"
	rubyIdentitySourcePlugin   = "plugin"
	rubyIdentityDependencies   = "dependencies"
	rubyIdentityPlatforms      = "platforms"
	elixirIdentitySourceHex    = "hex"
	publicRubyGemsRemote       = "https://rubygems.org"
	publicHexRepository        = "hexpm"
	invalidMixLockEntryFormat  = "invalid Mix lock entry for %q"
)

var (
	rubyIdentityLockedSpecPattern = regexp.MustCompile(`^ {4}([A-Za-z0-9_.-]+) \(([^)]+)\)\s*$`)
	rubyIdentityDirectSpecPattern = regexp.MustCompile(`^ {2}([A-Za-z0-9_.-]+)(?: \([^)]*\))?!?\s*$`)
	rubyIdentityRemotePattern     = regexp.MustCompile(`^ {2}remote:\s*(\S+)\s*$`)
	rubyIdentityPlatformPattern   = regexp.MustCompile(`^ {2}([A-Za-z0-9_.-]+)\s*$`)
	elixirIdentityAtomPattern     = regexp.MustCompile(`^[a-z][a-z0-9_]*[!?]?$`)
	elixirIdentityQuotedAtom      = regexp.MustCompile(`^[a-z][a-z0-9_.-]*[!?]?$`)
	elixirIdentityDepsListPattern = []*regexp.Regexp{
		regexp.MustCompile(`(?m)\bdefp?\s+deps(?:\s*\(\s*\))?\s*,\s*do\s*:\s*\[`),
		regexp.MustCompile(`(?m)\bdefp?\s+deps(?:\s*\(\s*\))?\s+do\s*\[`),
	}
)

type rubyIdentityLockedSpec struct {
	packageName string
	version     string
	source      string
	remote      string
}

type rubyIdentityLock struct {
	direct    map[string]struct{}
	specs     map[string][]rubyIdentityLockedSpec
	platforms map[string]struct{}
}

type rubyIdentityLockParser struct {
	lock            rubyIdentityLock
	section         string
	remote          string
	inSpecs         bool
	sawSource       bool
	sawDependencies bool
}

type elixirIdentityLockedPackage struct {
	lookupName  string
	packageName string
	version     string
	source      string
	repository  string
}

type elixirIdentityDomain = identityManifestLockFiles

type elixirIdentityManifest struct {
	declared map[string]struct{}
	umbrella bool
	appsPath string
}

type identityTopLevelTermSplitter struct {
	raw    string
	masked string
	terms  []string
	stack  []byte
	start  int
}

func discoverRubyIdentityManifests(repoPath string, snapshot *identityManifestSnapshot, warnings *identityWarningCollector) {
	paths := discoverAdapterIdentityManifests(repoPath, warnings, rubylang.ShouldSkipDirectory, func(name string) bool {
		return name == rubyIdentityLockName
	})
	snapshot.rubyFiles = append(snapshot.rubyFiles, paths...)
}

func discoverElixirIdentityManifests(repoPath string, snapshot *identityManifestSnapshot, warnings *identityWarningCollector) {
	paths := discoverAdapterIdentityManifests(repoPath, warnings, elixirlang.ShouldSkipDirectory, func(name string) bool {
		return name == elixirIdentityManifestName || name == elixirIdentityLockName
	})
	snapshot.elixirFiles = append(snapshot.elixirFiles, paths...)
}

func collectRubyIdentityEvidenceFromPaths(repoPath string, index identityIndex, paths []string, warnings *identityWarningCollector) {
	for _, path := range paths {
		data, err := safeio.ReadFileUnder(repoPath, path)
		if err != nil {
			warnings.addFailure("read", path, identityReadFailed, err)
			continue
		}
		lock, err := parseRubyIdentityLock(data)
		if err != nil {
			warnings.addFailure("parse", path, identityParseFailed, err)
			continue
		}
		addRubyIdentityLockEvidence(index, lock, relativeIdentitySource(repoPath, path))
	}
}

func parseRubyIdentityLock(data []byte) (rubyIdentityLock, error) {
	parser := rubyIdentityLockParser{lock: rubyIdentityLock{
		direct:    make(map[string]struct{}),
		specs:     make(map[string][]rubyIdentityLockedSpec),
		platforms: make(map[string]struct{}),
	}}
	for _, rawLine := range strings.Split(string(data), "\n") {
		parser.applyLine(strings.TrimRight(rawLine, "\r"))
	}
	if !parser.valid() {
		return rubyIdentityLock{}, fmt.Errorf("invalid Bundler lockfile")
	}
	return parser.lock, nil
}

func (p *rubyIdentityLockParser) applyLine(line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return
	}
	if line == trimmed {
		p.startSection(trimmed)
		return
	}
	switch {
	case p.section == rubyIdentityDependencies:
		p.addDirectDependency(line)
	case p.section == rubyIdentityPlatforms:
		p.addPlatform(line)
	case rubyIdentityRemotePattern.MatchString(line):
		p.addRemote(line)
	case trimmed == "specs:":
		p.inSpecs = isRubyIdentitySource(p.section)
	case p.inSpecs:
		p.addLockedSpec(line)
	}
}

func (p *rubyIdentityLockParser) startSection(value string) {
	p.section = rubyIdentitySection(value)
	p.remote = ""
	p.inSpecs = false
	p.sawSource = p.sawSource || isRubyIdentitySource(p.section)
	p.sawDependencies = p.sawDependencies || p.section == rubyIdentityDependencies
}

func (p *rubyIdentityLockParser) addDirectDependency(line string) {
	matches := rubyIdentityDirectSpecPattern.FindStringSubmatch(line)
	if len(matches) != 2 {
		return
	}
	if name := normalizeRubyIdentityLookupName(matches[1]); name != "" {
		p.lock.direct[name] = struct{}{}
	}
}

func (p *rubyIdentityLockParser) addPlatform(line string) {
	matches := rubyIdentityPlatformPattern.FindStringSubmatch(line)
	if len(matches) == 2 {
		p.lock.platforms[strings.ToLower(matches[1])] = struct{}{}
	}
}

func (p *rubyIdentityLockParser) addRemote(line string) {
	matches := rubyIdentityRemotePattern.FindStringSubmatch(line)
	if len(matches) != 2 {
		return
	}
	remote := strings.TrimSpace(matches[1])
	if p.remote != "" && p.remote != remote {
		p.remote = "ambiguous"
		return
	}
	p.remote = remote
}

func (p *rubyIdentityLockParser) addLockedSpec(line string) {
	matches := rubyIdentityLockedSpecPattern.FindStringSubmatch(line)
	if len(matches) != 3 {
		return
	}
	packageName := normalizeRegistryIdentityName(matches[1])
	version := strings.TrimSpace(matches[2])
	lookupName := normalizeRubyIdentityLookupName(packageName)
	if lookupName == "" || version == "" || strings.ContainsAny(version, " \t\r\n") {
		return
	}
	p.lock.specs[lookupName] = append(p.lock.specs[lookupName], rubyIdentityLockedSpec{
		packageName: packageName, version: version, source: p.section, remote: p.remote,
	})
}

func (p *rubyIdentityLockParser) valid() bool {
	if !p.sawSource || !p.sawDependencies {
		return false
	}
	for name := range p.lock.direct {
		if len(p.lock.specs[name]) == 0 {
			return false
		}
	}
	return true
}

func rubyIdentitySection(value string) string {
	switch value {
	case "GEM":
		return rubyIdentitySourceGem
	case "GIT":
		return rubyIdentitySourceGit
	case "PATH":
		return rubyIdentitySourcePath
	case "PLUGIN SOURCE":
		return rubyIdentitySourcePlugin
	case "DEPENDENCIES":
		return rubyIdentityDependencies
	case "PLATFORMS":
		return rubyIdentityPlatforms
	default:
		return ""
	}
}

func isRubyIdentitySource(value string) bool {
	switch value {
	case rubyIdentitySourceGem, rubyIdentitySourceGit, rubyIdentitySourcePath, rubyIdentitySourcePlugin:
		return true
	default:
		return false
	}
}

func addRubyIdentityLockEvidence(index identityIndex, lock rubyIdentityLock, source string) {
	for _, lookupName := range sortedIdentityMapKeys(lock.direct) {
		specs := supportedRubyIdentitySpecs(lock.specs[lookupName], lock.platforms)
		if !hasUnambiguousRubyGemsSource(specs) {
			continue
		}
		for _, spec := range uniqueRubyIdentityLockedSpecs(specs) {
			addIdentityEvidence(index, identityEvidence{
				Language: "ruby", Ecosystem: "gem", LookupName: lookupName,
				Name: spec.packageName, Version: spec.version, Status: identityStatusResolved,
				Source: source, Confidence: "high",
			})
		}
	}
}

func supportedRubyIdentitySpecs(specs []rubyIdentityLockedSpec, platforms map[string]struct{}) []rubyIdentityLockedSpec {
	for _, spec := range specs {
		if isRubyIdentityPlatformVersion(spec.version, platforms) {
			return nil
		}
	}
	return specs
}

func isRubyIdentityPlatformVersion(version string, platforms map[string]struct{}) bool {
	for platform := range platforms {
		if strings.HasSuffix(strings.ToLower(version), "-"+platform) {
			return true
		}
	}
	return false
}

func hasUnambiguousRubyGemsSource(specs []rubyIdentityLockedSpec) bool {
	packageName := ""
	for _, spec := range specs {
		if spec.source != rubyIdentitySourceGem || !isPublicRubyGemsRemote(spec.remote) || spec.packageName == "" {
			return false
		}
		if packageName != "" && packageName != spec.packageName {
			return false
		}
		packageName = spec.packageName
	}
	return len(specs) > 0
}

func isPublicRubyGemsRemote(value string) bool {
	return strings.TrimRight(strings.TrimSpace(value), "/") == publicRubyGemsRemote
}

func uniqueRubyIdentityLockedSpecs(specs []rubyIdentityLockedSpec) []rubyIdentityLockedSpec {
	unique := make(map[string]rubyIdentityLockedSpec)
	for _, spec := range specs {
		unique[spec.packageName+"\x00"+spec.version] = spec
	}
	keys := sortedIdentityMapKeys(unique)
	result := make([]rubyIdentityLockedSpec, 0, len(keys))
	for _, key := range keys {
		result = append(result, unique[key])
	}
	return result
}

func collectElixirIdentityEvidenceFromPaths(repoPath string, index identityIndex, paths []string, warnings *identityWarningCollector) {
	domains := groupElixirIdentityDomains(paths)
	for _, directory := range sortedIdentityMapKeys(domains) {
		domain := domains[directory]
		if domain.lockPath == "" {
			continue
		}
		manifest := readElixirIdentityManifest(repoPath, domain.manifestPath, warnings)
		if manifest.umbrella {
			addElixirUmbrellaDeclarations(repoPath, directory, manifest.appsPath, domains, manifest.declared, warnings)
		}
		locked, ok := readElixirIdentityLock(repoPath, domain.lockPath, warnings)
		if ok {
			addElixirIdentityLockEvidence(index, manifest.declared, locked, relativeIdentitySource(repoPath, domain.lockPath))
		}
	}
}

func groupElixirIdentityDomains(paths []string) map[string]elixirIdentityDomain {
	return groupIdentityManifestLockFiles(paths, elixirIdentityManifestName, elixirIdentityLockName)
}

func readElixirIdentityManifest(repoPath, path string, warnings *identityWarningCollector) elixirIdentityManifest {
	manifest := elixirIdentityManifest{declared: make(map[string]struct{})}
	if path == "" {
		return manifest
	}
	data, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		warnings.addFailure("read", path, identityReadFailed, err)
		return manifest
	}
	manifest.declared = parseElixirIdentityDeclarations(data)
	manifest.umbrella, manifest.appsPath = elixirlang.DetectUmbrellaAppsPath(data)
	return manifest
}

func addElixirUmbrellaDeclarations(repoPath, directory, appsPath string, domains map[string]elixirIdentityDomain, declared map[string]struct{}, warnings *identityWarningCollector) {
	appsRoot := filepath.Clean(filepath.Join(directory, appsPath))
	if !shared.IsPathWithin(repoPath, appsRoot) {
		return
	}
	for _, childDirectory := range sortedIdentityMapKeys(domains) {
		child := domains[childDirectory]
		if child.manifestPath == "" || child.lockPath != "" || !isImmediateIdentityChild(appsRoot, childDirectory) {
			continue
		}
		childManifest := readElixirIdentityManifest(repoPath, child.manifestPath, warnings)
		for name := range childManifest.declared {
			declared[name] = struct{}{}
		}
	}
}

func isImmediateIdentityChild(parent, candidate string) bool {
	relative, err := filepath.Rel(parent, candidate)
	if err != nil || relative == "." || relative == ".." {
		return false
	}
	return filepath.Dir(relative) == "."
}

func parseElixirIdentityDeclarations(data []byte) map[string]struct{} {
	raw := string(data)
	masked := string(elixirlang.MaskSource(data))
	declared := make(map[string]struct{})
	for _, pattern := range elixirIdentityDepsListPattern {
		for _, match := range pattern.FindAllStringIndex(masked, -1) {
			addElixirIdentityDeclarations(raw, masked, match[1]-1, declared)
		}
	}
	return declared
}

func addElixirIdentityDeclarations(raw, masked string, opening int, declared map[string]struct{}) {
	closing, ok := matchingIdentityDelimiterEnd(masked, opening)
	if !ok || hasDynamicElixirIdentityListTail(masked, closing) {
		return
	}
	dependencies, literal := parseElixirIdentityDepsList(raw[opening+1 : closing])
	if !literal {
		return
	}
	for name := range dependencies {
		declared[name] = struct{}{}
	}
}

func hasDynamicElixirIdentityListTail(masked string, closing int) bool {
	tail := strings.TrimLeft(masked[closing+1:], " \t\r\n")
	return strings.HasPrefix(tail, "++") || strings.HasPrefix(tail, "--")
}

func parseElixirIdentityDepsList(content string) (map[string]struct{}, bool) {
	masked := string(elixirlang.MaskSource([]byte(content)))
	terms, ok := splitIdentityTopLevelTerms(content, masked)
	if !ok {
		return nil, false
	}
	declared := make(map[string]struct{})
	for index, term := range terms {
		if index == len(terms)-1 && isElixirIdentityCommentOnly(term) {
			continue
		}
		trimmed := strings.TrimSpace(term)
		name, ok := parseElixirIdentityDependencyTuple(trimmed)
		if !ok {
			return nil, false
		}
		declared[name] = struct{}{}
	}
	return declared, true
}

func isElixirIdentityCommentOnly(value string) bool {
	sawComment := false
	for _, line := range strings.Split(value, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "#") {
			return false
		}
		sawComment = true
	}
	return sawComment
}

func parseElixirIdentityDependencyTuple(value string) (string, bool) {
	masked := string(elixirlang.MaskSource([]byte(value)))
	opening := skipIdentityWhitespace(masked, 0)
	if opening >= len(masked) || masked[opening] != '{' {
		return "", false
	}
	closing, ok := matchingIdentityDelimiterEnd(masked, opening)
	if !ok || strings.TrimSpace(masked[closing+1:]) != "" {
		return "", false
	}
	fields, ok := splitIdentityTopLevelTerms(value[opening+1:closing], masked[opening+1:closing])
	if !ok || len(fields) < 2 {
		return "", false
	}
	name, ok := parseElixirIdentityAtom(fields[0])
	if !ok {
		return "", false
	}
	return normalizeElixirIdentityLookupName(name), true
}

func readElixirIdentityLock(repoPath, path string, warnings *identityWarningCollector) ([]elixirIdentityLockedPackage, bool) {
	data, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		warnings.addFailure("read", path, identityReadFailed, err)
		return nil, false
	}
	locked, err := parseElixirIdentityLock(data)
	if err != nil {
		warnings.addFailure("parse", path, identityParseFailed, err)
		return nil, false
	}
	return locked, true
}

func parseElixirIdentityLock(data []byte) ([]elixirIdentityLockedPackage, error) {
	raw := string(data)
	masked := string(elixirlang.MaskSource(data))
	opening := skipIdentityWhitespace(masked, 0)
	if opening+1 >= len(raw) || raw[opening] != '%' || raw[opening+1] != '{' {
		return nil, fmt.Errorf("invalid Mix lockfile")
	}
	closing, ok := matchingIdentityDelimiterEnd(masked, opening+1)
	if !ok || strings.TrimSpace(masked[closing+1:]) != "" {
		return nil, fmt.Errorf("invalid Mix lockfile")
	}
	return parseElixirIdentityLockEntries(raw, masked, opening+2, closing)
}

func parseElixirIdentityLockEntries(raw, masked string, position, end int) ([]elixirIdentityLockedPackage, error) {
	locked := make([]elixirIdentityLockedPackage, 0)
	afterEntry := false
	for {
		var done, valid bool
		position, done, valid = nextElixirIdentityLockEntry(raw, position, end, afterEntry)
		if !valid {
			return nil, fmt.Errorf("invalid Mix lock separator")
		}
		if done {
			return locked, nil
		}
		item, next, err := parseElixirIdentityLockEntry(raw, masked, position, end)
		if err != nil {
			return nil, err
		}
		locked = append(locked, item)
		position = next
		afterEntry = true
	}
}

func parseElixirIdentityLockEntry(raw, masked string, position, end int) (elixirIdentityLockedPackage, int, error) {
	key, next, ok := parseIdentityQuotedStringAt(raw, position)
	if !ok {
		return elixirIdentityLockedPackage{}, position, fmt.Errorf("invalid Mix lock key")
	}
	position, ok = skipElixirIdentityKeySeparator(raw, next)
	if !ok || position >= end || masked[position] != '{' {
		return elixirIdentityLockedPackage{}, position, fmt.Errorf(invalidMixLockEntryFormat, key)
	}
	entryEnd, balanced := matchingIdentityDelimiterEnd(masked, position)
	if !balanced || entryEnd >= end {
		return elixirIdentityLockedPackage{}, position, fmt.Errorf(invalidMixLockEntryFormat, key)
	}
	item, err := parseElixirIdentityLockedPackage(key, raw[position:entryEnd+1], masked[position:entryEnd+1])
	return item, entryEnd + 1, err
}

func nextElixirIdentityLockEntry(value string, position, end int, afterEntry bool) (int, bool, bool) {
	position = skipIdentityWhitespace(value, position)
	if position >= end {
		return position, true, true
	}
	if !afterEntry {
		return position, false, value[position] != ','
	}
	if value[position] != ',' {
		return position, false, false
	}
	position = skipIdentityWhitespace(value, position+1)
	return position, position >= end, position >= end || value[position] != ','
}

func skipElixirIdentityKeySeparator(raw string, position int) (int, bool) {
	position = skipIdentityWhitespace(raw, position)
	switch {
	case position < len(raw) && raw[position] == ':':
		return skipIdentityWhitespace(raw, position+1), true
	case position+1 < len(raw) && raw[position:position+2] == "=>":
		return skipIdentityWhitespace(raw, position+2), true
	default:
		return position, false
	}
}

func parseElixirIdentityLockedPackage(key, rawTuple, maskedTuple string) (elixirIdentityLockedPackage, error) {
	fields, ok := splitIdentityTopLevelTerms(rawTuple[1:len(rawTuple)-1], maskedTuple[1:len(maskedTuple)-1])
	if !ok || len(fields) == 0 {
		return elixirIdentityLockedPackage{}, fmt.Errorf(invalidMixLockEntryFormat, key)
	}
	source, ok := parseElixirIdentityAtom(fields[0])
	if !ok {
		return elixirIdentityLockedPackage{}, fmt.Errorf("invalid Mix lock source for %q", key)
	}
	item := elixirIdentityLockedPackage{lookupName: normalizeElixirIdentityLookupName(key), source: source}
	if source != elixirIdentitySourceHex {
		return item, nil
	}
	if len(fields) < 7 {
		return elixirIdentityLockedPackage{}, fmt.Errorf("invalid Hex lock entry for %q", key)
	}
	packageName, packageOK := parseElixirIdentityAtom(fields[1])
	version, versionOK := parseIdentityQuotedString(strings.TrimSpace(fields[2]))
	repository, repositoryOK := parseIdentityQuotedString(strings.TrimSpace(fields[6]))
	if !packageOK || !versionOK || !repositoryOK || !isExactElixirIdentityVersion(version) {
		return elixirIdentityLockedPackage{}, fmt.Errorf("invalid Hex lock entry for %q", key)
	}
	item.packageName = normalizeRegistryIdentityName(packageName)
	item.version = version
	item.repository = repository
	return item, nil
}

func addElixirIdentityLockEvidence(index identityIndex, declared map[string]struct{}, locked []elixirIdentityLockedPackage, source string) {
	byLookupName := make(map[string][]elixirIdentityLockedPackage)
	for _, item := range locked {
		if _, ok := declared[item.lookupName]; ok {
			byLookupName[item.lookupName] = append(byLookupName[item.lookupName], item)
		}
	}
	for _, lookupName := range sortedIdentityMapKeys(byLookupName) {
		items := byLookupName[lookupName]
		if !hasUnambiguousHexSource(items) {
			continue
		}
		item := items[0]
		addIdentityEvidence(index, identityEvidence{
			Language: "elixir", Ecosystem: "hex", LookupName: lookupName,
			Name: item.packageName, Version: item.version, Status: identityStatusResolved,
			Source: source, Confidence: "high",
		})
	}
}

func hasUnambiguousHexSource(items []elixirIdentityLockedPackage) bool {
	return len(items) == 1 && items[0].source == elixirIdentitySourceHex &&
		items[0].repository == publicHexRepository && items[0].packageName != "" && items[0].version != ""
}

func matchingIdentityDelimiterEnd(value string, opening int) (int, bool) {
	if opening < 0 || opening >= len(value) {
		return 0, false
	}
	closing, ok := identityClosingDelimiter(value[opening])
	if !ok {
		return 0, false
	}
	stack := []byte{closing}
	for index := opening + 1; index < len(value); index++ {
		if nestedClosing, nested := identityClosingDelimiter(value[index]); nested {
			stack = append(stack, nestedClosing)
			continue
		}
		if !isIdentityClosingDelimiter(value[index]) {
			continue
		}
		if len(stack) == 0 || value[index] != stack[len(stack)-1] {
			return 0, false
		}
		stack = stack[:len(stack)-1]
		if len(stack) == 0 {
			return index, true
		}
	}
	return 0, false
}

func splitIdentityTopLevelTerms(raw, masked string) ([]string, bool) {
	if len(raw) != len(masked) {
		return nil, false
	}
	if strings.TrimSpace(raw) == "" {
		return []string{}, true
	}
	splitter := identityTopLevelTermSplitter{raw: raw, masked: masked, terms: make([]string, 0), stack: make([]byte, 0)}
	if !splitter.scan() {
		return nil, false
	}
	return splitter.terms, true
}

func (s *identityTopLevelTermSplitter) scan() bool {
	for index := 0; index < len(s.masked); index++ {
		if !s.apply(index) {
			return false
		}
	}
	if len(s.stack) != 0 {
		return false
	}
	if strings.TrimSpace(s.raw[s.start:]) != "" {
		s.terms = append(s.terms, s.raw[s.start:])
	}
	return true
}

func (s *identityTopLevelTermSplitter) apply(index int) bool {
	value := s.masked[index]
	if closing, opening := identityClosingDelimiter(value); opening {
		s.stack = append(s.stack, closing)
		return true
	}
	if isIdentityClosingDelimiter(value) {
		return s.close(value)
	}
	if value != ',' || len(s.stack) != 0 {
		return true
	}
	if strings.TrimSpace(s.raw[s.start:index]) == "" {
		return false
	}
	s.terms = append(s.terms, s.raw[s.start:index])
	s.start = index + 1
	return true
}

func (s *identityTopLevelTermSplitter) close(value byte) bool {
	if len(s.stack) == 0 || value != s.stack[len(s.stack)-1] {
		return false
	}
	s.stack = s.stack[:len(s.stack)-1]
	return true
}

func identityClosingDelimiter(value byte) (byte, bool) {
	switch value {
	case '(':
		return ')', true
	case '[':
		return ']', true
	case '{':
		return '}', true
	default:
		return 0, false
	}
}

func isIdentityClosingDelimiter(value byte) bool {
	return value == ')' || value == ']' || value == '}'
}

func parseElixirIdentityAtom(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, ":") {
		return "", false
	}
	atom := strings.TrimPrefix(value, ":")
	if strings.HasPrefix(atom, "\"") {
		decoded, ok := parseIdentityQuotedString(atom)
		return decoded, ok && elixirIdentityQuotedAtom.MatchString(decoded)
	}
	return atom, elixirIdentityAtomPattern.MatchString(atom)
}

func parseIdentityQuotedString(value string) (string, bool) {
	decoded, err := strconv.Unquote(value)
	return decoded, err == nil
}

func parseIdentityQuotedStringAt(value string, start int) (string, int, bool) {
	if start < 0 || start >= len(value) || value[start] != '"' {
		return "", start, false
	}
	escaped := false
	for index := start + 1; index < len(value); index++ {
		switch {
		case escaped:
			escaped = false
		case value[index] == '\\':
			escaped = true
		case value[index] == '"':
			decoded, ok := parseIdentityQuotedString(value[start : index+1])
			return decoded, index + 1, ok
		}
	}
	return "", start, false
}

func skipIdentityWhitespace(value string, position int) int {
	for position < len(value) {
		switch value[position] {
		case ' ', '\t', '\r', '\n':
			position++
		default:
			return position
		}
	}
	return position
}

func normalizeRubyIdentityLookupName(value string) string {
	value = normalizeRegistryIdentityName(value)
	value = strings.ReplaceAll(value, "_", "-")
	return strings.ReplaceAll(value, ".", "-")
}

func normalizeElixirIdentityLookupName(value string) string {
	return strings.ReplaceAll(normalizeRegistryIdentityName(value), "_", "-")
}

func normalizeRegistryIdentityName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func isExactElixirIdentityVersion(value string) bool {
	version := strings.TrimSpace(value)
	return version != "" && semver.IsValid("v"+version)
}
