package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

const defaultTraceRelPath = ".artifacts/lopper-runtime.ndjson"

type CaptureRequest struct {
	RepoPath             string
	TracePath            string
	TracePathExplicit    bool
	Command              string
	Provider             CaptureProvider
	PythonRunnerProfiles bool
}

// ValidatedTraceSnapshot contains the exact bounded bytes validated during capture.
type ValidatedTraceSnapshot struct {
	data []byte
}

// CaptureResult records the resolved path and whether this capture produced trace bytes.
type CaptureResult struct {
	TracePath     string
	TraceProduced bool
	Snapshot      *ValidatedTraceSnapshot
}

type CaptureProvider string

const (
	CaptureProviderNode   CaptureProvider = "node"
	CaptureProviderPython CaptureProvider = "python"
)

type capturePlan struct {
	repoPath             string
	tracePath            string
	tracePathExplicit    bool
	command              string
	provider             CaptureProvider
	pythonRunnerProfiles bool
}

type explicitTraceCaptureStage struct {
	root     safeio.Root
	target   string
	tempRel  string
	tempPath string
}

var runtimeTraceEnvBuilder = withRuntimeTraceEnv
var explicitTraceTempFileCreator = safeio.CreateTempFileWithinRoot
var explicitTraceTempFileCloseHook = closeExplicitTracePublishFile
var explicitTracePublishWriteHook = safeio.PublishFileWithinRoot

func DefaultTracePath(repoPath string) string {
	return filepath.Join(repoPath, defaultTraceRelPath)
}

func Capture(ctx context.Context, req CaptureRequest) error {
	_, err := CaptureValidatedTrace(ctx, req)
	return err
}

// CaptureValidatedTrace captures a trace and returns the exact validated bytes, if any.
func CaptureValidatedTrace(ctx context.Context, req CaptureRequest) (result CaptureResult, err error) {
	plan, err := resolveCapturePlan(req)
	if err != nil {
		return CaptureResult{}, err
	}
	result = CaptureResult{TracePath: plan.tracePath}
	commandOptions := CommandOptions{PythonRunnerProfiles: plan.pythonRunnerProfiles}
	if err := ValidateCommand(plan.command, commandOptions); err != nil {
		return result, err
	}

	capturePath := plan.tracePath
	explicitStage, err := prepareExplicitTraceCaptureStage(plan)
	if err != nil {
		return result, err
	}
	if explicitStage != nil {
		defer func() {
			if cleanupErr := explicitStage.cleanup(); cleanupErr != nil {
				err = errors.Join(err, cleanupErr)
			}
		}()
		capturePath = explicitStage.tempPath
	} else if err := prepareTracePath(plan.tracePath); err != nil {
		return result, err
	}

	cmd, err := buildRuntimeCommand(ctx, plan.command, commandOptions)
	if err != nil {
		return result, err
	}
	cmd.Dir = plan.repoPath
	cmd.Env, err = runtimeTraceEnvBuilder(os.Environ(), capturePath, plan.provider)
	if err != nil {
		return result, err
	}

	output := newRuntimeCommandOutput()
	cmd.Stdout = output
	cmd.Stderr = output
	err = cmd.Run()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		return result, formatRuntimeCommandError(err, output.diagnostic())
	}
	snapshot, err := stableRuntimeTraceFileSnapshot(capturePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return result, nil
		}
		return result, fmt.Errorf("validate runtime trace: %w", err)
	}
	if len(bytes.TrimSpace(snapshot.data)) == 0 {
		return result, nil
	}
	validatedSnapshot := &ValidatedTraceSnapshot{data: snapshot.data}
	if _, err := validatedSnapshot.Load(); err != nil {
		return result, fmt.Errorf("validate runtime trace: %w", err)
	}
	if explicitStage != nil {
		if err := explicitStage.publish(validatedSnapshot.data); err != nil {
			return result, fmt.Errorf("publish runtime trace: %w", err)
		}
	}
	result.TraceProduced = true
	result.Snapshot = validatedSnapshot
	return result, nil
}

func resolveCapturePlan(req CaptureRequest) (capturePlan, error) {
	plan := capturePlan{
		repoPath:             strings.TrimSpace(req.RepoPath),
		tracePath:            strings.TrimSpace(req.TracePath),
		tracePathExplicit:    req.TracePathExplicit,
		command:              strings.TrimSpace(req.Command),
		provider:             normalizeCaptureProvider(req.Provider),
		pythonRunnerProfiles: req.PythonRunnerProfiles,
	}
	if plan.repoPath == "" {
		return capturePlan{}, fmt.Errorf("repo path is required")
	}
	realRepoPath, err := resolveRealRepoPath(plan.repoPath)
	if err != nil {
		return capturePlan{}, err
	}
	plan.repoPath = realRepoPath
	if plan.command == "" {
		return capturePlan{}, fmt.Errorf("runtime test command is required")
	}
	plan.tracePath, err = resolveTracePathForRealRepo(plan.repoPath, plan.tracePath)
	if err != nil {
		return capturePlan{}, err
	}
	if plan.provider == "" {
		return capturePlan{}, fmt.Errorf("unsupported runtime capture provider %q", req.Provider)
	}
	return plan, nil
}

func prepareExplicitTraceCaptureStage(plan capturePlan) (*explicitTraceCaptureStage, error) {
	if !plan.tracePathExplicit {
		return nil, nil
	}
	traceDir := filepath.Dir(plan.tracePath)
	root, err := openPreparedTraceRoot(traceDir)
	if err != nil {
		return nil, fmt.Errorf("create runtime trace directory: %w", err)
	}
	tempRel, tempFile, err := explicitTraceTempFileCreator(root, ".", 0o600)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("create runtime trace temp file: %w", err), root.Close())
	}
	if err := explicitTraceTempFileCloseHook(tempFile); err != nil {
		return nil, errors.Join(fmt.Errorf("close runtime trace temp file: %w", err), safeio.CleanupTempFileWithinRoot(root, tempRel, tempFile), root.Close())
	}
	return &explicitTraceCaptureStage{
		root:     root,
		target:   filepath.Base(plan.tracePath),
		tempRel:  tempRel,
		tempPath: filepath.Join(traceDir, tempRel),
	}, nil
}

func (s *explicitTraceCaptureStage) publish(validatedData []byte) error {
	if s == nil || s.tempRel == "" {
		return nil
	}
	if len(validatedData) == 0 {
		return errors.Join(fmt.Errorf("validated runtime trace is empty"), s.cleanupTempOnly())
	}
	// Publish validated bytes with safeio's rename-only, fail-closed commit.
	return errors.Join(explicitTracePublishWriteHook(s.root, s.target, validatedData, 0o600), s.cleanupTempOnly())
}

func (s *explicitTraceCaptureStage) cleanup() error {
	if s == nil {
		return nil
	}
	return errors.Join(s.cleanupTempOnly(), s.root.Close())
}

func (s *explicitTraceCaptureStage) cleanupTempOnly() error {
	if s == nil || s.tempRel == "" {
		return nil
	}
	tempRel := s.tempRel
	s.tempRel = ""
	s.tempPath = ""
	return safeio.CleanupTempFileWithinRoot(s.root, tempRel, nil)
}

func closeExplicitTracePublishFile(file safeio.File) error {
	return file.Close()
}

// ResolveTracePathForRepo returns the real, repo-confined trace path used for runtime capture.
func ResolveTracePathForRepo(repoPath, tracePath string) (string, error) {
	realRepoPath, err := resolveRealRepoPath(repoPath)
	if err != nil {
		return "", err
	}
	return resolveTracePathForRealRepo(realRepoPath, tracePath)
}

func normalizeCaptureProvider(provider CaptureProvider) CaptureProvider {
	switch provider {
	case "", CaptureProviderNode:
		return CaptureProviderNode
	case CaptureProviderPython:
		return CaptureProviderPython
	default:
		return ""
	}
}

func resolveRealRepoPath(repoPath string) (string, error) {
	repoAbs, err := filepath.Abs(filepath.Clean(strings.TrimSpace(repoPath)))
	if err != nil {
		return "", fmt.Errorf("resolve repo path: %w", err)
	}
	realRepoPath, err := filepath.EvalSymlinks(repoAbs)
	if err != nil {
		return "", fmt.Errorf("resolve repo path: %w", err)
	}
	info, err := os.Stat(realRepoPath)
	if err != nil {
		return "", fmt.Errorf("stat repo path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repo path is not a directory: %s", repoPath)
	}
	return filepath.Clean(realRepoPath), nil
}

func resolveTracePathForRealRepo(realRepoPath, tracePath string) (string, error) {
	return resolveRuntimePathUnderRepo(realRepoPath, tracePath, defaultTraceRelPath, "runtime trace path")
}

func resolveRuntimePathUnderRepo(realRepoPath, configuredPath, defaultRelPath, label string) (string, error) {
	targetPath := strings.TrimSpace(configuredPath)
	if targetPath == "" {
		targetPath = filepath.Join(realRepoPath, defaultRelPath)
	} else if !filepath.IsAbs(targetPath) {
		targetPath = filepath.Join(realRepoPath, targetPath)
	}
	targetPath, err := filepath.Abs(filepath.Clean(targetPath))
	if err != nil {
		return "", fmt.Errorf("%s: %w", label, err)
	}
	resolvedTargetPath, err := resolveExistingOrPlannedRuntimePath(targetPath)
	if err != nil {
		return "", fmt.Errorf("%s: %w", label, err)
	}
	withinRepo, err := runtimePathWithinRoot(realRepoPath, resolvedTargetPath)
	if err != nil {
		return "", fmt.Errorf("%s: %w", label, err)
	}
	if !withinRepo {
		return "", fmt.Errorf("%s must stay within repo: %s", label, targetPath)
	}
	return resolvedTargetPath, nil
}

func resolveExistingOrPlannedRuntimePath(targetPath string) (string, error) {
	ancestorPath, missingParts, err := existingRuntimeTraceAncestor(targetPath)
	if err != nil {
		return "", err
	}
	if len(missingParts) == 0 {
		info, err := os.Lstat(targetPath)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("runtime trace path must not be a symlink: %s", targetPath)
		}
	}
	resolvedAncestorPath, err := filepath.EvalSymlinks(ancestorPath)
	if err != nil {
		return "", err
	}
	if len(missingParts) == 0 {
		return filepath.Clean(resolvedAncestorPath), nil
	}
	return filepath.Join(resolvedAncestorPath, filepath.Join(missingParts...)), nil
}

func runtimePathWithinRoot(rootPath, targetPath string) (bool, error) {
	rel, err := filepath.Rel(filepath.Clean(rootPath), filepath.Clean(targetPath))
	if err != nil {
		return false, err
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))), nil
}

func prepareTracePath(tracePath string) (err error) {
	traceRoot, err := openPreparedTraceRoot(filepath.Dir(tracePath))
	if err != nil {
		return fmt.Errorf("create runtime trace directory: %w", err)
	}
	defer func() {
		err = errors.Join(err, traceRoot.Close())
	}()
	if err := removePreparedTracePath(traceRoot, filepath.Base(tracePath)); err != nil {
		return fmt.Errorf("remove previous runtime trace: %w", err)
	}
	return nil
}

func openPreparedTraceRoot(traceDir string) (safeio.Root, error) {
	cleanDir := filepath.Clean(traceDir)
	ancestorPath, missingParts, err := existingRuntimeTraceAncestor(cleanDir)
	if err != nil {
		return nil, err
	}
	rootPath, err := runtimeTraceRootPath(ancestorPath)
	if err != nil {
		return nil, err
	}
	root, err := safeio.OpenRootNoFollow(rootPath)
	if err != nil {
		return nil, err
	}
	if len(missingParts) == 0 {
		return root, nil
	}
	return createPreparedTraceRoot(root, missingParts, 0o750)
}

func existingRuntimeTraceAncestor(traceDir string) (string, []string, error) {
	absoluteDir, err := filepath.Abs(traceDir)
	if err != nil {
		return "", nil, err
	}

	missingParts := make([]string, 0, 2)
	current := absoluteDir
	for {
		if _, err := os.Lstat(current); err == nil {
			return current, missingParts, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", nil, err
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", nil, os.ErrNotExist
		}
		missingParts = append([]string{filepath.Base(current)}, missingParts...)
		current = parent
	}
}

func createPreparedTraceRoot(root safeio.Root, missingParts []string, perm os.FileMode) (safeio.Root, error) {
	current := root
	for _, part := range missingParts {
		next, nextErr := openPreparedTraceRootChild(current, part, perm)
		if nextErr != nil {
			return nil, errors.Join(nextErr, current.Close())
		}
		if closeErr := current.Close(); closeErr != nil {
			return nil, errors.Join(closeErr, next.Close())
		}
		current = next
	}
	return current, nil
}

func openPreparedTraceRootChild(root safeio.Root, name string, perm os.FileMode) (safeio.Root, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		if err := root.Mkdir(name, perm); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return nil, err
			}
		}
		info, err = root.Lstat(name)
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("root contains symlink: %s", name)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("root is not a directory: %s", name)
	}
	next, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	openedInfo, err := next.Lstat(".")
	if err != nil {
		return nil, errors.Join(err, next.Close())
	}
	if !os.SameFile(info, openedInfo) {
		return nil, errors.Join(fmt.Errorf("root changed while opening: %s", name), next.Close())
	}
	return next, nil
}

func removePreparedTracePath(root safeio.Root, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("runtime trace path must not be a symlink: %s", name)
	}
	return root.Remove(name)
}

func formatRuntimeCommandError(runErr error, output []byte) error {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return fmt.Errorf("runtime test command failed: %w", runErr)
	}
	return fmt.Errorf("runtime test command failed: %w: %s", runErr, trimmed)
}
