package report

import (
	"encoding/json"
	"net/url"
	"path"
	"testing"
)

func TestSARIFArtifactLocationsPreserveFilenameURISemantics(t *testing.T) {
	cases := []struct{ file, uri, decodedPath string }{
		{"src/main.ts", "src/main.ts", "src/main.ts"},
		{"src/100%.ts", "src/100%25.ts", "src/100%.ts"},
		{"src/space name.ts", "src/space%20name.ts", "src/space name.ts"},
		{" leading.ts ", "%20leading.ts%20", " leading.ts "},
		{"src/café.ts", "src/caf%C3%A9.ts", "src/café.ts"},
		{"src/\xff.ts", "src/%FF.ts", "src/\xff.ts"},
		{"src/hash#part?.ts", "src/hash%23part%3F.ts", "src/hash#part?.ts"},
		{"javascript:alert(1).js", "./javascript:alert%281%29.js", "javascript:alert(1).js"},
		{"https://example.com/file.ts?x#y", "./https:/example.com/file.ts%3Fx%23y", "https:/example.com/file.ts?x#y"},
		{"%2e%2e/secret.ts", "%252e%252e/secret.ts", "%2e%2e/secret.ts"},
		{"pkg/sub/../file.ts", "pkg/file.ts", "pkg/file.ts"},
		{`pkg\file.ts`, "pkg/file.ts", "pkg/file.ts"},
		{"C:relative.ts", "./C:relative.ts", "C:relative.ts"},
		{"src/new\nline.ts", "src/new%0Aline.ts", "src/new\nline.ts"},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			assertSARIFRelativeFilename(t, tc.file, tc.uri, tc.decodedPath)
		})
	}
}

func assertSARIFRelativeFilename(t *testing.T, file, uri, decodedPath string) {
	t.Helper()
	base := &url.URL{Scheme: "file", Path: "/repo/"}
	result := sarifFilenameResult(t, file)
	if len(result.Locations) != 1 {
		t.Fatalf("expected one location: %#v", result)
	}
	location := result.Locations[0].PhysicalLocation
	if location.ArtifactLocation.URI != uri {
		t.Fatalf("uri = %q, want %q", location.ArtifactLocation.URI, uri)
	}
	parsed, err := url.Parse(location.ArtifactLocation.URI)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "" || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		t.Fatalf("filename acquired URI semantics: %#v", parsed)
	}
	if resolved := base.ResolveReference(parsed); resolved.Path != "/repo/"+decodedPath {
		t.Fatalf("URI resolves to %q, want repository filename %q", resolved, decodedPath)
	}
	if location.Region == nil || location.Region.StartLine != 7 || location.Region.StartColumn != 3 {
		t.Fatalf("lost source region: %#v", location.Region)
	}
}

func TestSARIFArtifactLocationsRejectEscapingTraversal(t *testing.T) {
	for _, file := range []string{"../secret.ts", "pkg/../../secret.ts", `..\secret.ts`, ".", "pkg/..", "", "   "} {
		t.Run(file, func(t *testing.T) {
			if result := sarifFilenameResult(t, file); len(result.Locations) != 0 {
				t.Fatalf("unsafe location remained attached to finding: %#v", result.Locations)
			}
		})
	}
}

func TestSARIFArtifactLocationsPreserveLocalAbsoluteFiles(t *testing.T) {
	for _, file := range []string{"/tmp/space #?.ts", `C:\tmp\space #?.ts`, `\\server\share\space #?.ts`} {
		t.Run(file, func(t *testing.T) {
			result := sarifFilenameResult(t, file)
			if len(result.Locations) != 1 {
				t.Fatalf("expected local absolute location: %#v", result)
			}
			parsed, err := url.Parse(result.Locations[0].PhysicalLocation.ArtifactLocation.URI)
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Scheme != "file" || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" || path.Base(parsed.Path) != "space #?.ts" {
				t.Fatalf("absolute filename gained external URI semantics: %#v", parsed)
			}
		})
	}
}

func sarifFilenameResult(t *testing.T, file string) sarifResult {
	t.Helper()
	rep := Report{Dependencies: []DependencyReport{{Name: "pkg", Language: "js-ts", UnusedImports: []ImportUse{{Name: "unused", Module: "pkg", Locations: []Location{{File: file, Line: 7, Column: 3}}}}}}}
	formatted, err := NewFormatter().Format(rep, FormatSARIF)
	if err != nil {
		t.Fatal(err)
	}
	assertSARIFSchema(t, formatted)
	var log sarifLog
	if err := json.Unmarshal([]byte(formatted), &log); err != nil {
		t.Fatal(err)
	}
	if len(log.Runs) != 1 || len(log.Runs[0].Results) != 1 {
		t.Fatalf("expected one retained finding: %#v", log.Runs)
	}
	return log.Runs[0].Results[0]
}

func TestDependencyAnchorSkipsRejectedSortedLocations(t *testing.T) {
	for _, locations := range [][]Location{
		{{File: "../outside.go", Line: 1}, {File: "src/main.go", Line: 9}, {File: "src/z.go", Line: 2}},
		{{File: "src/z.go", Line: 2}, {File: "src/main.go", Line: 9}, {File: "../outside.go", Line: 1}},
	} {
		dep := DependencyReport{UsedImports: []ImportUse{{Locations: locations[:1]}}, UnusedImports: []ImportUse{{Locations: locations[1:]}}}
		anchor := dependencyAnchorLocation(dep)
		if anchor == nil || anchor.PhysicalLocation.Region == nil {
			t.Fatalf("expected valid anchor after rejected location, got %#v", anchor)
		}
		if got := anchor.PhysicalLocation; got.ArtifactLocation.URI != "src/main.go" || got.Region.StartLine != 9 {
			t.Fatalf("expected earliest valid source location, got %#v", got)
		}
	}
}
