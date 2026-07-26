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
	readData        []byte
	readInfo        fs.FileInfo
	readErr         error
	private         bool
	privacyErr      error
	privacyOverride bool
	lstatInfo       fs.FileInfo
	lstatErr        error
}

func (r *fakeAuthKeyReadRoot) ReadRegularFileUnderLimit(string, int64) ([]byte, fs.FileInfo, error) {
	return r.readData, r.readInfo, r.readErr
}

func (r *fakeAuthKeyReadRoot) ReadRegularFilePrivateToOwnerUnderLimit(string, int64) ([]byte, fs.FileInfo, bool, error) {
	if r.readErr != nil {
		return nil, nil, false, r.readErr
	}
	if r.privacyOverride {
		return r.readData, r.readInfo, r.private, r.privacyErr
	}
	return r.readData, r.readInfo, true, nil
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

func (r *fakeAuthKeyTempRoot) CreatePrivateTempFile() (string, safeio.File, error) {
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

func TestInvalidAuthKeyGenerationHandlesPrivacyRevalidation(t *testing.T) {
	validData := []byte(strings.Repeat("ab", analysisCacheAuthKeyLength))
	info := &stubFileInfo{mode: 0o600}
	privacyErr := errors.New("privacy lookup failed")

	tests := []struct {
		name           string
		root           *fakeAuthKeyReadRoot
		wantChanged    bool
		wantErr        error
		wantGeneration bool
	}{
		{
			name: "target changed during privacy check",
			root: &fakeAuthKeyReadRoot{
				readData:        validData,
				readInfo:        info,
				privacyErr:      safeio.ErrFileChanged,
				privacyOverride: true,
			},
			wantChanged: true,
		},
		{
			name: "privacy lookup error",
			root: &fakeAuthKeyReadRoot{
				readData:        validData,
				readInfo:        info,
				privacyErr:      privacyErr,
				privacyOverride: true,
			},
			wantErr: privacyErr,
		},
		{
			name: "permissive key has stable generation",
			root: &fakeAuthKeyReadRoot{
				readData:        validData,
				readInfo:        info,
				private:         false,
				privacyOverride: true,
			},
			wantGeneration: true,
		},
		{
			name: "strict valid key changed before rotation",
			root: &fakeAuthKeyReadRoot{
				readData:        validData,
				readInfo:        info,
				private:         true,
				privacyOverride: true,
			},
			wantChanged: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			generation, err := invalidAuthKeyGenerationWith(test.root, "cache.key")
			switch {
			case test.wantChanged && !errors.Is(err, errAnalysisCacheAuthKeyChanged):
				t.Fatalf("expected changed-key identity, got generation=%q err=%v", generation, err)
			case test.wantErr != nil && !errors.Is(err, test.wantErr):
				t.Fatalf("expected privacy error identity %v, got %v", test.wantErr, err)
			case test.wantGeneration && (err != nil || generation == ""):
				t.Fatalf("expected stable generation for permissive key, got generation=%q err=%v", generation, err)
			}
		})
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

func TestWriteAuthKeyCandidateReturnsFlushErrors(t *testing.T) {
	tests := []struct {
		name string
		file *fakeAuthKeyFile
		want string
	}{
		{name: "sync", file: &fakeAuthKeyFile{syncErr: errors.New("sync failed")}, want: "sync cache auth key candidate"},
		{name: "close", file: &fakeAuthKeyFile{closeErr: errors.New("close failed")}, want: "close cache auth key candidate"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := writeAuthKeyCandidateWith(&fakeAuthKeyTempRoot{path: "candidate.tmp", file: test.file}, []byte("key"))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %s failure, got %v", test.name, err)
			}
		})
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
