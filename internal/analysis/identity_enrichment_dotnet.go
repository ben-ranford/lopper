package analysis

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

const nugetConditionalMarker = "conditional"

type nugetMSBuildItem struct {
	name            string
	version         string
	versionOverride string
	condition       string
	update          bool
}

type nugetMSBuildDocument struct {
	items                      []nugetMSBuildItem
	centralActivationSeen      bool
	centralEnabled             bool
	centralActivationAmbiguous bool
}

type nugetMSBuildParser struct {
	decoder     *xml.Decoder
	elementName string
	document    nugetMSBuildDocument
	conditional bool
	parents     []bool
}

type nugetProjectVersion struct {
	version         string
	versionOverride string
}

type nugetProjectDependency struct {
	name       string
	candidates []nugetProjectVersion
}

type nugetProjectModel map[string]*nugetProjectDependency

type nugetProjectFile struct {
	centralAllowed bool
	packages       nugetProjectModel
}

type nugetVersionCandidate struct {
	name    string
	version string
	source  string
}

type nugetCentralModel map[string][]nugetVersionCandidate
type nugetLockModel map[string][]nugetVersionCandidate

type nugetCentralFile struct {
	enabled  bool
	packages nugetCentralModel
}

type nugetLockDocument struct {
	Dependencies map[string]map[string]nugetLockDependency `json:"dependencies"`
}

type nugetLockDependency struct {
	Type     string `json:"type"`
	Resolved string `json:"resolved"`
}

type nugetPackageEvidence struct {
	resolved []identityEvidence
	declared []identityEvidence
	unknown  []identityEvidence
}

func collectDotNetIdentityEvidenceFromSnapshot(repoPath string, index identityIndex, snapshot identityManifestSnapshot, warnings *identityWarningCollector) {
	projects := collectNuGetProjectModels(repoPath, snapshot.dotnetProjectFiles, warnings)
	centrals := collectNuGetCentralModels(repoPath, snapshot.dotnetCentralFiles, warnings)
	locks := collectNuGetLockModels(repoPath, snapshot.dotnetLockFiles, warnings)
	centralByDir := nugetCentralModelsByDir(centrals)
	projectCounts := countNuGetProjectsByDir(snapshot.dotnetProjectFiles)
	packages := map[string]*nugetPackageEvidence{}

	for _, projectPath := range sortedNuGetModelPaths(projects) {
		project := projects[projectPath]
		central := nugetCentralModel(nil)
		if project.centralAllowed {
			central = activeNuGetCentralModel(nearestNuGetCentralFile(repoPath, projectPath, centralByDir))
		}
		lock := nugetLockForProject(projectPath, projectCounts, locks)
		appendNuGetProjectEvidence(packages, relativeIdentitySource(repoPath, projectPath), project.packages, central, lock)
	}
	addCollectedNuGetEvidence(index, packages)
}

func collectNuGetProjectModels(repoPath string, paths []string, warnings *identityWarningCollector) map[string]nugetProjectFile {
	models := make(map[string]nugetProjectFile, len(paths))
	for _, path := range paths {
		document, ok := readNuGetMSBuildDocument(repoPath, path, "PackageReference", warnings)
		if !ok {
			continue
		}
		model := nugetProjectModel{}
		for _, item := range document.items {
			applyNuGetProjectItem(model, item)
		}
		models[filepath.Clean(path)] = nugetProjectFile{
			centralAllowed: document.centralPackageVersionsAllowed(),
			packages:       model,
		}
	}
	return models
}

func collectNuGetCentralModels(repoPath string, paths []string, warnings *identityWarningCollector) map[string]*nugetCentralFile {
	models := make(map[string]*nugetCentralFile, len(paths))
	for _, path := range paths {
		cleanPath := filepath.Clean(path)
		models[cleanPath] = nil
		document, ok := readNuGetMSBuildDocument(repoPath, path, "PackageVersion", warnings)
		if !ok {
			continue
		}
		source := relativeIdentitySource(repoPath, path)
		model := nugetCentralModel{}
		for _, item := range document.items {
			applyNuGetCentralItem(model, item, source)
		}
		models[cleanPath] = &nugetCentralFile{
			enabled:  document.centralActivationSeen && document.centralPackageVersionsAllowed(),
			packages: model,
		}
	}
	return models
}

func collectNuGetLockModels(repoPath string, paths []string, warnings *identityWarningCollector) map[string]nugetLockModel {
	models := make(map[string]nugetLockModel, len(paths))
	for _, path := range paths {
		cleanPath := filepath.Clean(path)
		models[cleanPath] = nil
		data, err := safeio.ReadFileUnder(repoPath, path)
		if err != nil {
			warnings.addFailure("read", path, identityReadFailed, err)
			continue
		}
		var document nugetLockDocument
		if err := json.Unmarshal(data, &document); err != nil {
			warnings.addFailure("parse", path, identityParseFailed, err)
			continue
		}
		source := relativeIdentitySource(repoPath, path)
		model := nugetLockModel{}
		for _, framework := range sortedNuGetLockFrameworks(document.Dependencies) {
			dependencies := document.Dependencies[framework]
			for _, name := range sortedNuGetDependencyNames(dependencies) {
				dependency := dependencies[name]
				version, validVersion := normalizeNuGetVersion(dependency.Resolved)
				if !strings.EqualFold(strings.TrimSpace(dependency.Type), "Direct") || !validVersion || !validNuGetPackageName(name) {
					continue
				}
				key := normalizeIdentityName(name)
				model[key] = appendUniqueNuGetCandidate(model[key], nugetVersionCandidate{
					name: strings.TrimSpace(name), version: version, source: source,
				})
			}
		}
		models[cleanPath] = model
	}
	return models
}

func readNuGetMSBuildDocument(repoPath, path, elementName string, warnings *identityWarningCollector) (nugetMSBuildDocument, bool) {
	data, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		warnings.addFailure("read", path, identityReadFailed, err)
		return nugetMSBuildDocument{}, false
	}
	document, err := parseNuGetMSBuildDocument(data, elementName)
	if err != nil {
		warnings.addFailure("parse", path, identityParseFailed, err)
		return nugetMSBuildDocument{}, false
	}
	return document, true
}

func parseNuGetMSBuildItems(data []byte, elementName string) ([]nugetMSBuildItem, error) {
	document, err := parseNuGetMSBuildDocument(data, elementName)
	return document.items, err
}

func parseNuGetMSBuildDocument(data []byte, elementName string) (nugetMSBuildDocument, error) {
	parser := nugetMSBuildParser{
		decoder:     xml.NewDecoder(bytes.NewReader(data)),
		elementName: elementName,
		document:    nugetMSBuildDocument{items: make([]nugetMSBuildItem, 0)},
		parents:     make([]bool, 0),
	}
	for {
		token, err := parser.decoder.Token()
		if errors.Is(err, io.EOF) {
			return parser.document, nil
		}
		if err != nil {
			return nugetMSBuildDocument{}, err
		}
		if err := parser.consume(token); err != nil {
			return nugetMSBuildDocument{}, err
		}
	}
}

func (p *nugetMSBuildParser) consume(token xml.Token) error {
	switch typed := token.(type) {
	case xml.StartElement:
		return p.consumeStart(typed)
	case xml.EndElement:
		p.consumeEnd()
	}
	return nil
}

func (p *nugetMSBuildParser) consumeStart(start xml.StartElement) error {
	if strings.EqualFold(start.Name.Local, p.elementName) {
		items, err := decodeNuGetMSBuildItems(p.decoder, start, p.conditional)
		if err != nil {
			return err
		}
		p.appendValidItems(items)
		return nil
	}
	if strings.EqualFold(start.Name.Local, "ManagePackageVersionsCentrally") {
		value, err := decodeNuGetTextElement(p.decoder, start)
		if err != nil {
			return err
		}
		p.document.applyCentralActivation(value, p.conditional || hasMSBuildCondition(start))
		return nil
	}
	p.parents = append(p.parents, p.conditional)
	p.conditional = p.conditional || isConditionalMSBuildScope(start)
	return nil
}

func (p *nugetMSBuildParser) appendValidItems(items []nugetMSBuildItem) {
	for _, item := range items {
		if validNuGetPackageName(item.name) {
			p.document.items = append(p.document.items, item)
		}
	}
}

func (p *nugetMSBuildParser) consumeEnd() {
	if len(p.parents) == 0 {
		return
	}
	p.conditional = p.parents[len(p.parents)-1]
	p.parents = p.parents[:len(p.parents)-1]
}

func (d *nugetMSBuildDocument) applyCentralActivation(value string, conditional bool) {
	d.centralActivationSeen = true
	if conditional {
		d.centralActivationAmbiguous = true
		return
	}
	d.centralEnabled = strings.EqualFold(strings.TrimSpace(value), "true")
}

func (d *nugetMSBuildDocument) centralPackageVersionsAllowed() bool {
	if !d.centralActivationSeen {
		return true
	}
	return d.centralEnabled && !d.centralActivationAmbiguous
}

func decodeNuGetMSBuildItems(decoder *xml.Decoder, start xml.StartElement, ancestorConditional bool) ([]nugetMSBuildItem, error) {
	item := nugetMSBuildItemFromAttributes(start)
	if ancestorConditional {
		item.condition = nugetConditionalMarker
	}
	items := []nugetMSBuildItem{item}
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			field, value, conditional, handled, decodeErr := decodeNuGetMetadataElement(decoder, typed)
			if decodeErr != nil {
				return nil, decodeErr
			}
			if handled {
				items = applyNuGetMetadata(items, field, value, conditional)
			}
		case xml.EndElement:
			if typed.Name == start.Name {
				return items, nil
			}
		}
	}
}

func nugetMSBuildItemFromAttributes(start xml.StartElement) nugetMSBuildItem {
	item := nugetMSBuildItem{}
	include := ""
	update := ""
	for _, attr := range start.Attr {
		switch strings.ToLower(attr.Name.Local) {
		case "include":
			include = strings.TrimSpace(attr.Value)
		case "update":
			update = strings.TrimSpace(attr.Value)
		case "version":
			item.version = strings.TrimSpace(attr.Value)
		case "versionoverride":
			item.versionOverride = strings.TrimSpace(attr.Value)
		case "condition":
			if strings.TrimSpace(attr.Value) != "" {
				item.condition = nugetConditionalMarker
			}
		}
	}
	item.name = include
	if item.name == "" {
		item.name = update
		item.update = update != ""
	}
	return item
}

func decodeNuGetMetadataElement(decoder *xml.Decoder, start xml.StartElement) (string, string, bool, bool, error) {
	field := strings.ToLower(start.Name.Local)
	if field != "version" && field != "versionoverride" {
		return "", "", false, false, decoder.Skip()
	}
	value, err := decodeNuGetTextElement(decoder, start)
	if err != nil {
		return "", "", false, false, err
	}
	return field, value, hasMSBuildCondition(start), true, nil
}

func decodeNuGetTextElement(decoder *xml.Decoder, start xml.StartElement) (string, error) {
	var value string
	if err := decoder.DecodeElement(&value, &start); err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func applyNuGetMetadata(items []nugetMSBuildItem, field, value string, conditional bool) []nugetMSBuildItem {
	if !conditional {
		for i := range items {
			setNuGetItemMetadata(&items[i], field, value)
		}
		return uniqueNuGetMSBuildItems(items)
	}
	result := append([]nugetMSBuildItem{}, items...)
	for _, item := range items {
		setNuGetItemMetadata(&item, field, value)
		item.condition = nugetConditionalMarker
		result = append(result, item)
	}
	return uniqueNuGetMSBuildItems(result)
}

func setNuGetItemMetadata(item *nugetMSBuildItem, field, value string) {
	if field == "versionoverride" {
		item.versionOverride = value
		return
	}
	item.version = value
}

func uniqueNuGetMSBuildItems(items []nugetMSBuildItem) []nugetMSBuildItem {
	result := make([]nugetMSBuildItem, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		key := strings.Join([]string{item.name, item.version, item.versionOverride, item.condition}, "\x00")
		if item.update {
			key += "\x00update"
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, item)
	}
	return result
}

func hasMSBuildCondition(start xml.StartElement) bool {
	for _, attr := range start.Attr {
		if strings.EqualFold(attr.Name.Local, "Condition") && strings.TrimSpace(attr.Value) != "" {
			return true
		}
	}
	return false
}

func isConditionalMSBuildScope(start xml.StartElement) bool {
	if hasMSBuildCondition(start) {
		return true
	}
	switch strings.ToLower(start.Name.Local) {
	case "choose", "when", "otherwise":
		return true
	default:
		return false
	}
}

func applyNuGetProjectItem(model nugetProjectModel, item nugetMSBuildItem) {
	key := normalizeIdentityName(item.name)
	if key == "" {
		return
	}
	dependency := model[key]
	if !item.update {
		if dependency == nil {
			dependency = &nugetProjectDependency{name: strings.TrimSpace(item.name)}
			model[key] = dependency
		}
		dependency.candidates = appendUniqueNuGetProjectVersion(dependency.candidates, nugetProjectVersion{
			version: item.version, versionOverride: item.versionOverride,
		})
		return
	}
	if dependency == nil || (item.version == "" && item.versionOverride == "") {
		return
	}
	originals := append([]nugetProjectVersion{}, dependency.candidates...)
	updated := make([]nugetProjectVersion, 0, len(originals))
	for _, candidate := range originals {
		applyNuGetProjectVersionUpdate(&candidate, item)
		updated = appendUniqueNuGetProjectVersion(updated, candidate)
	}
	if item.condition != "" {
		updated = appendUniqueNuGetProjectVersions(originals, updated...)
	}
	dependency.candidates = updated
}

func applyNuGetProjectVersionUpdate(candidate *nugetProjectVersion, item nugetMSBuildItem) {
	if item.version != "" {
		candidate.version = item.version
	}
	if item.versionOverride != "" {
		candidate.versionOverride = item.versionOverride
	}
}

func appendUniqueNuGetProjectVersions(candidates []nugetProjectVersion, additions ...nugetProjectVersion) []nugetProjectVersion {
	for _, addition := range additions {
		candidates = appendUniqueNuGetProjectVersion(candidates, addition)
	}
	return candidates
}

func appendUniqueNuGetProjectVersion(candidates []nugetProjectVersion, addition nugetProjectVersion) []nugetProjectVersion {
	for _, candidate := range candidates {
		if candidate == addition {
			return candidates
		}
	}
	return append(candidates, addition)
}

func applyNuGetCentralItem(model nugetCentralModel, item nugetMSBuildItem, source string) {
	key := normalizeIdentityName(item.name)
	if key == "" {
		return
	}
	candidate := nugetVersionCandidate{
		name: strings.TrimSpace(item.name), version: strings.TrimSpace(item.version), source: source,
	}
	if !item.update {
		if item.condition != "" {
			unknown := candidate
			unknown.version = ""
			model[key] = appendUniqueNuGetCandidate(model[key], unknown)
		}
		model[key] = appendUniqueNuGetCandidate(model[key], candidate)
		return
	}
	if candidate.version == "" || len(model[key]) == 0 {
		return
	}
	originals := append([]nugetVersionCandidate{}, model[key]...)
	updated := make([]nugetVersionCandidate, 0, len(originals))
	for range originals {
		updated = appendUniqueNuGetCandidate(updated, candidate)
	}
	if item.condition != "" {
		for _, original := range originals {
			updated = appendUniqueNuGetCandidate(updated, original)
		}
	}
	model[key] = updated
}

func appendUniqueNuGetCandidate(candidates []nugetVersionCandidate, addition nugetVersionCandidate) []nugetVersionCandidate {
	for _, candidate := range candidates {
		if candidate == addition {
			return candidates
		}
	}
	return append(candidates, addition)
}

func appendNuGetProjectEvidence(packages map[string]*nugetPackageEvidence, projectSource string, project nugetProjectModel, central nugetCentralModel, lock nugetLockModel) {
	for _, key := range sortedNuGetProjectDependencyKeys(project) {
		dependency := project[key]
		pkg := nugetEvidencePackage(packages, key)
		for _, candidate := range lock[key] {
			pkg.resolved = append(pkg.resolved, newNuGetIdentityEvidence(candidate, identityStatusResolved))
		}
		candidates := effectiveNuGetProjectVersions(dependency, central[key], projectSource)
		appendNuGetDeclarationEvidence(pkg, dependency, candidates, projectSource, len(lock[key]) > 0)
	}
}

func appendNuGetDeclarationEvidence(pkg *nugetPackageEvidence, dependency *nugetProjectDependency, candidates []nugetVersionCandidate, projectSource string, resolvedByLock bool) {
	exact := make([]identityEvidence, 0, len(candidates))
	unknown := false
	unknownSource := ""
	for _, candidate := range candidates {
		version, ok := exactNuGetVersion(candidate.version)
		if !ok {
			unknown = true
			if unknownSource == "" {
				unknownSource = candidate.source
			}
			continue
		}
		candidate.version = version
		exact = append(exact, newNuGetIdentityEvidence(candidate, identityStatusDeclared))
	}
	if unknown || len(exact) == 0 {
		if !resolvedByLock {
			pkg.unknown = append(pkg.unknown, identityEvidence{
				Language: "dotnet", Ecosystem: "nuget", Name: dependency.name,
				Status: identityStatusUnknown, Source: firstNonBlankString(unknownSource, projectSource), Confidence: "high",
			})
		}
		if resolvedByLock {
			pkg.declared = append(pkg.declared, exact...)
		}
		return
	}
	pkg.declared = append(pkg.declared, exact...)
}

func effectiveNuGetProjectVersions(dependency *nugetProjectDependency, central []nugetVersionCandidate, projectSource string) []nugetVersionCandidate {
	candidates := make([]nugetVersionCandidate, 0, len(dependency.candidates)+len(central))
	for _, projectVersion := range dependency.candidates {
		version := strings.TrimSpace(projectVersion.versionOverride)
		if version == "" {
			version = strings.TrimSpace(projectVersion.version)
		}
		if version != "" {
			candidates = appendUniqueNuGetCandidate(candidates, nugetVersionCandidate{
				name: dependency.name, version: version, source: projectSource,
			})
			continue
		}
		if len(central) == 0 {
			candidates = appendUniqueNuGetCandidate(candidates, nugetVersionCandidate{name: dependency.name, source: projectSource})
			continue
		}
		for _, centralVersion := range central {
			centralVersion.name = dependency.name
			candidates = appendUniqueNuGetCandidate(candidates, centralVersion)
		}
	}
	return candidates
}

func newNuGetIdentityEvidence(candidate nugetVersionCandidate, status string) identityEvidence {
	return identityEvidence{
		Language: "dotnet", Ecosystem: "nuget", Name: candidate.name, Version: candidate.version,
		Status: status, Source: candidate.source, Confidence: "high",
	}
}

func addCollectedNuGetEvidence(index identityIndex, packages map[string]*nugetPackageEvidence) {
	for _, key := range sortedNuGetModelPaths(packages) {
		pkg := packages[key]
		if len(pkg.unknown) > 0 {
			addNuGetIdentityEvidence(index, pkg.unknown)
			continue
		}
		addNuGetIdentityEvidence(index, pkg.resolved)
		addNuGetIdentityEvidence(index, pkg.declared)
	}
}

func addNuGetIdentityEvidence(index identityIndex, evidence []identityEvidence) {
	for _, item := range evidence {
		addIdentityEvidence(index, item)
	}
}

func nugetEvidencePackage(packages map[string]*nugetPackageEvidence, key string) *nugetPackageEvidence {
	if packages[key] == nil {
		packages[key] = &nugetPackageEvidence{}
	}
	return packages[key]
}

func activeNuGetCentralModel(file *nugetCentralFile) nugetCentralModel {
	if file == nil || !file.enabled {
		return nil
	}
	return file.packages
}

func nugetCentralModelsByDir(models map[string]*nugetCentralFile) map[string]*nugetCentralFile {
	byDir := make(map[string]*nugetCentralFile, len(models))
	for path, model := range models {
		byDir[filepath.Clean(filepath.Dir(path))] = model
	}
	return byDir
}

func nearestNuGetCentralFile(repoPath, projectPath string, byDir map[string]*nugetCentralFile) *nugetCentralFile {
	root := filepath.Clean(repoPath)
	for dir := filepath.Clean(filepath.Dir(projectPath)); ; dir = filepath.Dir(dir) {
		if model, found := byDir[dir]; found {
			return model
		}
		if dir == root || dir == filepath.Dir(dir) {
			return nil
		}
	}
}

func countNuGetProjectsByDir(paths []string) map[string]int {
	counts := make(map[string]int)
	for _, path := range paths {
		counts[filepath.Clean(filepath.Dir(path))]++
	}
	return counts
}

func nugetLockForProject(projectPath string, projectCounts map[string]int, models map[string]nugetLockModel) nugetLockModel {
	dir := filepath.Clean(filepath.Dir(projectPath))
	if projectCounts[dir] != 1 {
		return nil
	}
	for _, path := range sortedNuGetModelPaths(models) {
		if filepath.Clean(filepath.Dir(path)) == dir && strings.EqualFold(filepath.Base(path), dotnetLockFileName) {
			return models[path]
		}
	}
	return nil
}

func isDotNetLockFileName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), dotnetLockFileName)
}

func exactNuGetVersion(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if len(value) < 3 || value[0] != '[' || value[len(value)-1] != ']' {
		return "", false
	}
	return normalizeNuGetVersion(strings.TrimSpace(value[1 : len(value)-1]))
}

func normalizeNuGetVersion(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, " \t\r\n") {
		return "", false
	}
	version, ok := removeNuGetBuildMetadata(value)
	if !ok {
		return "", false
	}
	core, prerelease, ok := splitNuGetPrerelease(version)
	if !ok {
		return "", false
	}
	parts, ok := normalizeNuGetCore(core)
	if !ok {
		return "", false
	}
	normalized := strings.Join(parts, ".")
	if prerelease != "" {
		normalized += "-" + prerelease
	}
	return normalized, true
}

func removeNuGetBuildMetadata(value string) (string, bool) {
	version, metadata, found := strings.Cut(value, "+")
	if !found {
		return version, true
	}
	return version, validNuGetVersionIdentifiers(metadata, false) && !strings.Contains(metadata, "+")
}

func splitNuGetPrerelease(value string) (string, string, bool) {
	core, prerelease, found := strings.Cut(value, "-")
	if !found {
		return core, "", true
	}
	return core, prerelease, validNuGetVersionIdentifiers(prerelease, true)
}

func normalizeNuGetCore(core string) ([]string, bool) {
	parts := strings.Split(core, ".")
	if len(parts) == 0 || len(parts) > 4 {
		return nil, false
	}
	for i, part := range parts {
		normalized, ok := normalizeNuGetNumericPart(part)
		if !ok {
			return nil, false
		}
		parts[i] = normalized
	}
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	if len(parts) == 4 && parts[3] == "0" {
		parts = parts[:3]
	}
	return parts, true
}

func normalizeNuGetNumericPart(value string) (string, bool) {
	if value == "" {
		return "", false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return "", false
		}
	}
	value = strings.TrimLeft(value, "0")
	if value == "" {
		return "0", true
	}
	n, err := strconv.ParseUint(value, 10, 31)
	if err != nil {
		return "", false
	}
	return strconv.FormatUint(n, 10), true
}

func validNuGetVersionIdentifiers(value string, rejectNumericLeadingZero bool) bool {
	if value == "" {
		return false
	}
	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" || (rejectNumericLeadingZero && hasNumericLeadingZero(identifier)) {
			return false
		}
		for _, char := range identifier {
			if !isNuGetIdentifierChar(char) {
				return false
			}
		}
	}
	return true
}

func hasNumericLeadingZero(value string) bool {
	if len(value) < 2 || value[0] != '0' {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func isNuGetIdentifierChar(char rune) bool {
	switch {
	case char >= '0' && char <= '9':
		return true
	case char >= 'A' && char <= 'Z':
		return true
	case char >= 'a' && char <= 'z':
		return true
	default:
		return char == '-'
	}
}

func validNuGetPackageName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, expression := range []string{"$(", "@(", "%("} {
		if strings.Contains(name, expression) {
			return false
		}
	}
	return true
}

func sortedNuGetModelPaths[T any](models map[string]T) []string {
	paths := make([]string, 0, len(models))
	for path := range models {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func sortedNuGetLockFrameworks(frameworks map[string]map[string]nugetLockDependency) []string {
	return sortedNuGetModelPaths(frameworks)
}

func sortedNuGetDependencyNames(dependencies map[string]nugetLockDependency) []string {
	return sortedNuGetModelPaths(dependencies)
}

func sortedNuGetProjectDependencyKeys(project nugetProjectModel) []string {
	return sortedNuGetModelPaths(project)
}
