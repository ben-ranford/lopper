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

func assertSuppressionDetectedForFilename(t *testing.T, filename string) {
	t.Helper()

	repoDir := newInlineSuppressionRepo(t)
	outputPath := filepath.Join(repoDir, ".artifacts", "inline-suppressions.json")
	writeFile(t, filepath.Join(repoDir, filename), mainGoWithTrackedSuppression("nolint:staticcheck"))
	runCommand(t, repoDir, "git", "add", filename)

	output, err := runSuppressionCheckWithEnv(repoDir, "SUPPRESSION_TRACKING_OUTPUT="+outputPath)
	if err != nil {
		t.Fatalf("expected suppression detection to pass for %q, output:\n%s", filename, output)
	}

	records := readSuppressionRecords(t, outputPath)
	if len(records.Suppressions) != 1 {
		t.Fatalf("expected one suppression record for %q, got %#v", filename, records.Suppressions)
	}
	if records.Suppressions[0].File != filename {
		t.Fatalf("record file = %q, want %q", records.Suppressions[0].File, filename)
	}
}

func TestInlineSuppressionCheckMatchesUppercaseExtensionCaseInsensitively(t *testing.T) {
	t.Parallel()

	// The trusted tracker's fileExtension() lowercases before matching, so
	// it recognizes a suppression added to e.g. main.GO. This detector's
	// source-file scope must not silently skip such files while the
	// trusted tracker still finds and tracks them.
	assertSuppressionDetectedForFilename(t, "main.GO")
}

func assertSuppressionDetectedForLine(t *testing.T, line string) suppressionRecords {
	t.Helper()

	repoDir := newInlineSuppressionRepo(t)
	outputPath := filepath.Join(repoDir, ".artifacts", "inline-suppressions.json")
	content := "package main\n\nfunc main() {\n" + line + "\n}\n"
	writeFile(t, filepath.Join(repoDir, mainGoPath), content)
	runCommand(t, repoDir, "git", "add", mainGoPath)

	output, err := runSuppressionCheckWithEnv(repoDir, "SUPPRESSION_TRACKING_OUTPUT="+outputPath)
	if err != nil {
		t.Fatalf("expected the suppression to be detected, output:\n%s", output)
	}

	records := readSuppressionRecords(t, outputPath)
	if len(records.Suppressions) != 1 {
		t.Fatalf("expected one suppression record, got %#v", records.Suppressions)
	}
	return records
}

func TestInlineSuppressionCheckDetectsAddedLineStartingWithDoublePlus(t *testing.T) {
	t.Parallel()

	// A raw diff "+" prefix over content that itself starts with "++" and
	// has no leading whitespace (e.g. an unindented added C/C++ increment
	// statement) produces a line beginning with three literal "+"
	// characters, colliding with the genuine "+++ b/..." file-header
	// exclusion unless that exclusion is scoped to just the header line.
	marker := "nolint:staticcheck"
	line := "++counter; //" + marker + " // rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard"
	assertSuppressionDetectedForLine(t, line)
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

// runTrackedSuppressionCheck writes content, runs the check in "track" mode
// with the given extra env, asserts wantOutput appears in its output and
// every entry of wantLogSubstrings appears in the mock gh log, then returns
// that log content for any further caller-specific assertions.
func runTrackedSuppressionCheck(t *testing.T, content string, extraEnv []string, wantOutput string, wantLogSubstrings []string) string {
	t.Helper()

	repoDir := newInlineSuppressionRepo(t)
	ghPath, logPath := newMockGH(t)
	writeFile(t, filepath.Join(repoDir, mainGoPath), content)
	runCommand(t, repoDir, "git", "add", mainGoPath)

	env := append([]string{"GH_BIN=" + ghPath, "SUPPRESSION_TRACKING_MODE=track"}, extraEnv...)
	output, err := runSuppressionCheckWithEnv(repoDir, env...)
	if err != nil {
		t.Fatalf("expected tracked suppression to pass, output:\n%s", output)
	}
	if !strings.Contains(output, wantOutput) {
		t.Fatalf("expected %q in output, got:\n%s", wantOutput, output)
	}

	logContent := readFile(t, logPath)
	for _, want := range wantLogSubstrings {
		if !strings.Contains(logContent, want) {
			t.Fatalf("expected mock gh log to contain %q, got:\n%s", want, logContent)
		}
	}
	return logContent
}

func TestInlineSuppressionCheckCreatesTrackingIssueForStagedMarker(t *testing.T) {
	t.Parallel()

	runTrackedSuppressionCheck(t,
		mainGoWithTrackedSuppression("nolint:staticcheck"),
		[]string{
			"SUPPRESSION_GITHUB_REPOSITORY=ben-ranford/lopper",
			"GITHUB_SHA=abc123",
			"GITHUB_SERVER_URL=https://github.com",
		},
		"Opened GitHub tracking issue for inline suppression main.go:4",
		[]string{
			"issue list",
			"issue create",
			"author:github-actions[bot]",
			`select(.author.login == "github-actions[bot]"`,
			"Location: `main.go:4`",
			"Source: https://github.com/ben-ranford/lopper/blob/abc123/main.go#L4",
			"Rationale: temporary scanner false positive",
			"Owner: @security",
			"Removal condition: analyzer handles generated guard",
		},
	)
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

	logContent := runTrackedSuppressionCheck(t,
		mainGoWithTrackedSuppression("nosec G404"),
		[]string{"GH_MOCK_EXISTING_ISSUE=77"},
		"Updated GitHub tracking issue #77 for inline suppression main.go:4",
		[]string{"issue comment 77"},
	)
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
	output, err := runSuppressionCheckWithEnv(repoDir,
		"SUPPRESSION_BASE="+baseSHA,
		"SUPPRESSION_TRACKING_OUTPUT="+outputPath,
		"GITHUB_EVENT_NAME=pull_request",
	)
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

func TestInlineSuppressionCheckIgnoresOrdinaryLocalMergeCommits(t *testing.T) {
	t.Parallel()

	// An ordinary local merge (merging an updated main into a topic
	// branch, as make suppression-check might run after) is also a
	// two-parent commit, but its second parent is the merged-in branch,
	// not "this change's own head". Without GITHUB_EVENT_NAME=pull_request
	// signaling an actual CI checkout of GitHub's synthetic PR merge, the
	// second-parent heuristic must not activate: topic.go only exists on
	// the topic side, so reading it from the wrong parent would fail
	// outright under pipefail instead of falling back to the working tree.
	repoDir := newInlineSuppressionRepo(t)
	writeFile(t, filepath.Join(repoDir, mainGoPath), "package main\n\nfunc main() {\n\tx := 1\n}\n")
	runCommand(t, repoDir, "git", "add", mainGoPath)
	runCommand(t, repoDir, "git", "commit", "-m", "base")
	baseSHA := strings.TrimSpace(testutil.GitOutput(t, repoDir, "rev-parse", "HEAD"))

	runCommand(t, repoDir, "git", "checkout", "-b", "topic")
	marker := "nolint:staticcheck"
	line := "\t_ = 1 //" + marker + " // rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard"
	topicContent := "package main\n\nfunc topic() {\n" + line + "\n}\n"
	topicPath := "topic.go"
	writeFile(t, filepath.Join(repoDir, topicPath), topicContent)
	runCommand(t, repoDir, "git", "add", topicPath)
	runCommand(t, repoDir, "git", "commit", "-m", "topic adds its own suppression in a topic-only file")

	runCommand(t, repoDir, "git", "checkout", "main")
	writeFile(t, filepath.Join(repoDir, mainGoPath), "package main\n\nfunc main() {\n\tx := 2\n}\n")
	runCommand(t, repoDir, "git", "add", mainGoPath)
	runCommand(t, repoDir, "git", "commit", "-m", "main advances independently")

	runCommand(t, repoDir, "git", "checkout", "topic")
	runCommand(t, repoDir, "git", "merge", "--no-edit", "main")

	outputPath := filepath.Join(repoDir, ".artifacts", "inline-suppressions.json")
	// Deliberately not setting GITHUB_EVENT_NAME, simulating a local run.
	output, err := runSuppressionCheckWithEnv(repoDir, "SUPPRESSION_BASE="+baseSHA, "SUPPRESSION_TRACKING_OUTPUT="+outputPath)
	if err != nil {
		t.Fatalf("expected a local merge-commit scan to fall back to the working tree and pass, output:\n%s", output)
	}

	records := readSuppressionRecords(t, outputPath)
	if len(records.Suppressions) != 1 {
		t.Fatalf("expected one suppression record, got %#v", records.Suppressions)
	}
	if records.Suppressions[0].File != topicPath {
		t.Fatalf("record file = %q, want %q", records.Suppressions[0].File, topicPath)
	}
}

func TestInlineSuppressionCheckParsesTabTerminatedFilenames(t *testing.T) {
	t.Parallel()

	// Git appends a trailing tab to a "+++ b/<path>" diff header when
	// <path> needs disambiguation, e.g. because it contains a space.
	assertSuppressionDetectedForFilename(t, "odd name.go")
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

func TestInlineSuppressionCheckSurvivesOccurrenceCountingInALargeFile(t *testing.T) {
	t.Parallel()

	// occurrence_in_file's awk exits as soon as it passes the target line,
	// closing its read end of the pipe while the producer (cat/git show) is
	// still writing a sufficiently large file. Under `set -o pipefail`, the
	// resulting SIGPIPE makes the producer exit 141 and fails the whole
	// pipeline even though the count itself is already correct; the
	// suppression must still be tracked instead of aborting the check.
	repoDir := newInlineSuppressionRepo(t)
	outputPath := filepath.Join(repoDir, ".artifacts", "inline-suppressions.json")

	marker := "nolint:staticcheck"
	line := "\t_ = 1 //" + marker + " // rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard"
	var builder strings.Builder
	builder.WriteString("package main\n\nfunc main() {\n")
	builder.WriteString(line)
	builder.WriteString("\n}\n")
	for i := 0; i < 200000; i++ {
		builder.WriteString("// filler line to force a large enough file to fill the pipe buffer\n")
	}
	writeFile(t, filepath.Join(repoDir, mainGoPath), builder.String())
	runCommand(t, repoDir, "git", "add", mainGoPath)

	output, err := runSuppressionCheckWithEnv(repoDir, "SUPPRESSION_TRACKING_OUTPUT="+outputPath)
	if err != nil {
		t.Fatalf("expected occurrence counting in a large file to pass, output:\n%s", output)
	}

	records := readSuppressionRecords(t, outputPath)
	if len(records.Suppressions) != 1 {
		t.Fatalf("expected one suppression record, got %#v", records.Suppressions)
	}
}

func TestInlineSuppressionCheckPreservesColonsInFilePaths(t *testing.T) {
	t.Parallel()

	assertSuppressionDetectedForFilename(t, "odd:name.go")
}

func TestInlineSuppressionCheckReadsStagedOccurrencesFromTheIndex(t *testing.T) {
	t.Parallel()

	repoDir := newInlineSuppressionRepo(t)
	outputPath := filepath.Join(repoDir, ".artifacts", "inline-suppressions.json")

	marker := "nolint:staticcheck"
	line := "\t_ = 1 //" + marker + " // rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard"

	// The first occurrence is already committed on the base branch, so it
	// never appears in the diff; the newly staged second occurrence does,
	// and must be assigned occurrence 2.
	baseline := "package main\n\nfunc main() {\n" + line + "\n}\n"
	writeFile(t, filepath.Join(repoDir, mainGoPath), baseline)
	runCommand(t, repoDir, "git", "add", mainGoPath)
	runCommand(t, repoDir, "git", "commit", "-m", "pre-existing suppression")

	staged := "package main\n\nfunc main() {\n" + line + "\n" + line + "\n}\n"
	writeFile(t, filepath.Join(repoDir, mainGoPath), staged)
	runCommand(t, repoDir, "git", "add", mainGoPath)

	// Then edit the working tree only (no re-staging) so the committed,
	// pre-existing first occurrence no longer matches there. If occurrence
	// counting reads the working tree instead of the index for a
	// staged-diff run, it would see only one remaining match up to the
	// target line and misidentify the new suppression as occurrence 1
	// instead of 2.
	worktreeOnly := "package main\n\nfunc main() {\n\t_ = 2 // unrelated\n" + line + "\n}\n"
	writeFile(t, filepath.Join(repoDir, mainGoPath), worktreeOnly)

	output, err := runSuppressionCheckWithEnv(repoDir, "SUPPRESSION_TRACKING_OUTPUT="+outputPath)
	if err != nil {
		t.Fatalf("expected the staged occurrence to pass, output:\n%s", output)
	}

	records := readSuppressionRecords(t, outputPath)
	if len(records.Suppressions) != 1 {
		t.Fatalf("expected exactly one new suppression record, got %#v", records.Suppressions)
	}

	want := suppressionFingerprint(mainGoPath, line, 2)
	if got := records.Suppressions[0].Fingerprint; got != want {
		t.Fatalf("fingerprint = %s, want occurrence-2 fingerprint %s (occurrence was computed from the working tree instead of the staged index)", got, want)
	}
}

func TestInlineSuppressionCheckHandlesNonASCIIFilePaths(t *testing.T) {
	t.Parallel()

	// Git C-style-quotes and octal-escapes non-ASCII path bytes in a diff's
	// "+++ b/<path>" header by default; a parser that only strips a plain
	// trailing tab wouldn't recognize this header at all and would silently
	// skip every suppression in the file.
	assertSuppressionDetectedForFilename(t, "café.go")
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

	assertSuppressionCheckPassesForSourceNamed(t, mainGoPath, "package main\n\nconst marker = \""+"//"+"nosec"+"\"\n")
}

func TestInlineSuppressionCheckIgnoresMarkerTextInsideAnOpenStringLiteral(t *testing.T) {
	t.Parallel()

	// The character immediately before the comment delimiter is a space,
	// not a quote, but the delimiter is still inside the still-open string
	// started earlier on the line; checking only that single preceding
	// character would miss this.
	assertSuppressionCheckPassesForSourceNamed(t, mainGoPath, "package main\n\nconst help = \"Use "+"//"+"nolint to suppress\"\n")
}

func TestInlineSuppressionCheckDetectsMarkerImmediatelyAfterAClosedString(t *testing.T) {
	t.Parallel()

	repoDir := newInlineSuppressionRepo(t)
	outputPath := filepath.Join(repoDir, ".artifacts", "inline-suppressions.json")
	marker := "nolint:staticcheck"
	// The comment delimiter is preceded by a closed string's closing quote,
	// not open string content; this is real code and must still be
	// detected, distinguishing a closed string from an unterminated one.
	line := "\t_ = \"done\" //" + marker + " // rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard"
	source := "package main\n\nfunc main() {\n" + line + "\n}\n"
	writeFile(t, filepath.Join(repoDir, mainGoPath), source)
	runCommand(t, repoDir, "git", "add", mainGoPath)

	output, err := runSuppressionCheckWithEnv(repoDir, "SUPPRESSION_TRACKING_OUTPUT="+outputPath)
	if err != nil {
		t.Fatalf("expected marker after a closed string to pass, output:\n%s", output)
	}

	records := readSuppressionRecords(t, outputPath)
	if len(records.Suppressions) != 1 {
		t.Fatalf("expected one suppression record, got %#v", records.Suppressions)
	}
}

func assertSuppressionDetectedForNarrowSingleQuoteSource(t *testing.T, path string, wrapperStart string, wrapperEnd string, line string) {
	t.Helper()

	repoDir := newInlineSuppressionRepo(t)
	outputPath := filepath.Join(repoDir, ".artifacts", "inline-suppressions.json")
	source := wrapperStart + "\n" + line + "\n" + wrapperEnd + "\n"
	writeFile(t, filepath.Join(repoDir, path), source)
	runCommand(t, repoDir, "git", "add", path)

	output, err := runSuppressionCheckWithEnv(repoDir, "SUPPRESSION_TRACKING_OUTPUT="+outputPath)
	if err != nil {
		t.Fatalf("expected the marker in %q to pass, output:\n%s", path, output)
	}

	records := readSuppressionRecords(t, outputPath)
	if len(records.Suppressions) != 1 {
		t.Fatalf("expected one suppression record, got %#v", records.Suppressions)
	}
}

func TestInlineSuppressionCheckDetectsMarkerAfterARustLifetime(t *testing.T) {
	t.Parallel()

	// Rust has no multi-character single-quoted strings: a leading
	// apostrophe here starts a lifetime ('static) that never closes.
	// Treating every apostrophe as a generic string delimiter (as other
	// covered languages require) would make the unterminated lifetime
	// swallow the rest of the line, hiding the real suppression comment
	// that follows it.
	line := "\tlet value: &'static str = \"x\"; //coverage: ignore // rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard"
	assertSuppressionDetectedForNarrowSingleQuoteSource(t, "main.rs", "fn main() {", "}", line)
}

func TestInlineSuppressionCheckDetectsMarkerAfterACPlusPlusDigitSeparator(t *testing.T) {
	t.Parallel()

	// C++14 digit separators group the digits of a large numeric literal
	// with apostrophes that never close the way a real string does.
	// Treating every apostrophe as a generic string delimiter would make
	// the unterminated separator swallow the rest of the line, hiding the
	// real suppression comment that follows it.
	line := "\tauto n = 1'000; //NOLINT rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard"
	assertSuppressionDetectedForNarrowSingleQuoteSource(t, "main.cpp", "int main() {", "}", line)
}

func TestInlineSuppressionCheckIgnoresURLSchemesInSource(t *testing.T) {
	t.Parallel()

	// A URL scheme ("http://...") must not be mistaken for a comment
	// delimiter just because the marker-boundary check was loosened to
	// recognize comments with no leading whitespace.
	assertSuppressionCheckPassesForSourceNamed(t, mainGoPath, "package main\n\nconst url = \"http:"+"//"+"nosec.example.com\"\n")
}

func TestInlineSuppressionCheckDetectsMarkerAfterAMultilineTemplateLiteral(t *testing.T) {
	t.Parallel()

	// Quote state resets per line by default; a template literal opened on
	// one added line and closed on a later added line must carry that
	// state across, or the closing backtick looks like it opens a fresh
	// quoted region and masks the real suppression comment that follows
	// it -- silently reporting nothing found rather than failing loudly,
	// since the marker itself becomes invisible to the scan.
	repoDir := newInlineSuppressionRepo(t)
	outputPath := filepath.Join(repoDir, ".artifacts", "inline-suppressions.json")
	source := "const s = `line one\nline two\ntail`; //" +
		"eslint-disable-line rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard\n"
	jsPath := "main.js"
	writeFile(t, filepath.Join(repoDir, jsPath), source)
	runCommand(t, repoDir, "git", "add", jsPath)

	output, err := runSuppressionCheckWithEnv(repoDir, "SUPPRESSION_TRACKING_OUTPUT="+outputPath)
	if err != nil {
		t.Fatalf("expected marker after a multi-line template literal to pass, output:\n%s", output)
	}

	records := readSuppressionRecords(t, outputPath)
	if len(records.Suppressions) != 1 {
		t.Fatalf("expected one suppression record, got %#v", records.Suppressions)
	}
}

func TestInlineSuppressionCheckSeedsQuoteStateAcrossADiffContextGap(t *testing.T) {
	t.Parallel()

	// check-inline-suppressions.sh diffs with --unified=0, so a change to
	// only the closing line of a multi-line template literal shows zero
	// context: the opening backtick, several lines above, never appears in
	// the diff at all. Resetting quote state to "no open quote" at the
	// hunk boundary -- instead of deriving it from the complete head file
	// -- would make the closing backtick look like a fresh opener and mask
	// the real suppression comment that follows it.
	repoDir := newInlineSuppressionRepo(t)
	outputPath := filepath.Join(repoDir, ".artifacts", "inline-suppressions.json")
	jsPath := "tpl.js"
	base := "package main\n\nvar tpl = `line1\nline2\nline3\nline4\ntail`;\n"
	writeFile(t, filepath.Join(repoDir, jsPath), base)
	runCommand(t, repoDir, "git", "add", jsPath)
	runCommand(t, repoDir, "git", "commit", "-m", "add template literal")

	changed := "package main\n\nvar tpl = `line1\nline2\nline3\nline4\ntail`; //" +
		"eslint-disable-line rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard\n"
	writeFile(t, filepath.Join(repoDir, jsPath), changed)

	output, err := runSuppressionCheckWithEnv(repoDir, "SUPPRESSION_TRACKING_OUTPUT="+outputPath)
	if err != nil {
		t.Fatalf("expected the marker beyond the diff context gap to pass, output:\n%s", output)
	}

	records := readSuppressionRecords(t, outputPath)
	if len(records.Suppressions) != 1 {
		t.Fatalf("expected one suppression record, got %#v", records.Suppressions)
	}
}

func assertSuppressionCheckPassesForSourceNamed(t *testing.T, filename string, source string) {
	t.Helper()

	repoDir := newInlineSuppressionRepo(t)
	writeFile(t, filepath.Join(repoDir, filename), source)
	runCommand(t, repoDir, "git", "add", filename)

	output, err := runSuppressionCheck(repoDir)
	if err != nil {
		t.Fatalf("expected the check to pass, output:\n%s", output)
	}
	if !strings.Contains(output, "Inline suppression check passed (staged changes)") {
		t.Fatalf("expected pass message, got:\n%s", output)
	}
}

func TestInlineSuppressionCheckRequiresAFreeStandingHashInAHashOnlyLanguage(t *testing.T) {
	t.Parallel()

	// YAML requires "#" to be separated from the preceding scalar by
	// whitespace (or start the line); a "#" embedded in a URL's fragment
	// identifier is not a comment delimiter.
	assertSuppressionCheckPassesForSourceNamed(t, "deploy.yaml", "url: https://example.test/#noqa\n")

	// Shell only treats "#" as a comment when it begins a word; "#" glued
	// directly onto the preceding token is a literal character.
	assertSuppressionCheckPassesForSourceNamed(t, "build.sh", "echo foo#nolint\n")
}

func assertSuppressionDetectedForFileAndLine(t *testing.T, filename string, line string) {
	t.Helper()

	repoDir := newInlineSuppressionRepo(t)
	outputPath := filepath.Join(repoDir, ".artifacts", "inline-suppressions.json")
	writeFile(t, filepath.Join(repoDir, filename), line)
	runCommand(t, repoDir, "git", "add", filename)

	output, err := runSuppressionCheckWithEnv(repoDir, "SUPPRESSION_TRACKING_OUTPUT="+outputPath)
	if err != nil {
		t.Fatalf("expected the marker to be detected, output:\n%s", output)
	}

	records := readSuppressionRecords(t, outputPath)
	if len(records.Suppressions) != 1 {
		t.Fatalf("expected one suppression record, got %#v", records.Suppressions)
	}
}

func TestInlineSuppressionCheckDoesNotRequireAFreeStandingHashInPythonOrRuby(t *testing.T) {
	t.Parallel()

	// Unlike YAML/shell, Python and Ruby start a comment with "#" anywhere
	// outside a string -- exactly like "//" does in slash-style languages --
	// with no whitespace requirement. Applying the YAML/shell free-standing
	// rule there too would reject a valid suppression immediately following
	// code with no space in between.
	assertSuppressionDetectedForFileAndLine(t, "calc.py", "value = unsafe()#noqa rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard\n")
}

func TestInlineSuppressionCheckDetectsAFreeStandingHashMarkerInShell(t *testing.T) {
	t.Parallel()

	assertSuppressionDetectedForFileAndLine(t, "build.sh", "echo foo #nolint rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard\n")
}

func TestInlineSuppressionCheckHonorsShellSingleQuoteEscapingRules(t *testing.T) {
	t.Parallel()

	// POSIX shell single-quoted strings have no escape character at all: a
	// backslash inside one is literal, and the string closes at the very
	// next apostrophe. Treating "\" as escaping the following character,
	// the way every other quote/language combination does, would let a
	// trailing backslash before the closing quote swallow that quote as
	// escaped content, leaving the string open and masking the real
	// suppression comment that follows it.
	assertSuppressionDetectedForFileAndLine(t, "build.sh", "echo 'foo\\' #noqa rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard\n")
}

func TestInlineSuppressionCheckDetectsMarkerOnLineAfterCommentContainingApostrophe(t *testing.T) {
	t.Parallel()

	// An apostrophe inside a genuine line comment (e.g. "don't") is
	// comment prose, not code; it must not be treated as opening a
	// string that leaks into the next added line and masks a real
	// suppression comment there, the same way an actual unterminated
	// string legitimately would.
	marker := "nolint:staticcheck"
	line := "\t_ = 1 //" + marker + " // rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard"
	source := "package main\n\nfunc main() {\n\t// don" + "'" + "t use this path\n" + line + "\n}\n"
	repoDir := newInlineSuppressionRepo(t)
	outputPath := filepath.Join(repoDir, ".artifacts", "inline-suppressions.json")
	writeFile(t, filepath.Join(repoDir, mainGoPath), source)
	runCommand(t, repoDir, "git", "add", mainGoPath)

	output, err := runSuppressionCheckWithEnv(repoDir, "SUPPRESSION_TRACKING_OUTPUT="+outputPath)
	if err != nil {
		t.Fatalf("expected marker after a comment containing an apostrophe to pass, output:\n%s", output)
	}

	records := readSuppressionRecords(t, outputPath)
	if len(records.Suppressions) != 1 {
		t.Fatalf("expected one suppression record, got %#v", records.Suppressions)
	}
}

func TestInlineSuppressionCheckIgnoresMarkerShapedExampleTextQuotedInAComment(t *testing.T) {
	t.Parallel()

	// Stopping quote tracking at a genuine comment delimiter must not stop
	// *masking* for the rest of that same line: a well-formed quoted span
	// later in the very same comment (e.g. documentation quoting an
	// example marker in backticks) still needs its interior masked, or
	// the example text is indistinguishable from a real suppression. Only
	// the carry into the *next* line should discard a comment's dangling
	// quote state, not the comment's own remaining content. This is the
	// exact shape of comment this detector's own source file uses to
	// document itself.
	source := "package main\n\n// e.g. `\"Use //nolint to suppress\"`. Blanking out the region.\nfunc main() {}\n"
	assertSuppressionCheckPassesForSourceNamed(t, mainGoPath, source)
}

func TestInlineSuppressionCheckDetectsMarkerWithoutLeadingWhitespace(t *testing.T) {
	t.Parallel()

	// A comment delimiter needs no preceding whitespace: it can follow
	// code directly, with no space in between. All three detection
	// layers previously required whitespace or start-of-line before the
	// comment prefix, so such a line was invisible to all of them.
	marker := "nolint:staticcheck"
	line := "call();//" + marker + " // rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard"
	assertSuppressionDetectedForLine(t, line)
}

func TestInlineSuppressionCheckIgnoresPythonFloorDivisionAsACommentPrefix(t *testing.T) {
	t.Parallel()

	// Python only has "#" comments, so "//" here is floor division, not the
	// start of a comment. Recognizing every comment style universally would
	// reject this valid code for missing suppression metadata it was never
	// meant to carry.
	source := "value = numerator " + "/" + "/ noqa denominator\n"
	repoDir := newInlineSuppressionRepo(t)
	writeFile(t, filepath.Join(repoDir, "calc.py"), source)
	runCommand(t, repoDir, "git", "add", "calc.py")

	output, err := runSuppressionCheck(repoDir)
	if err != nil {
		t.Fatalf("expected Python floor division to pass, output:\n%s", output)
	}
	if !strings.Contains(output, "Inline suppression check passed (staged changes)") {
		t.Fatalf("expected pass message, got:\n%s", output)
	}
}

func TestInlineSuppressionCheckDetectsGenuineHashMarkerInAPythonFile(t *testing.T) {
	t.Parallel()

	repoDir := newInlineSuppressionRepo(t)
	outputPath := filepath.Join(repoDir, ".artifacts", "inline-suppressions.json")
	line := "value = numerator " + "/" + "/ denominator  # noqa rationale=temporary scanner false positive; owner=@security; remove-when=analyzer handles generated guard\n"
	writeFile(t, filepath.Join(repoDir, "calc.py"), line)
	runCommand(t, repoDir, "git", "add", "calc.py")

	output, err := runSuppressionCheckWithEnv(repoDir, "SUPPRESSION_TRACKING_OUTPUT="+outputPath)
	if err != nil {
		t.Fatalf("expected the genuine hash marker to be detected, output:\n%s", output)
	}

	records := readSuppressionRecords(t, outputPath)
	if len(records.Suppressions) != 1 {
		t.Fatalf("expected one suppression record, got %#v", records.Suppressions)
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
		// This test binary itself runs as a step inside GitHub Actions CI,
		// so GITHUB_EVENT_NAME is ambiently "pull_request" there even
		// though these tests aren't simulating that context by default;
		// leaving it in would leak actual CI state into subprocess runs
		// that specifically mean to exercise the non-CI (local dev) path.
		// Tests that do want it set pass it explicitly via env... instead.
		if strings.HasPrefix(entry, "GITHUB_EVENT_NAME=") {
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
