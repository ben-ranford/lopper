package advisory

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"unsafe"
)

func TestValidateDownloadedOSVZipErrors(t *testing.T) {
	payload := testOSVZip(t, "GO-2021-0113.json", testOSVAdvisory("GO-2021-0113"))
	assertSnapshotOpenFailures(t, func(openSnapshot snapshotOpener) error {
		return validateDownloadedOSVZip(openSnapshot, int64(len(payload)), &osvZipInventory{})
	})
	for _, tc := range []struct {
		name         string
		openSnapshot snapshotOpener
		wantError    string
	}{
		{
			name: "random access unavailable",
			openSnapshot: func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(payload)), nil
			},
			wantError: "random access unavailable",
		},
		{
			name: "close failure",
			openSnapshot: func() (io.ReadCloser, error) {
				return &errCloseReaderAt{Reader: bytes.NewReader(payload)}, nil
			},
			wantError: "close snapshot after validation",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDownloadedOSVZip(tc.openSnapshot, int64(len(payload)), &osvZipInventory{})
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("expected %q validation error, got %v", tc.wantError, err)
			}
		})
	}
}

func TestValidateOSVZipSnapshotRejectsUnusableArchives(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload []byte
	}{
		{name: "malformed", payload: []byte("PK\x03\x04zip")},
		{name: "no JSON entries", payload: testOSVZip(t, "README.txt", "not an advisory")},
		{name: "directory only", payload: testOSVZip(t, "nested/", "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateOSVZipSnapshot(bytes.NewReader(tc.payload), int64(len(tc.payload)), &osvZipInventory{}); err == nil {
				t.Fatal("expected unusable ZIP archive to be rejected")
			}
		})
	}
}

func TestValidateOSVZipSnapshotValidatesEveryEntry(t *testing.T) {
	t.Run("multiple valid entries", func(t *testing.T) {
		payload := testOSVZipEntries(t, testOSVZipEntry{name: "GO-1.json", payload: testOSVAdvisory("GO-1"), method: zip.Deflate}, testOSVZipEntry{name: "README.txt", payload: "OSV snapshot", method: zip.Store}, testOSVZipEntry{name: "GO-2.json", payload: testOSVAdvisory("GO-2"), method: zip.Deflate})
		if err := validateOSVZipSnapshot(bytes.NewReader(payload), int64(len(payload)), &osvZipInventory{}); err != nil {
			t.Fatalf("validate complete OSV ZIP snapshot: %v", err)
		}
	})

	t.Run("invalid later JSON", func(t *testing.T) {
		payload := testOSVZipEntries(t, testOSVZipEntry{name: "GO-1.json", payload: testOSVAdvisory("GO-1"), method: zip.Deflate}, testOSVZipEntry{name: "response.json", payload: `{"error":"quota exceeded"}`, method: zip.Deflate})
		if err := validateOSVZipSnapshot(bytes.NewReader(payload), int64(len(payload)), &osvZipInventory{}); err == nil {
			t.Fatal("expected invalid later JSON entry to be rejected")
		}
	})

	t.Run("corrupt later entry", func(t *testing.T) {
		laterPayload := "later entry must be checksum verified"
		payload := testOSVZipEntries(t, testOSVZipEntry{name: "GO-1.json", payload: testOSVAdvisory("GO-1"), method: zip.Deflate}, testOSVZipEntry{name: "metadata.txt", payload: laterPayload, method: zip.Store})
		payloadOffset := bytes.Index(payload, []byte(laterPayload))
		if payloadOffset < 0 {
			t.Fatal("locate stored ZIP entry payload")
		}
		payload[payloadOffset] ^= 0xff

		if err := validateOSVZipSnapshot(bytes.NewReader(payload), int64(len(payload)), &osvZipInventory{}); !errors.Is(err, zip.ErrChecksum) {
			t.Fatalf("expected later ZIP checksum error, got %v", err)
		}
	})

	t.Run("excessive expansion", func(t *testing.T) {
		largeAdvisory := strings.Replace(testOSVAdvisory("GO-1"), `"affected":`, `"details":"`+strings.Repeat("a", 2*1024*1024)+`","affected":`, 1)
		payload := testOSVZip(t, "GO-1.json", largeAdvisory)
		if err := validateOSVZipSnapshot(bytes.NewReader(payload), int64(len(payload)), &osvZipInventory{}); err == nil {
			t.Fatal("expected excessive ZIP expansion to be rejected")
		}
	})
}

func TestOSVZipInventoryBoundsAndNormalization(t *testing.T) {
	inventory := osvZipInventory{ecosystems: map[string]struct{}{}, ecosystemBytes: maxOSVZipEcosystemBytes - 132}
	if err := inventory.addEcosystem("  Go  "); err != nil {
		t.Fatal(err)
	}
	for _, ecosystem := range []string{"", " ", "Go"} {
		if err := inventory.addEcosystem(ecosystem); err != nil {
			t.Fatal(err)
		}
	}
	if err := inventory.addEcosystem("PyPI"); err == nil {
		t.Fatal("expected ecosystem metadata budget to be enforced")
	}
	payload := []byte(strings.ReplaceAll(testOSVAdvisory("PY-1"), `"Go"`, `"PyPI"`))
	if err := inspectOSVJSONSnapshot(bytes.NewReader(payload), &inventory); err == nil {
		t.Fatal("expected entry collection to enforce metadata budget")
	}
}

func TestOSVZipInventoryEscapedManifestBudget(t *testing.T) {
	inventory := osvZipInventory{ecosystemBytes: maxOSVZipEcosystemBytes - 132}
	if err := inventory.addEcosystem("<"); err == nil {
		t.Fatal("expected JSON escaping to count against manifest budget")
	}
}

func TestOSVZipInventoryRejectsMalformedEcosystem(t *testing.T) {
	payload := strings.ReplaceAll(testOSVAdvisory("GO-1"), `"Go"`, `{}`)
	if err := inspectOSVJSONSnapshot(strings.NewReader(payload), &osvZipInventory{}); err == nil {
		t.Fatal("expected non-string ecosystem to be rejected")
	}
}

func TestOSVZipInventoryDoesNotRetainEcosystemPadding(t *testing.T) {
	padded := strings.Repeat(" ", 1024*1024) + "Go" + strings.Repeat(" ", 1024*1024)
	inventory := osvZipInventory{}
	if err := inventory.addEcosystem(padded); err != nil {
		t.Fatal(err)
	}
	ecosystems := inventory.sortedEcosystems()
	if len(ecosystems) != 1 || ecosystems[0] != "Go" {
		t.Fatalf("expected normalized Go ecosystem, got %v", ecosystems)
	}
	// Pointer equality observes retention without GC timing or heap-size assumptions.
	if unsafe.StringData(ecosystems[0]) == unsafe.StringData(strings.TrimSpace(padded)) {
		t.Fatal("normalized ecosystem retains the padded input allocation")
	}
	if inventory.ecosystemBytes != len(`"Go"`)+128 {
		t.Fatalf("unexpected normalized ecosystem budget: %d", inventory.ecosystemBytes)
	}
}
