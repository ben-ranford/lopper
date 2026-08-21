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
	scanner := goModModuleScanner{
		buffered: bufio.NewReaderSize(reader, 32*1024),
	}
	return scanner.scan()
}

type goModModuleScanner struct {
	buffered         *bufio.Reader
	line             strings.Builder
	modulePath       string
	blockDirective   string
	invalid          bool
	lineInvalid      bool
	lineTooLarge     bool
	lineLastSpace    bool
	inBlockComment   bool
	blockCommentStar bool
	inLineComment    bool
}

func (s *goModModuleScanner) scan() (string, error) {
	for {
		b, err := s.buffered.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				if s.inBlockComment {
					s.invalid = true
				} else {
					s.finishLine()
				}
				if s.invalid || s.blockDirective != "" {
					return "", nil
				}
				return s.modulePath, nil
			}
			return "", err
		}
		if err := s.consumeByte(b); err != nil {
			return "", err
		}
	}
}

func (s *goModModuleScanner) consumeByte(b byte) error {
	if s.inLineComment {
		s.consumeLineCommentByte(b)
		return nil
	}
	if s.inBlockComment {
		s.consumeBlockCommentByte(b)
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

func (s *goModModuleScanner) consumeBlockCommentByte(b byte) {
	if s.blockCommentStar && b == '/' {
		s.inBlockComment = false
		s.blockCommentStar = false
		return
	}
	s.blockCommentStar = b == '*'
}

func (s *goModModuleScanner) consumeCodeByte(b byte) error {
	if b == '/' {
		consumed, err := s.tryStartComment()
		if consumed || err != nil {
			return err
		}
	}
	s.appendLineByte(b)
	return nil
}

func (s *goModModuleScanner) tryStartComment() (bool, error) {
	next, err := s.buffered.Peek(1)
	if err != nil {
		return false, nil
	}
	switch next[0] {
	case '/':
		_, err = s.buffered.ReadByte()
		s.inLineComment = err == nil
		return true, err
	case '*':
		_, err = s.buffered.ReadByte()
		s.inBlockComment = err == nil
		s.invalid = true
		s.lineInvalid = true
		s.blockCommentStar = false
		return true, err
	default:
		return false, nil
	}
}

func (s *goModModuleScanner) appendLineByte(b byte) {
	if isGoModDirectiveSpace(b) {
		if s.line.Len() == 0 || s.lineLastSpace {
			return
		}
		b = ' '
		s.lineLastSpace = true
	} else {
		s.lineLastSpace = false
	}
	if s.line.Len() >= 64*1024 {
		s.lineTooLarge = true
		return
	}
	s.line.WriteByte(b)
}

func (s *goModModuleScanner) finishLine() {
	s.consumeGoModDirectiveLine(&s.line, s.lineInvalid || s.lineTooLarge)
	s.lineInvalid = false
	s.lineTooLarge = false
	s.lineLastSpace = false
}

func isGoModDirectiveSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\r', '\f':
		return true
	default:
		return false
	}
}

func (s *goModModuleScanner) consumeGoModDirectiveLine(line *strings.Builder, tooLarge bool) {
	if tooLarge {
		line.Reset()
		s.invalid = true
		return
	}
	lineText := line.String()
	line.Reset()
	if strings.TrimSpace(lineText) == "" {
		return
	}
	if s.blockDirective != "" {
		s.consumeGoModBlockLine(lineText)
		return
	}
	if strings.HasSuffix(lineText, " (") {
		s.startGoModBlock(strings.TrimSuffix(lineText, " ("))
		return
	}
	if strings.HasPrefix(lineText, modulePrefix) {
		s.consumeGoModModuleLine(lineText)
		return
	}
	if !isValidGoModDirectiveLine(s.validationModulePath(), lineText) {
		s.invalid = true
	}
}

func (s *goModModuleScanner) consumeGoModBlockLine(lineText string) {
	if lineText == ")" {
		s.blockDirective = ""
		return
	}
	if strings.HasPrefix(lineText, modulePrefix) || !isValidGoModBlockLine(s.validationModulePath(), s.blockDirective, lineText) {
		s.invalid = true
	}
}

func (s *goModModuleScanner) startGoModBlock(directive string) {
	if !isValidGoModBlockDirective(s.validationModulePath(), directive) {
		s.invalid = true
		return
	}
	s.blockDirective = directive
}

func (s *goModModuleScanner) validationModulePath() string {
	if s.modulePath != "" {
		return s.modulePath
	}
	return "example.com/lopper-oversized-gomod-scan"
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

func isValidGoModDirectiveLine(modulePath, lineText string) bool {
	return parseSyntheticGoMod(modulePath, lineText+"\n")
}

func isValidGoModBlockDirective(modulePath, directive string) bool {
	return parseSyntheticGoMod(modulePath, directive+" (\n)\n")
}

func isValidGoModBlockLine(modulePath, directive, lineText string) bool {
	return parseSyntheticGoMod(modulePath, directive+" (\n"+lineText+"\n)\n")
}

func parseSyntheticGoMod(modulePath, body string) bool {
	_, err := modfile.Parse(goModName, []byte(modulePrefix+modulePath+"\n"+body), nil)
	return err == nil
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
	nestedModules, nestedDeps, nestedReplacements, oversizedModuleDirs, err := discoverNestedModulesFromDirs(repoPath, metadataNestedDirs)
	if err != nil {
		return err
	}
	info.LocalModulePaths = append(info.LocalModulePaths, nestedModules...)
	info.DeclaredDependencies = append(info.DeclaredDependencies, nestedDeps...)
	info.OversizedModuleDirs = normalizedDirSet(oversizedModuleDirs)
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
