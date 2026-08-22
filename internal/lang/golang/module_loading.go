package golang

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

var errNilModuleInfo = errors.New("module info is nil")

func loadGoModuleInfoWithOptions(repoPath string, options moduleLoadOptions) (moduleInfo, error) {
	info := moduleInfo{
		ReplacementImports:         make(map[string]string),
		VendoredImportDependencies: make(map[string]string),
		VendoredDependencies:       make(map[string]vendoredDependencyMetadata),
	}

	if err := loadRootModuleInfo(repoPath, &info); err != nil {
		return moduleInfo{}, err
	}
	if err := loadWorkspaceModules(repoPath, &info); err != nil {
		return moduleInfo{}, err
	}
	if err := loadNestedModules(repoPath, &info); err != nil {
		return moduleInfo{}, err
	}
	if err := loadVendoredMetadata(repoPath, options, &info); err != nil {
		return moduleInfo{}, err
	}

	if err := finalizeGoModuleInfo(&info); err != nil {
		return moduleInfo{}, err
	}
	return info, nil
}

func loadGoModuleInfo(repoPath string, opts ...moduleLoadOptions) (moduleInfo, error) {
	options := moduleLoadOptions{}
	if len(opts) > 0 {
		options = opts[0]
	}
	return loadGoModuleInfoWithOptions(repoPath, options)
}

func loadRootModuleInfo(repoPath string, info *moduleInfo) error {
	if info == nil {
		return errNilModuleInfo
	}

	goModPath := filepath.Join(repoPath, goModName)
	exists, err := manifestPathExists(goModPath)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	content, err := safeio.ReadFileUnderLimit(repoPath, goModPath, maxGoModBytes)
	if isPureGoModSizeLimit(err) {
		info.RootGoModTooLarge = true
		if modulePath, pathErr := readOversizedGoModModulePath(repoPath, goModPath); pathErr != nil {
			return pathErr
		} else if modulePath != "" {
			info.ModulePath = modulePath
			info.LocalModulePaths = append(info.LocalModulePaths, modulePath)
		}
		return nil
	}
	if err != nil {
		return err
	}

	modulePath, dependencies, replacements := parseGoMod(content)
	info.ModulePath = modulePath
	info.DeclaredDependencies = dependencies
	info.ReplacementImports = replacements
	info.LocalModulePaths = append(info.LocalModulePaths, modulePath)
	return nil
}

func readOversizedGoModModulePath(repoPath, goModPath string) (_ string, err error) {
	relPath, err := filepath.Rel(repoPath, goModPath)
	if err != nil {
		return "", err
	}
	root, err := safeio.OpenRootNoFollow(repoPath)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	file, err := safeio.OpenFileWithinRoot(root, relPath)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	return scanGoModModulePath(file)
}

func isPureGoModSizeLimit(err error) bool {
	return shared.IsPureSentinelError(err, safeio.ErrFileTooLarge)
}

func scanGoModModulePath(reader io.Reader) (string, error) {
	return scanGoModModulePathWithParser(reader, parseSyntheticGoMod)
}

func scanGoModModulePathWithParser(reader io.Reader, parseSynthetic func(string, string) bool) (string, error) {
	scanner := goModModuleScanner{
		buffered:       bufio.NewReaderSize(reader, 32*1024),
		maxBytes:       maxGoModModuleScanBytes,
		parseSynthetic: parseSynthetic,
	}
	return scanner.scan()
}

const (
	goModModuleDirectivePrefix    = "module "
	maxGoModModuleScanBytes       = maxGoModBytes + 512*1024
	maxGoModModuleLineBytes       = 64 * 1024
	maxLongQuotedGoModTargetBytes = 512 * 1024
	maxLongQuotedGoModSuffixBytes = 1024
	maxLongUnquotedGoModLineBytes = maxLongQuotedGoModTargetBytes + maxLongQuotedGoModSuffixBytes
)

var errGoModModuleScanTooLarge = errors.New("go.mod module scanner read limit exceeded")

type goModModuleScanner struct {
	buffered            *bufio.Reader
	bytesRead           int
	maxBytes            int
	line                strings.Builder
	syntheticBody       strings.Builder
	modulePath          string
	blockDirective      string
	seenSingletons      map[string]struct{}
	parseSynthetic      func(string, string) bool
	invalid             bool
	lineInvalid         bool
	lineTooLarge        bool
	lineTooLargeInQuote bool
	lineQuoteClosed     bool
	longQuotedQuote     byte
	lineLastSpace       bool
	longQuotedTarget    strings.Builder
	longQuotedSuffix    strings.Builder
	longUnquotedStart   int
	longUnquotedLine    strings.Builder
	inQuotedString      bool
	quoteByte           byte
	quoteEscaped        bool
	inLineComment       bool
}

func (s *goModModuleScanner) scan() (string, error) {
	for {
		b, err := s.readByte()
		if err != nil {
			return s.finishScanWithReadError(err)
		}
		if err := s.consumeByte(b); err != nil {
			return "", suppressGoModModuleScanLimit(err)
		}
		if s.invalid {
			return "", nil
		}
	}
}

func (s *goModModuleScanner) finishScanWithReadError(err error) (string, error) {
	if errors.Is(err, io.EOF) {
		return s.finishScanAtEOF(), nil
	}
	return "", suppressGoModModuleScanLimit(err)
}

func (s *goModModuleScanner) finishScanAtEOF() string {
	s.finishLine()
	if s.invalid || s.blockDirective != "" || s.modulePath == "" {
		return ""
	}
	if !s.isSyntheticBodyValid() {
		return ""
	}
	return s.modulePath
}

func suppressGoModModuleScanLimit(err error) error {
	if errors.Is(err, errGoModModuleScanTooLarge) {
		return nil
	}
	return err
}

func (s *goModModuleScanner) readByte() (byte, error) {
	b, err := s.buffered.ReadByte()
	if err != nil {
		return 0, err
	}
	s.bytesRead++
	if s.maxBytes > 0 && s.bytesRead > s.maxBytes {
		return 0, errGoModModuleScanTooLarge
	}
	return b, nil
}

func (s *goModModuleScanner) consumeByte(b byte) error {
	if s.inLineComment {
		s.consumeLineCommentByte(b)
		return nil
	}
	if s.inQuotedString {
		s.consumeQuotedStringByte(b)
		return nil
	}
	if b == '\n' {
		s.finishLine()
		return nil
	}
	return s.consumeCodeByte(b)
}

func (s *goModModuleScanner) consumeLineCommentByte(b byte) {
	if b != '\n' {
		return
	}
	s.finishLine()
	s.inLineComment = false
}

func (s *goModModuleScanner) consumeQuotedStringByte(b byte) {
	if b == '\n' {
		s.lineInvalid = true
		s.inQuotedString = false
		s.quoteByte = 0
		s.quoteEscaped = false
		s.finishLine()
		return
	}
	if s.quoteByte == '`' {
		if b == '`' {
			if s.lineTooLargeInQuote {
				s.finishLongQuotedTarget()
				return
			}
			s.appendRawLineByte(b)
			s.inQuotedString = false
			s.quoteByte = 0
		}
		if s.inQuotedString {
			s.appendRawLineByte(b)
		}
		return
	}
	if s.quoteEscaped {
		s.appendRawLineByte(b)
		s.quoteEscaped = false
		return
	}
	if b == '\\' {
		s.appendRawLineByte(b)
		s.quoteEscaped = true
		return
	}
	if b == s.quoteByte {
		if s.lineTooLargeInQuote {
			s.finishLongQuotedTarget()
			return
		}
		s.appendRawLineByte(b)
		s.inQuotedString = false
		s.quoteByte = 0
		s.lineQuoteClosed = true
		return
	}
	s.appendRawLineByte(b)
}

func (s *goModModuleScanner) consumeCodeByte(b byte) error {
	if s.acceptingLongQuotedLineSuffix() {
		return s.consumeLongQuotedLineSuffix(b)
	}
	if b == '"' || b == '`' {
		s.startQuotedString(b)
		return nil
	}
	if b == '/' {
		consumed, err := s.tryStartComment()
		if consumed || err != nil {
			return err
		}
	}
	s.appendLineByte(b)
	return nil
}

func (s *goModModuleScanner) acceptingLongQuotedLineSuffix() bool {
	return s.lineTooLarge && s.lineTooLargeInQuote && s.lineQuoteClosed
}

func (s *goModModuleScanner) consumeLongQuotedLineSuffix(b byte) error {
	if b == '/' {
		consumed, err := s.tryStartComment()
		if consumed || err != nil {
			return err
		}
	}
	if s.longQuotedSuffix.Len() >= maxLongQuotedGoModSuffixBytes {
		s.lineInvalid = true
		return nil
	}
	s.longQuotedSuffix.WriteByte(b)
	return nil
}

func (s *goModModuleScanner) startQuotedString(quote byte) {
	s.inQuotedString = true
	s.quoteByte = quote
	s.quoteEscaped = false
	s.appendRawLineByte(quote)
}

func (s *goModModuleScanner) tryStartComment() (bool, error) {
	next, err := s.buffered.Peek(1)
	if err != nil {
		return false, nil
	}
	switch next[0] {
	case '/':
		_, err = s.readByte()
		s.inLineComment = err == nil
		return true, err
	case '*':
		_, err = s.readByte()
		// Block comments make the bounded fallback scan untrustworthy. The
		// caller returns as soon as it observes invalid, so do not consume the
		// rest of the comment.
		s.invalid = true
		s.lineInvalid = true
		return true, err
	default:
		return false, nil
	}
}

func (s *goModModuleScanner) appendLineByte(b byte) {
	if b == '\f' {
		s.lineInvalid = true
		return
	}
	if isGoModDirectiveSpace(b) {
		if s.line.Len() == 0 || s.lineLastSpace {
			return
		}
		b = ' '
		s.lineLastSpace = true
	} else {
		s.lineLastSpace = false
	}
	if s.lineTooLarge {
		s.appendLongUnquotedLineByte(b)
		return
	}
	if s.line.Len() >= maxGoModModuleLineBytes {
		s.lineTooLarge = true
		s.startLongUnquotedLine()
		s.appendLongUnquotedLineByte(b)
		return
	}
	s.line.WriteByte(b)
}

func (s *goModModuleScanner) appendRawLineByte(b byte) {
	s.lineLastSpace = false
	if s.line.Len() >= maxGoModModuleLineBytes {
		s.lineTooLarge = true
		s.lineTooLargeInQuote = true
		s.startLongQuotedTarget()
		s.appendLongQuotedTargetByte(b)
		return
	}
	s.line.WriteByte(b)
}

func (s *goModModuleScanner) startLongQuotedTarget() {
	if s.longQuotedTarget.Len() != 0 || s.lineInvalid {
		return
	}
	s.longQuotedQuote = s.quoteByte
	quoteIndex := strings.IndexByte(s.line.String(), s.quoteByte)
	if quoteIndex < 0 {
		s.lineInvalid = true
		return
	}
	s.appendLongQuotedTargetString(s.line.String()[quoteIndex+1:])
}

func (s *goModModuleScanner) appendLongQuotedTargetByte(b byte) {
	if s.longQuotedTarget.Len() >= maxLongQuotedGoModTargetBytes {
		s.lineInvalid = true
		return
	}
	s.longQuotedTarget.WriteByte(b)
}

func (s *goModModuleScanner) appendLongQuotedTargetString(value string) {
	if len(value) > maxLongQuotedGoModTargetBytes-s.longQuotedTarget.Len() {
		s.lineInvalid = true
		return
	}
	s.longQuotedTarget.WriteString(value)
}

func (s *goModModuleScanner) startLongUnquotedLine() {
	if s.longUnquotedLine.Len() != 0 || s.lineInvalid {
		return
	}
	lineText := s.line.String()
	s.longUnquotedStart = strings.LastIndexByte(lineText, ' ') + 1
	if s.longUnquotedStart == 0 && s.blockDirective == "" {
		s.lineInvalid = true
		return
	}
	s.appendLongUnquotedLineString(lineText[s.longUnquotedStart:])
}

func (s *goModModuleScanner) appendLongUnquotedLineByte(b byte) {
	if s.longUnquotedLine.Len() >= maxLongUnquotedGoModLineBytes {
		s.lineInvalid = true
		return
	}
	s.longUnquotedLine.WriteByte(b)
}

func (s *goModModuleScanner) appendLongUnquotedLineString(value string) {
	if len(value) > maxLongUnquotedGoModLineBytes-s.longUnquotedLine.Len() {
		s.lineInvalid = true
		return
	}
	s.longUnquotedLine.WriteString(value)
}

func (s *goModModuleScanner) finishLongQuotedTarget() {
	s.inQuotedString = false
	s.quoteByte = 0
	s.quoteEscaped = false
	s.lineQuoteClosed = true
}

func (s *goModModuleScanner) finishLine() {
	s.consumeGoModDirectiveLine(&s.line, s.lineInvalid, s.lineTooLarge, s.lineTooLargeInQuote, s.lineQuoteClosed)
	s.lineInvalid = false
	s.lineTooLarge = false
	s.lineTooLargeInQuote = false
	s.lineQuoteClosed = false
	s.lineLastSpace = false
	s.longQuotedQuote = 0
	s.longQuotedTarget.Reset()
	s.longQuotedSuffix.Reset()
	s.longUnquotedStart = 0
	s.longUnquotedLine.Reset()
}

func isGoModDirectiveSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\r':
		return true
	default:
		return false
	}
}

func trimGoModDirectiveSpace(lineText string) string {
	return strings.Trim(lineText, goModDirectiveSpaceCutset)
}

const goModDirectiveSpaceCutset = " \t\r"

func (s *goModModuleScanner) consumeGoModDirectiveLine(line *strings.Builder, invalid, tooLarge, tooLargeInQuote, quoteClosed bool) {
	if invalid {
		line.Reset()
		s.invalid = true
		return
	}
	if tooLarge {
		s.consumeTooLargeGoModDirectiveLine(line.String(), tooLargeInQuote, quoteClosed)
		line.Reset()
		return
	}
	lineText := trimGoModDirectiveSpace(line.String())
	line.Reset()
	if lineText == "" {
		return
	}
	if s.blockDirective != "" {
		s.consumeGoModBlockLine(lineText)
		return
	}
	if directive, ok := goModInlineEmptyBlockDirective(lineText); ok {
		s.consumeGoModInlineEmptyBlock(directive, lineText)
		return
	}
	if directive, ok := goModBlockDirective(lineText); ok {
		s.startGoModBlock(directive, lineText)
		return
	}
	if isGoModModuleDirectiveLine(lineText) {
		s.consumeGoModModuleLine(lineText)
		return
	}
	if !s.consumeGoModSingletonLine(lineText) {
		return
	}
	s.appendSyntheticGoModLine(lineText)
}

func (s *goModModuleScanner) consumeTooLargeGoModDirectiveLine(lineText string, tooLargeInQuote, quoteClosed bool) {
	lineText = trimGoModDirectiveSpace(lineText)
	if lineText == "" || s.blockDirective == "module" {
		s.invalid = true
		return
	}
	if s.blockDirective != "" {
		if !s.acceptLongGoModDirectiveLine(s.blockDirective, lineText, tooLargeInQuote, quoteClosed) {
			s.invalid = true
		}
		return
	}
	if s.acceptLongGoModDirectiveLine(firstToken(lineText), lineText, tooLargeInQuote, quoteClosed) {
		return
	}
	s.invalid = true
}

func isValidLongGoModDirective(directive string, quoted bool) bool {
	switch directive {
	case "require", "exclude", "tool", "godebug", "ignore":
		return true
	case "replace":
		return quoted
	default:
		return false
	}
}

func (s *goModModuleScanner) acceptLongGoModDirectiveLine(directive, lineText string, tooLargeInQuote, quoteClosed bool) bool {
	if !isValidLongGoModDirective(directive, tooLargeInQuote) {
		return false
	}
	if tooLargeInQuote {
		if !quoteClosed {
			return false
		}
		lineText, ok := s.longQuotedGoModLine(lineText)
		if !ok {
			return false
		}
		s.appendSyntheticGoModLine(lineText)
		return !s.invalid
	}
	lineText, ok := s.longUnquotedGoModLine(lineText)
	if !ok {
		return false
	}
	s.appendSyntheticGoModLine(lineText)
	return !s.invalid
}

func (s *goModModuleScanner) longQuotedGoModLine(lineText string) (string, bool) {
	quote := s.longQuotedQuote
	if quote == 0 {
		return "", false
	}
	quoteIndex := strings.IndexByte(lineText, quote)
	if quoteIndex < 0 || s.longQuotedTarget.Len() == 0 {
		return "", false
	}
	return lineText[:quoteIndex] + string(quote) + s.longQuotedTarget.String() + string(quote) + s.longQuotedSuffix.String(), true
}

func (s *goModModuleScanner) longUnquotedGoModLine(lineText string) (string, bool) {
	if s.longUnquotedStart < 0 || s.longUnquotedStart > len(lineText) || s.longUnquotedLine.Len() == 0 {
		return "", false
	}
	return lineText[:s.longUnquotedStart] + s.longUnquotedLine.String(), true
}

func goModBlockDirective(lineText string) (string, bool) {
	if strings.HasSuffix(lineText, " (") {
		return strings.TrimSuffix(lineText, " ("), true
	}
	if strings.HasSuffix(lineText, "(") {
		return strings.TrimSuffix(lineText, "("), true
	}
	return "", false
}

func goModInlineEmptyBlockDirective(lineText string) (string, bool) {
	directive := firstToken(lineText)
	rest := trimGoModDirectiveSpace(strings.TrimPrefix(lineText, directive))
	return directive, rest == "()" || rest == "( )"
}

func isGoModModuleDirectiveLine(lineText string) bool {
	return firstToken(lineText) == "module"
}

func (s *goModModuleScanner) consumeGoModInlineEmptyBlock(directive, lineText string) {
	if directive == "module" {
		return
	}
	if !isValidGoModBlockDirective(directive) {
		s.invalid = true
		return
	}
	s.appendSyntheticGoModLine(lineText)
}

func (s *goModModuleScanner) consumeGoModSingletonLine(lineText string) bool {
	directive, ok := goModSingletonDirective(lineText)
	if !ok {
		return true
	}
	if s.seenSingletons == nil {
		s.seenSingletons = make(map[string]struct{})
	}
	if _, exists := s.seenSingletons[directive]; exists {
		s.invalid = true
		return false
	}
	s.seenSingletons[directive] = struct{}{}
	return true
}

func goModSingletonDirective(lineText string) (string, bool) {
	directive := firstToken(lineText)
	switch directive {
	case "go", "toolchain":
		return directive, true
	default:
		return "", false
	}
}

func (s *goModModuleScanner) consumeGoModBlockLine(lineText string) {
	if lineText == ")" {
		if s.blockDirective != "module" {
			s.appendSyntheticGoModLine(lineText)
		}
		s.blockDirective = ""
		return
	}
	if s.blockDirective == "module" {
		s.consumeGoModModuleLine(goModModuleDirectivePrefix + lineText)
		return
	}
	if !isValidGoModBlockLine(s.modulePath, s.blockDirective, lineText) {
		s.invalid = true
		return
	}
	s.appendSyntheticGoModLine(lineText)
}

func (s *goModModuleScanner) startGoModBlock(directive, lineText string) {
	if directive == "module" {
		s.blockDirective = directive
		return
	}
	if !isValidGoModBlockDirective(directive) {
		s.invalid = true
		return
	}
	s.blockDirective = directive
	s.appendSyntheticGoModLine(lineText)
}

func (s *goModModuleScanner) consumeGoModModuleLine(lineText string) {
	if s.modulePath != "" {
		s.invalid = true
		return
	}
	file, err := modfile.Parse(goModName, []byte(lineText+"\n"), nil)
	if err != nil || file.Module == nil {
		s.invalid = true
		return
	}
	s.modulePath = file.Module.Mod.Path
}

func (s *goModModuleScanner) appendSyntheticGoModLine(lineText string) {
	if s.syntheticBody.Len()+len(lineText)+1 > maxGoModModuleScanBytes {
		s.invalid = true
		return
	}
	s.syntheticBody.WriteString(lineText)
	s.syntheticBody.WriteByte('\n')
}

func (s *goModModuleScanner) isSyntheticBodyValid() bool {
	if s.syntheticBody.Len() == 0 {
		return true
	}
	return s.parseSynthetic(s.modulePath, s.syntheticBody.String())
}

func isValidGoModBlockDirective(directive string) bool {
	switch directive {
	case "require", "exclude", "replace", "retract", "tool", "godebug", "ignore":
		return true
	default:
		return false
	}
}

func isValidGoModBlockLine(modulePath, directive, lineText string) bool {
	switch directive {
	case "require", "exclude", "replace", "tool", "godebug", "ignore":
		return true
	case "retract":
		return modulePath != "" || firstToken(lineText) != ""
	default:
		return false
	}
}

func parseSyntheticGoMod(modulePath, body string) bool {
	file, err := modfile.Parse(goModName, []byte(goModModuleDirectivePrefix+modulePath+"\n"+body), nil)
	return err == nil && hasValidRetracts(file)
}

func hasValidRetracts(file *modfile.File) bool {
	if file == nil || file.Module == nil || len(file.Retract) == 0 {
		return true
	}
	_, pathMajor, ok := module.SplitPathVersion(file.Module.Mod.Path)
	if !ok {
		return false
	}
	for _, retraction := range file.Retract {
		if !isValidRetractVersionMajor(retraction.Low, pathMajor) || !isValidRetractVersionMajor(retraction.High, pathMajor) {
			return false
		}
	}
	return true
}

func isValidRetractVersionMajor(version, pathMajor string) bool {
	return version == "" || module.CheckPathMajor(version, pathMajor) == nil
}

func loadWorkspaceModules(repoPath string, info *moduleInfo) error {
	if info == nil {
		return errNilModuleInfo
	}

	workModules, err := loadGoWorkLocalModules(repoPath)
	if err != nil {
		return err
	}
	info.LocalModulePaths = append(info.LocalModulePaths, workModules...)
	return nil
}

func loadNestedModules(repoPath string, info *moduleInfo) error {
	if info == nil {
		return errNilModuleInfo
	}

	workspaceModuleDirs, err := workspaceRootModuleDirs(repoPath, *info)
	if err != nil {
		return err
	}
	info.WorkspaceModuleExclusions = normalizedDirSet(workspaceModuleDirs)

	scanNestedDirs, err := nestedModuleDirs(repoPath, workspaceModuleDirs)
	if err != nil {
		return err
	}
	info.NestedModuleDirs = normalizedDirSet(scanNestedDirs)

	metadataNestedDirs := unionDirSet(info.NestedModuleDirs, info.WorkspaceModuleExclusions)
	nestedModules, nestedDeps, nestedReplacements, oversizedModuleDirs, trustedModuleDirs, err := discoverNestedModulesFromDirs(repoPath, metadataNestedDirs)
	if err != nil {
		return err
	}
	info.LocalModulePaths = append(info.LocalModulePaths, nestedModules...)
	info.DeclaredDependencies = append(info.DeclaredDependencies, nestedDeps...)
	info.OversizedModuleDirs = normalizedDirSet(oversizedModuleDirs)
	info.TrustedModuleDirs = normalizedDirSet(trustedModuleDirs)
	for replacementImport, dependency := range nestedReplacements {
		if _, ok := info.ReplacementImports[replacementImport]; !ok {
			info.ReplacementImports[replacementImport] = dependency
		}
	}
	return nil
}

func loadVendoredMetadata(repoPath string, options moduleLoadOptions, info *moduleInfo) error {
	if info == nil || !options.EnableVendoredProvenance {
		return nil
	}
	metadata, err := loadVendoredModuleMetadata(repoPath)
	if err != nil {
		return err
	}
	info.VendoredProvenanceEnabled = metadata.ManifestFound
	info.VendoringWarnings = append(info.VendoringWarnings, metadata.Warnings...)
	if !metadata.ManifestFound {
		return nil
	}
	for importPrefix, dependency := range metadata.ImportToDependency {
		if _, ok := info.VendoredImportDependencies[importPrefix]; !ok {
			info.VendoredImportDependencies[importPrefix] = normalizeDependencyID(dependency)
		}
	}
	for dependency, item := range metadata.Dependencies {
		info.VendoredDependencies[dependency] = item
	}
	return nil
}

func finalizeGoModuleInfo(info *moduleInfo) error {
	if info == nil {
		return errNilModuleInfo
	}

	info.LocalModulePaths = uniqueStrings(info.LocalModulePaths)
	info.DeclaredDependencies = uniqueStrings(info.DeclaredDependencies)
	sort.Strings(info.LocalModulePaths)
	sort.Strings(info.DeclaredDependencies)
	info.VendoringWarnings = append(info.VendoringWarnings, oversizedModuleWarnings(info.OversizedModuleDirs)...)
	sort.Strings(info.VendoringWarnings)
	return nil
}

func oversizedModuleWarnings(dirs map[string]struct{}) []string {
	if len(dirs) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("skipped Go source dependency attribution for %d module(s) because go.mod exceeds %d bytes", len(dirs), maxGoModBytes)}
}
