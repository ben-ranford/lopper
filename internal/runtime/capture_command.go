package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"unicode"

	"github.com/ben-ranford/lopper/internal/safeio"
)

const runtimeBinDirsEnvKey = "LOPPER_RUNTIME_BIN_DIRS"

const PythonRunnerProfilesFeature = "python-runner-profiles"

type CommandOptions struct {
	PythonRunnerProfiles bool
}

type resolvedRuntimeExecutable struct {
	path                      string
	selectedLauncherRoot      string
	canonicalInstallationRoot string
	source                    *runtimeExecutableSource
}

var runtimeOS = goruntime.GOOS
var runtimeWindowsExecutableRoots = platformRuntimeWindowsExecutableRoots

func buildRuntimeCommand(ctx context.Context, command string, requestedOptions ...CommandOptions) (*runtimeCommand, error) {
	options := resolveCommandOptions(requestedOptions)
	fields, err := parseValidatedCommand(command, options)
	if err != nil {
		return nil, err
	}

	executable := fields[0]
	args := fields[1:]
	if err := validateRuntimeExecutable(executable); err != nil {
		return nil, err
	}
	searchDirs := runtimeSearchDirs()
	resolution, err := resolvePinnedRuntimeExecutable(executable, searchDirs)
	if err != nil {
		return nil, err
	}
	return newTrustedRuntimeCommand(ctx, executable, &resolution, args)
}

func ValidateCommand(command string, requestedOptions ...CommandOptions) error {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	_, err := parseValidatedCommand(command, resolveCommandOptions(requestedOptions))
	return err
}

func resolveCommandOptions(options []CommandOptions) CommandOptions {
	if len(options) == 0 {
		return CommandOptions{}
	}
	return options[0]
}

type runtimeCommandParser struct {
	fields        []string
	current       strings.Builder
	inSingleQuote bool
	inDoubleQuote bool
	escaped       bool
	sawToken      bool
}

func parseRuntimeCommand(command string) ([]string, error) {
	if isWindowsRuntime() {
		return parseWindowsRuntimeCommand(command)
	}
	return parseUnixRuntimeCommand(command)
}

func parseUnixRuntimeCommand(command string) ([]string, error) {
	var parser runtimeCommandParser
	for _, ch := range command {
		parser.consume(ch)
	}

	if parser.escaped {
		return nil, fmt.Errorf("runtime test command ends with an unfinished escape sequence")
	}
	if parser.inSingleQuote || parser.inDoubleQuote {
		return nil, fmt.Errorf("runtime test command contains an unterminated quote")
	}
	parser.flush()

	return parser.fields, nil
}

func parseValidatedCommand(command string, options CommandOptions) ([]string, error) {
	fields, err := parseRuntimeCommand(command)
	if err != nil {
		return nil, err
	}
	if err := validateRuntimeCommand(command, fields, options); err != nil {
		return nil, err
	}
	return fields, nil
}

func validateRuntimeCommand(command string, fields []string, options CommandOptions) error {
	if containsUnsafeRuntimeCommandSyntax(command) {
		return fmt.Errorf("runtime test command uses indirect command execution operators; pass a direct executable and arguments instead")
	}

	if len(fields) == 0 {
		return fmt.Errorf("runtime test command is required")
	}
	if isInlineEnvironmentAssignment(fields[0]) {
		return fmt.Errorf("runtime test command uses inline environment assignment %q; configure the environment separately", fields[0])
	}

	return rejectRuntimeCommandUnsafeFlags(fields[0], fields[1:], options)
}

func isInlineEnvironmentAssignment(token string) bool {
	name, _, found := strings.Cut(token, "=")
	if !found || name == "" {
		return false
	}
	for index, ch := range name {
		if ch == '_' || unicode.IsLetter(ch) || (index > 0 && unicode.IsDigit(ch)) {
			continue
		}
		return false
	}
	return true
}

func containsUnsafeRuntimeCommandSyntax(command string) bool {
	parser := runtimeCommandUnsafeSyntaxParser{windows: isWindowsRuntime()}
	runes := []rune(command)

	for i, ch := range runes {
		if parser.consume(ch, nextRuntimeCommandRune(runes, i)) {
			return true
		}
	}

	return false
}

type runtimeCommandUnsafeSyntaxParser struct {
	inSingleQuote bool
	inDoubleQuote bool
	escaped       bool
	windows       bool
}

func (p *runtimeCommandUnsafeSyntaxParser) consume(ch, next rune) bool {
	if p.escaped {
		p.escaped = false
		return false
	}

	switch ch {
	case '\\':
		if p.windows {
			return false
		}
		if p.inSingleQuote {
			return false
		}
		p.escaped = true
		return false
	case '\'':
		if p.windows {
			return false
		}
		if p.inDoubleQuote {
			return false
		}
		p.inSingleQuote = !p.inSingleQuote
		return false
	case '"':
		if p.inSingleQuote {
			return false
		}
		p.inDoubleQuote = !p.inDoubleQuote
		return false
	case '$':
		return p.isSubshellStart(next)
	default:
		return p.isUnsafeOperator(ch)
	}
}

func (p *runtimeCommandUnsafeSyntaxParser) isSubshellStart(next rune) bool {
	if p.inSingleQuote {
		return false
	}
	return next == '('
}

func (p *runtimeCommandUnsafeSyntaxParser) isUnsafeOperator(ch rune) bool {
	if p.inSingleQuote {
		return false
	}

	switch ch {
	case '|', '&', ';', '>', '<', '`', '\n', '\r':
		return true
	default:
		return false
	}
}

func nextRuntimeCommandRune(runes []rune, index int) rune {
	if index+1 >= len(runes) {
		return 0
	}
	return runes[index+1]
}

func rejectRuntimeCommandUnsafeFlags(executable string, args []string, options CommandOptions) error {
	if isPythonRuntimeExecutable(executable) || executable == "uv" {
		return validatePythonRuntimeProfile(executable, args, options)
	}

	flags, ok := runtimeCommandUnsafeFlags[executable]
	if !ok {
		return nil
	}

	for _, arg := range args {
		for _, flag := range flags {
			if arg == flag || strings.HasPrefix(arg, flag+"=") {
				return fmt.Errorf("runtime test command uses unsafe executable flag %q for %q", arg, executable)
			}
		}
	}
	return nil
}

func IsPythonTestCommand(command string, requestedOptions ...CommandOptions) bool {
	fields, err := parseRuntimeCommand(command)
	if err != nil || len(fields) == 0 {
		return false
	}
	if fields[0] == "pytest" {
		return true
	}
	return validatePythonRuntimeProfile(fields[0], fields[1:], resolveCommandOptions(requestedOptions)) == nil
}

func isPythonRuntimeExecutable(executable string) bool {
	return executable == "python" || executable == "python3"
}

var runtimeCommandUnsafeFlags = map[string][]string{
	"node": {"-e", "--eval", "-p", "--print"},
	"bun":  {"-e", "--eval", "-p", "--print"},
	"deno": {"eval"},
	"npm":  {"exec"},
	"pnpm": {"exec"},
	"yarn": {"dlx"},
}

func (p *runtimeCommandParser) consume(ch rune) {
	switch {
	case p.escaped:
		p.write(ch)
		p.escaped = false
	case ch == '\\':
		if p.inSingleQuote {
			p.write(ch)
			return
		}
		p.escaped = true
		p.sawToken = true
	case ch == '\'':
		p.toggleQuote(&p.inSingleQuote, p.inDoubleQuote, ch)
	case ch == '"':
		p.toggleQuote(&p.inDoubleQuote, p.inSingleQuote, ch)
	case unicode.IsSpace(ch):
		if p.inSingleQuote || p.inDoubleQuote {
			p.write(ch)
			return
		}
		p.flush()
	default:
		p.write(ch)
	}
}

func (p *runtimeCommandParser) toggleQuote(active *bool, otherActive bool, ch rune) {
	if otherActive {
		p.write(ch)
		return
	}
	*active = !*active
	p.sawToken = true
}

func (p *runtimeCommandParser) write(ch rune) {
	p.current.WriteRune(ch)
	p.sawToken = true
}

func (p *runtimeCommandParser) flush() {
	if p.current.Len() == 0 && !p.sawToken {
		return
	}
	p.fields = append(p.fields, p.current.String())
	p.current.Reset()
	p.sawToken = false
}

func resolveRuntimeExecutablePath(executable string, searchDirs []string) (string, error) {
	resolution, err := resolveRuntimeExecutable(executable, searchDirs)
	if err != nil {
		return "", err
	}
	return resolution.path, nil
}

func resolveRuntimeExecutable(executable string, searchDirs []string) (resolvedRuntimeExecutable, error) {
	resolution, err := resolvePinnedRuntimeExecutable(executable, searchDirs)
	if err != nil {
		return resolvedRuntimeExecutable{}, err
	}
	if err := resolution.closeSource(); err != nil {
		return resolvedRuntimeExecutable{}, fmt.Errorf("close trusted runtime executable %q: %w", resolution.path, err)
	}
	return resolution, nil
}

func resolvePinnedRuntimeExecutable(executable string, searchDirs []string) (resolvedRuntimeExecutable, error) {
	for _, dir := range searchDirs {
		if resolution, ok := resolvePinnedRuntimeExecutableInDir(executable, dir); ok {
			return resolution, nil
		}
	}

	return resolvedRuntimeExecutable{}, fmt.Errorf("runtime test executable %q not found in trusted runtime directories", executable)
}

func resolveRuntimeExecutablePathInDir(executable, dir string) (path string, ok bool) {
	resolution, ok := resolveRuntimeExecutableInDir(executable, dir)
	if !ok {
		return "", false
	}
	return resolution.path, true
}

func resolveRuntimeExecutableInDir(executable, dir string) (resolvedRuntimeExecutable, bool) {
	resolution, ok := resolvePinnedRuntimeExecutableInDir(executable, dir)
	if !ok || resolution.closeSource() != nil {
		return resolvedRuntimeExecutable{}, false
	}
	return resolution, true
}

func resolvePinnedRuntimeExecutableInDir(executable, dir string) (resolvedRuntimeExecutable, bool) {
	for _, candidate := range runtimeExecutableCandidates(executable, dir) {
		source, trusted := openTrustedRuntimeExecutableCandidate(executable, candidate)
		if !trusted {
			continue
		}
		resolvedPath := source.path
		installationRoot := ""
		if runtimeNodeCLIScriptTarget(executable, resolvedPath) {
			var ok bool
			installationRoot, ok = runtimeNodeCLIInstallationRoot(executable, candidate, resolvedPath)
			if !ok {
				if err := source.Close(); err != nil {
					return resolvedRuntimeExecutable{}, false
				}
				continue
			}
		}
		return resolvedRuntimeExecutable{
			path:                      resolvedPath,
			selectedLauncherRoot:      filepath.Clean(dir),
			canonicalInstallationRoot: installationRoot,
			source:                    source,
		}, true
	}
	return resolvedRuntimeExecutable{}, false
}

func openTrustedRuntimeExecutableCandidate(executable, candidate string) (*runtimeExecutableSource, bool) {
	if !validateTrustedRuntimeAncestorChain(filepath.Dir(candidate), true) {
		return nil, false
	}
	candidateInfo, err := os.Lstat(candidate)
	if err != nil || candidateInfo.IsDir() {
		return nil, false
	}
	resolvedPath, err := filepath.EvalSymlinks(candidate)
	if err != nil || !filepath.IsAbs(resolvedPath) {
		return nil, false
	}
	canonicalName := filepath.Base(resolvedPath)
	if !runtimeExecutableCanonicalTargetAllowed(executable, canonicalName) {
		return nil, false
	}
	if !validateTrustedRuntimeAncestorChain(filepath.Dir(resolvedPath), false) {
		return nil, false
	}
	source, err := openTrustedRuntimeExecutableSource(resolvedPath)
	if err != nil {
		return nil, false
	}
	if candidateInfo.Mode()&os.ModeSymlink == 0 && !os.SameFile(candidateInfo, source.info) {
		if err := source.Close(); err != nil {
			return nil, false
		}
		return nil, false
	}
	return source, true
}

func validateTrustedRuntimeAncestorChain(path string, allowSymlinkComponents bool) bool {
	cleanPath := filepath.Clean(path)
	if !filepath.IsAbs(cleanPath) {
		return false
	}
	if isWindowsRuntime() {
		return validateTrustedRuntimeWindowsAncestorChain(cleanPath, allowSymlinkComponents)
	}
	current := filepath.VolumeName(cleanPath)
	if current == "" {
		current = string(os.PathSeparator)
	} else {
		current += string(os.PathSeparator)
	}
	if !trustedRuntimeAncestorPath(current, allowSymlinkComponents) {
		return false
	}
	if filepath.Clean(current) == cleanPath {
		return true
	}
	remainder := strings.TrimPrefix(cleanPath, current)
	for _, part := range strings.Split(remainder, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		if !trustedRuntimeAncestorPath(current, allowSymlinkComponents) {
			return false
		}
	}
	return true
}

func trustedRuntimeAncestorPath(path string, allowSymlink bool) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return allowSymlink
	}
	return isTrustedRuntimeSearchDirPath(path, info)
}

func openTrustedRuntimeSearchRoot(dir string) (safeio.Root, error) {
	return openTrustedRuntimeSearchRootCanonical(dir)
}

func openTrustedRuntimeSearchRootCanonical(dir string) (safeio.Root, error) {
	rootPath, err := runtimeTraceRootPath(dir)
	if err != nil {
		return nil, err
	}
	root, err := safeio.OpenRootNoFollow(rootPath)
	if err != nil {
		return nil, err
	}

	info, err := root.Lstat(".")
	if err != nil {
		return nil, closeRuntimeSearchRootWithError(root, err)
	}
	if !info.IsDir() {
		return nil, closeRuntimeSearchRootWithError(root, fmt.Errorf("runtime search path is not a directory: %s", dir))
	}
	if !isTrustedRuntimeSearchDirPath(dir, info) {
		return nil, closeRuntimeSearchRootWithError(root, fmt.Errorf("runtime search path is not trusted: %s", dir))
	}
	return root, nil
}

func closeRuntimeSearchRootWithError(root safeio.Root, err error) error {
	if closeErr := root.Close(); closeErr != nil {
		return errors.Join(err, closeErr)
	}
	return err
}

func validateTrustedRuntimeExecutable(root safeio.Root, name string) (trusted bool) {
	file, trusted := openTrustedRuntimeExecutable(root, name)
	if !trusted {
		return false
	}
	return file.Close() == nil
}

func openTrustedRuntimeExecutable(root safeio.Root, name string) (safeio.File, bool) {
	pathInfo, err := root.Lstat(name)
	if err != nil || pathInfo.IsDir() {
		return nil, false
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 {
		return nil, false
	}
	if !isTrustedRuntimeExecutable(pathInfo) {
		return nil, false
	}

	file, err := root.Open(name)
	if err != nil {
		return nil, false
	}
	openedInfo, err := file.Stat()
	if err != nil {
		if closeErr := file.Close(); closeErr != nil {
			return nil, false
		}
		return nil, false
	}
	if !isTrustedRuntimeExecutable(openedInfo) || !os.SameFile(pathInfo, openedInfo) {
		if closeErr := file.Close(); closeErr != nil {
			return nil, false
		}
		return nil, false
	}
	return file, true
}

func runtimeExecutableCandidates(executable, dir string) []string {
	base := filepath.Join(dir, executable)
	if !isWindowsRuntime() {
		return []string{base}
	}
	if ext := strings.ToLower(filepath.Ext(executable)); ext != "" {
		if !runtimeWindowsExecutableExtensionAllowed(ext) {
			return nil
		}
		return []string{base}
	}
	extensions := runtimeWindowsExecutableExtensions()
	candidates := make([]string, 0, len(extensions))
	for _, ext := range extensions {
		candidates = append(candidates, base+ext)
	}
	return candidates
}

func isTrustedRuntimeExecutable(info os.FileInfo) bool {
	if !info.Mode().IsRegular() {
		return false
	}
	if isWindowsRuntime() {
		return true
	}
	permissions := info.Mode().Perm()
	return permissions&0o111 != 0 && permissions&0o022 == 0
}

func isTrustedRuntimeSearchDirInfo(info os.FileInfo) bool {
	if !info.IsDir() {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	if isWindowsRuntime() {
		return true
	}
	return trustedRuntimeSearchDirMode(info)
}

func isTrustedRuntimeSearchDirPath(path string, info os.FileInfo) bool {
	if !isTrustedRuntimeSearchDirInfo(info) {
		return false
	}
	if !isWindowsRuntime() {
		return true
	}
	return trustedRuntimeWindowsPathAllowed(path)
}

func runtimeExecutableCanonicalTargetAllowed(executable, canonicalBase string) bool {
	if isWindowsRuntime() {
		return runtimeWindowsExecutableBasenameAllowed(executable, canonicalBase)
	}

	switch executable {
	case "npm":
		return canonicalBase == "npm" || canonicalBase == "npm-cli.js"
	case "npx":
		return canonicalBase == "npx" || canonicalBase == "npx-cli.js"
	case "python3":
		return runtimePython3CanonicalTargetAllowed(canonicalBase)
	default:
		return canonicalBase == executable
	}
}

func runtimePython3CanonicalTargetAllowed(canonicalBase string) bool {
	if canonicalBase == "python3" {
		return true
	}
	suffix, ok := strings.CutPrefix(canonicalBase, "python3.")
	if !ok || suffix == "" {
		return false
	}
	for _, part := range strings.Split(suffix, ".") {
		if part == "" {
			return false
		}
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return false
			}
		}
	}
	return true
}

func runtimeWindowsExecutableBasenameAllowed(executable, canonicalBase string) bool {
	executableLower := strings.ToLower(executable)
	canonicalLower := strings.ToLower(canonicalBase)
	if ext := filepath.Ext(executableLower); ext != "" {
		return runtimeWindowsExecutableExtensionAllowed(ext) && canonicalLower == executableLower
	}
	for _, ext := range runtimeWindowsExecutableExtensions() {
		if canonicalLower == executableLower+ext {
			return true
		}
	}
	return false
}

func validateRuntimeExecutable(executable string) error {
	switch executable {
	case "npm", "pnpm", "yarn", "bun", "npx", "node", "vitest", "jest",
		"mocha", "ava", "deno", "make", "pytest", "python", "python3", "uv":
	default:
		return fmt.Errorf("unsupported runtime test executable %q; use a direct command like 'npm test'", executable)
	}
	return nil
}

func newTrustedRuntimeCommand(ctx context.Context, executable string, resolution *resolvedRuntimeExecutable, args []string) (*runtimeCommand, error) {
	if resolution == nil {
		return nil, errors.New("trusted runtime executable resolution is unavailable")
	}
	isNodeCLI := runtimeNodeCLIScriptTarget(executable, resolution.path)
	var nodeResolution resolvedRuntimeExecutable
	if isNodeCLI {
		var err error
		nodeResolution, err = resolveTrustedRuntimeNodeForCLI(*resolution)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("resolve trusted node interpreter for %q: %w", executable, err), resolution.closeSource())
		}
	}

	launcherSource, err := pinRuntimeExecutableResolution(executable, resolution)
	if err != nil {
		err = errors.Join(err, nodeResolution.closeSource())
		if isNodeCLI {
			return nil, fmt.Errorf("validate canonical CLI script for %q: %w", executable, err)
		}
		return nil, err
	}

	commandSource := launcherSource
	commandArgs := args
	if isNodeCLI {
		commandSource = nodeResolution.takeSource()
	}

	trustedExecutable, err := newTrustedRuntimeExecutableFromSource(commandSource)
	if err != nil {
		if commandSource != launcherSource {
			err = errors.Join(err, launcherSource.Close())
		}
		return nil, err
	}
	if commandSource != launcherSource {
		trustedCLI, cliErr := newTrustedRuntimeExecutableFromSource(launcherSource)
		if cliErr != nil {
			return nil, errors.Join(cliErr, trustedExecutable.cleanupFn())
		}
		commandArgs = append([]string{trustedCLI.launchPath}, args...)
		commandCleanup := trustedExecutable.cleanupFn
		trustedExecutable.cleanupFn = func() error {
			return errors.Join(commandCleanup(), trustedCLI.cleanupFn())
		}
	}
	cmd, err := newTrustedRuntimeExecCommand(ctx, trustedExecutable, commandArgs)
	if err != nil {
		return nil, errors.Join(err, trustedExecutable.cleanupFn())
	}
	configureRuntimeCommand(cmd.Cmd)
	return cmd, nil
}

func pinRuntimeExecutableResolution(executable string, resolution *resolvedRuntimeExecutable) (*runtimeExecutableSource, error) {
	if source := resolution.takeSource(); source != nil {
		if sameRuntimeExecutablePath(source.path, resolution.path) {
			return source, nil
		}
		return nil, errors.Join(errors.New("trusted runtime executable identity does not match its resolved path"), source.Close())
	}
	source, trusted := openTrustedRuntimeExecutableCandidate(executable, resolution.path)
	if !trusted || !sameRuntimeExecutablePath(source.path, resolution.path) {
		boundaryErr := fmt.Errorf("runtime executable path %q is not trusted at launch boundary", resolution.path)
		if source != nil {
			return nil, errors.Join(boundaryErr, source.Close())
		}
		return nil, boundaryErr
	}
	return source, nil
}

func (r *resolvedRuntimeExecutable) takeSource() *runtimeExecutableSource {
	source := r.source
	r.source = nil
	return source
}

func (r *resolvedRuntimeExecutable) closeSource() error {
	source := r.takeSource()
	if source == nil {
		return nil
	}
	return source.Close()
}

func sameRuntimeExecutablePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if isWindowsRuntime() {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func resolveTrustedRuntimeNodeForCLI(launcher resolvedRuntimeExecutable) (resolvedRuntimeExecutable, error) {
	if launcher.selectedLauncherRoot == "" || launcher.canonicalInstallationRoot == "" {
		return resolvedRuntimeExecutable{}, errors.New("canonical launcher installation identity is unavailable")
	}

	searchDirs := []string{
		launcher.selectedLauncherRoot,
		filepath.Join(launcher.canonicalInstallationRoot, "bin"),
	}
	seen := make(map[string]struct{}, len(searchDirs))
	for _, dir := range searchDirs {
		dir = filepath.Clean(dir)
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}

		nodeResolution, ok := resolvePinnedRuntimeExecutableInDir("node", dir)
		if !ok {
			continue
		}
		nodeInstallationRoot, ok := runtimeNodeExecutableInstallationRoot(nodeResolution.path)
		if ok && nodeInstallationRoot == launcher.canonicalInstallationRoot {
			return nodeResolution, nil
		}
		if err := nodeResolution.closeSource(); err != nil {
			return resolvedRuntimeExecutable{}, fmt.Errorf("close rejected node interpreter: %w", err)
		}
	}

	return resolvedRuntimeExecutable{}, fmt.Errorf("trusted node interpreter not found in launcher installation %q", launcher.canonicalInstallationRoot)
}

func runtimeNodeCLIInstallationRoot(executable, candidate, resolvedPath string) (string, bool) {
	if !runtimeNodeCLIScriptTarget(executable, resolvedPath) {
		return "", false
	}
	if root, ok := runtimeHomebrewNodeCLIInstallationRoot(executable, candidate, resolvedPath); ok {
		return root, true
	}
	return runtimeNVMNodeCLIInstallationRoot(candidate, resolvedPath)
}

func runtimeHomebrewNodeCLIInstallationRoot(executable, candidate, resolvedPath string) (string, bool) {
	canonicalLauncherDir, err := filepath.EvalSymlinks(filepath.Dir(candidate))
	if err != nil || !filepath.IsAbs(canonicalLauncherDir) {
		return "", false
	}
	linkTarget, err := os.Readlink(candidate)
	if err != nil {
		return "", false
	}
	if !filepath.IsAbs(linkTarget) {
		linkTarget = filepath.Join(filepath.Dir(candidate), linkTarget)
	}
	installationRoot, ok := runtimeNodeBinEntryInstallationRoot(executable, linkTarget)
	if !ok {
		return "", false
	}
	installationRoot, ok = canonicalRuntimeInstallationRoot(installationRoot)
	if !ok || !runtimeHomebrewInstallationLayoutMatches(canonicalLauncherDir, installationRoot, resolvedPath) {
		return "", false
	}
	resolvedIntermediate, err := filepath.EvalSymlinks(filepath.Clean(linkTarget))
	if err != nil || filepath.Clean(resolvedIntermediate) != filepath.Clean(resolvedPath) {
		return "", false
	}
	return installationRoot, true
}

func runtimeHomebrewInstallationLayoutMatches(launcherDir, installationRoot, cliPath string) bool {
	nodeCellarDir := filepath.Dir(installationRoot)
	cellarDir := filepath.Dir(nodeCellarDir)
	if filepath.Base(nodeCellarDir) != "node" || filepath.Base(cellarDir) != "Cellar" {
		return false
	}
	prefix := filepath.Dir(cellarDir)
	if filepath.Clean(launcherDir) != filepath.Join(prefix, "bin") {
		return false
	}
	expectedCLI := filepath.Join(prefix, "lib", "node_modules", "npm", "bin", filepath.Base(cliPath))
	return filepath.Clean(cliPath) == expectedCLI
}

func runtimeNVMNodeCLIInstallationRoot(candidate, resolvedPath string) (string, bool) {
	canonicalLauncherDir, err := filepath.EvalSymlinks(filepath.Dir(candidate))
	if err != nil || !filepath.IsAbs(canonicalLauncherDir) || filepath.Base(canonicalLauncherDir) != "bin" {
		return "", false
	}
	installationRoot, ok := canonicalRuntimeInstallationRoot(filepath.Dir(canonicalLauncherDir))
	if !ok {
		return "", false
	}
	expectedCLI := filepath.Join(installationRoot, "lib", "node_modules", "npm", "bin", filepath.Base(resolvedPath))
	if filepath.Clean(resolvedPath) != expectedCLI {
		return "", false
	}
	return installationRoot, true
}

func runtimeNodeBinEntryInstallationRoot(executable, path string) (string, bool) {
	path = filepath.Clean(path)
	if filepath.Base(path) != executable || filepath.Base(filepath.Dir(path)) != "bin" {
		return "", false
	}
	return filepath.Dir(filepath.Dir(path)), true
}

func runtimeNodeExecutableInstallationRoot(nodePath string) (string, bool) {
	nodePath = filepath.Clean(nodePath)
	if filepath.Base(nodePath) != "node" || filepath.Base(filepath.Dir(nodePath)) != "bin" {
		return "", false
	}
	return canonicalRuntimeInstallationRoot(filepath.Dir(filepath.Dir(nodePath)))
}

func canonicalRuntimeInstallationRoot(root string) (string, bool) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil || !filepath.IsAbs(resolvedRoot) {
		return "", false
	}
	resolvedRoot = filepath.Clean(resolvedRoot)
	if !validateTrustedRuntimeAncestorChain(resolvedRoot, false) {
		return "", false
	}
	return resolvedRoot, true
}

func runtimeNodeCLIScriptTarget(executable, executablePath string) bool {
	switch executable {
	case "npm":
		return filepath.Base(executablePath) == "npm-cli.js"
	case "npx":
		return filepath.Base(executablePath) == "npx-cli.js"
	default:
		return false
	}
}

func runtimeSearchDirs() []string {
	configured := strings.TrimSpace(os.Getenv(runtimeBinDirsEnvKey))
	if configured != "" {
		return trustedSearchDirs(configured)
	}

	pathDirs := trustedSearchDirs(os.Getenv("PATH"))
	defaults := strings.Join(defaultTrustedRuntimeBinDirEntries(), string(os.PathListSeparator))
	return appendUniqueRuntimeSearchDirs(pathDirs, trustedSearchDirs(defaults))
}

func appendUniqueRuntimeSearchDirs(groups ...[]string) []string {
	seen := make(map[string]struct{})
	var dirs []string
	for _, group := range groups {
		for _, dir := range group {
			if _, ok := seen[dir]; ok {
				continue
			}
			seen[dir] = struct{}{}
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func defaultTrustedRuntimeBinDirEntries() []string {
	if !isWindowsRuntime() {
		return []string{"/usr/local/bin", "/opt/homebrew/bin", "/usr/bin", "/bin"}
	}

	return trustedRuntimeWindowsExecutableRoots()
}

func runtimeWindowsExecutableExtensions() [4]string {
	return [4]string{".com", ".exe", ".bat", ".cmd"}
}

func runtimeWindowsExecutableExtensionAllowed(extension string) bool {
	for _, allowed := range runtimeWindowsExecutableExtensions() {
		if strings.EqualFold(extension, allowed) {
			return true
		}
	}
	return false
}

func isWindowsRuntime() bool {
	return runtimeOS == "windows"
}

func trustedSearchDirs(dirListValue string) []string {
	seen := make(map[string]struct{})
	dirs := make([]string, 0)
	for _, raw := range filepath.SplitList(dirListValue) {
		if dir, ok := trustedSearchDir(raw, seen); ok {
			seen[dir] = struct{}{}
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func trustedSearchDir(raw string, seen map[string]struct{}) (string, bool) {
	dir := filepath.Clean(strings.TrimSpace(raw))
	if dir == "" || !filepath.IsAbs(dir) {
		return "", false
	}
	if _, ok := seen[dir]; ok {
		return "", false
	}
	resolvedDir, ok := resolveTrustedSearchDir(dir)
	if !ok {
		return "", false
	}
	root, err := openTrustedRuntimeSearchRootCanonical(resolvedDir)
	if err != nil {
		return "", false
	}
	if err := root.Close(); err != nil {
		return "", false
	}
	return dir, true
}

func resolveTrustedSearchDir(dir string) (string, bool) {
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil || !filepath.IsAbs(resolvedDir) {
		return "", false
	}
	if !validateTrustedRuntimeAncestorChain(dir, true) || !validateTrustedRuntimeAncestorChain(resolvedDir, false) {
		return "", false
	}
	return resolvedDir, true
}

func validateTrustedRuntimeWindowsAncestorChain(path string, allowSymlinkComponents bool) bool {
	root := trustedRuntimeWindowsExecutableRoot(path)
	if root == "" {
		return false
	}
	return trustedRuntimeAncestorPath(root, allowSymlinkComponents)
}

func trustedRuntimeWindowsPathAllowed(path string) bool {
	return trustedRuntimeWindowsExecutableRoot(path) != ""
}

func trustedRuntimeWindowsExecutableRoot(path string) string {
	cleanPath := filepath.Clean(path)
	for _, root := range trustedRuntimeWindowsExecutableRoots() {
		if strings.EqualFold(cleanPath, root) {
			return root
		}
	}
	return ""
}

func trustedRuntimeWindowsExecutableRoots() []string {
	entries := runtimeWindowsExecutableRoots()
	seen := make(map[string]struct{})
	roots := make([]string, 0, len(entries))
	for _, entry := range entries {
		cleanEntry := filepath.Clean(strings.TrimSpace(entry))
		if cleanEntry == "" || !filepath.IsAbs(cleanEntry) {
			continue
		}
		key := strings.ToLower(cleanEntry)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		roots = append(roots, cleanEntry)
	}
	return roots
}
