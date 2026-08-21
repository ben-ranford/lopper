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
				return s.finishLine(), nil
			}
			return "", err
		}
		if modulePath, found, err := s.consumeByte(b); found || err != nil {
			return modulePath, err
		}
	}
}

func (s *goModModuleScanner) consumeByte(b byte) (string, bool, error) {
	if s.inLineComment {
		modulePath := s.consumeLineCommentByte(b)
		return modulePath, modulePath != "", nil
	}
	if s.inBlockComment {
		s.consumeBlockCommentByte(b)
		return "", false, nil
	}
	if b == '\n' {
		modulePath := s.finishLine()
		return modulePath, modulePath != "", nil
	}
	return s.consumeCodeByte(b)
}

func (s *goModModuleScanner) consumeLineCommentByte(b byte) string {
	if b != '\n' {
		return ""
	}
	modulePath := s.finishLine()
	s.inLineComment = false
	return modulePath
}

func (s *goModModuleScanner) consumeBlockCommentByte(b byte) {
	if s.blockCommentStar && b == '/' {
		s.inBlockComment = false
		s.blockCommentStar = false
		return
	}
	s.blockCommentStar = b == '*'
}

func (s *goModModuleScanner) consumeCodeByte(b byte) (string, bool, error) {
	if b == '/' {
		consumed, err := s.tryStartComment()
		if consumed || err != nil {
			return "", false, err
		}
	}
	s.appendLineByte(b)
	return "", false, nil
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
		s.lineInvalid = s.lineInvalid || s.line.Len() > 0
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

func (s *goModModuleScanner) finishLine() string {
	modulePath := parseScannedGoModLine(&s.line, s.lineInvalid || s.lineTooLarge)
	s.lineInvalid = false
	s.lineTooLarge = false
	s.lineLastSpace = false
	return modulePath
}

func isGoModDirectiveSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\r', '\f':
		return true
	default:
		return false
	}
}

func parseScannedGoModLine(line *strings.Builder, tooLarge bool) string {
	if tooLarge {
		line.Reset()
		return ""
	}
	lineText := line.String()
	line.Reset()
	if strings.TrimSpace(lineText) == "" {
		return ""
	}
	file, err := modfile.Parse(goModName, []byte(lineText+"\n"), nil)
	if err != nil || file.Module == nil {
		return ""
	}
	return file.Module.Mod.Path
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
