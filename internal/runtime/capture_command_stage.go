package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	runtimeExecutableStagePrefix = "lopper-runtime-exec-"
	runtimeExecutableImageDir    = "image"
)

type trustedRuntimeExecutable struct {
	sourcePath string
	launchPath string
	cleanupFn  func() error
}

type runtimeExecutableSource struct {
	path string
	file safeio.File
	root safeio.Root
	info fs.FileInfo
}

type runtimeExecutableStage struct {
	dirPath  string
	fileName string
	root     safeio.Root
	rootInfo fs.FileInfo
	fileInfo fs.FileInfo
	pin      io.Closer

	layoutDirs  []string
	layoutLinks []string

	cleanupOnce sync.Once
	cleanupErr  error
}

type runtimeCommand struct {
	*exec.Cmd
	cleanupOnce sync.Once
	cleanupFn   func() error
	cleanupErr  error
}

func newTrustedRuntimeExecutableFromSource(source *runtimeExecutableSource) (*trustedRuntimeExecutable, error) {
	if source == nil {
		return nil, errors.New("trusted runtime executable source is unavailable")
	}
	canonicalPath := source.path
	if platformRuntimeExecutablePathImmutable(canonicalPath) {
		return &trustedRuntimeExecutable{
			sourcePath: canonicalPath,
			launchPath: canonicalPath,
			cleanupFn:  source.Close,
		}, nil
	}

	stage, stageErr := stageRuntimeExecutable(source)
	closeErr := source.Close()
	if stageErr != nil {
		return nil, errors.Join(fmt.Errorf("stage trusted runtime executable %q: %w", canonicalPath, stageErr), closeErr)
	}
	if closeErr != nil {
		return nil, errors.Join(fmt.Errorf("close trusted runtime executable source %q: %w", canonicalPath, closeErr), stage.cleanup())
	}
	return &trustedRuntimeExecutable{
		sourcePath: canonicalPath,
		launchPath: stage.launchPath(),
		cleanupFn:  stage.cleanup,
	}, nil
}

func openTrustedRuntimeExecutableSource(path string) (*runtimeExecutableSource, error) {
	root, err := openTrustedRuntimeSearchRootCanonical(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	file, info, err := openPinnedRuntimeExecutableSourceFile(root, filepath.Base(path), path)
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	return &runtimeExecutableSource{path: path, file: file, root: root, info: info}, nil
}

func (s *runtimeExecutableSource) Close() error {
	if s == nil {
		return nil
	}
	return errors.Join(s.file.Close(), s.root.Close())
}

func stageRuntimeExecutable(source *runtimeExecutableSource) (*runtimeExecutableStage, error) {
	createdDirPath, err := os.MkdirTemp("", runtimeExecutableStagePrefix)
	if err != nil {
		return nil, err
	}
	dirPath, err := filepath.EvalSymlinks(createdDirPath)
	if err != nil {
		return nil, errors.Join(err, os.Remove(createdDirPath))
	}
	dirPath, err = filepath.Abs(dirPath)
	if err != nil {
		return nil, errors.Join(err, os.Remove(createdDirPath))
	}

	root, err := safeio.OpenRootNoFollow(dirPath)
	if err != nil {
		return nil, errors.Join(err, os.Remove(dirPath))
	}
	rootInfo, err := root.Lstat(".")
	if err != nil {
		return nil, errors.Join(err, root.Close(), os.Remove(dirPath))
	}
	stage := &runtimeExecutableStage{
		dirPath:  dirPath,
		root:     root,
		rootInfo: rootInfo,
	}
	if err := stage.prepareLayout(source.path); err != nil {
		return nil, errors.Join(err, stage.cleanup())
	}
	sourceDigest, err := stage.copyAndSeal(source)
	if err != nil {
		return nil, errors.Join(err, stage.cleanup())
	}
	if err := stage.sealLayout(); err != nil {
		return nil, errors.Join(err, stage.cleanup())
	}
	stage.pin, err = pinRuntimeExecutable(stage.root, stage.fileName, stage.launchPath(), stage.dirPath, stage.fileInfo)
	if err != nil {
		return nil, errors.Join(err, stage.cleanup())
	}
	if err := verifyRuntimeExecutableDigest(stage.root, stage.fileName, sourceDigest); err != nil {
		return nil, errors.Join(err, stage.cleanup())
	}
	return stage, nil
}

func (s *runtimeExecutableStage) prepareLayout(sourcePath string) error {
	if err := s.root.Mkdir(runtimeExecutableImageDir, 0o700); err != nil {
		return err
	}
	s.layoutDirs = append(s.layoutDirs, runtimeExecutableImageDir)
	s.fileName = filepath.Join(runtimeExecutableImageDir, filepath.Base(sourcePath))

	sourceBinDir := filepath.Dir(sourcePath)
	if filepath.Base(sourceBinDir) != "bin" {
		return nil
	}
	stageBinDir := filepath.Join(runtimeExecutableImageDir, "bin")
	if err := s.root.Mkdir(stageBinDir, 0o700); err != nil {
		return err
	}
	s.layoutDirs = append(s.layoutDirs, stageBinDir)
	s.fileName = filepath.Join(stageBinDir, filepath.Base(sourcePath))

	installationRoot := filepath.Dir(sourceBinDir)
	if err := s.linkLayoutEntries(installationRoot, runtimeExecutableImageDir); err != nil {
		return err
	}
	return s.linkLayoutEntries(sourceBinDir, stageBinDir)
}

func (s *runtimeExecutableStage) linkLayoutEntries(sourceDir, stageDir string) error {
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		stageName := filepath.Join(stageDir, entry.Name())
		if stageName == s.fileName ||
			stageDir == runtimeExecutableImageDir && entry.Name() == "bin" {
			continue
		}
		target := filepath.Join(sourceDir, entry.Name())
		if err := os.Symlink(target, filepath.Join(s.dirPath, stageName)); err != nil {
			return err
		}
		s.layoutLinks = append(s.layoutLinks, stageName)
	}
	return nil
}

func (s *runtimeExecutableStage) copyAndSeal(source *runtimeExecutableSource) ([]byte, error) {
	destination, err := s.root.OpenFile(s.fileName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	sourceDigest, copyErr := copyRuntimeExecutable(destination, source)
	closeErr := destination.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return nil, err
	}

	mode := source.info.Mode().Perm() &^ 0o222
	if err := s.root.Chmod(s.fileName, mode); err != nil {
		return nil, err
	}
	fileInfo, err := s.root.Lstat(s.fileName)
	if err != nil {
		return nil, err
	}
	if !fileInfo.Mode().IsRegular() || fileInfo.Size() != source.info.Size() {
		return nil, errors.New("staged runtime executable metadata mismatch")
	}
	s.fileInfo = fileInfo
	return sourceDigest, nil
}

func (s *runtimeExecutableStage) copyAndVerify(source *runtimeExecutableSource) error {
	sourceDigest, err := s.copyAndSeal(source)
	if err != nil {
		return err
	}
	return verifyRuntimeExecutableDigest(s.root, s.fileName, sourceDigest)
}

func (s *runtimeExecutableStage) sealLayout() error {
	for _, dir := range s.layoutDirs {
		if err := s.root.Chmod(dir, 0o500); err != nil {
			return err
		}
	}
	return s.root.Chmod(".", 0o500)
}

func copyRuntimeExecutable(destination safeio.File, source *runtimeExecutableSource) ([]byte, error) {
	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(destination, digest), source.file)
	if err != nil {
		return nil, err
	}
	if written != source.info.Size() {
		return nil, fmt.Errorf("runtime executable size changed while staging: copied %d of %d bytes", written, source.info.Size())
	}
	destinationSyncer, ok := destination.(syncableRuntimeFile)
	if !ok {
		return nil, errors.New("staged runtime executable does not support sync")
	}
	if err := destinationSyncer.Sync(); err != nil {
		return nil, err
	}
	return digest.Sum(nil), nil
}

type syncableRuntimeFile interface {
	safeio.File
	Sync() error
}

func verifyRuntimeExecutableDigest(root safeio.Root, name string, want []byte) error {
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	digest := sha256.New()
	_, readErr := io.Copy(digest, file)
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	if !bytes.Equal(digest.Sum(nil), want) {
		return errors.New("staged runtime executable digest mismatch")
	}
	return nil
}

func (s *runtimeExecutableStage) launchPath() string {
	return filepath.Join(s.dirPath, s.fileName)
}

func (s *runtimeExecutableStage) cleanup() error {
	if s == nil {
		return nil
	}
	s.cleanupOnce.Do(func() {
		s.cleanupErr = cleanupRuntimeExecutableStage(s)
	})
	return s.cleanupErr
}

func cleanupRuntimeExecutableStage(stage *runtimeExecutableStage) error {
	var pinErr error
	if stage.pin != nil {
		pinErr = stage.pin.Close()
	}
	chmodErr := stage.root.Chmod(".", 0o700)
	for _, dir := range stage.layoutDirs {
		chmodErr = errors.Join(chmodErr, stage.root.Chmod(dir, 0o700))
	}
	removeErr := removeStagedRuntimeExecutable(stage.root, stage.fileName, stage.fileInfo)
	for index := len(stage.layoutLinks) - 1; index >= 0; index-- {
		removeErr = errors.Join(removeErr, removeStagedRuntimeLayoutLink(stage.root, stage.layoutLinks[index]))
	}
	for index := len(stage.layoutDirs) - 1; index >= 0; index-- {
		removeErr = errors.Join(removeErr, stage.root.Remove(stage.layoutDirs[index]))
	}
	closeErr := stage.root.Close()
	dirErr := removeRuntimeExecutableStageDir(stage.dirPath, stage.rootInfo)
	return errors.Join(pinErr, chmodErr, removeErr, closeErr, dirErr)
}

func removeStagedRuntimeExecutable(root safeio.Root, name string, expected fs.FileInfo) error {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if expected != nil && !os.SameFile(expected, info) {
		return errors.New("staged runtime executable changed before cleanup")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("staged runtime executable became a symlink before cleanup")
	}
	return root.Remove(name)
}

func removeStagedRuntimeLayoutLink(root safeio.Root, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("staged runtime layout entry %q is no longer a symlink", name)
	}
	return root.Remove(name)
}

func removeRuntimeExecutableStageDir(path string, expected fs.FileInfo) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if expected != nil && !os.SameFile(expected, info) {
		return errors.New("staged runtime executable directory changed before cleanup")
	}
	return os.Remove(path)
}

func newTrustedRuntimeExecCommand(ctx context.Context, executable *trustedRuntimeExecutable, args []string) (*runtimeCommand, error) {
	if executable == nil || executable.cleanupFn == nil {
		return nil, errors.New("trusted runtime executable is unavailable")
	}
	// The fixed path separator prevents ambient PATH lookup before the private,
	// pinned executable image replaces Path and argv[0].
	cmd := exec.CommandContext(ctx, "./.")
	cmd.Path = executable.launchPath
	cmd.Args = append([]string{executable.sourcePath}, args...)
	return &runtimeCommand{Cmd: cmd, cleanupFn: executable.cleanupFn}, nil
}

func (c *runtimeCommand) Start() error {
	err := c.Cmd.Start()
	if err != nil && c.Process == nil {
		return c.finish(err)
	}
	return err
}

func (c *runtimeCommand) Wait() error {
	return c.finish(c.Cmd.Wait())
}

func (c *runtimeCommand) Run() error {
	if err := c.Start(); err != nil {
		return err
	}
	return c.Wait()
}

func (c *runtimeCommand) Output() ([]byte, error) {
	if c.Stdout != nil {
		return nil, c.finish(errors.New("exec: Stdout already set"))
	}
	var stdout bytes.Buffer
	c.Stdout = &stdout
	err := c.Run()
	return stdout.Bytes(), err
}

func (c *runtimeCommand) CombinedOutput() ([]byte, error) {
	if c.Stdout != nil {
		return nil, c.finish(errors.New("exec: Stdout already set"))
	}
	if c.Stderr != nil {
		return nil, c.finish(errors.New("exec: Stderr already set"))
	}
	var output bytes.Buffer
	c.Stdout = &output
	c.Stderr = &output
	err := c.Run()
	return output.Bytes(), err
}

func (c *runtimeCommand) finish(commandErr error) error {
	c.cleanupOnce.Do(func() {
		if c.cleanupFn != nil {
			c.cleanupErr = c.cleanupFn()
		}
	})
	if c.cleanupErr == nil {
		return commandErr
	}
	return errors.Join(commandErr, fmt.Errorf("cleanup staged runtime executable: %w", c.cleanupErr))
}
