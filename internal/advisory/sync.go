package advisory

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	DefaultOSVSourceURL          = "https://osv-vulnerabilities.storage.googleapis.com/all.zip"
	defaultHTTPTimeout           = 15 * time.Minute
	maxSyncMetadataBytes         = 64 * 1024 * 1024
	maxSyncSnapshotBytes  int64  = 4 * 1024 * 1024 * 1024
	maxOSVZipEntries             = 2_000_000
	maxOSVZipExpandedSize uint64 = 32 * 1024 * 1024 * 1024
	maxOSVZipExpandRatio  uint64 = 100
	manifestFileName             = "manifest.json"
	manifestSchemaVersion        = "lopper.advisory-cache.v1"
	sha256Prefix                 = "sha256:"
	schemaOSVJSON                = "osv-json"
	schemaOSVZip                 = "osv-zip"
)

type SyncOptions struct {
	SourceURL string
	CachePath string
	Now       time.Time
	Client    *http.Client
}

type CacheManifest struct {
	SchemaVersion string          `json:"schemaVersion"`
	UpdatedAt     string          `json:"updatedAt"`
	Latest        string          `json:"latest,omitempty"`
	Snapshots     []CacheSnapshot `json:"snapshots,omitempty"`
}

type CacheSnapshot struct {
	ID          string   `json:"id"`
	SourceURL   string   `json:"sourceUrl"`
	RetrievedAt string   `json:"retrievedAt"`
	Digest      string   `json:"digest"`
	Path        string   `json:"path"`
	Schema      string   `json:"schema,omitempty"`
	Ecosystems  []string `json:"ecosystems,omitempty"`
	EntryCount  int      `json:"entryCount"`
	SizeBytes   int64    `json:"sizeBytes"`
}

type fetchedSnapshot struct {
	tempRel    string
	tempPath   string
	digest     string
	schema     string
	ecosystems []string
	entryCount int
	sizeBytes  int64
}

var syncAfterDownloadTestHook func(cachePath, tempRel string)

func SyncOSV(ctx context.Context, opts SyncOptions) (snapshot CacheSnapshot, err error) {
	sourceURL, cachePath, now, err := resolveSyncOptions(opts)
	if err != nil {
		return CacheSnapshot{}, err
	}
	if err := os.MkdirAll(cachePath, 0o750); err != nil {
		return CacheSnapshot{}, fmt.Errorf("create advisory cache: %w", err)
	}
	root, err := safeio.OpenRoot(cachePath)
	if err != nil {
		return CacheSnapshot{}, fmt.Errorf("create advisory cache: %w", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	if err := root.MkdirAll("snapshots", 0o750); err != nil {
		return CacheSnapshot{}, fmt.Errorf("create advisory cache: %w", err)
	}
	fetched, err := downloadSnapshotUnderRoot(ctx, sourceURL, opts.Client, root)
	if err != nil {
		return CacheSnapshot{}, err
	}
	tempOwned := true
	defer func() {
		if !tempOwned {
			return
		}
		err = errors.Join(err, safeio.CleanupTempFileWithinRoot(root, fetched.tempRel, nil))
	}()
	if syncAfterDownloadTestHook != nil {
		syncAfterDownloadTestHook(cachePath, fetched.tempRel)
	}

	id := snapshotIDFromDigest(fetched.digest)
	snapshotRel := filepath.Join("snapshots", id+snapshotExtension(fetched.schema))
	if err := safeio.MoveFileWithinRoot(root, fetched.tempRel, snapshotRel, 0o750, 0o640); err != nil {
		return CacheSnapshot{}, fmt.Errorf("write advisory snapshot: %w", err)
	}
	tempOwned = false
	snapshot = buildCacheSnapshot(id, sourceURL, now, snapshotRel, fetched)
	if err := updateManifest(root, snapshot, now); err != nil {
		return CacheSnapshot{}, err
	}
	return snapshot, nil
}

func resolveSyncOptions(opts SyncOptions) (sourceURL, cachePath string, now time.Time, err error) {
	sourceURL = strings.TrimSpace(opts.SourceURL)
	if sourceURL == "" {
		sourceURL = DefaultOSVSourceURL
	}
	if err := validateSyncURL(sourceURL); err != nil {
		return "", "", time.Time{}, err
	}
	cachePath = strings.TrimSpace(opts.CachePath)
	if cachePath == "" {
		return "", "", time.Time{}, fmt.Errorf("cache path is required")
	}
	now = opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return sourceURL, cachePath, now, nil
}

func snapshotIDFromDigest(digest string) string {
	id := strings.TrimPrefix(digest, sha256Prefix)
	if len(id) > 24 {
		return id[:24]
	}
	return id
}

func buildCacheSnapshot(id, sourceURL string, now time.Time, snapshotRel string, fetched fetchedSnapshot) CacheSnapshot {
	return CacheSnapshot{
		ID:          id,
		SourceURL:   sourceURL,
		RetrievedAt: now.UTC().Format(time.RFC3339),
		Digest:      fetched.digest,
		Path:        filepath.ToSlash(snapshotRel),
		Schema:      fetched.schema,
		Ecosystems:  fetched.ecosystems,
		EntryCount:  fetched.entryCount,
		SizeBytes:   fetched.sizeBytes,
	}
}

func snapshotExtension(schema string) string {
	switch schema {
	case schemaOSVZip:
		return ".zip"
	default:
		return ".json"
	}
}

func LoadCacheManifest(cachePath string) (CacheManifest, error) {
	data, err := safeio.ReadFileUnder(cachePath, filepath.Join(cachePath, manifestFileName))
	if err != nil {
		return CacheManifest{}, err
	}
	var manifest CacheManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return CacheManifest{}, fmt.Errorf("parse advisory cache manifest: %w", err)
	}
	return manifest, nil
}

func loadCacheManifestWithinRoot(root safeio.Root) (CacheManifest, error) {
	data, err := safeio.ReadFileWithinRoot(root, manifestFileName)
	if err != nil {
		return CacheManifest{}, err
	}
	var manifest CacheManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return CacheManifest{}, fmt.Errorf("parse advisory cache manifest: %w", err)
	}
	return manifest, nil
}

func validateSyncURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid advisory source URL: %w", err)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("advisory sync source URL must use https")
	}
	if parsed.Host == "" {
		return fmt.Errorf("advisory sync source URL must include a host")
	}
	return nil
}

func fetchSnapshot(ctx context.Context, sourceURL string, client *http.Client) (data []byte, err error) {
	tempDir, err := os.MkdirTemp("", "lopper-advisory-fetch-*")
	if err != nil {
		return nil, fmt.Errorf("create advisory temp dir: %w", err)
	}
	defer func() {
		removeErr := os.RemoveAll(tempDir)
		if removeErr != nil && err == nil {
			err = fmt.Errorf("remove advisory temp dir: %w", removeErr)
		}
	}()
	fetched, err := downloadSnapshot(ctx, sourceURL, client, tempDir)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(fetched.tempPath)
}

type snapshotMetadataLoader func(sizeBytes int64, schema string) ([]string, int)
type snapshotOpener func() (io.ReadCloser, error)

func downloadSnapshotUnderRoot(ctx context.Context, sourceURL string, client *http.Client, root safeio.Root) (_ fetchedSnapshot, err error) {
	resp, err := openSnapshotResponse(ctx, sourceURL, client)
	if err != nil {
		return fetchedSnapshot{}, err
	}
	if err := checkSnapshotResponse(resp); err != nil {
		return fetchedSnapshot{}, err
	}
	tempRel, tempFile, err := safeio.CreateTempFileWithinRoot(root, "", 0o600)
	if err == nil && tempFile == nil {
		err = errors.New("nil temp file")
	}
	if err != nil {
		return fetchedSnapshot{}, closeSnapshotResponse(resp.Body, fmt.Errorf("create advisory snapshot temp file: %w", err))
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, safeio.CleanupTempFileWithinRoot(root, tempRel, tempFile))
		}
	}()
	openTempSnapshot := func() (io.ReadCloser, error) {
		file, openErr := root.Open(tempRel)
		return file, openErr
	}
	loadTempMetadata := func(sizeBytes int64, schema string) ([]string, int) {
		return loadSnapshotMetadataUnderRoot(root, tempRel, sizeBytes, schema)
	}
	snapshot, err := finalizeDownloadedSnapshot(resp.Body, tempFile, openTempSnapshot, loadTempMetadata)
	if err != nil {
		return fetchedSnapshot{}, err
	}
	tempFile = nil
	snapshot.tempRel = tempRel
	return snapshot, nil
}

func downloadSnapshot(ctx context.Context, sourceURL string, client *http.Client, tempDir string) (_ fetchedSnapshot, err error) {
	resp, err := openSnapshotResponse(ctx, sourceURL, client)
	if err != nil {
		return fetchedSnapshot{}, err
	}
	if err := checkSnapshotResponse(resp); err != nil {
		return fetchedSnapshot{}, err
	}
	tempFile, err := os.CreateTemp(tempDir, "snapshot-*")
	if err != nil {
		return fetchedSnapshot{}, closeSnapshotResponse(resp.Body, fmt.Errorf("create advisory snapshot temp file: %w", err))
	}
	defer func() {
		if err != nil {
			closeErr := tempFile.Close()
			if closeErr != nil {
				err = errors.Join(err, fmt.Errorf("close advisory snapshot temp file: %w", closeErr))
			}
			removeErr := os.Remove(tempFile.Name())
			if removeErr != nil && !os.IsNotExist(removeErr) {
				err = errors.Join(err, fmt.Errorf("remove advisory snapshot temp file: %w", removeErr))
			}
		}
	}()
	openTempSnapshot := func() (io.ReadCloser, error) {
		return safeio.OpenFile(tempFile.Name())
	}
	loadTempMetadata := func(sizeBytes int64, schema string) ([]string, int) {
		return loadSnapshotMetadata(tempFile.Name(), sizeBytes, schema)
	}
	snapshot, err := finalizeDownloadedSnapshot(resp.Body, tempFile, openTempSnapshot, loadTempMetadata)
	if err != nil {
		return fetchedSnapshot{}, err
	}
	snapshot.tempPath = tempFile.Name()
	return snapshot, nil
}

func loadSnapshotMetadata(path string, sizeBytes int64, schema string) ([]string, int) {
	return loadSnapshotMetadataFromReader(schema, sizeBytes, func() ([]byte, error) {
		return safeio.ReadFile(path)
	})
}

func streamSnapshotResponse(body io.Reader, writer io.Writer) ([]byte, int64, error) {
	return streamSnapshotResponseWithLimit(body, writer, maxSyncSnapshotBytes)
}

func streamSnapshotResponseWithLimit(body io.Reader, writer io.Writer, maxBytes int64) ([]byte, int64, error) {
	recorder := previewRecorder{preview: make([]byte, 0, 512)}
	streamWriter := &snapshotStreamWriter{writer: writer, recorder: &recorder}
	if _, err := io.CopyBuffer(streamWriter, io.LimitReader(body, maxBytes+1), make([]byte, 32*1024)); err != nil {
		if isSnapshotWriteError(err) {
			return nil, 0, err
		}
		return nil, 0, fmt.Errorf("read advisory snapshot: %w", err)
	}
	if recorder.sizeBytes > maxBytes {
		return nil, 0, fmt.Errorf("read advisory snapshot: response exceeds %d-byte limit", maxBytes)
	}
	return recorder.preview, recorder.sizeBytes, nil
}

func closeSnapshotResponse(body io.Closer, bodyErr error) error {
	closeErr := body.Close()
	if closeErr == nil {
		return bodyErr
	}
	if bodyErr != nil {
		return errors.Join(bodyErr, fmt.Errorf("close advisory response: %w", closeErr))
	}
	return fmt.Errorf("close advisory response: %w", closeErr)
}

type previewRecorder struct {
	preview   []byte
	sizeBytes int64
}

type snapshotStreamWriter struct {
	writer   io.Writer
	recorder *previewRecorder
}

type snapshotWriteError struct {
	err error
}

func (r *previewRecorder) Write(p []byte) (int, error) {
	r.sizeBytes += int64(len(p))
	if remaining := cap(r.preview) - len(r.preview); remaining > 0 {
		if remaining > len(p) {
			remaining = len(p)
		}
		r.preview = append(r.preview, p[:remaining]...)
	}
	return len(p), nil
}

func (w *snapshotStreamWriter) Write(p []byte) (int, error) {
	if _, err := w.writer.Write(p); err != nil {
		return 0, &snapshotWriteError{err: err}
	}
	return w.recorder.Write(p)
}

func (e *snapshotWriteError) Error() string {
	return fmt.Sprintf("write advisory snapshot temp file: %v", e.err)
}

func (e *snapshotWriteError) Unwrap() error {
	return e.err
}

func isSnapshotWriteError(err error) bool {
	var writeErr *snapshotWriteError
	return errors.As(err, &writeErr)
}

func openSnapshotResponse(ctx context.Context, sourceURL string, client *http.Client) (*http.Response, error) {
	if client == nil {
		client = defaultSnapshotHTTPClient()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, err
	}
	redirectPolicy := client.CheckRedirect
	clientClone := *client
	clientClone.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL == nil || req.URL.Scheme != "https" {
			return fmt.Errorf("advisory snapshot redirect must use https")
		}
		if err := validateSyncURL(req.URL.String()); err != nil {
			return err
		}
		if redirectPolicy != nil {
			return redirectPolicy(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	resp, err := clientClone.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download advisory snapshot: %w", err)
	}
	return resp, nil
}

func defaultSnapshotHTTPClient() *http.Client {
	return &http.Client{Timeout: defaultHTTPTimeout}
}

func checkSnapshotResponse(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if resp.ContentLength > maxSyncSnapshotBytes {
			return closeSnapshotResponse(resp.Body, fmt.Errorf("download advisory snapshot: response exceeds %d-byte limit", maxSyncSnapshotBytes))
		}
		return nil
	}
	if err := closeSnapshotResponse(resp.Body, nil); err != nil {
		return err
	}
	return fmt.Errorf("download advisory snapshot: HTTP %d", resp.StatusCode)
}

func finalizeDownloadedSnapshot(body io.ReadCloser, tempFile io.WriteCloser, openSnapshot snapshotOpener, loadMetadata snapshotMetadataLoader) (fetchedSnapshot, error) {
	hasher := sha256.New()
	preview, sizeBytes, err := streamSnapshotResponse(body, io.MultiWriter(tempFile, hasher))
	if err != nil {
		return fetchedSnapshot{}, closeSnapshotResponse(body, err)
	}
	if err := closeSnapshotResponse(body, nil); err != nil {
		return fetchedSnapshot{}, err
	}
	if err := tempFile.Close(); err != nil {
		return fetchedSnapshot{}, fmt.Errorf("close advisory snapshot temp file: %w", err)
	}
	schema := inferSnapshotSchema(preview)
	switch schema {
	case schemaOSVJSON:
		if err := validateDownloadedOSVJSON(openSnapshot); err != nil {
			return fetchedSnapshot{}, fmt.Errorf("download advisory snapshot: invalid OSV JSON snapshot: %w", err)
		}
	case schemaOSVZip:
		if err := validateDownloadedOSVZip(openSnapshot, sizeBytes); err != nil {
			return fetchedSnapshot{}, fmt.Errorf("download advisory snapshot: invalid OSV ZIP snapshot: %w", err)
		}
	default:
		return fetchedSnapshot{}, fmt.Errorf("download advisory snapshot: unrecognized OSV snapshot schema")
	}
	ecosystems, entryCount := loadMetadata(sizeBytes, schema)
	return fetchedSnapshot{
		digest:     sha256Prefix + hex.EncodeToString(hasher.Sum(nil)),
		schema:     schema,
		ecosystems: ecosystems,
		entryCount: entryCount,
		sizeBytes:  sizeBytes,
	}, nil
}

func loadSnapshotMetadataFromReader(schema string, sizeBytes int64, read func() ([]byte, error)) ([]string, int) {
	if schema != schemaOSVJSON || sizeBytes > maxSyncMetadataBytes {
		return nil, 0
	}
	data, err := read()
	if err != nil {
		return nil, 0
	}
	return snapshotEcosystems(data), snapshotEntryCount(data)
}

func readAllAndClose(file io.ReadCloser) ([]byte, error) {
	data, err := io.ReadAll(file)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	return data, nil
}

func loadSnapshotMetadataUnderRoot(root safeio.Root, path string, sizeBytes int64, schema string) ([]string, int) {
	return loadSnapshotMetadataFromReader(schema, sizeBytes, func() ([]byte, error) {
		file, err := root.Open(path)
		if err != nil {
			return nil, err
		}
		return readAllAndClose(file)
	})
}

func updateManifest(root safeio.Root, snapshot CacheSnapshot, now time.Time) error {
	manifest, err := loadCacheManifestWithinRoot(root)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	manifest.SchemaVersion = manifestSchemaVersion
	manifest.UpdatedAt = now.UTC().Format(time.RFC3339)
	manifest.Latest = snapshot.ID
	byID := map[string]CacheSnapshot{snapshot.ID: snapshot}
	for _, existing := range manifest.Snapshots {
		if existing.ID == "" || existing.ID == snapshot.ID {
			continue
		}
		byID[existing.ID] = existing
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	manifest.Snapshots = make([]CacheSnapshot, 0, len(ids))
	for _, id := range ids {
		manifest.Snapshots = append(manifest.Snapshots, byID[id])
	}
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return safeio.WriteFileWithinRoot(root, manifestFileName, payload, 0o640)
}

func inferSnapshotSchema(data []byte) string {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return "unknown"
	}
	if trimmed[0] == '[' || trimmed[0] == '{' {
		return schemaOSVJSON
	}
	if bytes.HasPrefix(trimmed, []byte("PK")) {
		return schemaOSVZip
	}
	return "unknown"
}

func validateDownloadedOSVJSON(openSnapshot snapshotOpener) error {
	file, err := openSnapshot()
	if err != nil {
		return fmt.Errorf("open snapshot for validation: %w", err)
	}
	if file == nil {
		return errors.New("open snapshot for validation: nil file")
	}
	validationErr := validateOSVJSONSnapshot(file)
	closeErr := file.Close()
	if closeErr != nil {
		closeErr = fmt.Errorf("close snapshot after validation: %w", closeErr)
	}
	return errors.Join(validationErr, closeErr)
}

func validateDownloadedOSVZip(openSnapshot snapshotOpener, sizeBytes int64) (err error) {
	file, err := openSnapshot()
	if err != nil {
		return fmt.Errorf("open snapshot for validation: %w", err)
	}
	if file == nil {
		return errors.New("open snapshot for validation: nil file")
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close snapshot after validation: %w", closeErr))
		}
	}()
	readerAt, ok := file.(io.ReaderAt)
	if !ok {
		return errors.New("open snapshot for validation: random access unavailable")
	}
	return validateOSVZipSnapshot(readerAt, sizeBytes)
}

func validateOSVZipSnapshot(reader io.ReaderAt, sizeBytes int64) error {
	archive, err := zip.NewReader(reader, sizeBytes)
	if err != nil {
		return fmt.Errorf("open ZIP archive: %w", err)
	}
	if err := validateOSVZipBounds(archive.File, sizeBytes); err != nil {
		return err
	}
	jsonEntries := 0
	for _, entry := range archive.File {
		isJSON := !entry.FileInfo().IsDir() && strings.EqualFold(filepath.Ext(entry.Name), ".json")
		if isJSON && entry.UncompressedSize64 > uint64(maxSyncMetadataBytes) {
			return fmt.Errorf("zip archive JSON entry %q exceeds %d-byte limit", entry.Name, maxSyncMetadataBytes)
		}
		if err := validateOSVZipEntry(entry, isJSON); err != nil {
			return err
		}
		if isJSON {
			jsonEntries++
		}
	}
	if jsonEntries == 0 {
		return errors.New("archive contains no valid OSV advisory JSON entries")
	}
	return nil
}

func validateOSVZipBounds(entries []*zip.File, sizeBytes int64) error {
	if len(entries) > maxOSVZipEntries {
		return fmt.Errorf("zip archive contains %d entries; limit is %d", len(entries), maxOSVZipEntries)
	}
	expandedSize := uint64(0)
	for _, entry := range entries {
		if entry.UncompressedSize64 > maxOSVZipExpandedSize-expandedSize {
			return fmt.Errorf("zip archive expanded size exceeds %d-byte limit", maxOSVZipExpandedSize)
		}
		expandedSize += entry.UncompressedSize64
	}
	if sizeBytes > 0 {
		compressedSize := uint64(sizeBytes)
		if compressedSize <= maxOSVZipExpandedSize/maxOSVZipExpandRatio && expandedSize > compressedSize*maxOSVZipExpandRatio {
			return fmt.Errorf("zip archive expanded size exceeds %dx compressed size", maxOSVZipExpandRatio)
		}
	}
	return nil
}

func validateOSVZipEntry(entry *zip.File, validateJSON bool) error {
	contents, err := entry.Open()
	if err != nil {
		return fmt.Errorf("open ZIP entry %q: %w", entry.Name, err)
	}
	tracked := &zipEntryReadTracker{reader: contents}
	var validationErr error
	if validateJSON {
		validationErr = validateOSVJSONSnapshot(io.LimitReader(tracked, maxSyncMetadataBytes+1))
	}
	_, drainErr := io.Copy(io.Discard, tracked)
	closeErr := contents.Close()
	if tracked.err != nil || drainErr != nil || closeErr != nil {
		return fmt.Errorf("read ZIP entry %q: %w", entry.Name, errors.Join(tracked.err, drainErr, closeErr))
	}
	if validationErr != nil {
		return fmt.Errorf("validate OSV JSON ZIP entry %q: %w", entry.Name, validationErr)
	}
	return nil
}

type zipEntryReadTracker struct {
	reader io.Reader
	err    error
}

func (r *zipEntryReadTracker) Read(buffer []byte) (int, error) {
	count, err := r.reader.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		r.err = err
	}
	return count, err
}

func validateOSVJSONSnapshot(reader io.Reader) error {
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("read top-level JSON value: %w", err)
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return fmt.Errorf("top-level JSON value must be an advisory array, advisory object, or object with a vulns array")
	}
	switch delim {
	case '[':
		err = validateOSVJSONEntries(decoder)
	case '{':
		err = validateOSVJSONTopLevelObject(decoder)
	default:
		err = fmt.Errorf("top-level JSON value must be an advisory array, advisory object, or object with a vulns array")
	}
	if err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

type osvJSONAdvisoryShape struct {
	idSeen         bool
	affectedSeen   bool
	usableAffected bool
}

type osvJSONAffectedShape struct {
	packageSeen       bool
	packageNamed      bool
	versionsSeen      bool
	versionConstraint bool
	rangesSeen        bool
	rangeConstraint   bool
}

func validateOSVJSONTopLevelObject(decoder *json.Decoder) error {
	foundVulns := false
	shape := osvJSONAdvisoryShape{}
	for decoder.More() {
		name, err := readJSONObjectName(decoder)
		if err != nil {
			return err
		}
		if name == "vulns" {
			if foundVulns {
				return errors.New("duplicate vulns field")
			}
			if err := validateOSVJSONVulns(decoder); err != nil {
				return err
			}
			foundVulns = true
			continue
		}
		if err := readOSVJSONAdvisoryField(decoder, name, &shape); err != nil {
			return err
		}
	}
	if err := requireJSONDelimiter(decoder, '}'); err != nil {
		return err
	}
	if foundVulns {
		return nil
	}
	return requireUsableOSVJSONAdvisory(shape)
}

func validateOSVJSONVulns(decoder *json.Decoder) error {
	if err := requireJSONDelimiter(decoder, '['); err != nil {
		return fmt.Errorf("vulns must be an array: %w", err)
	}
	return validateOSVJSONEntries(decoder)
}

func validateOSVJSONEntries(decoder *json.Decoder) error {
	for decoder.More() {
		if err := requireJSONDelimiter(decoder, '{'); err != nil {
			return fmt.Errorf("osv advisory entries must be objects: %w", err)
		}
		if err := validateOSVJSONAdvisoryObject(decoder); err != nil {
			return fmt.Errorf("invalid OSV advisory entry: %w", err)
		}
	}
	return requireJSONDelimiter(decoder, ']')
}

func validateOSVJSONAdvisoryObject(decoder *json.Decoder) error {
	shape := osvJSONAdvisoryShape{}
	for decoder.More() {
		name, err := readJSONObjectName(decoder)
		if err != nil {
			return err
		}
		if err := readOSVJSONAdvisoryField(decoder, name, &shape); err != nil {
			return err
		}
	}
	if err := requireJSONDelimiter(decoder, '}'); err != nil {
		return err
	}
	return requireUsableOSVJSONAdvisory(shape)
}

func readOSVJSONAdvisoryField(decoder *json.Decoder, name string, shape *osvJSONAdvisoryShape) error {
	switch name {
	case "id":
		if shape.idSeen {
			return errors.New("duplicate advisory id field")
		}
		if err := readNonblankJSONString(decoder, "advisory id"); err != nil {
			return err
		}
		shape.idSeen = true
	case "affected":
		if shape.affectedSeen {
			return errors.New("duplicate advisory affected field")
		}
		usable, err := validateOSVJSONAffectedEntries(decoder)
		if err != nil {
			return err
		}
		shape.affectedSeen = true
		shape.usableAffected = usable
	default:
		if err := discardJSONValue(decoder); err != nil {
			return fmt.Errorf("read advisory field %q: %w", name, err)
		}
	}
	return nil
}

func requireUsableOSVJSONAdvisory(shape osvJSONAdvisoryShape) error {
	if !shape.idSeen {
		return errors.New("advisory is missing a nonblank string id")
	}
	if !shape.affectedSeen {
		return errors.New("advisory is missing an affected array")
	}
	if !shape.usableAffected {
		return errors.New("advisory has no affected package with version constraints")
	}
	return nil
}

func validateOSVJSONAffectedEntries(decoder *json.Decoder) (bool, error) {
	if err := requireJSONDelimiter(decoder, '['); err != nil {
		return false, fmt.Errorf("affected must be an array: %w", err)
	}
	usable := false
	for decoder.More() {
		if err := requireJSONDelimiter(decoder, '{'); err != nil {
			return false, fmt.Errorf("affected entries must be objects: %w", err)
		}
		entryUsable, err := validateOSVJSONAffectedObject(decoder)
		if err != nil {
			return false, err
		}
		usable = usable || entryUsable
	}
	return usable, requireJSONDelimiter(decoder, ']')
}

func validateOSVJSONAffectedObject(decoder *json.Decoder) (bool, error) {
	shape := osvJSONAffectedShape{}
	for decoder.More() {
		name, err := readJSONObjectName(decoder)
		if err != nil {
			return false, err
		}
		if err := readOSVJSONAffectedField(decoder, name, &shape); err != nil {
			return false, err
		}
	}
	if err := requireJSONDelimiter(decoder, '}'); err != nil {
		return false, err
	}
	return shape.packageNamed && (shape.versionConstraint || shape.rangeConstraint), nil
}

func readOSVJSONAffectedField(decoder *json.Decoder, name string, shape *osvJSONAffectedShape) error {
	switch name {
	case "package":
		if shape.packageSeen {
			return errors.New("duplicate affected package field")
		}
		named, err := validateOSVJSONPackage(decoder)
		if err != nil {
			return err
		}
		shape.packageSeen = true
		shape.packageNamed = named
	case "versions":
		if shape.versionsSeen {
			return errors.New("duplicate affected versions field")
		}
		versioned, err := validateOSVJSONVersions(decoder)
		if err != nil {
			return err
		}
		shape.versionsSeen = true
		shape.versionConstraint = versioned
	case "ranges":
		if shape.rangesSeen {
			return errors.New("duplicate affected ranges field")
		}
		ranged, err := validateOSVJSONRanges(decoder)
		if err != nil {
			return err
		}
		shape.rangesSeen = true
		shape.rangeConstraint = ranged
	default:
		if err := discardJSONValue(decoder); err != nil {
			return fmt.Errorf("read affected field %q: %w", name, err)
		}
	}
	return nil
}

func validateOSVJSONPackage(decoder *json.Decoder) (bool, error) {
	if err := requireJSONDelimiter(decoder, '{'); err != nil {
		return false, fmt.Errorf("affected package must be an object: %w", err)
	}
	foundName := false
	for decoder.More() {
		name, err := readJSONObjectName(decoder)
		if err != nil {
			return false, err
		}
		if name != "name" {
			if err := discardJSONValue(decoder); err != nil {
				return false, fmt.Errorf("read package field %q: %w", name, err)
			}
			continue
		}
		if foundName {
			return false, errors.New("duplicate package name field")
		}
		if err := readNonblankJSONString(decoder, "package name"); err != nil {
			return false, err
		}
		foundName = true
	}
	return foundName, requireJSONDelimiter(decoder, '}')
}

func validateOSVJSONVersions(decoder *json.Decoder) (bool, error) {
	if err := requireJSONDelimiter(decoder, '['); err != nil {
		return false, fmt.Errorf("affected versions must be an array: %w", err)
	}
	foundVersion := false
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return false, err
		}
		version, ok := token.(string)
		if !ok {
			return false, errors.New("affected versions must contain strings")
		}
		foundVersion = foundVersion || strings.TrimSpace(version) != ""
	}
	return foundVersion, requireJSONDelimiter(decoder, ']')
}

func validateOSVJSONRanges(decoder *json.Decoder) (bool, error) {
	if err := requireJSONDelimiter(decoder, '['); err != nil {
		return false, fmt.Errorf("affected ranges must be an array: %w", err)
	}
	foundRange := false
	for decoder.More() {
		if err := requireJSONDelimiter(decoder, '{'); err != nil {
			return false, fmt.Errorf("affected ranges must contain objects: %w", err)
		}
		if err := discardOpenedJSONValue(decoder); err != nil {
			return false, fmt.Errorf("read affected range: %w", err)
		}
		foundRange = true
	}
	return foundRange, requireJSONDelimiter(decoder, ']')
}

func readNonblankJSONString(decoder *json.Decoder, field string) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	value, ok := token.(string)
	if !ok || strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must be a nonblank string", field)
	}
	return nil
}

func readJSONObjectName(decoder *json.Decoder) (string, error) {
	token, err := decoder.Token()
	if err != nil {
		return "", err
	}
	name, ok := token.(string)
	if !ok {
		return "", errors.New("json object field name must be a string")
	}
	return name, nil
}

func discardJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' && delim != '[' {
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
	return discardOpenedJSONValue(decoder)
}

func discardOpenedJSONValue(decoder *json.Decoder) error {
	depth := 1
	for depth > 0 {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}

func requireJSONDelimiter(decoder *json.Decoder, want json.Delim) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != want {
		return fmt.Errorf("expected %q, got %v", want, token)
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read trailing JSON data: %w", err)
	}
	return fmt.Errorf("unexpected trailing JSON value starting with %v", token)
}

func snapshotEntryCount(data []byte) int {
	return len(snapshotOSVAdvisories(data))
}

func snapshotEcosystems(data []byte) []string {
	seen := map[string]struct{}{}
	for _, advisory := range snapshotOSVAdvisories(data) {
		for _, affected := range advisory.Affected {
			ecosystem := strings.TrimSpace(affected.Package.Ecosystem)
			if ecosystem != "" {
				seen[ecosystem] = struct{}{}
			}
		}
	}
	items := make([]string, 0, len(seen))
	for ecosystem := range seen {
		items = append(items, ecosystem)
	}
	sort.Strings(items)
	return items
}

func snapshotOSVAdvisories(data []byte) []osvAdvisory {
	var list []osvAdvisory
	if err := json.Unmarshal(data, &list); err == nil {
		return list
	}
	var wrapped struct {
		Vulns json.RawMessage `json:"vulns"`
	}
	if err := json.Unmarshal(data, &wrapped); err == nil && len(wrapped.Vulns) > 0 {
		var vulns []osvAdvisory
		if err := json.Unmarshal(wrapped.Vulns, &vulns); err == nil {
			return vulns
		}
	}
	var single osvAdvisory
	if err := json.Unmarshal(data, &single); err == nil && len(advisoriesFromOSV([]osvAdvisory{single})) > 0 {
		return []osvAdvisory{single}
	}
	return nil
}
