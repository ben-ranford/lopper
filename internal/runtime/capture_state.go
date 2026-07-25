package runtime

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

const runtimeTraceStateSchema = "v3"
const runtimeTraceStateSuffix = ".state.json"

type runtimeTraceState struct {
	Schema           string          `json:"schema"`
	Command          string          `json:"command"`
	Provider         CaptureProvider `json:"provider"`
	TrustedInputHash string          `json:"trustedInputHash"`
	TraceHash        string          `json:"traceHash"`
}

var hashRuntimeTraceFileAfterFirstSnapshotHook func()
var snapshotRuntimeTraceFileAfterPathSnapshotHook func()
var snapshotRuntimeTraceFileAfterOpenHook func()

func reuseRuntimeTraceIfPossible(tracePath, command string, provider CaptureProvider, trustedInputDigest string) (bool, error) {
	_, reused, err := reusableRuntimeTraceSnapshot(tracePath, command, provider, trustedInputDigest)
	return reused, err
}

func reusableRuntimeTraceSnapshot(tracePath, command string, provider CaptureProvider, trustedInputDigest string) (runtimeTraceSnapshot, bool, error) {
	if strings.TrimSpace(trustedInputDigest) == "" {
		return runtimeTraceSnapshot{}, false, nil
	}
	info, err := os.Stat(tracePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return runtimeTraceSnapshot{}, false, nil
		}
		return runtimeTraceSnapshot{}, false, err
	}
	if info.IsDir() {
		return runtimeTraceSnapshot{}, false, nil
	}

	stateData, err := os.ReadFile(runtimeTraceStatePath(tracePath))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return runtimeTraceSnapshot{}, false, nil
		}
		return runtimeTraceSnapshot{}, false, err
	}
	state, ok := parseRuntimeTraceState(stateData)
	if !ok {
		return runtimeTraceSnapshot{}, false, nil
	}
	if strings.TrimSpace(state.Command) != strings.TrimSpace(command) ||
		normalizeCaptureProvider(state.Provider) != normalizeCaptureProvider(provider) ||
		strings.TrimSpace(state.TrustedInputHash) != strings.TrimSpace(trustedInputDigest) {
		return runtimeTraceSnapshot{}, false, nil
	}
	snapshot, err := stableRuntimeTraceFileSnapshot(tracePath)
	if err != nil {
		return runtimeTraceSnapshot{}, false, err
	}
	if snapshot.digest != strings.TrimSpace(state.TraceHash) {
		return runtimeTraceSnapshot{}, false, nil
	}
	return snapshot, true, nil
}

func runtimeTraceStatePath(tracePath string) string {
	return tracePath + runtimeTraceStateSuffix
}

func parseRuntimeTraceState(stateData []byte) (runtimeTraceState, bool) {
	var state runtimeTraceState
	if err := json.Unmarshal(stateData, &state); err != nil {
		return runtimeTraceState{}, false
	}
	if strings.TrimSpace(state.Schema) != runtimeTraceStateSchema {
		return runtimeTraceState{}, false
	}
	if strings.TrimSpace(state.Command) == "" {
		return runtimeTraceState{}, false
	}
	if normalizeCaptureProvider(state.Provider) == "" {
		return runtimeTraceState{}, false
	}
	if strings.TrimSpace(state.TrustedInputHash) == "" || strings.TrimSpace(state.TraceHash) == "" {
		return runtimeTraceState{}, false
	}
	return state, true
}

func writeRuntimeTraceState(tracePath, command string, provider CaptureProvider, trustedInputDigest string) error {
	_, err := writeRuntimeTraceStateAndSnapshot(tracePath, command, provider, trustedInputDigest)
	return err
}

func writeRuntimeTraceStateAndSnapshot(tracePath, command string, provider CaptureProvider, trustedInputDigest string) (*runtimeTraceSnapshot, error) {
	info, err := os.Stat(tracePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if info.IsDir() {
		return nil, nil
	}
	snapshot, err := stableRuntimeTraceFileSnapshot(tracePath)
	if err != nil {
		return nil, err
	}
	payload := []byte(`{"schema":"` + runtimeTraceStateSchema +
		`","command":` + strconv.Quote(strings.TrimSpace(command)) +
		`,"provider":` + strconv.Quote(string(normalizeCaptureProvider(provider))) +
		`,"trustedInputHash":` + strconv.Quote(strings.TrimSpace(trustedInputDigest)) +
		`,"traceHash":` + strconv.Quote(snapshot.digest) + `}`)
	if err := os.WriteFile(runtimeTraceStatePath(tracePath), payload, 0o600); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func hashRuntimeTraceFile(tracePath string) (string, error) {
	snapshot, err := stableRuntimeTraceFileSnapshot(tracePath)
	if err != nil {
		return "", err
	}
	return snapshot.digest, nil
}

func stableRuntimeTraceFileSnapshot(tracePath string) (runtimeTraceSnapshot, error) {
	first, err := snapshotRuntimeTraceFile(tracePath)
	if err != nil {
		return runtimeTraceSnapshot{}, err
	}
	if hashRuntimeTraceFileAfterFirstSnapshotHook != nil {
		hashRuntimeTraceFileAfterFirstSnapshotHook()
	}
	second, err := snapshotRuntimeTraceFile(tracePath)
	if err != nil {
		return runtimeTraceSnapshot{}, err
	}
	if !os.SameFile(first.info, second.info) || !sameRuntimeTraceSnapshot(first, second) {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: trace file changed while hashing: %s", tracePath)
	}
	return first, nil
}

type runtimeTraceSnapshot struct {
	info   os.FileInfo
	data   []byte
	digest string
}

func snapshotRuntimeTraceFile(tracePath string) (_ runtimeTraceSnapshot, err error) {
	absolutePath, err := filepath.Abs(tracePath)
	if err != nil {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: %w", err)
	}
	root, err := safeio.OpenRoot(filepath.Dir(absolutePath))
	if err != nil {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: open trace root: %w", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	fileName := filepath.Base(absolutePath)
	pathInfo, err := root.Lstat(fileName)
	if err != nil {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: %w", err)
	}
	if err := validateRuntimeTraceFileInfo(pathInfo, tracePath); err != nil {
		return runtimeTraceSnapshot{}, err
	}
	if snapshotRuntimeTraceFileAfterPathSnapshotHook != nil {
		snapshotRuntimeTraceFileAfterPathSnapshotHook()
	}

	file, err := root.Open(fileName)
	if err != nil {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	if snapshotRuntimeTraceFileAfterOpenHook != nil {
		snapshotRuntimeTraceFileAfterOpenHook()
	}

	openedInfo, err := file.Stat()
	if err != nil {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: %w", err)
	}
	if err := validateRuntimeTraceFileInfo(openedInfo, tracePath); err != nil {
		return runtimeTraceSnapshot{}, err
	}
	if !os.SameFile(pathInfo, openedInfo) || !sameRuntimeTraceMetadata(pathInfo, openedInfo) {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: trace file changed while opening: %s", tracePath)
	}

	data, err := io.ReadAll(newRuntimeTraceByteLimitReader(file, maxRuntimeTraceBytes))
	if err != nil {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: %w", err)
	}
	descriptorInfo, err := file.Stat()
	if err != nil {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: %w", err)
	}
	if err := validateRuntimeTraceFileInfo(descriptorInfo, tracePath); err != nil {
		return runtimeTraceSnapshot{}, err
	}
	currentPathInfo, err := root.Lstat(fileName)
	if err != nil {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: %w", err)
	}
	if err := validateRuntimeTraceFileInfo(currentPathInfo, tracePath); err != nil {
		return runtimeTraceSnapshot{}, err
	}
	if !os.SameFile(openedInfo, descriptorInfo) ||
		!sameRuntimeTraceMetadata(openedInfo, descriptorInfo) ||
		!os.SameFile(descriptorInfo, currentPathInfo) ||
		!sameRuntimeTraceMetadata(descriptorInfo, currentPathInfo) {
		return runtimeTraceSnapshot{}, fmt.Errorf("hash runtime trace: trace file changed while hashing: %s", tracePath)
	}

	digest := sha256.Sum256(data)
	return runtimeTraceSnapshot{
		info:   descriptorInfo,
		data:   data,
		digest: fmt.Sprintf("%x", digest),
	}, nil
}

func validateRuntimeTraceFileInfo(info os.FileInfo, tracePath string) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("hash runtime trace: trace file must not be a symlink: %s", tracePath)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("hash runtime trace: trace file must be regular: %s", tracePath)
	}
	if info.Size() > maxRuntimeTraceBytes {
		return fmt.Errorf("hash runtime trace: %w", safeio.ErrFileTooLarge)
	}
	return nil
}

func sameRuntimeTraceSnapshot(first, second runtimeTraceSnapshot) bool {
	return sameRuntimeTraceMetadata(first.info, second.info) && first.digest == second.digest
}

func sameRuntimeTraceMetadata(first, second os.FileInfo) bool {
	return first.Size() == second.Size() &&
		first.Mode() == second.Mode() &&
		first.ModTime().Equal(second.ModTime())
}
