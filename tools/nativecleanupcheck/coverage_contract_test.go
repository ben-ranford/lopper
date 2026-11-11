package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func TestMainHelperForCLIError(t *testing.T) {
	if os.Getenv("NATIVECLEANUPCHECK_MAIN_HELPER") != "1" {
		return
	}
	os.Args = []string{"nativecleanupcheck"}
	main()
}

func TestMainReturnsUsageFailureForMissingMode(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainHelperForCLIError$")
	cmd.Env = append(os.Environ(), "NATIVECLEANUPCHECK_MAIN_HELPER=1")
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("main exit = %v, output %s", err, output)
	}
	if !strings.Contains(string(output), "expected verify-custody or audit-results") {
		t.Fatalf("missing usage error: %s", output)
	}
}

func TestRunAndParseOptionsRejectMalformedInvocation(t *testing.T) {
	if err := run(nil); err == nil || err.Error() != "expected verify-custody or audit-results" {
		t.Fatalf("run without mode = %v", err)
	}
	if err := run([]string{"unknown"}); err == nil || !strings.Contains(err.Error(), "missing reviewed source/policy/root") {
		t.Fatalf("run without authenticated anchors = %v", err)
	}
	if _, err := parseOptions([]string{"--unknown"}); err == nil {
		t.Fatal("unknown option accepted")
	}
	if _, err := parseOptions([]string{"--go-status", "not-an-int"}); err == nil {
		t.Fatal("malformed status accepted")
	}
	if _, err := parseOptions([]string{"--run-id", "123", "extra"}); err == nil {
		t.Fatal("trailing operand accepted")
	}
	parsed, err := parseOptions([]string{"--go-cache", "/cache/build", "--go-mod-cache", "/cache/mod", "--original-control", "/control.json", "--original-control-sha256", strings.Repeat("a", 64), "--manifest", "/root/custody.json", "--manifest-sha256", strings.Repeat("b", 64), "--verifier", "/root/bin/nativecleanupcheck.exe", "--verifier-sha256", strings.Repeat("c", 64), "--expected-source-sha", strings.Repeat("d", 40), "--expected-policy-sha256", strings.Repeat("e", 64), "--trusted-root", "/root", "--allocation", "/root/control/allocation.json", "--allocation-sha256", strings.Repeat("f", 64), "--join-record", "/root/control/join.json", "--join-record-sha256", strings.Repeat("1", 64), "--results", "/root/control/results.json", "--results-sha256", strings.Repeat("2", 64), "--invocation-nonce", "01234567-89ab-cdef-0123-456789abcdef", "--run-id", "123", "--run-attempt", "1", "--sid", "S-1-5-21-123", "--go-status", "0"})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.status != 0 || parsed.sourceSHA != strings.Repeat("d", 40) || parsed.root != "/root" {
		t.Fatalf("parsed options lost values: %+v", parsed)
	}
}

func TestPolicyDistributionAndEnvironmentBoundaries(t *testing.T) {
	p := coveragePolicy(t)
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyPolicy(p, digest(data)); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	for name, mutate := range map[string]func(*policy){
		"wrong policy digest": func(*policy) {},
		"parent count":        func(p *policy) { p.Parents = p.Parents[:2] },
		"invalid revision":    func(p *policy) { p.RunnerRevision = "short" },
		"missing image":       func(p *policy) { p.RunnerImage = "" },
		"unapproved env":      func(p *policy) { p.Environment = append(p.Environment, "GITHUB_OUTPUT=/tmp/output") },
	} {
		t.Run(name, func(t *testing.T) {
			bad := p
			bad.Parents = append([]string(nil), p.Parents...)
			bad.Environment = append([]string(nil), p.Environment...)
			mutate(&bad)
			encoded, marshalErr := json.Marshal(bad)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			want := digest(encoded)
			if name == "wrong policy digest" {
				want = strings.Repeat("0", 64)
			}
			if err := verifyPolicy(bad, want); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
	if err := verifyDistributions(p.Distributions); err != nil {
		t.Fatalf("valid distributions rejected: %v", err)
	}
	badDistributions := append([]distribution(nil), p.Distributions...)
	badDistributions[0].Name = "unknown"
	if err := verifyDistributions(badDistributions); err == nil {
		t.Fatal("unknown distribution accepted")
	}
	badDistributions = append([]distribution(nil), p.Distributions...)
	badDistributions[1].Files = append(badDistributions[1].Files, badDistributions[1].Files[0])
	if err := verifyDistributions(badDistributions); err == nil {
		t.Fatal("duplicate runtime file accepted")
	}
	badDistributions = append([]distribution(nil), p.Distributions...)
	badDistributions[0].Files = badDistributions[0].Files[:1]
	if err := verifyDistributions(badDistributions); err == nil {
		t.Fatal("incomplete Go runtime accepted")
	}
	if err := verifyEnvironment(cacheEnvironment()); err != nil {
		t.Fatalf("approved environment rejected: %v", err)
	}
	for _, env := range [][]string{
		append(cacheEnvironment(), "PATH=duplicate"),
		append(cacheEnvironment(), "not-a-binding"),
		[]string{"GOTOOLCHAIN=auto"},
	} {
		if err := verifyEnvironment(env); err == nil {
			t.Fatalf("invalid environment accepted: %v", env)
		}
	}
}

func coveragePolicy(t *testing.T) policy {
	t.Helper()
	distributions := []distribution{
		coverageDistribution(t, "go", []string{"go.exe", "compile.exe", "link.exe"}),
		coverageDistribution(t, "git-for-windows", []string{"git.exe", "bash.exe", "sh.exe", "stat.exe", "xargs.exe", "cygpath.exe", "libgit.dll"}),
		coverageDistribution(t, "native-cgo", []string{"gcc.exe", "ld.exe", "libgcc.dll"}),
		coverageDistribution(t, "powershell", []string{"pwsh.exe"}),
	}
	return policy{Parents: []string{strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)}, GoVersion: "go1.27.2", GOARCH: "amd64", Distributions: distributions, Environment: cacheEnvironment(), RunnerImage: "windows-2025", RunnerRevision: strings.Repeat("d", 40), SetupGoRevision: strings.Repeat("e", 40), CheckoutRevision: strings.Repeat("f", 40)}
}

func coverageDistribution(t *testing.T, name string, files []string) distribution {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bindings := make([]binding, 0, len(files))
	for _, filename := range files {
		path := filepath.Join(dir, filename)
		contents := []byte("fixture:" + name + ":" + filename)
		if err := os.WriteFile(path, contents, 0600); err != nil {
			t.Fatal(err)
		}
		bindings = append(bindings, binding{Path: path, SHA256: digest(contents)})
	}
	return distribution{Name: name, Revision: "release-1", Artifact: "official-" + name, SHA256: strings.Repeat("a", 64), Files: bindings}
}

func TestCustodyIdentityBindingsAndBuildInfoFailures(t *testing.T) {
	f := newCoverageCustodyFixture(t)
	if err := verifyCustodyIdentity(f.custody, f.options); err != nil {
		t.Fatalf("valid custody identity rejected: %v", err)
	}
	bad := f.custody
	bad.ControlACL = "bad"
	if err := verifyCustodyIdentity(bad, f.options); err == nil {
		t.Fatal("malformed ACL descriptor accepted")
	}
	if err := verifyBindings(f.custody.Sources, sourcePaths, filepath.Join(f.options.root, "source")); err != nil {
		t.Fatalf("valid source bindings rejected: %v", err)
	}
	if err := verifyBindings([]binding{{Path: "../escape", SHA256: strings.Repeat("a", 64)}}, []string{"../escape"}, f.options.root); err == nil {
		t.Fatal("traversing binding accepted")
	}
	if err := verifyBuilds(f.custody, f.options); err == nil || !strings.Contains(err.Error(), "native clean immutable buildinfo mismatch") {
		t.Fatalf("non-Windows test executable was not rejected as expected: %v", err)
	}
	missing := f.options
	missing.root = filepath.Join(f.options.root, "missing")
	if err := verifyBuilds(f.custody, missing); err == nil {
		t.Fatal("missing build artifact accepted")
	}
	if _, err := verifyCustody(f.options); err == nil || !strings.Contains(err.Error(), "native clean immutable buildinfo mismatch") {
		t.Fatalf("custody gate did not fail closed at platform build identity: %v", err)
	}
}

func TestVerifyCustodyFailsClosedAtExpectedAdmissionBoundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*testing.T, *coverageCustodyFixture)
		want string
	}{
		{
			name: "malformed reviewed source",
			edit: func(_ *testing.T, f *coverageCustodyFixture) { f.options.sourceSHA = "short" },
			want: "missing reviewed source/policy/root expectation",
		},
		{
			name: "malformed policy digest",
			edit: func(_ *testing.T, f *coverageCustodyFixture) { f.options.policySHA = "short" },
			want: "missing reviewed source/policy/root expectation",
		},
		{
			name: "relative trusted root",
			edit: func(_ *testing.T, f *coverageCustodyFixture) { f.options.root = "relative" },
			want: "missing reviewed source/policy/root expectation",
		},
		{
			name: "verifier anchor mismatch",
			edit: func(_ *testing.T, f *coverageCustodyFixture) {
				f.options.verifier = filepath.Join(f.options.root, "bin", "other.exe")
			},
			want: "anchor layout mismatch",
		},
		{
			name: "manifest anchor mismatch",
			edit: func(_ *testing.T, f *coverageCustodyFixture) {
				f.options.manifest = filepath.Join(f.options.root, "other.json")
			},
			want: "anchor layout mismatch",
		},
		{
			name: "malformed verifier digest",
			edit: func(_ *testing.T, f *coverageCustodyFixture) { f.options.verifierSHA = "bad" },
			want: "missing/malformed independent digest",
		},
		{
			name: "bad manifest digest",
			edit: func(_ *testing.T, f *coverageCustodyFixture) { f.options.manifestSHA = strings.Repeat("0", 64) },
			want: "artifact size/digest mismatch",
		},
		{
			name: "malformed manifest JSON",
			edit: func(t *testing.T, f *coverageCustodyFixture) {
				if err := os.WriteFile(f.options.manifest, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
				f.options.manifestSHA = hashCoverageFile(t, f.options.manifest)
			},
			want: "unexpected end of JSON input",
		},
		{
			name: "wrong case inventory",
			edit: func(t *testing.T, f *coverageCustodyFixture) {
				f.custody.Cases = []string{rootTest + "/drive/unknown"}
				writeCoverageJSON(t, f.options.manifest, f.custody)
				f.options.manifestSHA = hashCoverageFile(t, f.options.manifest)
			},
			want: "source-owned case inventory mismatch",
		},
		{
			name: "source binding digest mismatch",
			edit: func(t *testing.T, f *coverageCustodyFixture) {
				f.custody.Sources[0].SHA256 = strings.Repeat("0", 64)
				writeCoverageJSON(t, f.options.manifest, f.custody)
				f.options.manifestSHA = hashCoverageFile(t, f.options.manifest)
			},
			want: "artifact size/digest mismatch",
		},
		{
			name: "artifact binding digest mismatch",
			edit: func(t *testing.T, f *coverageCustodyFixture) {
				f.custody.Artifacts[0].SHA256 = strings.Repeat("0", 64)
				writeCoverageJSON(t, f.options.manifest, f.custody)
				f.options.manifestSHA = hashCoverageFile(t, f.options.manifest)
			},
			want: "artifact size/digest mismatch",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCoverageCustodyFixture(t)
			tc.edit(t, &f)
			if _, err := verifyCustody(f.options); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("verifyCustody error = %v, want predicate %q", err, tc.want)
			}
		})
	}
}

func TestAuthenticatedModeDispatch(t *testing.T) {
	plain := newCoverageCustodyFixture(t)
	if err := dispatchAuthenticatedMode(nil, plain.options, plain.custody); err == nil || err.Error() != "expected verify-custody or audit-results" {
		t.Fatalf("empty authenticated dispatch = %v", err)
	}
	if err := dispatchAuthenticatedMode([]string{"verify-custody"}, plain.options, plain.custody); err != nil {
		t.Fatalf("verify-custody without workload records = %v", err)
	}
	if err := dispatchAuthenticatedMode([]string{"unknown"}, plain.options, plain.custody); err == nil || err.Error() != "unsupported mode" {
		t.Fatalf("unsupported authenticated dispatch = %v", err)
	}

	control := newControlFlowFixture(t)
	if err := dispatchAuthenticatedMode([]string{"verify-custody"}, control.options, control.custody); err != nil {
		t.Fatalf("verify-custody with original control = %v", err)
	}
	if err := dispatchAuthenticatedMode([]string{"audit-results"}, control.options, control.custody); err != nil {
		t.Fatalf("audit-results with original observation = %v", err)
	}
}

func TestValidateBuildInfoComparisonRules(t *testing.T) {
	c := custody{Policy: policy{GoVersion: "go1.27.2", GOARCH: "amd64"}}
	o := options{sourceSHA: strings.Repeat("a", 40)}
	settings := []debug.BuildSetting{
		{Key: "GOOS", Value: "windows"},
		{Key: "GOARCH", Value: "amd64"},
		{Key: "vcs.revision", Value: o.sourceSHA},
		{Key: "vcs.modified", Value: "false"},
		{Key: "vcs", Value: "git"},
	}
	valid := func(extra ...debug.BuildSetting) *debug.BuildInfo {
		all := append(append([]debug.BuildSetting(nil), settings...), extra...)
		return &debug.BuildInfo{GoVersion: c.Policy.GoVersion, Settings: all}
	}

	if err := validateBuildInfo("nativecleanupcheck.exe", valid(), c, o); err != nil {
		t.Fatalf("valid checker metadata rejected: %v", err)
	}
	if err := validateBuildInfo("nativecleanupowner.exe", valid(debug.BuildSetting{Key: "CGO_ENABLED", Value: "1"}), c, o); err != nil {
		t.Fatalf("valid owner metadata rejected: %v", err)
	}
	for _, tc := range []struct {
		name string
		info *debug.BuildInfo
		file string
		want string
	}{
		{name: "duplicate key", info: valid(debug.BuildSetting{Key: "GOOS", Value: "windows"}), file: "nativecleanupcheck.exe", want: "duplicate build setting"},
		{name: "wrong platform", info: func() *debug.BuildInfo { i := valid(); i.Settings[0].Value = "darwin"; return i }(), file: "nativecleanupcheck.exe", want: "native clean immutable buildinfo mismatch"},
		{name: "wrong source revision", info: func() *debug.BuildInfo { i := valid(); i.Settings[2].Value = strings.Repeat("b", 40); return i }(), file: "nativecleanupcheck.exe", want: "native clean immutable buildinfo mismatch"},
		{name: "owner without native cgo", info: valid(), file: "nativecleanupowner.exe", want: "owner requires native CGO build"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateBuildInfo(tc.file, tc.info, c, o); err == nil || err.Error() != tc.want {
				t.Fatalf("validateBuildInfo error = %v, want %q", err, tc.want)
			}
		})
	}
}

type coverageCustodyFixture struct {
	custody custody
	options options
}

func newCoverageCustodyFixture(t *testing.T) coverageCustodyFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"bin", "source", "control"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"nativecleanupowner.exe", "nativecleanupcheck.exe"} {
		if err := os.Link(executable, filepath.Join(root, "bin", name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range sourcePaths {
		full := filepath.Join(root, "source", filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("source:"+path), 0600); err != nil {
			t.Fatal(err)
		}
	}
	goCache := filepath.Join(root, "cache", "01234567-89ab-cdef-0123-456789abcdef", "go-build")
	goModCache := filepath.Join(root, "cache", "01234567-89ab-cdef-0123-456789abcdef", "go-mod")
	for _, path := range []string{goCache, goModCache} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	p := coveragePolicy(t)
	p.GoVersion = runtime.Version()
	for i, value := range p.Environment {
		if strings.HasPrefix(value, "GOCACHE=") {
			p.Environment[i] = "GOCACHE=" + goCache
		}
		if strings.HasPrefix(value, "GOMODCACHE=") {
			p.Environment[i] = "GOMODCACHE=" + goModCache
		}
	}
	// The actual local test executable is bound to these files so checkedFile
	// and buildinfo.ReadFile exercise real bytes, then reject its non-Windows ID.
	executableBytes, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	executableSHA := digest(executableBytes)
	artifactBindings := []binding{{Path: "bin/nativecleanupowner.exe", SHA256: executableSHA}, {Path: "bin/nativecleanupcheck.exe", SHA256: executableSHA}}
	sources := make([]binding, 0, len(sourcePaths))
	for _, path := range sourcePaths {
		contents, err := os.ReadFile(filepath.Join(root, "source", filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, binding{Path: path, SHA256: digest(contents)})
	}
	c := custody{Schema: "native-cleanup-custody/v1", OwnerSource: strings.Repeat("a", 40), Policy: p, Sources: sources, Artifacts: artifactBindings, Cases: caseInventory(), InventorySHA256: digest([]byte(strings.Join(caseInventory(), "\n") + "\n")), ControlVolumeFileID: "12345678:1234567812345678", ControlACL: strings.Repeat("b", 64), ControlDirectory: filepath.Join(root, "control")}
	policyJSON, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	policySHA := digest(policyJSON)
	manifestBytes, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "custody.json")
	if err := os.WriteFile(manifestPath, manifestBytes, 0600); err != nil {
		t.Fatal(err)
	}
	o := options{root: root, sourceSHA: c.OwnerSource, policySHA: policySHA, manifest: manifestPath, manifestSHA: digest(manifestBytes), verifier: filepath.Join(root, "bin", "nativecleanupcheck.exe"), verifierSHA: executableSHA, goCache: goCache, goModCache: goModCache, invocation: "01234567-89ab-cdef-0123-456789abcdef"}
	return coverageCustodyFixture{custody: c, options: o}
}

func TestOriginalControlAndAuditRecordFlow(t *testing.T) {
	f := newControlFlowFixture(t)
	if err := verifyRecords(f.options, f.custody); err != nil {
		t.Fatalf("valid allocation/join records rejected: %v", err)
	}
	if err := verifyOriginalControl(f.options, f.custody); err != nil {
		t.Fatalf("valid original control rejected: %v", err)
	}
	if err := auditCustodyResults(f.options, f.custody); err != nil {
		t.Fatalf("valid bounded result stream rejected: %v", err)
	}
	wrongRun := f.options
	wrongRun.runID = "456"
	if err := verifyRecords(wrongRun, f.custody); err == nil {
		t.Fatal("join from a different run accepted")
	}
	wrongStatus := f.options
	wrongStatus.status = 1
	if err := auditCustodyResults(wrongStatus, f.custody); err == nil {
		t.Fatal("wrong terminal status accepted")
	}
}

func TestOriginalControlRejectsExpiredJoinedAuthority(t *testing.T) {
	f := newControlFlowFixture(t)
	start := time.Now().Add(-31 * time.Second).UTC()
	join := f.join
	join.CleanupStarted = start.Format(time.RFC3339Nano)
	join.CleanupFinished = start.Add(time.Second).Format(time.RFC3339Nano)
	writeCoverageJSON(t, f.joinPath, join)
	joinBytes, err := os.ReadFile(f.joinPath)
	if err != nil {
		t.Fatal(err)
	}
	control := controlMessage{Schema: "native-cleanup-control/v1", JoinRecord: f.joinPath, JoinSHA256: digest(joinBytes)}
	writeCoverageJSON(t, f.controlPath, control)
	f.options.controlSHA = hashCoverageFile(t, f.controlPath)
	if err := verifyOriginalControl(f.options, f.custody); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired original authority accepted: %v", err)
	}
}

type controlFlowFixture struct {
	custody     custody
	options     options
	join        joinRecord
	controlPath string
	joinPath    string
}

func newControlFlowFixture(t *testing.T) controlFlowFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	controlDir := filepath.Join(root, "control")
	if err := os.Mkdir(controlDir, 0700); err != nil {
		t.Fatal(err)
	}
	a := validAllocation()
	a.Path = filepath.Join(root, filepath.Base(a.Path))
	allocationBytes, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	allocationPath := filepath.Join(controlDir, "allocation.json")
	if err := os.WriteFile(allocationPath, allocationBytes, 0600); err != nil {
		t.Fatal(err)
	}
	results := passingStream(t)
	resultsPath := filepath.Join(controlDir, "results.jsonl")
	if err := os.WriteFile(resultsPath, results, 0600); err != nil {
		t.Fatal(err)
	}
	stderrPath := filepath.Join(controlDir, "stderr.txt")
	if err := os.WriteFile(stderrPath, []byte{}, 0600); err != nil {
		t.Fatal(err)
	}
	join := validJoin(a)
	join.AllocationSHA256 = digest(allocationBytes)
	join.Status = 0
	join.Stdout = binding{Path: resultsPath, SHA256: digest(results)}
	join.Stderr = binding{Path: stderrPath, SHA256: digest(nil)}
	joinPath := filepath.Join(controlDir, "join.json")
	writeCoverageJSON(t, joinPath, join)
	joinBytes, err := os.ReadFile(joinPath)
	if err != nil {
		t.Fatal(err)
	}
	controlPath := filepath.Join(controlDir, "owner-control.json")
	writeCoverageJSON(t, controlPath, controlMessage{Schema: "native-cleanup-control/v1", JoinRecord: joinPath, JoinSHA256: digest(joinBytes)})
	controlBytes, err := os.ReadFile(controlPath)
	if err != nil {
		t.Fatal(err)
	}
	c := custody{ControlDirectory: controlDir, Artifacts: []binding{{Path: "bin/nativecleanupowner.exe", SHA256: a.OwnerSHA256}}}
	o := options{root: root, control: controlPath, controlSHA: digest(controlBytes), allocation: allocationPath, allocationSHA: digest(allocationBytes), join: joinPath, joinSHA: digest(joinBytes), results: resultsPath, resultsSHA: digest(results), invocation: a.InvocationNonce, runID: a.RunID, attempt: a.RunAttempt, sid: a.SID, sourceSHA: a.OwnerSource, manifestSHA: a.ManifestSHA256, status: 0}
	return controlFlowFixture{custody: c, options: o, join: join, controlPath: controlPath, joinPath: joinPath}
}

func writeCoverageJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func hashCoverageFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestRequiredSliceJSONAndReparseMetadataControls(t *testing.T) {
	type envelope struct {
		Files []binding `json:"files"`
	}
	good := []byte(`{"files":[{"path":"src/main.go","sha256":"` + strings.Repeat("a", 64) + `"}]}`)
	var parsed envelope
	if err := strictJSON(good, &parsed); err != nil || len(parsed.Files) != 1 {
		t.Fatalf("valid nested slice rejected: %v %+v", err, parsed)
	}
	var array []string
	if err := strictJSON([]byte(`["one","two"]`), &array); err != nil || !reflect.DeepEqual(array, []string{"one", "two"}) {
		t.Fatalf("valid scalar array rejected: %v %v", err, array)
	}
	for _, bad := range [][]byte{
		[]byte(`{"files":null}`),
		[]byte(`{"files":[{"path":"src/main.go","sha256":null}]}`),
		[]byte(`["one","one"]`),
	} {
		var result envelope
		if err := strictJSON(bad, &result); err == nil {
			t.Fatalf("malformed required-slice JSON accepted: %s", bad)
		}
	}
	for _, tc := range []struct {
		name string
		sys  any
		want bool
	}{
		{name: "reparse tag", sys: &struct{ FileAttributes uint32 }{FileAttributes: 0x400}, want: true},
		{name: "ordinary attributes", sys: &struct{ FileAttributes uint32 }{FileAttributes: 0x20}},
		{name: "wrong integer width", sys: &struct{ FileAttributes uint64 }{FileAttributes: 0x400}},
		{name: "non-struct platform info", sys: "opaque"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasReparseAttribute(&coverageFileInfo{sys: tc.sys}); got != tc.want {
				t.Fatalf("hasReparseAttribute = %t, want %t", got, tc.want)
			}
		})
	}
}

type coverageFileInfo struct{ sys any }

func (i *coverageFileInfo) Name() string       { return "fixture" }
func (i *coverageFileInfo) Size() int64        { return 0 }
func (i *coverageFileInfo) Mode() os.FileMode  { return 0600 }
func (i *coverageFileInfo) ModTime() time.Time { return time.Time{} }
func (i *coverageFileInfo) IsDir() bool        { return false }
func (i *coverageFileInfo) Sys() any           { return i.sys }

func TestCoveragePackageEventStateMachineRejectsInvalidTransitions(t *testing.T) {
	for _, tc := range []struct {
		name       string
		action     string
		started    bool
		passed     bool
		wantErr    bool
		wantStart  bool
		wantPassed bool
	}{
		{name: "start package", action: "start", wantStart: true},
		{name: "duplicate package start", action: "start", started: true, wantErr: true, wantStart: true},
		{name: "pass before start", action: "pass", wantErr: true},
		{name: "pass package", action: "pass", started: true, wantStart: true, wantPassed: true},
		{name: "output before start", action: "output", wantErr: true},
		{name: "output after start", action: "output", started: true, wantStart: true},
		{name: "unexpected package action", action: "skip", wantErr: true},
	} {
		t.Run("package/"+tc.name, func(t *testing.T) {
			started, passed := tc.started, tc.passed
			err := packageEvent(testEvent{Action: tc.action}, &started, &passed)
			if (err != nil) != tc.wantErr || started != tc.wantStart || passed != tc.wantPassed {
				t.Fatalf("packageEvent = (%v, started=%t, passed=%t)", err, started, passed)
			}
		})
	}
}

func TestCoverageLeafEventStateMachineRejectsInvalidTransitions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		action   string
		run      bool
		pass     bool
		wantErr  bool
		wantRun  bool
		wantPass bool
	}{
		{name: "run test", action: "run", wantRun: true},
		{name: "duplicate run", action: "run", run: true, wantErr: true, wantRun: true},
		{name: "pass before run", action: "pass", wantErr: true},
		{name: "pass test", action: "pass", run: true, wantRun: true, wantPass: true},
		{name: "duplicate pass", action: "pass", run: true, pass: true, wantErr: true, wantRun: true, wantPass: true},
		{name: "output before run", action: "output", wantErr: true},
		{name: "output after pass", action: "output", run: true, pass: true, wantErr: true, wantRun: true, wantPass: true},
		{name: "output while running", action: "output", run: true, wantRun: true},
		{name: "skip is not a pass", action: "skip", wantErr: true},
	} {
		t.Run("leaf/"+tc.name, func(t *testing.T) {
			runs, passes := map[string]bool{}, map[string]bool{}
			if tc.run {
				runs["TestControl"] = true
			}
			if tc.pass {
				passes["TestControl"] = true
			}
			err := leafEvent(testEvent{Action: tc.action, Test: "TestControl"}, runs, passes)
			if (err != nil) != tc.wantErr || runs["TestControl"] != tc.wantRun || passes["TestControl"] != tc.wantPass {
				t.Fatalf("leafEvent = (%v, run=%t, pass=%t)", err, runs["TestControl"], passes["TestControl"])
			}
		})
	}
}

func TestCoverageHierarchyAndBoundedResultClassification(t *testing.T) {
	a := resultAudit{expected: []string{rootTest, rootTest + "/drive", rootTest + "/drive/case"}, runs: map[string]bool{}, passes: map[string]bool{}}
	if err := a.hierarchy(testEvent{Action: "run", Test: rootTest + "/drive/case"}); err == nil || !strings.Contains(err.Error(), "child outside live parent") {
		t.Fatalf("child without live parent accepted: %v", err)
	}
	a.runs[rootTest] = true
	a.runs[rootTest+"/drive"] = true
	if err := a.hierarchy(testEvent{Action: "pass", Test: rootTest + "/drive"}); err == nil || !strings.Contains(err.Error(), "parent passed") {
		t.Fatalf("parent passed before required child: %v", err)
	}
	if err := a.hierarchy(testEvent{Action: "run", Test: rootTest + "/drive"}); err != nil {
		t.Fatalf("nonterminal parent event rejected: %v", err)
	}

	good := passingStream(t)
	if err := auditBoundResults(good, 0); err != nil {
		t.Fatalf("authenticated exact result stream rejected: %v", err)
	}
	if err := auditBoundResults(good, 1); checkerExit(err) != 3 {
		t.Fatalf("well-formed nonzero subject result exit = %d, want 3 (err %v)", checkerExit(err), err)
	}
	if err := auditBoundResults([]byte("not-json\n"), 0); err == nil || checkerExit(err) != 1 {
		t.Fatalf("malformed stream did not remain a checker failure: %v", err)
	}
}

func TestCoverageAllocationAndJoinBoundaryTables(t *testing.T) {
	a := validAllocation()
	o := options{invocation: a.InvocationNonce, runID: a.RunID, attempt: a.RunAttempt, sid: a.SID, sourceSHA: a.OwnerSource, manifestSHA: a.ManifestSHA256}
	for _, state := range []string{"registered", "directory-pending", "acl-pending", "share-pending"} {
		t.Run("partial-state/"+state, func(t *testing.T) {
			partial := a
			partial.State = state
			partial.VolumeFileID, partial.NTFSACL, partial.ShareACL = "", "", ""
			if err := validateAllocation(partial, o); err != nil {
				t.Fatalf("valid partial state %q rejected: %v", state, err)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		mutate func(*allocation)
		want   string
	}{
		{"unknown partial state", func(a *allocation) { a.State = "unknown" }, "invalid partial allocation state"},
		{"ready without volume identity", func(a *allocation) { a.VolumeFileID = "" }, "incomplete allocation descriptors"},
		{"ready without ACL", func(a *allocation) { a.ShareACL = "" }, "incomplete allocation descriptors"},
		{"nontemporary allocation", func(a *allocation) { a.Temporary = false }, "invalid allocation premise"},
		{"relative path", func(a *allocation) { a.Path = "relative/lopper" }, "invalid allocation premise"},
		{"unexpected share", func(a *allocation) { a.Share = "other" }, "invalid allocation premise"},
		{"bad owner digest", func(a *allocation) { a.OwnerSHA256 = "bad" }, "invalid allocation premise"},
		{"identity mismatch", func(a *allocation) { a.RunID = "124" }, "allocation identity mismatch"},
	} {
		t.Run("allocation/"+tc.name, func(t *testing.T) {
			bad := a
			tc.mutate(&bad)
			if err := validateAllocation(bad, o); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("invalid allocation accepted or wrong error: %v", err)
			}
		})
	}

	j := validJoin(a)
	for _, tc := range []struct {
		name   string
		mutate func(*joinRecord)
		want   string
	}{
		{"no owner process", func(j *joinRecord) { j.OwnerPID = 0 }, "missing original owner identity"},
		{"missing helper join", func(j *joinRecord) { j.HelpersJoined = false }, "incomplete original owner join"},
		{"unknown workload", func(j *joinRecord) { j.Workload = "other" }, "incomplete original owner join"},
		{"invalid timestamp", func(j *joinRecord) { j.CleanupStarted = "not-a-time" }, "parsing time"},
		{"clock reversal", func(j *joinRecord) {
			start, err := time.Parse(time.RFC3339Nano, j.CleanupStarted)
			if err != nil {
				t.Fatal(err)
			}
			j.CleanupFinished = start.Add(-time.Second).Format(time.RFC3339Nano)
		}, "join exceeded single reserve"},
	} {
		t.Run("join/"+tc.name, func(t *testing.T) {
			bad := j
			tc.mutate(&bad)
			err := validateJoin(bad)
			if err == nil || (tc.want != "" && !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("invalid join accepted or wrong error: %v", err)
			}
		})
	}
}

func TestCoverageOriginalAuthorityDeadlineAndCacheBindings(t *testing.T) {
	start := time.Now().UTC()
	join := joinRecord{CleanupStarted: start.Format(time.RFC3339Nano)}
	if err := originalAuthorityDeadline(join, start.Add(time.Second)); err != nil {
		t.Fatalf("live original authority rejected: %v", err)
	}
	for _, now := range []time.Time{start.Add(-time.Second), start.Add(30 * time.Second)} {
		if err := originalAuthorityDeadline(join, now); err == nil {
			t.Fatalf("inconsistent/expired authority accepted at %s", now)
		}
	}

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	invocation := "01234567-89ab-cdef-0123-456789abcdef"
	goCache := filepath.Join(root, "cache", invocation, "go-build")
	modCache := filepath.Join(root, "cache", invocation, "go-mod")
	for _, dir := range []string{goCache, modCache} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	o := options{root: root, invocation: invocation, goCache: goCache, goModCache: modCache}
	env := []string{"GOCACHE=" + goCache, "GOMODCACHE=" + modCache}
	if err := verifyCaches(env, o); err != nil {
		t.Fatalf("matching invocation-private caches rejected: %v", err)
	}
	if err := verifyCaches(env, options{root: root}); err == nil || !strings.Contains(err.Error(), "missing cache invocation") {
		t.Fatalf("missing invocation accepted: %v", err)
	}
	env[0] = "GOCACHE=" + filepath.Join(root, "shared")
	if err := verifyCaches(env, o); err == nil {
		t.Fatal("cache outside the invocation-private root accepted")
	}
}

func TestCoverageRecordPresenceAndOwnerBindingFailures(t *testing.T) {
	if err := verifyRecords(options{}, custody{}); err != nil {
		t.Fatalf("absence of both allocation and join should be a valid no-workload record: %v", err)
	}
	if err := verifyRecords(options{join: "/unused/join.json"}, custody{}); err == nil || err.Error() != "join requires allocation" {
		t.Fatalf("join without allocation = %v", err)
	}

	f := newControlFlowFixture(t)
	allocationOnly := f.options
	allocationOnly.join = ""
	allocationOnly.joinSHA = ""
	if err := verifyRecords(allocationOnly, f.custody); err != nil {
		t.Fatalf("valid allocation without a join rejected: %v", err)
	}

	wrongOwner := f.custody
	wrongOwner.Artifacts = append([]binding(nil), f.custody.Artifacts...)
	wrongOwner.Artifacts[0].SHA256 = strings.Repeat("0", 64)
	if err := verifyRecords(allocationOnly, wrongOwner); err == nil || err.Error() != "allocation owner artifact mismatch" {
		t.Fatalf("allocation bound to another owner artifact = %v", err)
	}
}

func TestAuditCustodyResultsRejectsMissingAndMismatchedOriginalObservation(t *testing.T) {
	if err := auditCustodyResults(options{}, custody{}); err == nil || err.Error() != "audit requires original join and results" {
		t.Fatalf("audit without original records = %v", err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*controlFlowFixture)
	}{
		{"wrong workload", func(f *controlFlowFixture) {
			f.join.Workload = "no-workload-launched"
			writeCoverageJSON(t, f.joinPath, f.join)
			f.options.joinSHA = hashCoverageFile(t, f.joinPath)
		}},
		{"wrong result path", func(f *controlFlowFixture) {
			f.join.Stdout.Path = filepath.Join(filepath.Dir(f.options.results), "other-results.jsonl")
			if err := os.WriteFile(f.join.Stdout.Path, passingStream(t), 0600); err != nil {
				t.Fatal(err)
			}
			f.join.Stdout.SHA256 = hashCoverageFile(t, f.join.Stdout.Path)
			writeCoverageJSON(t, f.joinPath, f.join)
			f.options.joinSHA = hashCoverageFile(t, f.joinPath)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newControlFlowFixture(t)
			tc.mutate(&f)
			err := auditCustodyResults(f.options, f.custody)
			if err == nil || err.Error() != "audit does not match original terminal Go observation" {
				t.Fatalf("mismatched original observation accepted: %v", err)
			}
		})
	}
}

func TestVerifyOriginalControlRejectsLayoutAndControlBindings(t *testing.T) {
	f := newControlFlowFixture(t)
	wrongControl := f.options
	wrongControl.control = filepath.Join(f.options.root, "control", "alternate-control.json")
	if err := verifyOriginalControl(wrongControl, f.custody); err == nil || err.Error() != "original control layout mismatch" {
		t.Fatalf("control from an unbound path = %v", err)
	}

	for _, tc := range []struct {
		name string
		msg  controlMessage
	}{
		{"wrong schema", controlMessage{Schema: "native-cleanup-control/v9", JoinRecord: f.joinPath, JoinSHA256: f.options.joinSHA}},
		{"wrong join path", controlMessage{Schema: "native-cleanup-control/v1", JoinRecord: filepath.Join(filepath.Dir(f.joinPath), "other.json"), JoinSHA256: f.options.joinSHA}},
		{"malformed join digest", controlMessage{Schema: "native-cleanup-control/v1", JoinRecord: f.joinPath, JoinSHA256: "bad"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeCoverageJSON(t, f.controlPath, tc.msg)
			f.options.controlSHA = hashCoverageFile(t, f.controlPath)
			err := verifyOriginalControl(f.options, f.custody)
			if err == nil || err.Error() != "original control schema/path/digest mismatch" {
				t.Fatalf("invalid control accepted: %v", err)
			}
		})
	}
}

func TestVerifyBindingsRejectsInventoryLengthMismatch(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyBindings(nil, []string{"expected.go"}, root); err == nil || err.Error() != "source/artifact inventory mismatch" {
		t.Fatalf("short binding inventory = %v", err)
	}
}

func TestVerifyDistributionsRejectsAbsentBoundRuntimeAndDLL(t *testing.T) {
	p := coveragePolicy(t)
	missing := append([]distribution(nil), p.Distributions...)
	missing[0].Files = append([]binding(nil), missing[0].Files...)
	missing[0].Files[0].Path = filepath.Join(filepath.Dir(missing[0].Files[0].Path), "absent-go.exe")
	if err := verifyDistributions(missing); err == nil {
		t.Fatal("missing authenticated runtime file accepted")
	}
	withoutDLL := append([]distribution(nil), p.Distributions...)
	withoutDLL[1].Files = append([]binding(nil), withoutDLL[1].Files...)
	filtered := withoutDLL[1].Files[:0]
	for _, file := range withoutDLL[1].Files {
		if !strings.HasSuffix(strings.ToLower(file.Path), ".dll") {
			filtered = append(filtered, file)
		}
	}
	withoutDLL[1].Files = filtered
	if err := requiredRuntimeFiles(withoutDLL); err == nil || err.Error() != "missing bundled Git runtime DLL identities" {
		t.Fatalf("Git distribution without a bundled DLL = %v", err)
	}
}
