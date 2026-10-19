package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func passingStream(t *testing.T) []byte {
	t.Helper()
	var stream strings.Builder
	event := func(action, name string) {
		data, err := json.Marshal(testEvent{Action: action, Test: name, Package: subjectPackage})
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(&stream, string(data))
	}
	event("start", "")
	event("run", rootTest)
	for _, scheme := range []string{"drive", "unc"} {
		event("run", rootTest+"/"+scheme)
		for _, name := range caseInventory() {
			if strings.HasPrefix(name, rootTest+"/"+scheme+"/") {
				event("run", name)
				event("pass", name)
			}
		}
		event("pass", rootTest+"/"+scheme)
	}
	event("pass", rootTest)
	event("pass", "")
	return []byte(stream.String())
}
func TestExactResultAudit(t *testing.T) {
	good := passingStream(t)
	if err := auditResults(good, 0); err != nil {
		t.Fatal(err)
	}
	leaf := rootTest + "/unc/failed-config"
	controls := map[string][]byte{
		"empty": {}, "truncated": good[:len(good)-6], "missing-leaf": []byte(strings.ReplaceAll(string(good), leaf, rootTest+"/unc/unknown")),
		"skip":            []byte(strings.Replace(string(good), `"Action":"pass"`, `"Action":"skip"`, 1)),
		"fail":            []byte(strings.Replace(string(good), `"Action":"pass"`, `"Action":"fail"`, 1)),
		"duplicate-run":   append(good[:0:0], good...),
		"case-alias":      []byte(strings.Replace(string(good), `"Action":"start"`, `"Action":"start","action":"start"`, 1)),
		"unknown-field":   []byte(strings.Replace(string(good), `"Action":"start"`, `"Extra":true,"Action":"start"`, 1)),
		"duplicate-field": []byte(strings.Replace(string(good), `"Action":"start"`, `"Action":"start","Action":"start"`, 1)),
		"wrong-package":   []byte(strings.ReplaceAll(string(good), subjectPackage, "malicious")),
		"null-action":     []byte(strings.Replace(string(good), `"Action":"start"`, `"Action":null`, 1)),
		"aggregate-only":  []byte(fmt.Sprintf("{\"Action\":\"start\",\"Package\":%q}\n{\"Action\":\"pass\",\"Package\":%q}\n", subjectPackage, subjectPackage)),
	}
	lines := strings.Split(string(good), "\n")
	controls["duplicate-run"] = []byte(strings.Join(append(lines[:2:2], append([]string{lines[1]}, lines[2:]...)...), "\n"))
	for name, data := range controls {
		t.Run(name, func(t *testing.T) {
			if err := auditResults(data, 0); err == nil {
				t.Fatal("malformed result accepted")
			}
		})
	}
	if err := auditResults(good, 1); err == nil {
		t.Fatal("nonzero Go status accepted")
	}
}
func TestCustodyParserRejectsEveryMissingField(t *testing.T) {
	value := custody{Schema: "native-cleanup-custody/v1", OwnerSource: strings.Repeat("a", 40), Policy: policy{Parents: []string{}, Distributions: []distribution{}, Environment: []string{}}, Sources: []binding{}, Artifacts: []binding{}, Cases: []string{}, InventorySHA256: strings.Repeat("b", 64), ControlDirectory: "control"}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	assertMissingCustodyFields(t, data)
	for _, bad := range []string{strings.Replace(string(data), `"schema":`, `"schema":"bad","schema":`, 1), strings.Replace(string(data), `"policy":{`, `"policy":{"extra":true,`, 1), strings.Replace(string(data), `"parents":[]`, `"parents":null`, 1), strings.Replace(string(data), `"schema":"native-cleanup-custody/v1"`, `"schema":123`, 1), string(data) + " {}"} {
		if err := strictJSON([]byte(bad), &custody{}); err == nil {
			t.Fatal("malformed custody accepted", bad)
		}
	}
}
func assertMissingCustodyFields(t *testing.T, data []byte) {
	t.Helper()
	var err error
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if err = strictJSON(data, &custody{}); err != nil {
		t.Fatal(err)
	}
	for key := range fields {
		t.Run(key, func(t *testing.T) {
			copyFields := maps.Clone(fields)
			delete(copyFields, key)
			raw, err := json.Marshal(copyFields)
			if err != nil {
				t.Fatal(err)
			}
			if err = strictJSON(raw, &custody{}); err == nil {
				t.Fatal("missing field accepted")
			}
		})
	}
}
func TestHashLayoutAndInventoryControls(t *testing.T) {
	root, rootErr := filepath.EvalSymlinks(t.TempDir())
	if rootErr != nil {
		t.Fatal(rootErr)
	}
	file := filepath.Join(root, "data")
	if err := os.WriteFile(file, []byte("bound"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := checkedFile(file, digest([]byte("bound")), root, 64); err != nil {
		t.Fatal(err)
	}
	for _, control := range []struct {
		path, sha, root string
		limit           int64
	}{{file, "", root, 64}, {file, strings.Repeat("0", 64), root, 64}, {file, digest([]byte("bound")), root, 2}, {file, digest([]byte("bound")), filepath.Join(root, "nested"), 64}, {root, digest([]byte("bound")), root, 64}} {
		if _, err := checkedFile(control.path, control.sha, control.root, control.limit); err == nil {
			t.Fatal("unsafe anchor accepted")
		}
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := checkedFile(link, digest([]byte("bound")), root, 64); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := verifyBindings([]binding{{Path: "data", SHA256: digest([]byte("bound"))}}, []string{"data"}, root); err != nil {
		t.Fatal(err)
	}
	if err := verifyBindings([]binding{{Path: "data", SHA256: digest([]byte("bound"))}, {Path: "data", SHA256: digest([]byte("bound"))}}, []string{"data", "other"}, root); err == nil {
		t.Fatal("duplicate/missing source accepted")
	}
}
func validAllocation() allocation {
	return allocation{Schema: "native-cleanup-allocation/v1", AllocationNonce: "01234567-89ab-cdef-0123-456789abcdef", InvocationNonce: "11234567-89ab-cdef-0123-456789abcdef", RunID: "123", RunAttempt: "1", SID: "S-1-5-21-123", Share: "lopper-01234567-89ab-cdef-0123-456789abcdef", Temporary: true, Path: filepath.Join(string(filepath.Separator), "tmp", "lopper-01234567-89ab-cdef-0123-456789abcdef"), VolumeFileID: "12345678:1234567812345678", NTFSACL: strings.Repeat("c", 64), ShareACL: strings.Repeat("d", 64), ManifestSHA256: strings.Repeat("e", 64), OwnerSource: strings.Repeat("f", 40), OwnerSHA256: strings.Repeat("a", 64), State: "ready", SentinelSHA256: strings.Repeat("b", 64)}
}
func validJoin(a allocation) joinRecord {
	start := time.Now()
	return joinRecord{OwnerPID: 123, Schema: "native-cleanup-join/v1", AllocationSHA256: strings.Repeat("1", 64), AllocationNonce: a.AllocationNonce, InvocationNonce: a.InvocationNonce, RunID: a.RunID, RunAttempt: a.RunAttempt, ManifestSHA256: a.ManifestSHA256, OwnerSource: a.OwnerSource, OwnerSHA256: a.OwnerSHA256, Workload: "subject-go", Status: 1, OriginalCleanup: true, RootWait: true, Drains: true, HelpersJoined: true, CleanupStarted: start.UTC().Format(time.RFC3339Nano), CleanupFinished: start.Add(time.Second).UTC().Format(time.RFC3339Nano)}
}
func TestOriginalJoinAndAllocationControls(t *testing.T) {
	a := validAllocation()
	o := options{invocation: a.InvocationNonce, runID: a.RunID, attempt: a.RunAttempt, sid: a.SID, sourceSHA: a.OwnerSource, manifestSHA: a.ManifestSHA256}
	if err := validateAllocation(a, o); err != nil {
		t.Fatal(err)
	}
	j := validJoin(a)
	if err := validateJoin(j); err != nil {
		t.Fatal(err)
	}
	if err := matchJoin(j, a, j.AllocationSHA256); err != nil {
		t.Fatal(err)
	}
	controls := map[string]func(*joinRecord){"missing-original": func(j *joinRecord) { j.OriginalCleanup = false }, "failed-wait": func(j *joinRecord) { j.RootWait = false }, "failed-drain": func(j *joinRecord) { j.Drains = false }, "helper-uncertain": func(j *joinRecord) { j.HelpersJoined = false }, "unknown-status": func(j *joinRecord) { j.Status = -1 }, "fake-workload": func(j *joinRecord) { j.Workload = "logs-absent" }, "fresh-deadline": func(j *joinRecord) {
		start, parseErr := time.Parse(time.RFC3339Nano, j.CleanupStarted)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		j.CleanupFinished = start.Add(30 * time.Second).Format(time.RFC3339Nano)
	}}
	for name, change := range controls {
		t.Run(name, func(t *testing.T) {
			bad := j
			change(&bad)
			if err := validateJoin(bad); err == nil {
				t.Fatal("uncertain join accepted")
			}
		})
	}
	for _, change := range []func(*joinRecord){func(j *joinRecord) { j.RunAttempt = "2" }, func(j *joinRecord) { j.InvocationNonce = a.AllocationNonce }, func(j *joinRecord) { j.OwnerSHA256 = strings.Repeat("0", 64) }, func(j *joinRecord) { j.AllocationSHA256 = "" }} {
		bad := j
		change(&bad)
		if err := matchJoin(bad, a, j.AllocationSHA256); err == nil {
			t.Fatal("substituted original join accepted")
		}
	}
	a.Temporary = false
	if err := validateAllocation(a, o); err == nil {
		t.Fatal("durable share accepted")
	}
}
func TestCustodyBeforeExecutionAndManifestParsing(t *testing.T) {
	root, rootErr := filepath.EvalSymlinks(t.TempDir())
	if rootErr != nil {
		t.Fatal(rootErr)
	}
	if err := os.Mkdir(filepath.Join(root, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	verifier := filepath.Join(root, "bin", "nativecleanupcheck.exe")
	manifest := filepath.Join(root, "custody.json")
	marker := filepath.Join(root, "executed")
	sentinel := []byte("#!/bin/sh\ntouch " + marker + "\n")
	if err := os.WriteFile(verifier, sentinel, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	o := options{root: root, verifier: verifier, verifierSHA: strings.Repeat("0", 64), manifest: manifest, manifestSHA: digest([]byte("not json")), sourceSHA: strings.Repeat("a", 40), policySHA: strings.Repeat("b", 64)}
	if _, err := verifyCustody(o); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatal("did not reject anchor before manifest parser", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("sentinel verifier executed")
	}
	// This checks the Go guard only. The workflow PowerShell bootstrap remains
	// separate unexecuted review text and needs its own native marker controls.
}
func TestRuntimePolicyCannotBeManifestChosen(t *testing.T) {
	p := policy{Parents: []string{strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)}, Distributions: []distribution{}, Environment: []string{}}
	if err := verifyPolicy(p, strings.Repeat("0", 64)); err == nil {
		t.Fatal("unreviewed policy accepted")
	}
	if err := verifyEnvironment([]string{"GITHUB_OUTPUT=/tmp/channel"}); err == nil {
		t.Fatal("action channel inherited")
	}
	if err := requiredRuntimeFiles([]distribution{{Name: "native-cgo", Files: []binding{{Path: "gcc.exe"}}}}); err == nil {
		t.Fatal("compiler-only metadata accepted as complete native runtime")
	}
}

func TestOriginalControlParser(t *testing.T) {
	good := []byte(`{"schema":"native-cleanup-control/v1","join_record":"/original/join.json","join_record_sha256":"` + strings.Repeat("a", 64) + `"}`)
	if err := strictJSON(good, &controlMessage{}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(string(good), `"schema":`, `"schema":"forged","schema":`, 1), strings.Replace(string(good), `"schema":`, `"unknown":true,"schema":`, 1), strings.Replace(string(good), `"join_record":"/original/join.json",`, "", 1)} {
		if err := strictJSON([]byte(bad), &controlMessage{}); err == nil {
			t.Fatal("permissive original control parser")
		}
	}
}

func cacheEnvironment() []string {
	return []string{"PATH=C:/approved/bin", "SystemRoot=C:/Windows", "USERPROFILE=C:/Users/runner", "TEMP=C:/private/temp", "GOROOT=C:/approved/go", "GOARCH=amd64", "CC=C:/approved/cc/gcc.exe", "LOPPER_NATIVE_GIT=C:/approved/git/git.exe", "LOPPER_NATIVE_BASH=C:/approved/git/bash.exe", "GOTOOLCHAIN=local", "GOENV=off", "GOWORK=off", "GOFLAGS=", "GOOS=windows", "CGO_ENABLED=1", "GOCACHE=C:/private/cache/go-build", "GOMODCACHE=C:/private/cache/go-mod"}
}
func TestPrivateCacheEnvironmentRequired(t *testing.T) {
	good := cacheEnvironment()
	if err := verifyEnvironment(good); err != nil {
		t.Fatal("private caches rejected", err)
	}
	for _, key := range []string{"GOCACHE", "GOMODCACHE"} {
		var missing []string
		for _, value := range good {
			if !strings.HasPrefix(value, key+"=") {
				missing = append(missing, value)
			}
		}
		if err := verifyEnvironment(missing); err == nil {
			t.Fatal("missing private cache accepted", key)
		}
	}
	for _, key := range []string{"LocalAppData", "LOCALAPPDATA", "GOCACHEPROG", "gocache"} {
		if err := verifyEnvironment(append(append([]string{}, good...), key+"=injected")); err == nil {
			t.Fatal("injected environment accepted", key)
		}
	}
}

func TestAuditFailureClassification(t *testing.T) {
	good := passingStream(t)
	for _, status := range []int{1, 7} {
		if code := checkerExit(auditBoundResults(good, status)); code != 3 {
			t.Fatal("collected subject failure lost", code)
		}
	}
	if checkerExit(auditBoundResults(good, 0)) != 0 {
		t.Fatal("valid result rejected")
	}
	if checkerExit(auditBoundResults([]byte(strings.Replace(string(good), `"Action":"pass"`, `"Action":"skip"`, 1)), 0)) != 3 {
		t.Fatal("semantic rejection lost")
	}
	for _, data := range [][]byte{good[:len(good)-6], []byte(`{"Action":"run","Action":"pass"}`), []byte(`{"unknown":true}`)} {
		for _, status := range []int{0, 1} {
			if code := checkerExit(auditBoundResults(data, status)); code != 1 {
				t.Fatal("format uncertainty classified as safe rejection", code)
			}
		}
	}
	if checkerExit(fmt.Errorf("custody unavailable")) != 1 {
		t.Fatal("custody error not fatal")
	}
	if checkerExit(fmt.Errorf("wrapped: %w", &subjectResultError{cause: fmt.Errorf("subject")})) != 1 {
		t.Fatal("wrapped uncertainty accepted")
	}
}
func TestOriginalAuthorityExpiresAfterGuard(t *testing.T) {
	j := validJoin(validAllocation())
	start, err := time.Parse(time.RFC3339Nano, j.CleanupStarted)
	if err != nil {
		t.Fatal(err)
	}
	if err := originalAuthorityDeadline(j, start.Add(29*time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, now := range []time.Time{start.Add(-time.Nanosecond), start.Add(30 * time.Second), start.Add(31 * time.Second)} {
		if err := originalAuthorityDeadline(j, now); err == nil {
			t.Fatal("expired/inconsistent authority accepted", now)
		}
	}
}

func TestOriginalControlPropagatesMalformedHashBoundJoin(t *testing.T) {
	f := newControlFlowFixture(t)
	f.join.OwnerPID = 0
	writeCoverageJSON(t, f.joinPath, f.join)
	joinBytes, err := os.ReadFile(f.joinPath)
	if err != nil {
		t.Fatal(err)
	}
	writeCoverageJSON(t, f.controlPath, controlMessage{
		Schema:     "native-cleanup-control/v1",
		JoinRecord: f.joinPath,
		JoinSHA256: digest(joinBytes),
	})
	f.options.controlSHA = hashCoverageFile(t, f.controlPath)
	if err := verifyOriginalControl(f.options, f.custody); err == nil || err.Error() != "missing original owner identity" {
		t.Fatalf("original control did not propagate malformed joined record: %v", err)
	}
}

func TestOriginalControlRejectsAllocationPathOutsideBoundLayout(t *testing.T) {
	f := newControlFlowFixture(t)
	f.options.allocation = filepath.Join(f.custody.ControlDirectory, "alternate-allocation.json")
	if err := verifyOriginalControl(f.options, f.custody); err == nil || err.Error() != "original allocation layout mismatch" {
		t.Fatalf("original control allocation layout error = %v", err)
	}
}

func TestOriginalAuthorityDeadlineRejectsMalformedStartTime(t *testing.T) {
	join := validJoin(validAllocation())
	join.CleanupStarted = "not-rfc3339"
	err := originalAuthorityDeadline(join, time.Now())
	var parseErr *time.ParseError
	if !errors.As(err, &parseErr) || parseErr.Layout != time.RFC3339Nano || parseErr.Value != join.CleanupStarted {
		t.Fatalf("malformed authority start time error = %#v, want RFC3339Nano parse error", err)
	}
}

func cacheOptions(t *testing.T) options {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o := options{root: root, invocation: "11234567-89ab-cdef-0123-456789abcdef"}
	o.goCache = filepath.Join(root, "cache", o.invocation, "go-build")
	o.goModCache = filepath.Join(root, "cache", o.invocation, "go-mod")
	for _, p := range []string{o.goCache, o.goModCache} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return o
}
func TestCachePathsCannotBeManifestChosen(t *testing.T) {
	o := cacheOptions(t)
	env := []string{"GOCACHE=" + o.goCache, "GOMODCACHE=" + o.goModCache}
	if err := verifyCaches(env, o); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"off", "relative", filepath.Join(o.root, "control"), filepath.Join(o.root, "subject"), filepath.Join(o.root, "fixture"), o.root, o.goModCache} {
		bad := o
		bad.goCache = path
		if err := verifyCaches([]string{"GOCACHE=" + path, "GOMODCACHE=" + o.goModCache}, bad); err == nil {
			t.Fatal("arbitrary private cache accepted", path)
		}
	}
	bad := o
	bad.invocation = "21234567-89ab-cdef-0123-456789abcdef"
	if err := verifyCaches(env, bad); err == nil {
		t.Fatal("stale invocation accepted")
	}
	if err := verifyCaches([]string{"GOCACHE=" + o.goModCache, "GOMODCACHE=" + o.goModCache}, o); err == nil {
		t.Fatal("policy mismatch accepted")
	}
	if err := os.Remove(o.goCache); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(o.goModCache, o.goCache); err != nil {
		t.Fatal(err)
	}
	if err := verifyCaches(env, o); err == nil {
		t.Fatal("redirected cache accepted")
	}
}

func TestCommandAndDispatchRejectUntrustedModes(t *testing.T) {
	if err := run(nil); err == nil || !strings.Contains(err.Error(), "expected verify-custody") {
		t.Fatalf("empty command did not fail closed: %v", err)
	}
	if err := run([]string{"verify-custody", "--unknown"}); err == nil {
		t.Fatal("unknown option accepted")
	}
	if err := dispatchAuthenticatedMode(nil, options{}, custody{}); err == nil || !strings.Contains(err.Error(), "expected verify-custody") {
		t.Fatalf("empty authenticated dispatch did not fail closed: %v", err)
	}
	if err := dispatchAuthenticatedMode([]string{"unknown"}, options{}, custody{}); err == nil || !strings.Contains(err.Error(), "unsupported mode") {
		t.Fatalf("unknown mode accepted: %v", err)
	}
}

func TestCustodyIdentityAndPolicyFailuresAreSpecific(t *testing.T) {
	o := options{sourceSHA: strings.Repeat("a", 40), root: filepath.Join(string(filepath.Separator), "tmp", "root")}
	c := custody{Schema: "native-cleanup-custody/v1", OwnerSource: o.sourceSHA, ControlDirectory: filepath.Join(o.root, "control"), ControlVolumeFileID: "00000001:0000000000000001", ControlACL: strings.Repeat("b", 64)}
	for name, mutate := range map[string]func(*custody){
		"schema":       func(c *custody) { c.Schema = "wrong" },
		"owner":        func(c *custody) { c.OwnerSource = strings.Repeat("c", 40) },
		"control-root": func(c *custody) { c.ControlDirectory = filepath.Join(o.root, "other") },
		"descriptor":   func(c *custody) { c.ControlACL = "bad" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := c
			mutate(&bad)
			if err := verifyCustodyIdentity(bad, o); err == nil {
				t.Fatalf("%s identity accepted", name)
			}
		})
	}
	p := policy{Parents: []string{}, Distributions: []distribution{}, Environment: []string{}}
	if err := verifyPolicy(p, strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "policy mismatch") {
		t.Fatalf("policy digest mismatch was not reported: %v", err)
	}
	p.Parents = []string{strings.Repeat("a", 40)}
	if err := verifyPolicy(p, digest(mustJSON(t, p))); err == nil || !strings.Contains(err.Error(), "incomplete parent") {
		t.Fatalf("incomplete policy accepted: %v", err)
	}
	if err := verifyDistributions(nil); err == nil || !strings.Contains(err.Error(), "inventory mismatch") {
		t.Fatalf("missing distribution inventory accepted: %v", err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestStrictJSONRejectsNestedMalformedAndWrongShapes(t *testing.T) {
	for name, raw := range map[string]string{
		"nested-object": `{"schema":{"bad":]}`,
		"nested-array":  `{"schema":[true,]}`,
		"case-alias": func() string {
			value := custody{Schema: "native-cleanup-custody/v1", OwnerSource: strings.Repeat("a", 40), Policy: policy{Parents: []string{}, Distributions: []distribution{}, Environment: []string{}}, Sources: []binding{}, Artifacts: []binding{}, Cases: []string{}, InventorySHA256: strings.Repeat("b", 64), ControlVolumeFileID: "00000001:0000000000000001", ControlACL: strings.Repeat("c", 64), ControlDirectory: "/tmp/control"}
			raw := string(mustJSON(t, value))
			return strings.Replace(raw, `"schema"`, `"Schema"`, 1)
		}(),
		"wrong-slice-shape": `{"schema":"native-cleanup-custody/v1","owner_source":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","policy":{"parents":{},"go_version":"","goarch":"","distributions":[],"environment":[],"runner_image":"","runner_revision":"","setup_go_revision":"","checkout_revision":""},"sources":[],"artifacts":[],"cases":[],"inventory_sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","control_volume_file_id":"","control_acl_sha256":"","control_directory":""}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := strictJSON([]byte(raw), &custody{}); err == nil {
				t.Fatalf("malformed %s accepted", name)
			}
		})
	}
}

func TestRegularPathRejectsRelativeAndNoncanonicalPaths(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"relative/file", filepath.Join(root, "..", "outside")} {
		if err := regularPath(p, root, false); err == nil {
			t.Fatalf("noncanonical path accepted: %q", p)
		}
	}
}

func TestCheckedFileRejectsRegularFileAsTrustedRoot(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "trusted-root")
	data := []byte("not a directory")
	if err := os.WriteFile(root, data, 0600); err != nil {
		t.Fatal(err)
	}

	trustedRoot, wantErr := os.OpenRoot(root)
	if wantErr == nil {
		if err := trustedRoot.Close(); err != nil {
			t.Errorf("close unexpectedly opened regular-file root: %v", err)
		}
		t.Fatal("os.OpenRoot accepted a regular file as a root")
	}
	var pathErr *os.PathError
	if !errors.As(wantErr, &pathErr) {
		t.Fatalf("os.OpenRoot error = %T, want *os.PathError", wantErr)
	}
	got, err := checkedFile(root, digest(data), root, int64(len(data)))
	var gotPathErr *os.PathError
	if !errors.As(err, &gotPathErr) {
		t.Fatalf("checkedFile error = %T, want *os.PathError", err)
	}
	if gotPathErr.Op != pathErr.Op || gotPathErr.Path != pathErr.Path || gotPathErr.Err.Error() != pathErr.Err.Error() {
		t.Fatalf("checkedFile error = %#v, want os.OpenRoot error %#v", gotPathErr, pathErr)
	}
	if len(got) != 0 {
		t.Fatalf("checkedFile returned data after trusted-root failure: %q", got)
	}
}

func writeRecordFile(t *testing.T, path string, data []byte, mode os.FileMode) string {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	return digest(data)
}

func originalControlFixture(t *testing.T) (options, custody, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	control := filepath.Join(root, "control")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{control, bin} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	verifier := filepath.Join(bin, "nativecleanupcheck.exe")
	owner := filepath.Join(bin, "nativecleanupowner.exe")
	verifierSHA := writeRecordFile(t, verifier, []byte("verifier"), 0700)
	ownerSHA := writeRecordFile(t, owner, []byte("owner"), 0700)
	a := validAllocation()
	a.OwnerSHA256 = ownerSHA
	allocationData := mustJSON(t, a)
	allocationPath := filepath.Join(control, "allocation.json")
	allocationSHA := writeRecordFile(t, allocationPath, allocationData, 0600)
	stdoutPath := filepath.Join(control, "stdout.log")
	stderrPath := filepath.Join(control, "stderr.log")
	stdoutSHA := writeRecordFile(t, stdoutPath, []byte("subject stdout"), 0600)
	stderrSHA := writeRecordFile(t, stderrPath, []byte(""), 0600)
	j := validJoin(a)
	j.AllocationSHA256 = allocationSHA
	j.Stdout = binding{Path: stdoutPath, SHA256: stdoutSHA}
	j.Stderr = binding{Path: stderrPath, SHA256: stderrSHA}
	joinPath := filepath.Join(control, "join.json")
	joinSHA := writeRecordFile(t, joinPath, mustJSON(t, j), 0600)
	c := custody{Schema: "native-cleanup-custody/v1", OwnerSource: a.OwnerSource, Artifacts: []binding{{Path: "bin/nativecleanupowner.exe", SHA256: ownerSHA}, {Path: "bin/nativecleanupcheck.exe", SHA256: verifierSHA}}, ControlDirectory: control, ControlVolumeFileID: "00000001:0000000000000001", ControlACL: strings.Repeat("b", 64)}
	m := controlMessage{Schema: "native-cleanup-control/v1", JoinRecord: joinPath, JoinSHA256: joinSHA}
	controlPath := filepath.Join(control, "owner-control.json")
	controlSHA := writeRecordFile(t, controlPath, mustJSON(t, m), 0600)
	o := options{root: root, verifier: verifier, verifierSHA: verifierSHA, control: controlPath, controlSHA: controlSHA, allocation: allocationPath, allocationSHA: allocationSHA, join: joinPath, joinSHA: joinSHA, invocation: a.InvocationNonce, runID: a.RunID, attempt: a.RunAttempt, sid: a.SID, sourceSHA: a.OwnerSource, manifestSHA: a.ManifestSHA256}
	return o, c, control
}

func TestOriginalControlRejectsBadAnchorsAndSchema(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		setup      func(*testing.T, *options, custody)
	}{
		{"missing-control", "original control layout mismatch", func(_ *testing.T, o *options, _ custody) {
			o.control = filepath.Join(filepath.Dir(o.control), "missing.json")
		}},
		{"control-digest", "size/digest mismatch", func(_ *testing.T, o *options, _ custody) { o.controlSHA = strings.Repeat("0", 64) }},
		{"malformed-json", "unexpected end of JSON input", func(t *testing.T, o *options, _ custody) {
			o.controlSHA = writeRecordFile(t, o.control, []byte("{"), 0600)
		}},
		{"wrong-schema", "original control schema/path/digest mismatch", func(t *testing.T, o *options, _ custody) {
			m := controlMessage{Schema: "wrong", JoinRecord: o.join, JoinSHA256: o.joinSHA}
			o.controlSHA = writeRecordFile(t, o.control, mustJSON(t, m), 0600)
		}},
		{"wrong-join-path", "original control schema/path/digest mismatch", func(t *testing.T, o *options, _ custody) {
			m := controlMessage{Schema: "native-cleanup-control/v1", JoinRecord: filepath.Join(filepath.Dir(o.control), "other.json"), JoinSHA256: o.joinSHA}
			o.controlSHA = writeRecordFile(t, o.control, mustJSON(t, m), 0600)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, c, _ := originalControlFixture(t)
			tc.setup(t, &o, c)
			err := verifyOriginalControl(o, c)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("wanted %q, got %v", tc.want, err)
			}
		})
	}
}
func TestRecordReadersRejectMalformedAndUnboundFiles(t *testing.T) {
	for _, tc := range []struct {
		name string
		read func(*testing.T, options, custody) error
	}{
		{"allocation-missing", func(t *testing.T, o options, c custody) error {
			o.allocation = filepath.Join(c.ControlDirectory, "absent.json")
			o.allocationSHA = strings.Repeat("a", 64)
			_, err := readAllocation(o, c)
			return err
		}},
		{"allocation-digest", func(_ *testing.T, o options, c custody) error {
			o.allocationSHA = strings.Repeat("0", 64)
			_, err := readAllocation(o, c)
			return err
		}},
		{"allocation-json", func(t *testing.T, o options, c custody) error {
			o.allocationSHA = writeRecordFile(t, o.allocation, []byte("{"), 0600)
			_, err := readAllocation(o, c)
			return err
		}},
		{"allocation-invalid", func(t *testing.T, o options, c custody) error {
			a := validAllocation()
			a.SID = "invalid"
			o.allocationSHA = writeRecordFile(t, o.allocation, mustJSON(t, a), 0600)
			_, err := readAllocation(o, c)
			return err
		}},
		{"join-missing", func(t *testing.T, o options, c custody) error {
			o.join = filepath.Join(c.ControlDirectory, "absent-join.json")
			o.joinSHA = strings.Repeat("a", 64)
			_, err := readJoin(o, c)
			return err
		}},
		{"join-digest", func(_ *testing.T, o options, c custody) error {
			o.joinSHA = strings.Repeat("0", 64)
			_, err := readJoin(o, c)
			return err
		}},
		{"join-json", func(t *testing.T, o options, c custody) error {
			o.joinSHA = writeRecordFile(t, o.join, []byte("{"), 0600)
			_, err := readJoin(o, c)
			return err
		}},
		{"join-timestamp", func(t *testing.T, o options, c custody) error {
			j := validJoin(validAllocation())
			j.CleanupFinished = "not-a-timestamp"
			o.joinSHA = writeRecordFile(t, o.join, mustJSON(t, j), 0600)
			_, err := readJoin(o, c)
			return err
		}},
		{"join-interval", func(t *testing.T, o options, c custody) error {
			j := validJoin(validAllocation())
			start, err := time.Parse(time.RFC3339Nano, j.CleanupStarted)
			if err != nil {
				t.Fatal(err)
			}
			j.CleanupFinished = start.Add(30 * time.Second).Format(time.RFC3339Nano)
			o.joinSHA = writeRecordFile(t, o.join, mustJSON(t, j), 0600)
			_, err = readJoin(o, c)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, c, _ := originalControlFixture(t)
			if err := tc.read(t, o, c); err == nil {
				t.Fatalf("%s accepted", tc.name)
			}
		})
	}
}
func TestEventDecoderRejectsMalformedAndNonObjectJSON(t *testing.T) {
	for name, raw := range map[string][]byte{"malformed": []byte(`{"Action":]`), "non-object": []byte(`[]`), "trailing": []byte(`{} {}`)} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeEvent(raw); err == nil {
				t.Fatalf("%s event accepted", name)
			}
		})
	}
}

func TestAuditRejectsChildOutsideLiveParent(t *testing.T) {
	var stream strings.Builder
	for _, event := range []testEvent{
		{Action: "start", Package: subjectPackage},
		{Action: "run", Package: subjectPackage, Test: rootTest},
		{Action: "run", Package: subjectPackage, Test: rootTest + "/drive/equivalent"},
	} {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(&stream, string(data))
	}
	if err := auditResults([]byte(stream.String()), 0); err == nil || !strings.Contains(err.Error(), "child outside live parent") {
		t.Fatalf("child outside active parent was not rejected precisely: %v", err)
	}
}

func TestAuditCustodyRequiresOriginalTerminalBindings(t *testing.T) {
	if err := auditCustodyResults(options{}, custody{}); err == nil || !strings.Contains(err.Error(), "audit requires original join and results") {
		t.Fatalf("missing original terminal bindings were not rejected: %v", err)
	}
}

func TestVerifyRecordsPropagatesJoinReadFailure(t *testing.T) {
	o, c, _ := originalControlFixture(t)
	o.joinSHA = strings.Repeat("0", 64)
	if err := verifyRecords(o, c); err == nil || !strings.Contains(err.Error(), "size/digest") {
		t.Fatalf("join read failure was not propagated: %v", err)
	}
}

func TestStrictJSONCaseAliasReachesCanonicalFieldCheck(t *testing.T) {
	value := custody{Schema: "native-cleanup-custody/v1", OwnerSource: strings.Repeat("a", 40), Policy: policy{Parents: []string{}, Distributions: []distribution{}, Environment: []string{}}, Sources: []binding{}, Artifacts: []binding{}, Cases: []string{}, InventorySHA256: strings.Repeat("b", 64), ControlVolumeFileID: "00000001:0000000000000001", ControlACL: strings.Repeat("c", 64), ControlDirectory: "/tmp/control"}
	raw := strings.Replace(string(mustJSON(t, value)), `"schema"`, `"Schema"`, 1)
	if err := strictJSON([]byte(raw), &custody{}); err == nil || err.Error() != "missing JSON field schema" {
		t.Fatalf("case-aliased canonical field error = %v", err)
	}
}

func TestReadJoinPropagatesBoundOutputFailures(t *testing.T) {
	for _, field := range []string{"stdout", "stderr"} {
		t.Run(field, func(t *testing.T) {
			f := newControlFlowFixture(t)
			if field == "stdout" {
				f.join.Stdout.SHA256 = strings.Repeat("0", 64)
			} else {
				f.join.Stderr.SHA256 = strings.Repeat("0", 64)
			}
			writeCoverageJSON(t, f.joinPath, f.join)
			f.options.joinSHA = hashCoverageFile(t, f.joinPath)
			if _, err := readJoin(f.options, f.custody); err == nil || !strings.Contains(err.Error(), "artifact size/digest mismatch") {
				t.Fatalf("readJoin %s binding failure = %v", field, err)
			}
		})
	}
}

func TestVerifyCustodyIdentityRejectsEachAuthorityBinding(t *testing.T) {
	f := newCoverageCustodyFixture(t)
	for _, tc := range []struct {
		name string
		edit func(*custody)
		want string
	}{
		{"schema", func(c *custody) { c.Schema = "native-cleanup-custody/v2" }, "custody identity mismatch"},
		{"owner source", func(c *custody) { c.OwnerSource = strings.Repeat("c", 40) }, "custody identity mismatch"},
		{"control directory", func(c *custody) { c.ControlDirectory = filepath.Join(f.options.root, "other") }, "custody identity mismatch"},
		{"volume identity", func(c *custody) { c.ControlVolumeFileID = "malformed" }, "missing control directory identity/descriptor"},
		{"ACL descriptor", func(c *custody) { c.ControlACL = "malformed" }, "missing control directory identity/descriptor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := f.custody
			tc.edit(&bad)
			if err := verifyCustodyIdentity(bad, f.options); err == nil || err.Error() != tc.want {
				t.Fatalf("identity error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestVerifyPolicyRejectsSpecificNativeAndDistributionBoundaries(t *testing.T) {
	base := coveragePolicy(t)
	for _, tc := range []struct {
		name string
		edit func(*policy)
		want string
	}{
		{"unsupported architecture", func(p *policy) { p.GOARCH = "386" }, "incomplete parent/native Go policy"},
		{"missing Go version", func(p *policy) { p.GoVersion = "" }, "incomplete parent/native Go policy"},
		{"invalid parent revision", func(p *policy) { p.Parents[0] = "short" }, "policy requires full immutable revisions"},
		{"missing runner image", func(p *policy) { p.RunnerImage = "" }, "missing runner image"},
		{"invalid distribution binding", func(p *policy) { p.Distributions[0].SHA256 = "bad" }, "invalid distribution binding"},
		{"missing native linker", func(p *policy) { p.Distributions[2].Files = p.Distributions[2].Files[:1] }, "incomplete native CC/linker/DLL inventory"},
		{"unapproved environment", func(p *policy) { p.Environment = append(p.Environment, "GITHUB_TOKEN=secret") }, "unapproved child environment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := base
			bad.Parents = append([]string(nil), base.Parents...)
			bad.Environment = append([]string(nil), base.Environment...)
			bad.Distributions = append([]distribution(nil), base.Distributions...)
			for i := range bad.Distributions {
				bad.Distributions[i].Files = append([]binding(nil), base.Distributions[i].Files...)
			}
			tc.edit(&bad)
			if err := verifyPolicy(bad, digest(mustJSON(t, bad))); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("policy boundary error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestOriginalControlAndAuditPropagateAuthenticatedRecordFailures(t *testing.T) {
	t.Run("allocation mismatch through original control", func(t *testing.T) {
		f := newControlFlowFixture(t)
		f.join.AllocationNonce = "fedcba98-7654-3210-fedc-ba9876543210"
		writeCoverageJSON(t, f.joinPath, f.join)
		joinBytes, err := os.ReadFile(f.joinPath)
		if err != nil {
			t.Fatal(err)
		}
		writeCoverageJSON(t, f.controlPath, controlMessage{Schema: "native-cleanup-control/v1", JoinRecord: f.joinPath, JoinSHA256: digest(joinBytes)})
		f.options.controlSHA = hashCoverageFile(t, f.controlPath)
		if err := verifyOriginalControl(f.options, f.custody); err == nil || err.Error() != "original join allocation binding mismatch" {
			t.Fatalf("original control failed to propagate join binding error: %v", err)
		}
	})
	t.Run("audit rejects mismatched join stdout digest", func(t *testing.T) {
		f := newControlFlowFixture(t)
		f.options.resultsSHA = strings.Repeat("0", 64)
		f.join.Stdout.SHA256 = f.options.resultsSHA
		writeCoverageJSON(t, f.joinPath, f.join)
		f.options.joinSHA = hashCoverageFile(t, f.joinPath)
		if err := auditCustodyResults(f.options, f.custody); err == nil || !strings.Contains(err.Error(), "artifact size/digest mismatch") {
			t.Fatalf("audit failed to propagate result binding failure: %v", err)
		}
	})
}

func TestVerifyPolicyReachesDistributionInventoryAfterValidPolicy(t *testing.T) {
	p := coveragePolicy(t)
	p.Distributions = []distribution{}
	if err := verifyPolicy(p, digest(mustJSON(t, p))); err == nil || err.Error() != "distribution inventory mismatch" {
		t.Fatalf("valid policy with absent distribution inventory = %v", err)
	}
}

func TestVerifyCustodyPropagatesCacheBindingFailure(t *testing.T) {
	t.Run("option cache differs from invocation root", func(t *testing.T) {
		f := newCoverageCustodyFixture(t)
		f.options.goCache = filepath.Join(f.options.root, "cache", "shared", "go-build")
		if _, err := verifyCustody(f.options); err == nil || err.Error() != "cache differs from invocation-private root expectation" {
			t.Fatalf("verifyCustody cache validation error = %v", err)
		}
	})
	t.Run("policy environment differs from option", func(t *testing.T) {
		f := newCoverageCustodyFixture(t)
		for i, entry := range f.custody.Policy.Environment {
			if strings.HasPrefix(entry, "GOCACHE=") {
				f.custody.Policy.Environment[i] = "GOCACHE=" + filepath.Join(f.options.root, "cache", "other", "go-build")
			}
		}
		f.options.policySHA = digest(mustJSON(t, f.custody.Policy))
		writeCoverageJSON(t, f.options.manifest, f.custody)
		f.options.manifestSHA = hashCoverageFile(t, f.options.manifest)
		if _, err := verifyCustody(f.options); err == nil || err.Error() != "cache differs from independent policy environment" {
			t.Fatalf("verifyCustody policy cache validation error = %v", err)
		}
	})
}

func TestReadJoinRejectsUnboundOutputAndMalformedJoinPremises(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*testing.T, *controlFlowFixture)
		want string
	}{
		{"stdout digest", func(t *testing.T, f *controlFlowFixture) { f.join.Stdout.SHA256 = strings.Repeat("0", 64) }, "artifact size/digest mismatch"},
		{"stderr digest", func(t *testing.T, f *controlFlowFixture) { f.join.Stderr.SHA256 = strings.Repeat("0", 64) }, "artifact size/digest mismatch"},
		{"missing owner identity", func(_ *testing.T, f *controlFlowFixture) { f.join.OwnerPID = 0 }, "missing original owner identity"},
		{"missing cleanup proof", func(_ *testing.T, f *controlFlowFixture) { f.join.OriginalCleanup = false }, "incomplete original owner join"},
		{"invalid end timestamp", func(_ *testing.T, f *controlFlowFixture) { f.join.CleanupFinished = "not-a-timestamp" }, "parsing time"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newControlFlowFixture(t)
			tc.edit(t, &f)
			writeCoverageJSON(t, f.joinPath, f.join)
			f.options.joinSHA = hashCoverageFile(t, f.joinPath)
			if _, err := readJoin(f.options, f.custody); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("readJoin = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestAuditCustodyResultsPropagatesRecordAndObservationFailures(t *testing.T) {
	f := newControlFlowFixture(t)
	f.options.joinSHA = strings.Repeat("0", 64)
	if err := auditCustodyResults(f.options, f.custody); err == nil || !strings.Contains(err.Error(), "artifact size/digest mismatch") {
		t.Fatalf("auditCustodyResults did not propagate join failure: %v", err)
	}

	f = newControlFlowFixture(t)
	f.join.Workload = "no-workload-launched"
	writeCoverageJSON(t, f.joinPath, f.join)
	f.options.joinSHA = hashCoverageFile(t, f.joinPath)
	if err := auditCustodyResults(f.options, f.custody); err == nil || err.Error() != "audit does not match original terminal Go observation" {
		t.Fatalf("auditCustodyResults accepted non-Go observation: %v", err)
	}
}

func TestVerifyCustodyPropagatesAuthenticatedIdentityAndPolicyFailures(t *testing.T) {
	t.Run("custody identity", func(t *testing.T) {
		f := newCoverageCustodyFixture(t)
		f.custody.OwnerSource = strings.Repeat("0", 40)
		writeCoverageJSON(t, f.options.manifest, f.custody)
		f.options.manifestSHA = hashCoverageFile(t, f.options.manifest)
		if _, err := verifyCustody(f.options); err == nil || err.Error() != "custody identity mismatch" {
			t.Fatalf("verifyCustody identity failure = %v", err)
		}
	})
	t.Run("distribution inventory", func(t *testing.T) {
		f := newCoverageCustodyFixture(t)
		f.custody.Policy.Distributions = []distribution{}
		f.options.policySHA = digest(mustJSON(t, f.custody.Policy))
		writeCoverageJSON(t, f.options.manifest, f.custody)
		f.options.manifestSHA = hashCoverageFile(t, f.options.manifest)
		if _, err := verifyCustody(f.options); err == nil || err.Error() != "distribution inventory mismatch" {
			t.Fatalf("verifyCustody policy failure = %v", err)
		}
	})
}
