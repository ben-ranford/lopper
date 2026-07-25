package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	definitionVersion        = 1
	defaultRunPattern        = "^$"
	defaultOverlayDir        = "overlay"
	maxBenchmarkHarnessBytes = 2 * 1024 * 1024
	maxBenchmarkHarnessTotal = 16 * 1024 * 1024
)

type benchmarkDefinition struct {
	Version        int                    `json:"version"`
	ResolvedFrom   string                 `json:"resolved_from"`
	ResolvedCommit string                 `json:"resolved_commit,omitempty"`
	PackageTargets []string               `json:"package_targets"`
	Benchmarks     []resolvedBenchmark    `json:"benchmarks"`
	BenchPattern   string                 `json:"bench_pattern"`
	RunPattern     string                 `json:"run_pattern"`
	Count          int                    `json:"count"`
	Benchtime      string                 `json:"benchtime"`
	BenchMem       bool                   `json:"benchmem"`
	HarnessFiles   []benchmarkHarnessFile `json:"harness_files"`
	OverlayDir     string                 `json:"overlay_dir"`
}

type resolvedBenchmark struct {
	PackageTarget string `json:"package_target"`
	Name          string `json:"name"`
}

type benchmarkHarnessFile struct {
	Path        string `json:"path"`
	SHA256      string `json:"sha256"`
	OverlayPath string `json:"overlay_path"`
}

type stagedBenchmarkHarness struct {
	targetPath string
	content    []byte
}

type benchmarkSetWrite struct {
	targetPath string
	content    []byte
	perm       os.FileMode
	parentPerm os.FileMode
	errorPath  string
	errorLabel string
}

type benchmarkTargetSnapshot struct {
	targetPath string
	absPath    string
	content    []byte
	mode       os.FileMode
	existed    bool
}

type benchmarkPromotedPath struct {
	liveRel      string
	stagedRel    string
	backupRel    string
	discardRel   string
	backupExists bool
	promoted     bool
	errorLabel   string
}

type confinedWriteRoot interface {
	WriteFileCreatingParents(targetPath string, data []byte, perm, parentPerm os.FileMode) error
	Close() error
}

var openCanonicalWriteRoot = func(rootDir string) (confinedWriteRoot, error) {
	canonicalRoot, err := evalSymlinks(rootDir)
	if err != nil {
		return nil, fmt.Errorf("canonicalize root: %w", err)
	}
	return safeio.OpenCanonicalWriteRoot(canonicalRoot)
}

var (
	readFile = os.ReadFile
	readDir  = os.ReadDir

	marshalIndent = json.MarshalIndent

	absPath      = filepath.Abs
	relPath      = filepath.Rel
	evalSymlinks = filepath.EvalSymlinks

	openOSRoot = os.OpenRoot
	sameFile   = os.SameFile

	rootLstat = func(root *os.Root, name string) (os.FileInfo, error) {
		return root.Lstat(name)
	}
	rootOpenRoot = func(root *os.Root, name string) (*os.Root, error) {
		return root.OpenRoot(name)
	}
	rootOpen = func(root *os.Root, name string) (*os.File, error) {
		return root.Open(name)
	}
	rootMkdir = func(root *os.Root, name string, perm os.FileMode) error {
		return root.Mkdir(name, perm)
	}
	rootRename = func(root *os.Root, oldName, newName string) error {
		return root.Rename(oldName, newName)
	}
	rootRemove = func(root *os.Root, name string) error {
		return root.Remove(name)
	}
	closeOSRoot = func(root *os.Root) error {
		return root.Close()
	}

	resolveStageWriteSeam = func(string) error { return nil }
	resolvePromoteSeam    = func(int, string) error { return nil }
	applyStageWriteSeam   = func(string) error { return nil }
	applyPromoteSeam      = func(int, string) error { return nil }
)

type packageListFlag []string

func (f *packageListFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *packageListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("package target cannot be empty")
	}
	*f = append(*f, value)
	return nil
}

func runResolveCommand(args []string) error {
	fs := flag.NewFlagSet("benchdelta resolve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	repoPath := fs.String("repo", ".", "repository root to resolve benchmark definition from")
	outPath := fs.String("out", "", "path to write benchmark definition manifest")
	overlayDir := fs.String("overlay-dir", "", "directory to write benchmark harness overlay files")
	count := fs.Int("count", 3, "benchmark count")
	benchtime := fs.String("benchtime", "200ms", "benchmark benchtime")
	var packageTargets packageListFlag
	fs.Var(&packageTargets, "package", "package target to include (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*outPath) == "" {
		return errors.New("resolve requires -out")
	}
	if len(packageTargets) == 0 {
		return errors.New("resolve requires at least one -package")
	}
	if *count < 1 {
		return errors.New("resolve requires -count >= 1")
	}
	if strings.TrimSpace(*benchtime) == "" {
		return errors.New("resolve requires -benchtime")
	}
	resolvedOverlayDir := strings.TrimSpace(*overlayDir)
	if resolvedOverlayDir == "" {
		resolvedOverlayDir = filepath.Join(filepath.Dir(*outPath), defaultOverlayDir)
	}

	definition, overlayFiles, err := resolveBenchmarkDefinition(*repoPath, []string(packageTargets), *count, *benchtime)
	if err != nil {
		return fmt.Errorf("resolve benchmark definition: %w", err)
	}
	if err := writeBenchmarkDefinition(*outPath, resolvedOverlayDir, definition, overlayFiles); err != nil {
		return err
	}

	manifestBytes, err := os.ReadFile(*outPath)
	if err != nil {
		return fmt.Errorf("read written benchmark definition: %w", err)
	}
	fmt.Print(formatDefinitionMetadata(filepath.Clean(*outPath), definition, definitionDigest(manifestBytes)))
	return nil
}

func runBenchmarkCommand(args []string) error {
	fs := flag.NewFlagSet("benchdelta run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	repoPath := fs.String("repo", ".", "repository root to apply benchmark definition to")
	definitionPath := fs.String("definition", "", "path to benchmark definition manifest")
	outputPath := fs.String("output", "", "path to write benchmark output")
	ldflags := fs.String("ldflags", "", "ldflags value passed through to go test")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*definitionPath) == "" {
		return errors.New("run requires -definition")
	}

	definition, manifestBytes, err := readBenchmarkDefinition(*definitionPath)
	if err != nil {
		return err
	}
	if err := applyBenchmarkOverlay(*repoPath, *definitionPath, definition); err != nil {
		return fmt.Errorf("apply benchmark definition: %w", err)
	}
	header := formatDefinitionMetadata(filepath.Clean(*definitionPath), definition, definitionDigest(manifestBytes))
	output, err := executeBenchmarkDefinition(*repoPath, definition, strings.TrimSpace(*ldflags))
	combined := append([]byte(header), output...)
	fmt.Print(string(combined))
	if strings.TrimSpace(*outputPath) != "" {
		if writeErr := os.WriteFile(*outputPath, combined, 0o600); writeErr != nil {
			return fmt.Errorf("write benchmark output: %w", writeErr)
		}
	}
	if err != nil {
		return fmt.Errorf("run benchmark definition: %w", err)
	}
	return nil
}

func resolveBenchmarkDefinition(repoPath string, packageTargets []string, count int, benchtime string) (benchmarkDefinition, map[string][]byte, error) {
	repoRoot, err := absPath(repoPath)
	if err != nil {
		return benchmarkDefinition{}, nil, fmt.Errorf("resolve repo path: %w", err)
	}
	headCommit, err := gitHeadCommit(repoRoot)
	if err != nil {
		return benchmarkDefinition{}, nil, fmt.Errorf("resolve HEAD commit: %w", err)
	}
	repoRootHandle, err := openManifestRootNoFollow(repoRoot)
	if err != nil {
		return benchmarkDefinition{}, nil, fmt.Errorf("open repo root: %w", err)
	}
	defer func() {
		if closeErr := closeOSRoot(repoRootHandle); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	definition := benchmarkDefinition{
		Version:        definitionVersion,
		ResolvedFrom:   headCommit,
		ResolvedCommit: headCommit,
		PackageTargets: append([]string(nil), packageTargets...),
		RunPattern:     defaultRunPattern,
		Count:          count,
		Benchtime:      benchtime,
		BenchMem:       true,
	}
	overlayFiles := make(map[string][]byte)
	benchmarkNames := make([]string, 0)
	seenNames := make(map[string]struct{})
	seenFiles := make(map[string]struct{})
	remainingBytes := int64(maxBenchmarkHarnessTotal)

	for _, packageTarget := range packageTargets {
		harnesses, capturedFiles, benchmarks, resolveErr := resolvePackageBenchmarksWithinRoot(repoRoot, repoRootHandle, packageTarget, &remainingBytes)
		if resolveErr != nil {
			err = resolveErr
			return benchmarkDefinition{}, nil, err
		}
		if err != nil {
			return benchmarkDefinition{}, nil, err
		}
		for _, benchmarkName := range benchmarks {
			definition.Benchmarks = append(definition.Benchmarks, resolvedBenchmark{
				PackageTarget: packageTarget,
				Name:          benchmarkName,
			})
			if _, ok := seenNames[benchmarkName]; !ok {
				seenNames[benchmarkName] = struct{}{}
				benchmarkNames = append(benchmarkNames, benchmarkName)
			}
		}
		for _, harness := range harnesses {
			if _, ok := seenFiles[harness.Path]; ok {
				continue
			}
			seenFiles[harness.Path] = struct{}{}
			definition.HarnessFiles = append(definition.HarnessFiles, harness)
			overlayFiles[harness.OverlayPath] = capturedFiles[harness.OverlayPath]
		}
	}
	if len(definition.Benchmarks) == 0 {
		return benchmarkDefinition{}, nil, errors.New("no benchmarks resolved from package targets")
	}
	sort.Slice(definition.Benchmarks, func(i, j int) bool {
		if definition.Benchmarks[i].PackageTarget == definition.Benchmarks[j].PackageTarget {
			return definition.Benchmarks[i].Name < definition.Benchmarks[j].Name
		}
		return definition.Benchmarks[i].PackageTarget < definition.Benchmarks[j].PackageTarget
	})
	sort.Slice(definition.HarnessFiles, func(i, j int) bool {
		return definition.HarnessFiles[i].Path < definition.HarnessFiles[j].Path
	})
	slices.Sort(benchmarkNames)
	definition.BenchPattern = buildBenchmarkPattern(benchmarkNames)
	dirtyHarnesses, err := gitStatusPorcelainV2HasPaths(repoRoot, harnessPaths(definition.HarnessFiles))
	if err != nil {
		return benchmarkDefinition{}, nil, fmt.Errorf("resolve benchmark identity: %w", err)
	}
	if dirtyHarnesses {
		definition.ResolvedFrom = benchmarkCapturedContentIdentity(definition.PackageTargets, definition.HarnessFiles, overlayFiles)
	}
	return definition, overlayFiles, nil
}

func resolvePackageBenchmarks(repoRoot, packageTarget string) (harnesses []benchmarkHarnessFile, benchmarks []string, err error) {
	repoRootHandle, err := openManifestRootNoFollow(repoRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("open repo root: %w", err)
	}
	defer func() {
		err = errors.Join(err, closeOSRoot(repoRootHandle))
	}()
	remainingBytes := int64(maxBenchmarkHarnessTotal)
	harnesses, _, benchmarks, err = resolvePackageBenchmarksWithinRoot(repoRoot, repoRootHandle, packageTarget, &remainingBytes)
	return harnesses, benchmarks, err
}

func resolvePackageBenchmarksWithinRoot(repoRoot string, repoRootHandle *os.Root, packageTarget string, remainingBytes *int64) (harnesses []benchmarkHarnessFile, overlayFiles map[string][]byte, benchmarks []string, err error) {
	packageDir, packageRel, packageRoot, err := openPackageTargetRootNoFollow(repoRoot, repoRootHandle, packageTarget)
	if err != nil {
		return nil, nil, nil, err
	}
	defer func() {
		err = errors.Join(err, closeOSRoot(packageRoot))
	}()
	entries, err := readRootDir(packageRoot)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read package %s: %w", packageTarget, err)
	}

	harnesses = make([]benchmarkHarnessFile, 0)
	overlayFiles = make(map[string][]byte)
	benchmarks = make([]string, 0)
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		info, err := rootLstat(packageRoot, entry.Name())
		if err != nil {
			return nil, nil, nil, fmt.Errorf("read package %s: %w", packageTarget, err)
		}
		harnessRel := filepath.ToSlash(filepath.Join(packageRel, entry.Name()))
		harnessAbs := filepath.Join(packageDir, entry.Name())
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, nil, nil, fmt.Errorf("benchmark harness path is a symlink: %s", harnessAbs)
		}
		if !info.Mode().IsRegular() {
			return nil, nil, nil, fmt.Errorf("benchmark harness path is not a regular file: %s", harnessAbs)
		}
		content, err := readCapturedHarnessBytes(repoRootHandle, harnessRel, remainingBytes)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("read benchmark harness %s: %w", harnessAbs, err)
		}
		fileBenchmarks, err := benchmarkFunctionsInBytes(harnessAbs, content)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("parse benchmark harness %s: %w", harnessAbs, err)
		}
		benchmarks = append(benchmarks, fileBenchmarks...)
		harnesses = append(harnesses, benchmarkHarnessFile{
			Path:        harnessRel,
			SHA256:      bytesDigest(content),
			OverlayPath: harnessRel,
		})
		overlayFiles[harnessRel] = append([]byte(nil), content...)
	}
	sort.Slice(harnesses, func(i, j int) bool {
		return harnesses[i].Path < harnesses[j].Path
	})
	slices.Sort(benchmarks)
	if len(benchmarks) == 0 {
		return nil, nil, nil, fmt.Errorf("package %s does not define any benchmarks", packageTarget)
	}
	return harnesses, overlayFiles, benchmarks, nil
}

func benchmarkFunctionsInFile(path string) ([]string, error) {
	content, err := safeio.ReadFileLimit(path, maxBenchmarkHarnessBytes)
	if err != nil {
		return nil, err
	}
	return benchmarkFunctionsInBytes(path, content)
}

func benchmarkFunctionsInBytes(path string, content []byte) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, content, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	benchmarks := make([]string, 0)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name == nil || !strings.HasPrefix(fn.Name.Name, "Benchmark") {
			continue
		}
		if fn.Type == nil || fn.Type.Params == nil || len(fn.Type.Params.List) != 1 {
			continue
		}
		param := fn.Type.Params.List[0]
		star, ok := param.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		selector, ok := star.X.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		pkgIdent, ok := selector.X.(*ast.Ident)
		if !ok || pkgIdent.Name != "testing" || selector.Sel == nil || selector.Sel.Name != "B" {
			continue
		}
		benchmarks = append(benchmarks, fn.Name.Name)
	}
	return benchmarks, nil
}

func openPackageTargetRootNoFollow(repoRoot string, repoRootHandle *os.Root, packageTarget string) (string, string, *os.Root, error) {
	packageDir, err := packageTargetDir(repoRoot, packageTarget)
	if err != nil {
		return "", "", nil, err
	}
	packageRel, err := relPath(repoRoot, packageDir)
	if err != nil {
		return "", "", nil, err
	}
	if packageRel == "." {
		return "", "", nil, fmt.Errorf("package target %q must not resolve to repo root", packageTarget)
	}

	current := repoRootHandle
	owned := false
	currentPath := repoRoot
	for _, part := range strings.Split(packageRel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		currentPath = filepath.Join(currentPath, part)
		next, err := openRootChildNoFollow(current, part, currentPath)
		if err != nil {
			return "", "", nil, closeOwnedRootWithError(current, owned, fmt.Errorf("read package %s: %w", packageTarget, err))
		}
		if owned {
			if err := closeOSRoot(current); err != nil {
				return "", "", nil, closeOSRootWithError(next, err)
			}
		}
		current = next
		owned = true
	}
	if !owned {
		return "", "", nil, fmt.Errorf("package target %q must not resolve to repo root", packageTarget)
	}
	return packageDir, packageRel, current, nil
}

func readRootDir(root *os.Root) (entries []os.DirEntry, err error) {
	dir, err := rootOpen(root, ".")
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, dir.Close())
	}()
	return dir.ReadDir(-1)
}

func readCapturedHarnessBytes(repoRootHandle *os.Root, harnessRel string, remainingBytes *int64) ([]byte, error) {
	content, err := readRootFileLimit(repoRootHandle, filepath.FromSlash(harnessRel), maxBenchmarkHarnessBytes)
	if err != nil {
		return nil, err
	}
	if remainingBytes != nil {
		if int64(len(content)) > *remainingBytes {
			return nil, fmt.Errorf("captured benchmark harness bytes exceed total limit of %d", maxBenchmarkHarnessTotal)
		}
		*remainingBytes -= int64(len(content))
	}
	return content, nil
}

func readRootFileLimit(root *os.Root, rel string, maxBytes int64) (content []byte, err error) {
	file, err := rootOpen(root, rel)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()
	if info, err := file.Stat(); err == nil && info.Mode().IsRegular() && info.Size() > maxBytes {
		return nil, safeio.ErrFileTooLarge
	}
	content, err = io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maxBytes {
		return nil, safeio.ErrFileTooLarge
	}
	return content, nil
}

func harnessPaths(harnesses []benchmarkHarnessFile) []string {
	paths := make([]string, 0, len(harnesses))
	for _, harness := range harnesses {
		paths = append(paths, harness.Path)
	}
	return paths
}

func benchmarkCapturedContentIdentity(packageTargets []string, harnesses []benchmarkHarnessFile, overlayFiles map[string][]byte) string {
	contentIdentity := make([]byte, 0, len(packageTargets)*16+len(harnesses)*64)
	for _, packageTarget := range packageTargets {
		contentIdentity = append(contentIdentity, "package:"...)
		contentIdentity = append(contentIdentity, packageTarget...)
		contentIdentity = append(contentIdentity, '\n')
	}
	for _, harness := range harnesses {
		contentIdentity = append(contentIdentity, "harness:"...)
		contentIdentity = append(contentIdentity, harness.Path...)
		contentIdentity = append(contentIdentity, "\nsha256:"...)
		contentIdentity = append(contentIdentity, harness.SHA256...)
		contentIdentity = append(contentIdentity, '\n')
		contentIdentity = append(contentIdentity, overlayFiles[harness.OverlayPath]...)
		contentIdentity = append(contentIdentity, '\n')
	}
	sum := sha256.Sum256(contentIdentity)
	return "content-sha256:" + hex.EncodeToString(sum[:])
}

func packageTargetDir(repoRoot, packageTarget string) (string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(packageTarget))
	switch {
	case cleaned == ".", cleaned == "":
		return "", fmt.Errorf("package target %q must not resolve to repo root", packageTarget)
	case cleaned == "..", strings.HasPrefix(cleaned, ".."+string(filepath.Separator)):
		return "", fmt.Errorf("package target %q escapes repository root", packageTarget)
	case filepath.IsAbs(cleaned):
		return "", fmt.Errorf("package target %q must be relative", packageTarget)
	}
	return filepath.Join(repoRoot, strings.TrimPrefix(cleaned, "."+string(filepath.Separator))), nil
}

func buildBenchmarkPattern(benchmarkNames []string) string {
	escaped := make([]string, 0, len(benchmarkNames))
	for _, name := range benchmarkNames {
		escaped = append(escaped, regexp.QuoteMeta(name))
	}
	return "^(" + strings.Join(escaped, "|") + ")$"
}

func writeBenchmarkDefinition(outPath, overlayDir string, definition benchmarkDefinition, overlayFiles map[string][]byte) (returnErr error) {
	rootDir, definitionRel, overlayRel, _, err := resolveManifestLayout(outPath, overlayDir)
	if err != nil {
		return err
	}
	if err := validateManifestRootPathNoFollow(rootDir); err != nil {
		return fmt.Errorf("open benchmark manifest root: %w", err)
	}
	if err := os.MkdirAll(rootDir, 0o750); err != nil {
		return fmt.Errorf("create benchmark definition directory: %w", err)
	}
	canonicalRootDir, err := evalSymlinks(rootDir)
	if err != nil {
		return fmt.Errorf("canonicalize benchmark manifest root: %w", err)
	}
	manifestRoot, err := openManifestRootNoFollow(canonicalRootDir)
	if err != nil {
		return fmt.Errorf("open benchmark manifest root: %w", err)
	}
	defer func() {
		if closeErr := closeOSRoot(manifestRoot); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()
	root, err := openCanonicalWriteRoot(rootDir)
	if err != nil {
		return fmt.Errorf("open benchmark manifest root: %w", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()
	definition.OverlayDir = overlayRel
	if err := normalizeDefinitionPaths(&definition); err != nil {
		return err
	}
	manifestBytes, err := marshalIndent(definition, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal benchmark definition: %w", err)
	}
	manifestBytes = append(manifestBytes, '\n')

	stageRootRel, err := createBenchmarkTempDir(rootDir, ".benchdelta-resolve-stage-")
	if err != nil {
		return fmt.Errorf("create benchmark staging root: %w", err)
	}
	backupRootRel, err := createBenchmarkTempDir(rootDir, ".benchdelta-resolve-backup-")
	if err != nil {
		cleanupErr := removeManifestSubtree(manifestRoot, stageRootRel)
		return errors.Join(fmt.Errorf("create benchmark backup root: %w", err), cleanupErr)
	}

	stageWrites, err := buildResolveStageWrites(rootDir, definitionRel, overlayRel, definition, overlayFiles, manifestBytes, stageRootRel)
	if err != nil {
		cleanupErr := cleanupBenchmarkSetRoots(manifestRoot, stageRootRel, backupRootRel)
		return errors.Join(err, cleanupErr)
	}
	if err := validateManifestSubtreePathNoFollow(manifestRoot, overlayRel); err != nil {
		cleanupErr := cleanupBenchmarkSetRoots(manifestRoot, stageRootRel, backupRootRel)
		return errors.Join(fmt.Errorf("clear benchmark overlay: %w", err), cleanupErr)
	}
	if err := writeBenchmarkSet(root, stageWrites, resolveStageWriteSeam); err != nil {
		cleanupErr := cleanupBenchmarkSetRoots(manifestRoot, stageRootRel, backupRootRel)
		return errors.Join(err, cleanupErr)
	}

	promotions := []benchmarkPromotedPath{
		{
			liveRel:    overlayRel,
			stagedRel:  filepath.Join(stageRootRel, filepath.FromSlash(overlayRel)),
			backupRel:  filepath.Join(backupRootRel, filepath.FromSlash(overlayRel)),
			discardRel: filepath.Join(stageRootRel, "discard", filepath.FromSlash(overlayRel)),
			errorLabel: "clear benchmark overlay",
		},
		{
			liveRel:    definitionRel,
			stagedRel:  filepath.Join(stageRootRel, definitionRel),
			backupRel:  filepath.Join(backupRootRel, definitionRel),
			discardRel: filepath.Join(stageRootRel, "discard", definitionRel),
			errorLabel: "write benchmark definition",
		},
	}
	if err := promoteBenchmarkSet(manifestRoot, promotions, resolvePromoteSeam); err != nil {
		cleanupErr := cleanupBenchmarkSetRoots(manifestRoot, stageRootRel, backupRootRel)
		return errors.Join(err, cleanupErr)
	}
	return cleanupBenchmarkSetRoots(manifestRoot, stageRootRel, backupRootRel)
}

func readBenchmarkDefinition(definitionPath string) (benchmarkDefinition, []byte, error) {
	content, err := safeio.ReadFile(definitionPath)
	if err != nil {
		return benchmarkDefinition{}, nil, fmt.Errorf("read benchmark definition: %w", err)
	}
	var definition benchmarkDefinition
	if err := json.Unmarshal(content, &definition); err != nil {
		return benchmarkDefinition{}, nil, fmt.Errorf("parse benchmark definition: %w", err)
	}
	if definition.Version != definitionVersion {
		return benchmarkDefinition{}, nil, fmt.Errorf("unsupported benchmark definition version %d", definition.Version)
	}
	if len(definition.PackageTargets) == 0 || len(definition.Benchmarks) == 0 || len(definition.HarnessFiles) == 0 {
		return benchmarkDefinition{}, nil, errors.New("benchmark definition is incomplete")
	}
	if strings.TrimSpace(definition.BenchPattern) == "" || strings.TrimSpace(definition.RunPattern) == "" || definition.Count < 1 || strings.TrimSpace(definition.Benchtime) == "" {
		return benchmarkDefinition{}, nil, errors.New("benchmark definition is missing required execution fields")
	}
	if strings.TrimSpace(definition.OverlayDir) == "" {
		definition.OverlayDir = defaultOverlayDir
	}
	if err := normalizeDefinitionPaths(&definition); err != nil {
		return benchmarkDefinition{}, nil, err
	}
	return definition, content, nil
}

func applyBenchmarkOverlay(repoPath, definitionPath string, definition benchmarkDefinition) (returnErr error) {
	repoRoot, err := absPath(repoPath)
	if err != nil {
		return fmt.Errorf("resolve repo path: %w", err)
	}
	if err := normalizeDefinitionPaths(&definition); err != nil {
		return err
	}
	definitionDir, err := absPath(filepath.Dir(definitionPath))
	if err != nil {
		return fmt.Errorf("resolve benchmark definition directory: %w", err)
	}
	stagedHarnesses, err := stageBenchmarkOverlay(repoRoot, definitionDir, definition)
	if err != nil {
		return err
	}
	canonicalRepoRoot, err := evalSymlinks(repoRoot)
	if err != nil {
		return fmt.Errorf("open benchmark repo root: %w", err)
	}
	writeRoot, err := openCanonicalWriteRoot(repoRoot)
	if err != nil {
		return fmt.Errorf("open benchmark repo root: %w", err)
	}
	defer func() {
		if closeErr := writeRoot.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()
	repoRootHandle, err := openManifestRootNoFollow(canonicalRepoRoot)
	if err != nil {
		return fmt.Errorf("open benchmark repo root: %w", err)
	}
	defer func() {
		if closeErr := closeOSRoot(repoRootHandle); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()

	stageRootRel, err := createBenchmarkTempDir(canonicalRepoRoot, ".benchdelta-apply-stage-")
	if err != nil {
		return fmt.Errorf("create benchmark staging root: %w", err)
	}
	backupRootRel, err := createBenchmarkTempDir(canonicalRepoRoot, ".benchdelta-apply-backup-")
	if err != nil {
		cleanupErr := removeManifestSubtree(repoRootHandle, stageRootRel)
		return errors.Join(fmt.Errorf("create benchmark backup root: %w", err), cleanupErr)
	}

	_, stageWrites, promotions, err := buildApplyStagePlan(repoRootHandle, canonicalRepoRoot, stageRootRel, backupRootRel, stagedHarnesses)
	if err != nil {
		cleanupErr := cleanupBenchmarkSetRoots(repoRootHandle, stageRootRel, backupRootRel)
		return errors.Join(err, cleanupErr)
	}
	if err := writeBenchmarkSet(writeRoot, stageWrites, applyStageWriteSeam); err != nil {
		cleanupErr := cleanupBenchmarkSetRoots(repoRootHandle, stageRootRel, backupRootRel)
		return errors.Join(err, cleanupErr)
	}
	if err := promoteBenchmarkSet(repoRootHandle, promotions, applyPromoteSeam); err != nil {
		cleanupErr := cleanupBenchmarkSetRoots(repoRootHandle, stageRootRel, backupRootRel)
		return errors.Join(err, cleanupErr)
	}
	return cleanupBenchmarkSetRoots(repoRootHandle, stageRootRel, backupRootRel)
}

func stageBenchmarkOverlay(repoRoot, definitionDir string, definition benchmarkDefinition) ([]stagedBenchmarkHarness, error) {
	if err := validateBenchmarkHarnessTargets(repoRoot, definition); err != nil {
		return nil, err
	}

	staged := make([]stagedBenchmarkHarness, 0, len(definition.HarnessFiles))
	for _, harness := range definition.HarnessFiles {
		sourcePath := filepath.Join(definitionDir, filepath.FromSlash(definition.OverlayDir), filepath.FromSlash(harness.OverlayPath))
		content, err := safeio.ReadFileUnderLimit(definitionDir, sourcePath, maxBenchmarkHarnessBytes)
		if err != nil {
			return nil, fmt.Errorf("read overlay file %s: %w", harness.OverlayPath, err)
		}
		if got := bytesDigest(content); got != harness.SHA256 {
			return nil, fmt.Errorf("overlay file %s digest mismatch: got %s want %s", harness.OverlayPath, got, harness.SHA256)
		}
		staged = append(staged, stagedBenchmarkHarness{
			targetPath: harness.Path,
			content:    content,
		})
	}
	return staged, nil
}

func resolveManifestLayout(outPath, overlayDir string) (string, string, string, string, error) {
	outAbs, err := absPath(outPath)
	if err != nil {
		return "", "", "", "", fmt.Errorf("resolve benchmark definition path: %w", err)
	}
	overlayAbs, err := absPath(overlayDir)
	if err != nil {
		return "", "", "", "", fmt.Errorf("resolve benchmark overlay path: %w", err)
	}
	rootDir := filepath.Dir(outAbs)
	definitionRel := filepath.Base(outAbs)
	overlayRel, err := relPath(rootDir, overlayAbs)
	if err != nil {
		return "", "", "", "", fmt.Errorf("resolve benchmark overlay path: %w", err)
	}
	overlayRel, err = normalizeManifestRelativePath("overlay directory", overlayRel)
	if err != nil {
		return "", "", "", "", err
	}
	return rootDir, definitionRel, overlayRel, overlayAbs, nil
}

func createBenchmarkTempDir(rootDir, pattern string) (string, error) {
	stageDir, err := os.MkdirTemp(rootDir, pattern)
	if err != nil {
		return "", err
	}
	return filepath.Base(stageDir), nil
}

func buildResolveStageWrites(rootDir, definitionRel, overlayRel string, definition benchmarkDefinition, overlayFiles map[string][]byte, manifestBytes []byte, stageRootRel string) ([]benchmarkSetWrite, error) {
	writes := make([]benchmarkSetWrite, 0, len(overlayFiles)+1)
	manifestPerm, err := existingRegularFilePerm(filepath.Join(rootDir, definitionRel), 0o600)
	if err != nil {
		return nil, fmt.Errorf("write benchmark definition: %w", err)
	}
	overlayPaths := make([]string, 0, len(overlayFiles))
	for overlayPath := range overlayFiles {
		overlayPaths = append(overlayPaths, overlayPath)
	}
	slices.Sort(overlayPaths)
	for _, overlayPath := range overlayPaths {
		content := overlayFiles[overlayPath]
		normalizedOverlayPath, err := normalizeManifestRelativePath("overlay file path", overlayPath)
		if err != nil {
			return nil, err
		}
		liveOverlayAbs := filepath.Join(rootDir, filepath.FromSlash(overlayRel), filepath.FromSlash(normalizedOverlayPath))
		filePerm, err := existingRegularFilePerm(liveOverlayAbs, 0o600)
		if err != nil {
			return nil, fmt.Errorf("write overlay file %s: %w", overlayPath, err)
		}
		writes = append(writes, benchmarkSetWrite{
			targetPath: filepath.Join(stageRootRel, filepath.FromSlash(overlayRel), filepath.FromSlash(normalizedOverlayPath)),
			content:    content,
			perm:       filePerm,
			parentPerm: 0o755,
			errorPath:  normalizedOverlayPath,
			errorLabel: "write overlay file",
		})
	}
	writes = append(writes, benchmarkSetWrite{
		targetPath: filepath.Join(stageRootRel, definitionRel),
		content:    manifestBytes,
		perm:       manifestPerm,
		parentPerm: 0o755,
		errorLabel: "write benchmark definition",
	})
	return writes, nil
}

func buildApplyStagePlan(repoRootHandle *os.Root, repoRoot, stageRootRel, backupRootRel string, stagedHarnesses []stagedBenchmarkHarness) ([]benchmarkTargetSnapshot, []benchmarkSetWrite, []benchmarkPromotedPath, error) {
	snapshots := make([]benchmarkTargetSnapshot, 0, len(stagedHarnesses))
	stageWrites := make([]benchmarkSetWrite, 0, len(stagedHarnesses))
	promotions := make([]benchmarkPromotedPath, 0, len(stagedHarnesses))
	for _, harness := range stagedHarnesses {
		if err := ensureRootPathNoFollow(repoRootHandle, filepath.Dir(filepath.FromSlash(harness.targetPath)), false, 0); err != nil {
			return nil, nil, nil, fmt.Errorf("write benchmark harness %s: %w", harness.targetPath, err)
		}
		targetAbs := filepath.Join(repoRoot, filepath.FromSlash(harness.targetPath))
		snapshot, err := captureBenchmarkTargetSnapshot(repoRootHandle, targetAbs, harness.targetPath)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("write benchmark harness %s: %w", harness.targetPath, err)
		}
		snapshots = append(snapshots, snapshot)
		stageWrites = append(stageWrites, benchmarkSetWrite{
			targetPath: filepath.Join(stageRootRel, filepath.FromSlash(harness.targetPath)),
			content:    harness.content,
			perm:       snapshot.mode,
			parentPerm: 0o755,
			errorPath:  harness.targetPath,
			errorLabel: "write benchmark harness",
		})
		promotions = append(promotions, benchmarkPromotedPath{
			liveRel:      filepath.FromSlash(harness.targetPath),
			stagedRel:    filepath.Join(stageRootRel, filepath.FromSlash(harness.targetPath)),
			backupRel:    filepath.Join(backupRootRel, filepath.FromSlash(harness.targetPath)),
			discardRel:   filepath.Join(stageRootRel, "discard", filepath.FromSlash(harness.targetPath)),
			backupExists: snapshot.existed,
			errorLabel:   "write benchmark harness " + harness.targetPath,
		})
	}
	sort.Slice(stageWrites, func(i, j int) bool {
		return stageWrites[i].targetPath < stageWrites[j].targetPath
	})
	return snapshots, stageWrites, promotions, nil
}

func captureBenchmarkTargetSnapshot(root *os.Root, targetAbs, targetPath string) (benchmarkTargetSnapshot, error) {
	info, err := rootLstat(root, filepath.FromSlash(targetPath))
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return benchmarkTargetSnapshot{}, fmt.Errorf("target path is a symlink: %s", targetAbs)
		}
		if !info.Mode().IsRegular() {
			return benchmarkTargetSnapshot{}, fmt.Errorf("target path is not a regular file: %s", targetAbs)
		}
		file, err := rootOpen(root, filepath.FromSlash(targetPath))
		if err != nil {
			return benchmarkTargetSnapshot{}, err
		}
		content, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if readErr == nil && closeErr != nil {
			readErr = closeErr
		} else if closeErr != nil {
			readErr = errors.Join(readErr, closeErr)
		}
		if readErr != nil {
			return benchmarkTargetSnapshot{}, readErr
		}
		return benchmarkTargetSnapshot{
			targetPath: targetPath,
			absPath:    targetAbs,
			content:    content,
			mode:       info.Mode().Perm(),
			existed:    true,
		}, nil
	case os.IsNotExist(err):
		return benchmarkTargetSnapshot{
			targetPath: targetPath,
			absPath:    targetAbs,
			mode:       0o600,
			existed:    false,
		}, nil
	default:
		return benchmarkTargetSnapshot{}, err
	}
}

func existingRegularFilePerm(path string, fallback os.FileMode) (os.FileMode, error) {
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return 0, fmt.Errorf("target path is a symlink: %s", path)
		}
		if !info.Mode().IsRegular() {
			return 0, fmt.Errorf("target path is not a regular file: %s", path)
		}
		return info.Mode().Perm(), nil
	case os.IsNotExist(err):
		return fallback, nil
	default:
		return 0, err
	}
}

func writeBenchmarkSet(root confinedWriteRoot, writes []benchmarkSetWrite, seam func(string) error) error {
	for _, write := range writes {
		if seam != nil {
			if err := seam(filepath.ToSlash(write.targetPath)); err != nil {
				return benchmarkWriteError(write, err)
			}
		}
		if err := root.WriteFileCreatingParents(write.targetPath, write.content, write.perm, write.parentPerm); err != nil {
			return benchmarkWriteError(write, err)
		}
	}
	return nil
}

func benchmarkWriteError(write benchmarkSetWrite, err error) error {
	if write.errorPath != "" {
		return fmt.Errorf("%s %s: %w", write.errorLabel, filepath.ToSlash(write.errorPath), err)
	}
	return fmt.Errorf("%s: %w", write.errorLabel, err)
}

func promoteBenchmarkSet(root *os.Root, promotions []benchmarkPromotedPath, seam func(int, string) error) (returnErr error) {
	for i := range promotions {
		exists, err := rootPathExists(root, promotions[i].liveRel)
		if err != nil {
			return benchmarkRollbackCleanup(root, promotions, i-1, fmt.Errorf("%s: %w", promotions[i].errorLabel, err))
		}
		promotions[i].backupExists = promotions[i].backupExists || exists
		if exists {
			if err := renameWithinRoot(root, promotions[i].liveRel, promotions[i].backupRel); err != nil {
				return benchmarkRollbackCleanup(root, promotions, i-1, fmt.Errorf("%s: %w", promotions[i].errorLabel, err))
			}
		}
		if err := renameWithinRoot(root, promotions[i].stagedRel, promotions[i].liveRel); err != nil {
			return benchmarkRollbackCleanup(root, promotions, i, fmt.Errorf("%s: %w", promotions[i].errorLabel, err))
		}
		promotions[i].promoted = true
		if seam != nil {
			if err := seam(i+1, filepath.ToSlash(promotions[i].liveRel)); err != nil {
				return benchmarkRollbackCleanup(root, promotions, i, fmt.Errorf("%s: %w", promotions[i].errorLabel, err))
			}
		}
	}
	return nil
}

func benchmarkRollbackCleanup(root *os.Root, promotions []benchmarkPromotedPath, last int, cause error) error {
	rollbackErr := rollbackBenchmarkPromotions(root, promotions, last)
	if rollbackErr != nil {
		return errors.Join(cause, rollbackErr)
	}
	return cause
}

func rollbackBenchmarkPromotions(root *os.Root, promotions []benchmarkPromotedPath, last int) (returnErr error) {
	for i := last; i >= 0; i-- {
		if promotions[i].promoted {
			if err := renameWithinRoot(root, promotions[i].liveRel, promotions[i].discardRel); err != nil {
				returnErr = errors.Join(returnErr, err)
			}
		}
		if promotions[i].backupExists {
			if err := renameWithinRoot(root, promotions[i].backupRel, promotions[i].liveRel); err != nil {
				returnErr = errors.Join(returnErr, err)
			}
		}
	}
	return returnErr
}

func cleanupBenchmarkSetRoots(root *os.Root, relPaths ...string) (returnErr error) {
	for _, relPath := range relPaths {
		if strings.TrimSpace(relPath) == "" {
			continue
		}
		if err := removeManifestSubtree(root, relPath); err != nil && !os.IsNotExist(err) {
			returnErr = errors.Join(returnErr, err)
		}
	}
	return returnErr
}

func openManifestRootNoFollow(rootDir string) (*os.Root, error) {
	rootAbs, err := canonicalizeTrustedManifestRoot(rootDir)
	if err != nil {
		return nil, fmt.Errorf("resolve root path: %w", err)
	}
	volumeRoot := filepath.VolumeName(rootAbs) + string(os.PathSeparator)
	rel, err := relPath(volumeRoot, rootAbs)
	if err != nil {
		return nil, err
	}
	root, err := openOSRoot(volumeRoot)
	if err != nil {
		return nil, err
	}
	if rel == "." {
		return root, nil
	}

	currentPath := volumeRoot
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		currentPath = filepath.Join(currentPath, part)
		next, err := openRootChildNoFollow(root, part, currentPath)
		if err != nil {
			return nil, closeOSRootWithError(root, err)
		}
		if err := closeOSRoot(root); err != nil {
			return nil, closeOSRootWithError(next, err)
		}
		root = next
	}
	return root, nil
}

func validateManifestRootPathNoFollow(rootDir string) error {
	rootAbs, err := canonicalizeTrustedManifestRoot(rootDir)
	if err != nil {
		return fmt.Errorf("resolve root path: %w", err)
	}
	volumeRoot := filepath.VolumeName(rootAbs) + string(os.PathSeparator)
	currentPath := filepath.Clean(volumeRoot)

	info, err := os.Lstat(currentPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("root contains symlink: %s", currentPath)
	}
	if !info.IsDir() {
		return fmt.Errorf("root is not a directory: %s", currentPath)
	}

	rel, err := relPath(volumeRoot, rootAbs)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}

	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		currentPath = filepath.Join(currentPath, part)
		info, err := os.Lstat(currentPath)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("root contains symlink: %s", currentPath)
		}
		if !info.IsDir() {
			return fmt.Errorf("root is not a directory: %s", currentPath)
		}
	}
	return nil
}

func canonicalizeTrustedManifestRoot(rootDir string) (string, error) {
	rootAbs, err := absPath(rootDir)
	if err != nil {
		return "", err
	}
	for _, aliasRoot := range []string{"/tmp", "/var"} {
		if !hasPathPrefix(rootAbs, aliasRoot) {
			continue
		}
		canonicalAlias, err := evalSymlinks(aliasRoot)
		if err != nil {
			return "", err
		}
		rel, err := relPath(aliasRoot, rootAbs)
		if err != nil {
			return "", err
		}
		if rel == "." {
			return canonicalAlias, nil
		}
		return filepath.Join(canonicalAlias, rel), nil
	}
	return rootAbs, nil
}

func hasPathPrefix(path, prefix string) bool {
	cleanPath := filepath.Clean(path)
	cleanPrefix := filepath.Clean(prefix)
	return cleanPath == cleanPrefix || strings.HasPrefix(cleanPath, cleanPrefix+string(os.PathSeparator))
}

func openRootChildNoFollow(root *os.Root, name, path string) (*os.Root, error) {
	info, err := rootLstat(root, name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("root contains symlink: %s", path)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("root is not a directory: %s", path)
	}

	next, err := rootOpenRoot(root, name)
	if err != nil {
		return nil, err
	}
	openedInfo, err := rootLstat(next, ".")
	if err != nil {
		return nil, closeOSRootWithError(next, err)
	}
	if !sameFile(info, openedInfo) {
		return nil, closeOSRootWithError(next, fmt.Errorf("root changed while opening: %s", path))
	}
	return next, nil
}

func closeOSRootWithError(root *os.Root, err error) error {
	if closeErr := closeOSRoot(root); closeErr != nil {
		return errors.Join(err, closeErr)
	}
	return err
}

func rootPathExists(root *os.Root, rel string) (bool, error) {
	_, err := rootLstat(root, filepath.Clean(rel))
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func renameWithinRoot(root *os.Root, fromRel, toRel string) error {
	fromRel = filepath.Clean(fromRel)
	toRel = filepath.Clean(toRel)
	if err := ensureRootPathNoFollow(root, filepath.Dir(toRel), true, 0o755); err != nil {
		return err
	}
	return rootRename(root, fromRel, toRel)
}

func ensureRootPathNoFollow(root *os.Root, rel string, create bool, perm os.FileMode) (returnErr error) {
	rel = filepath.Clean(rel)
	if rel == "." || rel == "" {
		return nil
	}

	current := root
	owned := false
	currentPath := string(os.PathSeparator)
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		currentPath = filepath.Join(currentPath, part)
		info, err := rootLstat(current, part)
		if os.IsNotExist(err) && create {
			if err := rootMkdir(current, part, perm); err != nil && !os.IsExist(err) {
				return closeOwnedRootWithError(current, owned, err)
			}
			info, err = rootLstat(current, part)
		}
		if err != nil {
			return closeOwnedRootWithError(current, owned, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return closeOwnedRootWithError(current, owned, fmt.Errorf("output parent contains symlink: %s", currentPath))
		}
		if !info.IsDir() {
			return closeOwnedRootWithError(current, owned, fmt.Errorf("output parent is not a directory: %s", currentPath))
		}
		next, err := rootOpenRoot(current, part)
		if err != nil {
			return closeOwnedRootWithError(current, owned, err)
		}
		if owned {
			if err := closeOSRoot(current); err != nil {
				return closeOSRootWithError(next, err)
			}
		}
		current = next
		owned = true
	}
	if owned {
		return closeOSRoot(current)
	}
	return nil
}

func closeOwnedRootWithError(root *os.Root, owned bool, err error) error {
	if !owned {
		return err
	}
	return closeOSRootWithError(root, err)
}

func removeManifestSubtree(root *os.Root, relPath string) error {
	parts := strings.Split(filepath.FromSlash(relPath), string(os.PathSeparator))
	return removeManifestSubtreeParts(root, parts, relPath)
}

func validateManifestSubtreePathNoFollow(root *os.Root, relPath string) error {
	parts := strings.Split(filepath.FromSlash(relPath), string(os.PathSeparator))
	return validateManifestSubtreePathParts(root, parts, relPath)
}

func validateManifestSubtreePathParts(root *os.Root, parts []string, displayPath string) error {
	if len(parts) == 0 {
		return nil
	}
	name := parts[0]
	info, err := rootLstat(root, name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(parts) > 1 {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("overlay path contains symlink: %s", filepath.Clean(displayPath))
		}
		if !info.IsDir() {
			return fmt.Errorf("overlay path component is not a directory: %s", filepath.Clean(displayPath))
		}
		child, err := openRootChildNoFollow(root, name, filepath.Clean(displayPath))
		if err != nil {
			return err
		}
		err = validateManifestSubtreePathParts(child, parts[1:], displayPath)
		return closeOSRootWithError(child, err)
	}
	return nil
}

func removeManifestSubtreeParts(root *os.Root, parts []string, displayPath string) error {
	if len(parts) == 0 {
		return nil
	}
	name := parts[0]
	info, err := rootLstat(root, name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(parts) > 1 {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("overlay path contains symlink: %s", filepath.Clean(displayPath))
		}
		if !info.IsDir() {
			return fmt.Errorf("overlay path component is not a directory: %s", filepath.Clean(displayPath))
		}
		child, err := openRootChildNoFollow(root, name, filepath.Clean(displayPath))
		if err != nil {
			return err
		}
		err = removeManifestSubtreeParts(child, parts[1:], displayPath)
		return closeOSRootWithError(child, err)
	}

	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		if err := rootRemove(root, name); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}

	child, err := openRootChildNoFollow(root, name, filepath.Clean(displayPath))
	if err != nil {
		return err
	}
	dir, err := rootOpen(child, ".")
	if err != nil {
		return closeOSRootWithError(child, err)
	}
	entries, err := dir.ReadDir(-1)
	closeErr := dir.Close()
	if err == nil && closeErr != nil {
		err = closeErr
	} else if closeErr != nil {
		err = errors.Join(err, closeErr)
	}
	if err != nil {
		return closeOSRootWithError(child, err)
	}
	for _, entry := range entries {
		if err := removeManifestSubtreeParts(child, []string{entry.Name()}, filepath.Join(displayPath, entry.Name())); err != nil {
			return closeOSRootWithError(child, err)
		}
	}
	if err := closeOSRoot(child); err != nil {
		return err
	}
	if err := rootRemove(root, name); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func normalizeDefinitionPaths(definition *benchmarkDefinition) error {
	overlayDir, err := normalizeManifestRelativePath("overlay directory", definition.OverlayDir)
	if err != nil {
		return err
	}
	definition.OverlayDir = overlayDir
	seenTargets := make(map[string]int, len(definition.HarnessFiles))
	seenOverlays := make(map[string]int, len(definition.HarnessFiles))
	for i := range definition.HarnessFiles {
		targetPath, err := normalizeManifestRelativePath("benchmark harness path", definition.HarnessFiles[i].Path)
		if err != nil {
			return err
		}
		overlayPath, err := normalizeManifestRelativePath("benchmark harness overlay path", definition.HarnessFiles[i].OverlayPath)
		if err != nil {
			return err
		}
		if firstIndex, exists := seenTargets[targetPath]; exists {
			return fmt.Errorf("duplicate benchmark harness path %q at entries %d and %d", targetPath, firstIndex+1, i+1)
		}
		if firstIndex, exists := seenOverlays[overlayPath]; exists {
			return fmt.Errorf("duplicate benchmark harness overlay path %q at entries %d and %d", overlayPath, firstIndex+1, i+1)
		}
		seenTargets[targetPath] = i
		seenOverlays[overlayPath] = i
		definition.HarnessFiles[i].Path = targetPath
		definition.HarnessFiles[i].OverlayPath = overlayPath
	}
	return nil
}

func validateBenchmarkHarnessTargets(repoRoot string, definition benchmarkDefinition) error {
	allowedTargets := make(map[string]struct{}, len(definition.HarnessFiles))
	packagePrefixes := make([]string, 0, len(definition.PackageTargets))
	for _, packageTarget := range definition.PackageTargets {
		packageDir, err := packageTargetDir(repoRoot, packageTarget)
		if err != nil {
			return err
		}
		packagePrefix := filepath.ToSlash(strings.TrimPrefix(packageDir, repoRoot+string(filepath.Separator)))
		packagePrefixes = append(packagePrefixes, packagePrefix)
	}
	for _, harness := range definition.HarnessFiles {
		if !strings.HasSuffix(harness.Path, "_test.go") {
			return fmt.Errorf("benchmark harness path must reference a package _test.go file: %s", harness.Path)
		}
		if !pathUnderAnyPrefix(harness.Path, packagePrefixes) {
			return fmt.Errorf("benchmark harness path %q is outside benchmark package targets", harness.Path)
		}
		allowedTargets[harness.Path] = struct{}{}
	}

	unexpected := make([]string, 0)
	for i, packageTarget := range definition.PackageTargets {
		packageDir, err := packageTargetDir(repoRoot, packageTarget)
		if err != nil {
			return err
		}
		info, err := os.Lstat(packageDir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect package %s: %w", packageTarget, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if !info.IsDir() {
			return fmt.Errorf("inspect package %s: package directory is not a directory: %s", packageTarget, packageDir)
		}

		entries, err := readDir(packageDir)
		if err != nil {
			return fmt.Errorf("read package %s: %w", packageTarget, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			targetPath := packagePrefixes[i] + "/" + entry.Name()
			if _, ok := allowedTargets[targetPath]; !ok {
				unexpected = append(unexpected, targetPath)
			}
		}
	}
	if len(unexpected) > 0 {
		slices.Sort(unexpected)
		return fmt.Errorf("package test files not in benchmark manifest: %s", strings.Join(unexpected, ", "))
	}
	return nil
}

func pathUnderAnyPrefix(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if hasPathPrefix(filepath.FromSlash(path), filepath.FromSlash(prefix)) {
			return true
		}
	}
	return false
}

func normalizeManifestRelativePath(label, value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("%s must not be empty", label)
	}
	if filepath.IsAbs(filepath.FromSlash(trimmed)) {
		return "", fmt.Errorf("%s must be relative: %s", label, value)
	}
	cleaned := filepath.Clean(filepath.FromSlash(trimmed))
	switch {
	case cleaned == ".", cleaned == "":
		return "", fmt.Errorf("%s must not resolve to the root directory: %s", label, value)
	case cleaned == "..", strings.HasPrefix(cleaned, ".."+string(filepath.Separator)):
		return "", fmt.Errorf("%s escapes its root: %s", label, value)
	}
	return filepath.ToSlash(cleaned), nil
}

func executeBenchmarkDefinition(repoPath string, definition benchmarkDefinition, ldflags string) ([]byte, error) {
	args := []string{"test"}
	if ldflags != "" {
		args = append(args, "-ldflags", ldflags)
	}
	args = append(args, "-run", definition.RunPattern, "-bench", definition.BenchPattern)
	if definition.BenchMem {
		args = append(args, "-benchmem")
	}
	args = append(args, "-count", strconv.Itoa(definition.Count), "-benchtime", definition.Benchtime)
	args = append(args, definition.PackageTargets...)

	cmd := exec.Command("go", args...)
	cmd.Dir = repoPath
	cmd.Env = withGoFlagsDisabledBuildVCS(os.Environ())
	return cmd.CombinedOutput()
}

func withGoFlagsDisabledBuildVCS(env []string) []string {
	result := make([]string, 0, len(env)+1)
	replaced := false
	for _, entry := range env {
		if !strings.HasPrefix(entry, "GOFLAGS=") {
			result = append(result, entry)
			continue
		}
		value := strings.TrimPrefix(entry, "GOFLAGS=")
		if !strings.Contains(value, "-buildvcs=false") {
			value = strings.TrimSpace(value + " -buildvcs=false")
		}
		result = append(result, "GOFLAGS="+value)
		replaced = true
	}
	if !replaced {
		result = append(result, "GOFLAGS=-buildvcs=false")
	}
	return result
}

func gitHeadCommit(repoRoot string) (string, error) {
	cmd := exec.Command("git", "-C", repoRoot, "rev-parse", "HEAD")
	cmd.Env = envWithoutGitOverrides(os.Environ())
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func gitStatusPorcelainV2HasPaths(repoRoot string, paths []string) (bool, error) {
	if len(paths) == 0 {
		return false, nil
	}
	args := []string{
		"-C", repoRoot,
		"status",
		"--porcelain=v2",
		"-z",
		"--untracked-files=all",
		"--ignored=no",
		"--no-renames",
		"--",
	}
	args = append(args, paths...)
	// #nosec G204 -- repo-local git status is constrained to explicit subcommands and normalized repository-relative paths.
	cmd := exec.Command("git", args...)
	cmd.Env = envWithoutGitOverrides(os.Environ())
	output, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return len(output) != 0, nil
}

func envWithoutGitOverrides(env []string) []string {
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, "GIT_") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func formatDefinitionMetadata(definitionPath string, definition benchmarkDefinition, digest string) string {
	var buf strings.Builder
	buf.WriteString("lopper-bench-definition: ")
	buf.WriteString(definitionPath)
	buf.WriteByte('\n')
	buf.WriteString("lopper-bench-definition-sha256: ")
	buf.WriteString(digest)
	buf.WriteByte('\n')
	buf.WriteString("lopper-bench-resolved-from: ")
	buf.WriteString(definition.ResolvedFrom)
	buf.WriteByte('\n')
	if strings.TrimSpace(definition.ResolvedCommit) != "" {
		buf.WriteString("lopper-bench-resolved-commit: ")
		buf.WriteString(definition.ResolvedCommit)
		buf.WriteByte('\n')
	}
	buf.WriteString("lopper-bench-packages: ")
	buf.WriteString(strings.Join(definition.PackageTargets, ","))
	buf.WriteByte('\n')
	buf.WriteString("lopper-bench-selection: ")
	buf.WriteString(definition.BenchPattern)
	buf.WriteByte('\n')
	buf.WriteString("lopper-bench-run: ")
	buf.WriteString(definition.RunPattern)
	buf.WriteByte('\n')
	buf.WriteString("lopper-bench-count: ")
	buf.WriteString(strconv.Itoa(definition.Count))
	buf.WriteByte('\n')
	buf.WriteString("lopper-bench-benchtime: ")
	buf.WriteString(definition.Benchtime)
	buf.WriteByte('\n')
	buf.WriteString("lopper-bench-benchmem: ")
	buf.WriteString(strconv.FormatBool(definition.BenchMem))
	buf.WriteByte('\n')
	for _, harness := range definition.HarnessFiles {
		buf.WriteString("lopper-bench-harness: ")
		buf.WriteString(harness.Path)
		buf.WriteByte(' ')
		buf.WriteString(harness.SHA256)
		buf.WriteByte('\n')
	}
	return buf.String()
}

func definitionDigest(content []byte) string {
	return bytesDigest(content)
}

func bytesDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
