package report

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	baselineutil "github.com/ben-ranford/lopper/internal/baseline"
)

type BaselineSnapshotMetadata struct {
	Key                   string    `json:"key"`
	KeyType               string    `json:"keyType"`
	Label                 string    `json:"label,omitempty"`
	Commit                string    `json:"commit,omitempty"`
	CreatedAt             time.Time `json:"createdAt"`
	BaselineSchemaVersion string    `json:"baselineSchemaVersion"`
	ReportSchemaVersion   string    `json:"reportSchemaVersion"`
	RepoIdentity          string    `json:"repoIdentity"`
	Summary               Summary   `json:"summary"`
	File                  string    `json:"file"`
}

type BaselineStoreDiagnostic struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

type BaselineCatalog struct {
	Store       string                     `json:"store"`
	Snapshots   []BaselineSnapshotMetadata `json:"snapshots"`
	Diagnostics []BaselineStoreDiagnostic  `json:"diagnostics"`
}

func ListBaselineSnapshots(dir string, limit int) (BaselineCatalog, error) {
	if limit <= 0 {
		return BaselineCatalog{}, fmt.Errorf("baseline list limit must be greater than zero")
	}
	catalog := BaselineCatalog{
		Store:       strings.TrimSpace(dir),
		Snapshots:   []BaselineSnapshotMetadata{},
		Diagnostics: []BaselineStoreDiagnostic{},
	}
	names, err := baselineutil.ListStoreEntries(dir)
	if err != nil {
		return BaselineCatalog{}, err
	}
	for _, name := range names {
		if !strings.EqualFold(filepath.Ext(name), ".json") {
			continue
		}
		data, readErr := baselineutil.ReadStoreEntry(dir, name, baselineutil.MaxSnapshotBytes)
		if readErr != nil {
			catalog.Diagnostics = append(catalog.Diagnostics, baselineDiagnostic(name, readErr))
			continue
		}
		metadata, decodeErr := decodeBaselineSnapshotMetadata(name, data)
		if decodeErr != nil {
			catalog.Diagnostics = append(catalog.Diagnostics, baselineDiagnostic(name, decodeErr))
			continue
		}
		catalog.Snapshots = append(catalog.Snapshots, metadata)
	}
	sort.Slice(catalog.Snapshots, func(i, j int) bool {
		left, right := catalog.Snapshots[i], catalog.Snapshots[j]
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.After(right.CreatedAt)
		}
		if left.Key != right.Key {
			return left.Key < right.Key
		}
		return left.File < right.File
	})
	if len(catalog.Snapshots) > limit {
		catalog.Snapshots = catalog.Snapshots[:limit]
	}
	return catalog, nil
}

func InspectBaselineSnapshot(dir, key string) (BaselineSnapshotMetadata, error) {
	trimmedKey := strings.TrimSpace(key)
	path := ResolveBaselineSnapshotPath(dir, trimmedKey)
	name := filepath.Base(path)
	data, err := baselineutil.ReadStoreEntry(dir, name, baselineutil.MaxSnapshotBytes)
	if err != nil {
		return BaselineSnapshotMetadata{}, err
	}
	metadata, err := decodeBaselineSnapshotMetadata(name, data)
	if err != nil {
		return BaselineSnapshotMetadata{}, err
	}
	if err := ValidateBaselineSnapshotKey(trimmedKey, metadata.Key); err != nil {
		return BaselineSnapshotMetadata{}, err
	}
	return metadata, nil
}

func decodeBaselineSnapshotMetadata(name string, data []byte) (BaselineSnapshotMetadata, error) {
	var snapshot BaselineSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return BaselineSnapshotMetadata{}, fmt.Errorf("decode baseline snapshot: %w", err)
	}
	if snapshot.BaselineSchemaVersion == "" {
		return BaselineSnapshotMetadata{}, fmt.Errorf("missing baseline schema version")
	}
	if snapshot.BaselineSchemaVersion != BaselineSnapshotSchemaVersion {
		return BaselineSnapshotMetadata{}, fmt.Errorf("unsupported baseline schema version: %s", snapshot.BaselineSchemaVersion)
	}
	key := strings.TrimSpace(snapshot.Key)
	if key == "" {
		return BaselineSnapshotMetadata{}, fmt.Errorf("missing baseline snapshot key")
	}
	if name != baselineutil.SnapshotFileName(key) && name != baselineutil.LegacySnapshotFileName(key) {
		return BaselineSnapshotMetadata{}, fmt.Errorf("snapshot filename does not match embedded key %q", key)
	}
	if snapshot.SavedAt.IsZero() {
		return BaselineSnapshotMetadata{}, fmt.Errorf("missing baseline creation time")
	}
	if strings.TrimSpace(snapshot.Report.SchemaVersion) == "" {
		return BaselineSnapshotMetadata{}, fmt.Errorf("missing baseline report schema version")
	}
	if snapshot.Report.GeneratedAt.IsZero() {
		return BaselineSnapshotMetadata{}, fmt.Errorf("missing baseline report generation time")
	}
	repoIdentity := strings.TrimSpace(snapshot.Report.RepoPath)
	if repoIdentity == "" {
		return BaselineSnapshotMetadata{}, fmt.Errorf("missing baseline repository identity")
	}
	reportData := normalizeSnapshotReport(snapshot.Report)
	keyType, value := baselineKeyType(key)
	metadata := BaselineSnapshotMetadata{
		Key:                   key,
		KeyType:               keyType,
		CreatedAt:             snapshot.SavedAt.UTC(),
		BaselineSchemaVersion: snapshot.BaselineSchemaVersion,
		ReportSchemaVersion:   reportData.SchemaVersion,
		RepoIdentity:          repoIdentity,
		Summary:               *reportData.Summary,
		File:                  name,
	}
	if keyType == "label" {
		metadata.Label = value
	}
	if keyType == "commit" {
		metadata.Commit = value
	}
	return metadata, nil
}

func baselineKeyType(key string) (string, string) {
	prefix, value, found := strings.Cut(key, ":")
	if found {
		switch strings.ToLower(strings.TrimSpace(prefix)) {
		case "label":
			return "label", strings.TrimSpace(value)
		case "commit":
			return "commit", strings.TrimSpace(value)
		}
	}
	return "custom", key
}

func baselineDiagnostic(name string, err error) BaselineStoreDiagnostic {
	return BaselineStoreDiagnostic{File: name, Error: err.Error()}
}
