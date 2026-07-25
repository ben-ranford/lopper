package analysis

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/safeio"
)

type fakeAuthKeyReadRoot struct {
	readData  []byte
	readInfo  fs.FileInfo
	readErr   error
	lstatInfo fs.FileInfo
	lstatErr  error
}

func (r *fakeAuthKeyReadRoot) ReadRegularFileUnderLimit(string, int64) ([]byte, fs.FileInfo, error) {
	return r.readData, r.readInfo, r.readErr
}

func (r *fakeAuthKeyReadRoot) Lstat(string) (fs.FileInfo, error) {
	return r.lstatInfo, r.lstatErr
}

type fakeAuthKeyTempRoot struct {
	path       string
	file       safeio.File
	createErr  error
	cleanupErr error
}

func (r *fakeAuthKeyTempRoot) CreateTempFile(os.FileMode) (string, safeio.File, error) {
	return r.path, r.file, r.createErr
}

func (r *fakeAuthKeyTempRoot) CleanupTempFile(string, safeio.File) error {
	return r.cleanupErr
}

type fakeAuthKeyFile struct {
	writeN   int
	writeErr error
	syncErr  error
	closeErr error
}

func (f *fakeAuthKeyFile) Read([]byte) (int, error) { return 0, nil }
func (f *fakeAuthKeyFile) Write(p []byte) (int, error) {
	if f.writeN == 0 && f.writeErr == nil {
		return len(p), nil
	}
	return f.writeN, f.writeErr
}
func (f *fakeAuthKeyFile) Close() error               { return f.closeErr }
func (f *fakeAuthKeyFile) Stat() (fs.FileInfo, error) { return nil, nil }
func (f *fakeAuthKeyFile) Chmod(os.FileMode) error    { return nil }
func (f *fakeAuthKeyFile) Sync() error                { return f.syncErr }

type stubFileInfo struct {
	mode    fs.FileMode
	size    int64
	modTime time.Time
}

func (i *stubFileInfo) Name() string       { return "stub" }
func (i *stubFileInfo) Size() int64        { return i.size }
func (i *stubFileInfo) Mode() fs.FileMode  { return i.mode }
func (i *stubFileInfo) ModTime() time.Time { return i.modTime }
func (i *stubFileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i *stubFileInfo) Sys() any           { return nil }

type failingSignatureWriter struct {
	failOnCall int
	callCount  int
}

func (w *failingSignatureWriter) Write(p []byte) (int, error) {
	w.callCount++
	if w.callCount == w.failOnCall {
		return 0, errors.New("write failed")
	}
	return len(p), nil
}

func TestInvalidAuthKeyGenerationReturnsChangedForFileChangedRead(t *testing.T) {
	_, err := invalidAuthKeyGenerationWith(&fakeAuthKeyReadRoot{readErr: safeio.ErrFileChanged}, "cache.key")
	if !errors.Is(err, errAnalysisCacheAuthKeyChanged) {
		t.Fatalf("expected file-changed read to report changed key, got %v", err)
	}
}

func TestInvalidAuthKeyGenerationReturnsChangedWhenOversizedKeyDisappears(t *testing.T) {
	_, err := invalidAuthKeyGenerationWith(&fakeAuthKeyReadRoot{readErr: safeio.ErrFileTooLarge, lstatErr: os.ErrNotExist}, "cache.key")
	if !errors.Is(err, errAnalysisCacheAuthKeyChanged) {
		t.Fatalf("expected missing oversized key to report changed key, got %v", err)
	}
}

func TestInvalidAuthKeyGenerationReturnsLstatErrorForOversizedKey(t *testing.T) {
	_, err := invalidAuthKeyGenerationWith(&fakeAuthKeyReadRoot{readErr: safeio.ErrFileTooLarge, lstatErr: errors.New("stat failed")}, "cache.key")
	if err == nil || !strings.Contains(err.Error(), "read invalid cache auth key") || !strings.Contains(err.Error(), "stat failed") {
		t.Fatalf("expected oversized-key lstat error, got %v", err)
	}
}

func TestInvalidAuthKeyGenerationRejectsOversizedNonRegularTarget(t *testing.T) {
	_, err := invalidAuthKeyGenerationWith(&fakeAuthKeyReadRoot{readErr: safeio.ErrFileTooLarge, lstatInfo: &stubFileInfo{mode: os.ModeSymlink}}, "cache.key")
	if err == nil || !strings.Contains(err.Error(), "read invalid cache auth key") {
		t.Fatalf("expected oversized non-regular key rejection, got %v", err)
	}
}

func TestWriteAuthKeyCandidateCleansUpAfterWriteError(t *testing.T) {
	_, err := writeAuthKeyCandidateWith(&fakeAuthKeyTempRoot{path: "candidate.tmp", file: &fakeAuthKeyFile{writeErr: errors.New("write failed")}, cleanupErr: errors.New("cleanup failed")}, []byte("key"))
	if err == nil || !strings.Contains(err.Error(), "write cache auth key candidate") || !strings.Contains(err.Error(), "cleanup failed") {
		t.Fatalf("expected joined write and cleanup error, got %v", err)
	}
}

func TestWriteAuthKeyCandidateRejectsShortWrite(t *testing.T) {
	_, err := writeAuthKeyCandidateWith(&fakeAuthKeyTempRoot{path: "candidate.tmp", file: &fakeAuthKeyFile{writeN: 1}}, []byte("key"))
	if err == nil || !strings.Contains(err.Error(), "short write") {
		t.Fatalf("expected short write error, got %v", err)
	}
}

func TestWriteAuthKeyCandidateReturnsSyncError(t *testing.T) {
	_, err := writeAuthKeyCandidateWith(&fakeAuthKeyTempRoot{path: "candidate.tmp", file: &fakeAuthKeyFile{syncErr: errors.New("sync failed")}}, []byte("key"))
	if err == nil || !strings.Contains(err.Error(), "sync cache auth key candidate") {
		t.Fatalf("expected sync failure, got %v", err)
	}
}

func TestWriteAuthKeyCandidateReturnsCloseError(t *testing.T) {
	_, err := writeAuthKeyCandidateWith(&fakeAuthKeyTempRoot{path: "candidate.tmp", file: &fakeAuthKeyFile{closeErr: errors.New("close failed")}}, []byte("key"))
	if err == nil || !strings.Contains(err.Error(), "close cache auth key candidate") {
		t.Fatalf("expected close failure, got %v", err)
	}
}

func TestWritePointerSignaturePartsReturnsPartWriteError(t *testing.T) {
	err := writePointerSignatureParts(&failingSignatureWriter{failOnCall: 1}, cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}, "object")
	if err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("expected part write failure, got %v", err)
	}
}

func TestWritePointerSignaturePartsReturnsSeparatorWriteError(t *testing.T) {
	err := writePointerSignatureParts(&failingSignatureWriter{failOnCall: 2}, cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}, "object")
	if err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("expected separator write failure, got %v", err)
	}
}

func TestSignPointerPropagatesSignaturePartWriterError(t *testing.T) {
	prevWriteParts := analysisCacheWritePointerSigPartsFn
	analysisCacheWritePointerSigPartsFn = func(io.Writer, cacheEntryDescriptor, string) error {
		return errors.New("signature write failed")
	}
	t.Cleanup(func() {
		analysisCacheWritePointerSigPartsFn = prevWriteParts
	})

	cache := &analysisCache{authKey: []byte(strings.Repeat("a", analysisCacheAuthKeyLength))}
	if _, err := cache.signPointer(cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}, "object"); err == nil || !strings.Contains(err.Error(), "signature write failed") {
		t.Fatalf("expected signPointer to propagate signature write failure, got %v", err)
	}
}

func TestPointerSignaturePropagatesSignaturePartWriterError(t *testing.T) {
	prevWriteParts := analysisCacheWritePointerSigPartsFn
	analysisCacheWritePointerSigPartsFn = func(io.Writer, cacheEntryDescriptor, string) error {
		return errors.New("signature write failed")
	}
	t.Cleanup(func() {
		analysisCacheWritePointerSigPartsFn = prevWriteParts
	})

	if _, err := pointerSignature([]byte(strings.Repeat("a", analysisCacheAuthKeyLength)), cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}, "object"); err == nil || !strings.Contains(err.Error(), "signature write failed") {
		t.Fatalf("expected pointerSignature to propagate signature write failure, got %v", err)
	}
}
