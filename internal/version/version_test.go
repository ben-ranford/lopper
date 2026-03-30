package version

import "testing"

func TestCurrentNormalizesReleaseVersion(t *testing.T) {
	originalVersion, originalCommit, originalBuildDate := version, commit, buildDate
	t.Cleanup(func() {
		version = originalVersion
		commit = originalCommit
		buildDate = originalBuildDate
	})

	version = "v1.2.1"
	commit = "abc1234"
	buildDate = "2026-03-30T06:50:54Z"

	got := Current()
	if got.Version != "1.2.1" {
		t.Fatalf("expected normalized release version, got %q", got.Version)
	}
	if got.Commit != "abc1234" {
		t.Fatalf("expected commit metadata, got %q", got.Commit)
	}
	if got.BuildDate != "2026-03-30T06:50:54Z" {
		t.Fatalf("expected build date metadata, got %q", got.BuildDate)
	}
}

func TestStringOmitsUnknownMetadata(t *testing.T) {
	originalVersion, originalCommit, originalBuildDate := version, commit, buildDate
	t.Cleanup(func() {
		version = originalVersion
		commit = originalCommit
		buildDate = originalBuildDate
	})

	version = "dev"
	commit = "unknown"
	buildDate = ""

	if got := String(); got != "lopper dev" {
		t.Fatalf("expected compact dev version string, got %q", got)
	}
}

func TestInfoStringIncludesMetadata(t *testing.T) {
	info := Info{
		Version:   "1.2.2",
		Commit:    "abc1234",
		BuildDate: "2026-03-30T08:44:43Z",
	}

	if got := info.String(); got != "lopper 1.2.2 (commit abc1234, built 2026-03-30T08:44:43Z)" {
		t.Fatalf("expected formatted metadata string, got %q", got)
	}
}

func TestNormalizeVersionDefaultsBlankToDev(t *testing.T) {
	if got := normalizeVersion("  \t  "); got != "dev" {
		t.Fatalf("expected blank version to normalize to dev, got %q", got)
	}
}
