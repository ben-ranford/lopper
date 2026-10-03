package python

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestVisitRequirementIdentityPinsPreservesLegacyMatching(t *testing.T) {
	for _, tc := range []struct {
		label, text string
		want        [][2]string
	}{
		{"empty", "", nil},
		{"comments and options", "# requests==1\n--index-url=https://example.test/==1\n-r other.txt\n-c constraints.txt\n", nil},
		{"ordinary pin", "requests==2.32.3\n", [][2]string{{"requests", "2.32.3"}}},
		{"extras and CRLF", "\tRequests._-Name [security, socks] == 2.0\r\n", [][2]string{{"Requests._-Name", "2.0"}}},
		{"marker prefix", "requests==2.0; python_version < '3.12'\n", [][2]string{{"requests", "2.0"}}},
		{"comment prefix", "requests==2.0#comment\n", [][2]string{{"requests", "2.0"}}},
		{"trailing options", "requests==2.0 --hash=sha256:unused\n", [][2]string{{"requests", "2.0"}}},
		{"extra equals", "requests===2.0\nrequests====\n", [][2]string{{"requests", "=2.0"}, {"requests", "=="}}},
		{"wildcard and invalid version", "requests==2.*\nother==not-a-version\n", [][2]string{{"requests", "2.*"}, {"other", "not-a-version"}}},
		{"unvalidated suffix", "requests==2.0,!=2.1\n", [][2]string{{"requests", "2.0,!=2.1"}}},
		{"continuation prefix", "requests==\\\n2.0\n", [][2]string{{"requests", "\\"}}},
		{"space extras", "requests[ ]==2.0", [][2]string{{"requests", "2.0"}}},
		{"hyphenated extras", "requests[hyphen-extra]==2.0\n", [][2]string{{"requests", "2.0"}}},
		{"empty extras", "requests[]==2.0\n", nil},
		{"not exact operator", "requests>=2\nrequests @ https://example.test/==2\n", nil},
		{"empty version", "requests==\nrequests==#comment\nrequests==;marker\n", nil},
		{"invalid name", "-requests==2.0\n.requests==2.0\n", nil},
		{"ASCII whitespace only", "requests\u00a0==2\nother==\u00a02", [][2]string{{"other", "\u00a02"}}},
		{"conflicts and duplicates stay ordered", "requests==2\nrequests==1\nrequests==2\n", [][2]string{{"requests", "2"}, {"requests", "1"}, {"requests", "2"}}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			got := collectRequirementIdentityPinsForTest(tc.text)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("identity pins = %#v, want %#v", got, tc.want)
			}
			compact := compactRequirementsIdentityText(tc.text)
			if reparsed := collectRequirementIdentityPinsForTest(compact); !reflect.DeepEqual(reparsed, tc.want) {
				t.Fatalf("compact projection changed pins: %q -> %#v, want %#v", compact, reparsed, tc.want)
			}
		})
	}
}

func TestRequirementsIdentityProjectionDiscardsCommentsAndOptionsBeforeBudget(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, pythonRequirementsTxt)
	ignored := "# " + strings.Repeat("large comment ", 1024) + "\n--index-url=https://example.test/" + strings.Repeat("index/", 1024) + "\n-r other.txt\n"
	content := ignored + "requests[security]==2.32.3; python_version >= '3.10' # marker\nother===1.* --hash=sha256:unused\nrequests==2.31.0\n"
	testutil.MustWriteFile(t, path, content)
	catalog := newPackagingCatalog()
	catalog.bytes = maxPackagingCatalogBytes
	catalog.identityBytes = maxPackagingIdentityProjectionBytes - 128
	initial, err := catalog.read(repo, path)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Text != content {
		t.Fatal("identity compaction changed the initial inventory document")
	}
	stored := catalog.documents[path]
	if !stored.Deferred || !stored.IdentityProjectionSet || stored.IdentityProjectionError != "" {
		t.Fatalf("unused requirements text exhausted the projection budget: %+v", stored)
	}
	if stored.IdentityText != "requests==2.32.3\nother===1.*\nrequests==2.31.0\n" {
		t.Fatalf("compact requirements text = %q", stored.IdentityText)
	}
	if catalog.identityBytes <= maxPackagingIdentityProjectionBytes-128 || catalog.identityBytes > maxPackagingIdentityProjectionBytes {
		t.Fatalf("projection accounting = %d", catalog.identityBytes)
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	var cached report.PythonManifestDocument
	if err := json.Unmarshal(encoded, &cached); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got, want := collectRequirementIdentityPinsForTest(cached.IdentityText), collectRequirementIdentityPinsForTest(content); !reflect.DeepEqual(got, want) {
		t.Fatalf("cached projection changed identity pins: %#v, want %#v", got, want)
	}
}

func collectRequirementIdentityPinsForTest(text string) [][2]string {
	var pins [][2]string
	VisitRequirementIdentityPins(text, func(name, version string) {
		pins = append(pins, [2]string{name, version})
	})
	return pins
}
