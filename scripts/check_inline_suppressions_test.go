package scripts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func suppressionFingerprint(file, content string, occurrence int) string {
	payload := file + "\n" + content
	if occurrence > 1 {
		payload += fmt.Sprintf("\noccurrence:%d", occurrence)
	}
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

const mainGoPath = "main.go"

func TestInlineSuppressionCheckRejectsStagedMarkers(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		path    string
		content string
		want    string
	}{
		{
			name:    "nosec",
			path:    mainGoPath,
			content: mainGoWithComment("nosec G404"),
			want:    "//" + "nosec",
		},
		{
			name:    "NOSEC uppercase",
			path:    mainGoPath,
			content: mainGoWithComment("NOSEC G404"),
			want:    "//" + "NOSEC",
		},
		{
			name:    "NOSONAR uppercase block comment",
			path:    mainGoPath,
			content: mainGoWithBlockComment("NOSONAR"),
			want:    "NOSONAR",
		},
		{
			name:    "nolint",
			path:    mainGoPath,
			content: mainGoWithComment("nolint:staticcheck"),
			want:    "//" + "nolint",
		},
		{
			name:    "ts-ignore",
			path:    "main.ts",
			content: "const value = 1;\n// @" + "ts-ignore\nconsole.log(value as string);\n",
			want:    "@" + "ts-ignore",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repoDir := newInlineSuppressionRepo(t)
			writeFile(t, filepath.Join(repoDir, tc.path), tc.content)
			runCommand(t, repoDir, "git", "add", tc.path)

			output, err := runSuppressionCheck(repoDir)
			if err == nil {
				t.Fatalf("expected suppression check to fail for %s, output:\n%s", tc.name, output)
			}
			if !strings.Contains(output, tc.want) {
				t.Fatalf("expected output to mention %q, got:\n%s", tc.want, output)
			}
			if !strings.Contains(output, "Inline suppression markers require tracking metadata in staged changes.") {
				t.Fatalf("expected staged change failure message, got:\n%s", output)
			}
			if !strings.Contains(output, "Missing inline suppression tracking metadata") {
				t.Fatalf("expected missing metadata message, got:\n%s", output)
			}
		})
	}
}

func TestInlineSuppressionCheckRejectsWorkingTreeMarkers(t *testing.T) {
	t.Parallel()

	repoDir := newInlineSuppressionRepo(t)
	writeFile(t, filepath.Join(repoDir, mainGoPath), mainGoWithoutComment())
	runCommand(t, repoDir, "git", "add", mainGoPath)
	runCommand(t, repoDir, "git", "commit", "-m", "add source file")

	writeFile(t, filepath.Join(repoDir, mainGoPath), mainGoWithComment("nolint:staticcheck"))

	output, err := runSuppressionCheck(repoDir)
	if err == nil {
		t.Fatalf("expected working tree suppression check to fail, output:\n%s", output)
	}
	if !strings.Contains(output, "Inline suppression markers require tracking metadata in working tree changes.") {
		t.Fatalf("expected working tree failure message, got:\n%s", output)
	}
}

func TestInlineSuppressionCheckDetectsTrackedMarkerWithoutGitHubCredentials(t *testing.T) {
	t.Parallel()

	repoDir := newInlineSuppressionRepo(t)
	outputPath := filepath.Join(repoDir, ".artifacts", "inline-suppressions.json")
	writeFile(t, filepath.Join(repoDir, mainGoPath), mainGoWithTrackedSuppression("nolint:staticcheck"))
	runCommand(t, repoDir, "git", "add", mainGoPath)

	output, err := runSuppressionCheckWithEnv(repoDir,
		"GH_BIN="+filepath.Join(repoDir, "missing-gh"),
		"SUPPRESSION_TRACKING_OUTPUT="+outputPath,
		"SUPPRESSION_GITHUB_REPOSITORY=ben-ranford/lopper",
		"GITHUB_SHA=abc123",
		"GITHUB_SERVER_URL=https://github.com",
	)
	if err != nil {
		t.Fatalf("expected tokenless suppression detection to pass, output:\n%s", output)
	}
	if !strings.Contains(output, "Inline suppression metadata passed (staged changes)") {
		t.Fatalf("expected metadata pass message, got:\n%s", output)
	}

	records := readSuppressionRecords(t, outputPath)
	if len(records.Suppressions) != 1 {
		t.Fatalf("expected one suppression record, got %#v", records.Suppressions)
	}
	record := records.Suppressions[0]
	if record.File != mainGoPath || record.Line != 4 {
		t.Fatalf("record location = %s:%d, want main.go:4", record.File, record.Line)
	}
	if record.Source != "https://github.com/ben-ranford/lopper/blob/abc123/main.go#L4" {
		t.Fatalf("record source = %q", record.Source)
	}
	if record.Rationale != "temporary scanner false positive" || record.Owner != "@security" || record.RemoveWhen != "analyzer handles generated guard" {
		t.Fatalf("record metadata = %#v", record)
	}
}

func TestInlineSuppressionCheckMatchesUppercaseExtensionCaseInsensitively(t *testing.T) {
	t.Parallel()

	// The trusted tracker's fileExtension() lowercases before matching, so
	// it recognizes a suppression added to e.g. main.GO. This detector's
	// source-file scope must not silently skip such files while the
	// trusted tracker still finds and tracks them.
	repoDir := newInlineSuppressionRepo(t)
	outputPath := filepath.Join(repoDir, ".artifacts", "inline-suppressions.json")
	uppercasePath := "main.GO"
	writeFile(t, filepath.Join(repoDir, uppercasePath), mainGoWithTrackedSuppression("nolint:staticcheck"))
	runCommand(t, repoDir, "git", "add", uppercasePath)

	output, err := runSuppressionCheckWithEnv(repoDir, "SUPPRESSION_TRACKING_OUTPUT="+outputPath)
	if err != nil {
		t.Fatalf("expected uppercase-extension suppression detection to pass, output:\n%s", output)
	}

	records := readSuppressionRecords(t, outputPath)
	if len(records.Suppressions) != 1 {
		t.Fatalf("expected one suppression record for %s, got %#v", uppercasePath, records.Suppressions)
	}
	if records.Suppressions[0].File != uppercasePath {
		t.Fatalf("record file = %q, want %q", records.Suppressions[0].File, uppercasePath)
	}
}

func TestInlineSuppressionCheckDetectsAddedLineStartingWithDoublePlus(t *testing.T) {
	t.Parallel()

	// A raw diff "+" prefix over content that itself starts with "++" and
	// has no leading whitespace (e.g. an unindented added C/C++ increment
	// statement) produces a line beginning with three literal "+"
	// characters, colliding with the genuine "+++ b/..." file-header
	// exclusion unless that exclusion is scoped to just the header line.
	repoDir := newInlineSuppressionRepo(t)
	outputPath := filepath.Join(repoDir, ".artifacts", "inline-suppressions.json")
	marker := "nolint:staticcheck"
	line := "++counter; //" + marker + " // rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard"
	content := "package main\n\nfunc main() {\n" + line + "\n}\n"
	writeFile(t, filepath.Join(repoDir, mainGoPath), content)
	runCommand(t, repoDir, "git", "add", mainGoPath)

	output, err := runSuppressionCheckWithEnv(repoDir, "SUPPRESSION_TRACKING_OUTPUT="+outputPath)
	if err != nil {
		t.Fatalf("expected an added line starting with ++ to be detected, output:\n%s", output)
	}

	records := readSuppressionRecords(t, outputPath)
	if len(records.Suppressions) != 1 {
		t.Fatalf("expected one suppression record for a line beginning with ++, got %#v", records.Suppressions)
	}
}

func TestInlineSuppressionCheckDetectsTrackedMarkerInRenamedSource(t *testing.T) {
	t.Parallel()

	repoDir := newInlineSuppressionRepo(t)
	outputPath := filepath.Join(repoDir, ".artifacts", "inline-suppressions.json")
	writeFile(t, filepath.Join(repoDir, "old.go"), mainGoWithoutComment())
	runCommand(t, repoDir, "git", "add", "old.go")
	runCommand(t, repoDir, "git", "commit", "-m", "add old source")
	runCommand(t, repoDir, "git", "mv", "old.go", "renamed.go")
	writeFile(t, filepath.Join(repoDir, "renamed.go"), mainGoWithTrackedSuppression("nolint:staticcheck"))
	runCommand(t, repoDir, "git", "add", "renamed.go")

	output, err := runSuppressionCheckWithEnv(repoDir,
		"SUPPRESSION_TRACKING_OUTPUT="+outputPath,
		"SUPPRESSION_GITHUB_REPOSITORY=ben-ranford/lopper",
		"GITHUB_SHA=abc123",
	)
	if err != nil {
		t.Fatalf("expected renamed source suppression detection to pass, output:\n%s", output)
	}

	records := readSuppressionRecords(t, outputPath)
	if len(records.Suppressions) != 1 {
		t.Fatalf("expected one suppression record, got %#v", records.Suppressions)
	}
	record := records.Suppressions[0]
	if record.File != "renamed.go" || record.Line != 4 {
		t.Fatalf("record location = %s:%d, want renamed.go:4", record.File, record.Line)
	}
}

func TestInlineSuppressionCheckCreatesTrackingIssueForStagedMarker(t *testing.T) {
	t.Parallel()

	repoDir := newInlineSuppressionRepo(t)
	ghPath, logPath := newMockGH(t)
	writeFile(t, filepath.Join(repoDir, mainGoPath), mainGoWithTrackedSuppression("nolint:staticcheck"))
	runCommand(t, repoDir, "git", "add", mainGoPath)

	output, err := runSuppressionCheckWithEnv(repoDir,
		"GH_BIN="+ghPath,
		"SUPPRESSION_TRACKING_MODE=track",
		"SUPPRESSION_GITHUB_REPOSITORY=ben-ranford/lopper",
		"GITHUB_SHA=abc123",
		"GITHUB_SERVER_URL=https://github.com",
	)
	if err != nil {
		t.Fatalf("expected tracked suppression to pass, output:\n%s", output)
	}
	if !strings.Contains(output, "Opened GitHub tracking issue for inline suppression main.go:4") {
		t.Fatalf("expected created issue message, got:\n%s", output)
	}

	logContent := readFile(t, logPath)
	for _, want := range []string{
		"issue list",
		"issue create",
		"author:github-actions[bot]",
		`select(.author.login == "github-actions[bot]"`,
		"Location: `main.go:4`",
		"Source: https://github.com/ben-ranford/lopper/blob/abc123/main.go#L4",
		"Rationale: temporary scanner false positive",
		"Owner: @security",
		"Removal condition: analyzer handles generated guard",
	} {
		if !strings.Contains(logContent, want) {
			t.Fatalf("expected mock gh log to contain %q, got:\n%s", want, logContent)
		}
	}
}

func TestInlineSuppressionCheckIgnoresCodeSideAssignmentsBeforeMarker(t *testing.T) {
	t.Parallel()

	repoDir := newInlineSuppressionRepo(t)
	ghPath, logPath := newMockGH(t)
	marker := "nolint:staticcheck"
	content := "package main\n\nfunc main() {\n\towner := service //" + marker +
		" // rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard\n}\n"
	writeFile(t, filepath.Join(repoDir, mainGoPath), content)
	runCommand(t, repoDir, "git", "add", mainGoPath)

	output, err := runSuppressionCheckWithEnv(repoDir,
		"GH_BIN="+ghPath,
		"SUPPRESSION_TRACKING_MODE=track",
		"SUPPRESSION_GITHUB_REPOSITORY=ben-ranford/lopper",
		"GITHUB_SHA=abc123",
		"GITHUB_SERVER_URL=https://github.com",
	)
	if err != nil {
		t.Fatalf("expected tracked suppression to pass, output:\n%s", output)
	}

	logContent := readFile(t, logPath)
	if !strings.Contains(logContent, "Owner: @security") {
		t.Fatalf("expected owner taken from the suppression comment, got:\n%s", logContent)
	}
	if strings.Contains(logContent, "Owner: = service") || strings.Contains(logContent, "Owner: service") {
		t.Fatalf("expected the code-side `owner := service` assignment before the marker not to be treated as metadata, got:\n%s", logContent)
	}
}

func TestInlineSuppressionCheckUpdatesExistingTrackingIssue(t *testing.T) {
	t.Parallel()

	repoDir := newInlineSuppressionRepo(t)
	ghPath, logPath := newMockGH(t)
	writeFile(t, filepath.Join(repoDir, mainGoPath), mainGoWithTrackedSuppression("nosec G404"))
	runCommand(t, repoDir, "git", "add", mainGoPath)

	output, err := runSuppressionCheckWithEnv(repoDir,
		"GH_BIN="+ghPath,
		"SUPPRESSION_TRACKING_MODE=track",
		"GH_MOCK_EXISTING_ISSUE=77",
	)
	if err != nil {
		t.Fatalf("expected existing tracked suppression to pass, output:\n%s", output)
	}
	if !strings.Contains(output, "Updated GitHub tracking issue #77 for inline suppression main.go:4") {
		t.Fatalf("expected updated issue message, got:\n%s", output)
	}

	logContent := readFile(t, logPath)
	if !strings.Contains(logContent, "issue comment 77") {
		t.Fatalf("expected mock gh to comment on issue 77, got:\n%s", logContent)
	}
	if strings.Contains(logContent, "issue create") {
		t.Fatalf("did not expect mock gh to create a new issue, got:\n%s", logContent)
	}
}

func TestInlineSuppressionCheckFailsClosedWhenTrackingIssueCannotBeCreated(t *testing.T) {
	t.Parallel()

	repoDir := newInlineSuppressionRepo(t)
	ghPath, _ := newMockGH(t)
	writeFile(t, filepath.Join(repoDir, mainGoPath), mainGoWithTrackedSuppression("nolint:staticcheck"))
	runCommand(t, repoDir, "git", "add", mainGoPath)

	output, err := runSuppressionCheckWithEnv(repoDir,
		"GH_BIN="+ghPath,
		"SUPPRESSION_TRACKING_MODE=track",
		"GH_MOCK_FAIL_CREATE=1",
	)
	if err == nil {
		t.Fatalf("expected suppression check to fail when issue creation fails, output:\n%s", output)
	}
	if !strings.Contains(output, "Unable to create GitHub tracking issue for inline suppression main.go:4") {
		t.Fatalf("expected create failure message, got:\n%s", output)
	}
	if !strings.Contains(output, "new inline suppressions fail closed") {
		t.Fatalf("expected fail-closed guidance, got:\n%s", output)
	}
}

func TestInlineSuppressionCheckReusesFingerprintAcrossLineMoves(t *testing.T) {
	t.Parallel()

	firstFingerprint := detectSuppressionFingerprint(t, mainGoWithTrackedSuppression("nolint:staticcheck"))
	moved := "package main\n\nfunc helper() {}\n\nfunc main() {\n\t_ = 1 //" + "nolint:staticcheck // rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard\n}\n"
	movedFingerprint := detectSuppressionFingerprint(t, moved)

	if firstFingerprint != movedFingerprint {
		t.Fatalf("fingerprint changed after line move: %s != %s", firstFingerprint, movedFingerprint)
	}
}

func TestInlineSuppressionCheckTracksDuplicateSuppressionsSeparately(t *testing.T) {
	t.Parallel()

	repoDir := newInlineSuppressionRepo(t)
	ghPath, logPath := newMockGH(t)
	outputPath := filepath.Join(repoDir, ".artifacts", "inline-suppressions.json")
	source := "package main\n\nfunc main() {\n\t_ = 1 //" + "nolint:staticcheck // rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard\n\t_ = 1 //" + "nolint:staticcheck // rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard\n}\n"
	writeFile(t, filepath.Join(repoDir, mainGoPath), source)
	runCommand(t, repoDir, "git", "add", mainGoPath)

	output, err := runSuppressionCheckWithEnv(repoDir, "SUPPRESSION_TRACKING_OUTPUT="+outputPath)
	if err != nil {
		t.Fatalf("expected duplicate fingerprint detection to pass, output:\n%s", output)
	}
	records := readSuppressionRecords(t, outputPath)
	if len(records.Suppressions) != 2 {
		t.Fatalf("expected two suppression records, got %#v", records.Suppressions)
	}
	if records.Suppressions[0].Fingerprint == records.Suppressions[1].Fingerprint {
		t.Fatalf("expected duplicate suppressions to receive distinct fingerprints: %#v", records.Suppressions)
	}

	output, err = runSuppressionCheckWithEnv(repoDir,
		"GH_BIN="+ghPath,
		"SUPPRESSION_TRACKING_MODE=track",
	)
	if err != nil {
		t.Fatalf("expected duplicate fingerprint tracking to pass, output:\n%s", output)
	}

	logContent := readFile(t, logPath)
	if got := strings.Count(logContent, "issue create"); got != 2 {
		t.Fatalf("issue create count = %d, want 2; log:\n%s", got, logContent)
	}
}

func TestInlineSuppressionCheckReadsOccurrencesFromThePRHeadNotTheMergeCommit(t *testing.T) {
	t.Parallel()

	// actions/checkout's default pull_request behavior checks out the
	// synthetic PR merge commit (base + head merged), not the PR's own
	// head tree. If the base branch independently adds an identical
	// suppression after the PR opened, that merge commit's working tree
	// contains both copies, while the trusted tracker (which reads
	// pull.head.sha via the API) sees only the PR's own -- producing a
	// different occurrence ordinal unless this reads from the PR's own
	// head tree instead of the merged working tree.
	repoDir := newInlineSuppressionRepo(t)
	marker := "nolint:staticcheck"
	line := "\t_ = 1 //" + marker + " // rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard"

	base := "package main\n\nfunc main() {\n\tx := 1\n}\n"
	writeFile(t, filepath.Join(repoDir, mainGoPath), base)
	runCommand(t, repoDir, "git", "add", mainGoPath)
	runCommand(t, repoDir, "git", "commit", "-m", "base")
	baseSHA := strings.TrimSpace(testutil.GitOutput(t, repoDir, "rev-parse", "HEAD"))

	runCommand(t, repoDir, "git", "checkout", "-b", "pr")
	prContent := "package main\n\nfunc main() {\n\tx := 1\n" + line + "\n}\n"
	writeFile(t, filepath.Join(repoDir, mainGoPath), prContent)
	runCommand(t, repoDir, "git", "add", mainGoPath)
	runCommand(t, repoDir, "git", "commit", "-m", "pr adds its own suppression")

	runCommand(t, repoDir, "git", "checkout", "main")
	mainContent := "package main\n\nfunc main() {\n" + line + "\n\tx := 1\n}\n"
	writeFile(t, filepath.Join(repoDir, mainGoPath), mainContent)
	runCommand(t, repoDir, "git", "add", mainGoPath)
	runCommand(t, repoDir, "git", "commit", "-m", "main independently adds an identical suppression")

	runCommand(t, repoDir, "git", "merge", "--no-edit", "pr")

	outputPath := filepath.Join(repoDir, ".artifacts", "inline-suppressions.json")
	output, err := runSuppressionCheckWithEnv(repoDir, "SUPPRESSION_BASE="+baseSHA, "SUPPRESSION_TRACKING_OUTPUT="+outputPath)
	if err != nil {
		t.Fatalf("expected the merge-commit scan to pass, output:\n%s", output)
	}

	records := readSuppressionRecords(t, outputPath)
	if len(records.Suppressions) != 2 {
		t.Fatalf("expected two suppression records (one per branch's addition), got %#v", records.Suppressions)
	}

	prFingerprint := suppressionFingerprint(mainGoPath, line, 1)
	found := false
	for _, record := range records.Suppressions {
		if record.Fingerprint == prFingerprint {
			found = true
		}
		if record.Fingerprint == suppressionFingerprint(mainGoPath, line, 2) {
			t.Fatalf("a record used the occurrence-2 fingerprint, meaning it counted main's independent addition against the PR's own occurrence: %#v", records.Suppressions)
		}
	}
	if !found {
		t.Fatalf("expected a record with the PR's own occurrence-1 fingerprint %s, got %#v", prFingerprint, records.Suppressions)
	}
}

func TestInlineSuppressionCheckCountsPreExistingOccurrenceOutsideTheDiff(t *testing.T) {
	t.Parallel()

	repoDir := newInlineSuppressionRepo(t)
	outputPath := filepath.Join(repoDir, ".artifacts", "inline-suppressions.json")

	marker := "nolint:staticcheck"
	line := "\t_ = 1 //" + marker + " // rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard"
	// The first occurrence is already committed on the base branch, so it
	// never appears in the diff this check scans; only the second, newly
	// staged occurrence does. The occurrence ordinal must still be derived
	// from the complete file (matching the trusted tracker, which sees this
	// pre-existing line as diff context) rather than only from lines visible
	// in this zero-context diff, or the two fingerprints would disagree.
	baseline := "package main\n\nfunc main() {\n" + line + "\n}\n"
	writeFile(t, filepath.Join(repoDir, mainGoPath), baseline)
	runCommand(t, repoDir, "git", "add", mainGoPath)
	runCommand(t, repoDir, "git", "commit", "-m", "pre-existing suppression")

	withSecond := "package main\n\nfunc main() {\n" + line + "\n" + line + "\n}\n"
	writeFile(t, filepath.Join(repoDir, mainGoPath), withSecond)
	runCommand(t, repoDir, "git", "add", mainGoPath)

	output, err := runSuppressionCheckWithEnv(repoDir, "SUPPRESSION_TRACKING_OUTPUT="+outputPath)
	if err != nil {
		t.Fatalf("expected the newly staged occurrence to pass, output:\n%s", output)
	}

	records := readSuppressionRecords(t, outputPath)
	if len(records.Suppressions) != 1 {
		t.Fatalf("expected exactly one new suppression record, got %#v", records.Suppressions)
	}

	want := suppressionFingerprint(mainGoPath, line, 2)
	if got := records.Suppressions[0].Fingerprint; got != want {
		t.Fatalf("fingerprint = %s, want occurrence-2 fingerprint %s (pre-existing occurrence outside the diff was not counted)", got, want)
	}
}

func TestInlineSuppressionCheckPreservesColonsInFilePaths(t *testing.T) {
	t.Parallel()

	repoDir := newInlineSuppressionRepo(t)
	outputPath := filepath.Join(repoDir, ".artifacts", "inline-suppressions.json")
	oddPath := filepath.Join(repoDir, "odd:name.go")
	if err := os.WriteFile(oddPath, []byte(mainGoWithTrackedSuppression("nolint:staticcheck")), 0o644); err != nil {
		t.Fatalf("write odd:name.go: %v", err)
	}
	runCommand(t, repoDir, "git", "add", "odd:name.go")

	output, err := runSuppressionCheckWithEnv(repoDir, "SUPPRESSION_TRACKING_OUTPUT="+outputPath)
	if err != nil {
		t.Fatalf("expected colon-path suppression detection to pass, output:\n%s", output)
	}

	records := readSuppressionRecords(t, outputPath)
	if len(records.Suppressions) != 1 {
		t.Fatalf("expected one suppression record, got %#v", records.Suppressions)
	}
	record := records.Suppressions[0]
	if record.File != "odd:name.go" || record.Line != 4 {
		t.Fatalf("record location = %q:%d, want \"odd:name.go\":4", record.File, record.Line)
	}
}

func TestInlineSuppressionCheckAllowsDocumentationMentions(t *testing.T) {
	t.Parallel()

	repoDir := newInlineSuppressionRepo(t)
	docContent := "# Policy\n\nDo not add `" + "//" + "nolint` or `" + "//" + "nosec` markers in source files.\n"
	writeFile(t, filepath.Join(repoDir, "docs", "policy.md"), docContent)
	runCommand(t, repoDir, "git", "add", "docs/policy.md")

	output, err := runSuppressionCheck(repoDir)
	if err != nil {
		t.Fatalf("expected documentation mention to pass, output:\n%s", output)
	}
	if !strings.Contains(output, "Inline suppression check passed (staged changes)") {
		t.Fatalf("expected pass message, got:\n%s", output)
	}
}

func TestInlineSuppressionCheckIgnoresQuotedMarkersInSource(t *testing.T) {
	t.Parallel()

	repoDir := newInlineSuppressionRepo(t)
	source := "package main\n\nconst marker = \"" + "//" + "nosec" + "\"\n"
	writeFile(t, filepath.Join(repoDir, mainGoPath), source)
	runCommand(t, repoDir, "git", "add", mainGoPath)

	output, err := runSuppressionCheck(repoDir)
	if err != nil {
		t.Fatalf("expected quoted marker to pass, output:\n%s", output)
	}
	if !strings.Contains(output, "Inline suppression check passed (staged changes)") {
		t.Fatalf("expected pass message, got:\n%s", output)
	}
}

func mainGoWithoutComment() string {
	return "package main\n\nfunc main() {\n\t_ = 1\n}\n"
}

func mainGoWithComment(comment string) string {
	return "package main\n\nfunc main() {\n\t_ = 1 //" + comment + "\n}\n"
}

func mainGoWithBlockComment(comment string) string {
	return "package main\n\nfunc main() {\n\t_ = 1 /* " + comment + " */\n}\n"
}

func mainGoWithTrackedSuppression(marker string) string {
	return "package main\n\nfunc main() {\n\t_ = 1 //" + marker + " // rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard\n}\n"
}

type suppressionRecords struct {
	Schema       string              `json:"schema"`
	Suppressions []suppressionRecord `json:"suppressions"`
}

type suppressionRecord struct {
	Fingerprint string `json:"fingerprint"`
	File        string `json:"file"`
	Line        int    `json:"line"`
	Source      string `json:"source"`
	Content     string `json:"content"`
	Rationale   string `json:"rationale"`
	Owner       string `json:"owner"`
	RemoveWhen  string `json:"remove_when"`
}

func detectSuppressionFingerprint(t *testing.T, source string) string {
	t.Helper()

	repoDir := newInlineSuppressionRepo(t)
	outputPath := filepath.Join(repoDir, ".artifacts", "inline-suppressions.json")
	writeFile(t, filepath.Join(repoDir, mainGoPath), source)
	runCommand(t, repoDir, "git", "add", mainGoPath)

	output, err := runSuppressionCheckWithEnv(repoDir, "SUPPRESSION_TRACKING_OUTPUT="+outputPath)
	if err != nil {
		t.Fatalf("expected suppression detection to pass, output:\n%s", output)
	}
	records := readSuppressionRecords(t, outputPath)
	if len(records.Suppressions) != 1 {
		t.Fatalf("expected one suppression record, got %#v", records.Suppressions)
	}
	return records.Suppressions[0].Fingerprint
}

func readSuppressionRecords(t *testing.T, path string) suppressionRecords {
	t.Helper()

	var records suppressionRecords
	data := []byte(readFile(t, path))
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("parse suppression records: %v\n%s", err, data)
	}
	if records.Schema != "lopper-inline-suppressions-v1" {
		t.Fatalf("suppression record schema = %q", records.Schema)
	}
	return records
}

func newInlineSuppressionRepo(t *testing.T) string {
	t.Helper()

	repoDir := t.TempDir()
	scriptDir := filepath.Join(repoDir, "scripts")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	scriptPath := filepath.Join(cwd, "check-inline-suppressions.sh")
	scriptData, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	writeFileMode(t, filepath.Join(scriptDir, "check-inline-suppressions.sh"), string(scriptData), 0o755)

	runCommand(t, repoDir, "git", "init", "-b", "main")
	runCommand(t, repoDir, "git", "config", "user.name", "Test User")
	runCommand(t, repoDir, "git", "config", "user.email", "test@example.com")
	runCommand(t, repoDir, "git", "add", "scripts/check-inline-suppressions.sh")
	runCommand(t, repoDir, "git", "commit", "-m", "baseline")

	return repoDir
}

func runSuppressionCheck(repoDir string) (string, error) {
	return runSuppressionCheckWithEnv(repoDir)
}

func runSuppressionCheckWithEnv(repoDir string, env ...string) (string, error) {
	cmd := exec.Command(filepath.Join(repoDir, "scripts", "check-inline-suppressions.sh"))
	cmd.Dir = repoDir
	cmd.Env = append(withoutGitEnv(), env...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func runCommand(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()

	if name == "git" {
		testutil.RunGit(t, dir, args...)
		return ""
	}

	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = withoutGitEnv()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, output)
	}
	return string(output)
}

func withoutGitEnv() []string {
	env := os.Environ()
	filtered := env[:0]
	for _, entry := range env {
		if strings.HasPrefix(entry, "GIT_") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	writeFileMode(t, path, content, 0o644)
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

func writeFileMode(t *testing.T, path string, content string, mode os.FileMode) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func newMockGH(t *testing.T) (string, string) {
	t.Helper()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "gh.log")
	scriptPath := filepath.Join(dir, "gh")
	script := `#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$*" >> "` + logPath + `"

body_file=""
previous=""
for arg in "$@"; do
	if [ "$previous" = "--body-file" ]; then
		body_file="$arg"
		break
	fi
	previous="$arg"
done

if [ -n "$body_file" ] && [ -f "$body_file" ]; then
	cat "$body_file" >> "` + logPath + `"
fi

if [ "$1" = "issue" ] && [ "$2" = "list" ]; then
	printf '%s\n' "${GH_MOCK_EXISTING_ISSUE:-}"
	exit 0
fi

if [ "$1" = "issue" ] && [ "$2" = "comment" ]; then
	exit 0
fi

if [ "$1" = "issue" ] && [ "$2" = "create" ]; then
	if [ "${GH_MOCK_FAIL_CREATE:-}" = "1" ]; then
		exit 1
	fi
	printf '%s\n' "https://github.com/ben-ranford/lopper/issues/123"
	exit 0
fi

exit 1
`
	writeFileMode(t, scriptPath, script, 0o755)
	return scriptPath, logPath
}
