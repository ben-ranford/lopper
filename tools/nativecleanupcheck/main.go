// nativecleanupcheck validates the fixed #1753 custody and result protocols.
package main

import (
	"bytes"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"time"
)

// These bounds are source-review policy, not native timing evidence.
const documentLimit = 1 << 20
const resultLimit = 64 << 20
const rootTest = "TestHooksCleanupNativeWindows"
const subjectPackage = "github.com/ben-ranford/lopper/scripts"

var revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var guidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// All fields are required, including empty strings/arrays in partial records.
type binding struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type distribution struct {
	Name     string    `json:"name"`
	Revision string    `json:"revision"`
	Artifact string    `json:"artifact"`
	SHA256   string    `json:"sha256"`
	Files    []binding `json:"files"`
}
type policy struct {
	Parents          []string       `json:"parents"`
	GoVersion        string         `json:"go_version"`
	GOARCH           string         `json:"goarch"`
	Distributions    []distribution `json:"distributions"`
	Environment      []string       `json:"environment"`
	RunnerImage      string         `json:"runner_image"`
	RunnerRevision   string         `json:"runner_revision"`
	SetupGoRevision  string         `json:"setup_go_revision"`
	CheckoutRevision string         `json:"checkout_revision"`
}
type custody struct {
	Schema              string    `json:"schema"`
	OwnerSource         string    `json:"owner_source"`
	Policy              policy    `json:"policy"`
	Sources             []binding `json:"sources"`
	Artifacts           []binding `json:"artifacts"`
	Cases               []string  `json:"cases"`
	InventorySHA256     string    `json:"inventory_sha256"`
	ControlVolumeFileID string    `json:"control_volume_file_id"`
	ControlACL          string    `json:"control_acl_sha256"`
	ControlDirectory    string    `json:"control_directory"`
}
type allocation struct {
	Schema          string `json:"schema"`
	AllocationNonce string `json:"allocation_nonce"`
	InvocationNonce string `json:"invocation_nonce"`
	RunID           string `json:"run_id"`
	RunAttempt      string `json:"run_attempt"`
	SID             string `json:"sid"`
	Share           string `json:"share"`
	Temporary       bool   `json:"temporary"`
	Path            string `json:"path"`
	VolumeFileID    string `json:"volume_file_id"`
	NTFSACL         string `json:"ntfs_acl_sha256"`
	ShareACL        string `json:"share_acl_sha256"`
	ManifestSHA256  string `json:"manifest_sha256"`
	OwnerSource     string `json:"owner_source"`
	OwnerSHA256     string `json:"owner_sha256"`
	State           string `json:"state"`
	SentinelSHA256  string `json:"sentinel_sha256"`
}
type joinRecord struct {
	OwnerPID         int     `json:"owner_pid"`
	Schema           string  `json:"schema"`
	AllocationSHA256 string  `json:"allocation_sha256"`
	AllocationNonce  string  `json:"allocation_nonce"`
	InvocationNonce  string  `json:"invocation_nonce"`
	RunID            string  `json:"run_id"`
	RunAttempt       string  `json:"run_attempt"`
	ManifestSHA256   string  `json:"manifest_sha256"`
	OwnerSource      string  `json:"owner_source"`
	OwnerSHA256      string  `json:"owner_sha256"`
	Workload         string  `json:"workload"`
	Status           int     `json:"status"`
	OriginalCleanup  bool    `json:"original_cleanup"`
	RootWait         bool    `json:"root_wait"`
	Drains           bool    `json:"drains"`
	HelpersJoined    bool    `json:"helpers_joined"`
	CleanupStarted   string  `json:"cleanup_started"`
	CleanupFinished  string  `json:"cleanup_finished"`
	Stdout           binding `json:"stdout"`
	Stderr           binding `json:"stderr"`
}
type controlMessage struct {
	Schema     string `json:"schema"`
	JoinRecord string `json:"join_record"`
	JoinSHA256 string `json:"join_record_sha256"`
}
type options struct {
	control, controlSHA                                                      string
	goCache, goModCache                                                      string
	manifest, manifestSHA, verifier, verifierSHA, sourceSHA, policySHA, root string
	allocation, allocationSHA, join, joinSHA, results, resultsSHA            string
	invocation, runID, attempt, sid                                          string
	status                                                                   int
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(checkerExit(err))
	}
}

// Only authenticated, well-formed subject outcomes receive the teardown-safe code.
type subjectResultError struct{ cause error }

func (e *subjectResultError) Error() string { return e.cause.Error() }
func checkerExit(err error) int {
	if err == nil {
		return 0
	}
	var subject *subjectResultError
	if errors.As(err, &subject) && reflect.TypeOf(err) == reflect.TypeFor[*subjectResultError]() {
		return 3
	}
	return 1
}
func auditBoundResults(data []byte, status int) error {
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if _, err := decodeEvent(line); err != nil {
			return err
		}
	}
	if err := auditResults(data, status); err != nil {
		return &subjectResultError{cause: err}
	}
	return nil
}
func run(args []string) error {
	if len(args) == 0 {
		return errors.New("expected verify-custody or audit-results")
	}
	o, err := parseOptions(args[1:])
	if err != nil {
		return err
	}
	c, err := verifyCustody(o)
	if err != nil {
		return err
	}
	return dispatchAuthenticatedMode(args, o, c)
}

// dispatchAuthenticatedMode is called only after run has authenticated custody.
// Keeping this switch separate lets its policy be tested without a fake native build.
func dispatchAuthenticatedMode(args []string, o options, c custody) error {
	if len(args) == 0 {
		return errors.New("expected verify-custody or audit-results")
	}
	switch args[0] {
	case "verify-custody":
		if o.control != "" {
			return verifyOriginalControl(o, c)
		}
		return verifyRecords(o, c)
	case "audit-results":
		return auditCustodyResults(o, c)
	default:
		return errors.New("unsupported mode")
	}
}
func auditCustodyResults(o options, c custody) error {
	if err := verifyRecords(o, c); err != nil {
		return err
	}
	if o.join == "" || o.results == "" {
		return errors.New("audit requires original join and results")
	}
	j, err := readJoin(o, c)
	if err != nil {
		return err
	}
	if j.Workload != "subject-go" || j.Status != o.status || j.Stdout.Path != o.results || j.Stdout.SHA256 != o.resultsSHA {
		return errors.New("audit does not match original terminal Go observation")
	}
	data, err := checkedFile(o.results, o.resultsSHA, c.ControlDirectory, resultLimit)
	if err != nil {
		return err
	}
	return auditBoundResults(data, o.status)
}
func parseOptions(args []string) (options, error) {
	var o options
	f := flag.NewFlagSet("nativecleanupcheck", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	fields := map[string]*string{"go-cache": &o.goCache, "go-mod-cache": &o.goModCache, "original-control": &o.control, "original-control-sha256": &o.controlSHA, "manifest": &o.manifest, "manifest-sha256": &o.manifestSHA, "verifier": &o.verifier, "verifier-sha256": &o.verifierSHA, "expected-source-sha": &o.sourceSHA, "expected-policy-sha256": &o.policySHA, "trusted-root": &o.root, "allocation": &o.allocation, "allocation-sha256": &o.allocationSHA, "join-record": &o.join, "join-record-sha256": &o.joinSHA, "results": &o.results, "results-sha256": &o.resultsSHA, "invocation-nonce": &o.invocation, "run-id": &o.runID, "run-attempt": &o.attempt, "sid": &o.sid}
	for name, p := range fields {
		f.StringVar(p, name, "", name)
	}
	f.IntVar(&o.status, "go-status", -1, "observed terminal Go status")
	if err := f.Parse(args); err != nil {
		return o, err
	}
	if f.NArg() != 0 {
		return o, errors.New("unexpected operands")
	}
	return o, nil
}
func verifyCustody(o options) (custody, error) {
	var c custody
	if !revisionPattern.MatchString(o.sourceSHA) || !digestPattern.MatchString(o.policySHA) || !filepath.IsAbs(o.root) {
		return c, errors.New("missing reviewed source/policy/root expectation")
	}
	// Both anchors are checked before parsing any manifest bytes.
	if o.verifier != filepath.Join(o.root, "bin", "nativecleanupcheck.exe") || o.manifest != filepath.Join(o.root, "custody.json") {
		return c, errors.New("anchor layout mismatch")
	}
	if _, err := checkedFile(o.verifier, o.verifierSHA, o.root, resultLimit); err != nil {
		return c, err
	}
	data, err := checkedFile(o.manifest, o.manifestSHA, o.root, documentLimit)
	if err != nil {
		return c, err
	}
	if err = strictJSON(data, &c); err != nil {
		return c, err
	}
	if err = verifyCustodyIdentity(c, o); err != nil {
		return c, err
	}
	if err = verifyPolicy(c.Policy, o.policySHA); err != nil {
		return c, err
	}
	if err = verifyCaches(c.Policy.Environment, o); err != nil {
		return c, err
	}
	if !slices.Equal(c.Cases, caseInventory()) || c.InventorySHA256 != digest([]byte(strings.Join(caseInventory(), "\n")+"\n")) {
		return c, errors.New("source-owned case inventory mismatch")
	}
	if err = verifyBindings(c.Sources, sourcePaths, filepath.Join(o.root, "source")); err != nil {
		return c, err
	}
	if err = verifyBindings(c.Artifacts, []string{"bin/nativecleanupowner.exe", "bin/nativecleanupcheck.exe"}, o.root); err != nil {
		return c, err
	}
	if err = verifyBuilds(c, o); err != nil {
		return c, err
	}
	return c, regularPath(c.ControlDirectory, o.root, true)
}
func verifyCustodyIdentity(c custody, o options) error {
	if !regexp.MustCompile(`^[0-9a-f]{8}:[0-9a-f]{16}$`).MatchString(c.ControlVolumeFileID) || !digestPattern.MatchString(c.ControlACL) {
		return errors.New("missing control directory identity/descriptor")
	}
	if c.Schema != "native-cleanup-custody/v1" || c.OwnerSource != o.sourceSHA || c.ControlDirectory != filepath.Join(o.root, "control") {
		return errors.New("custody identity mismatch")
	}
	return nil
}
func verifyPolicy(p policy, expected string) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if digest(data) != expected {
		return errors.New("workflow-fixed distribution policy mismatch")
	}
	if len(p.Parents) != 3 || !strings.HasPrefix(p.GoVersion, "go1.") || (p.GOARCH != "amd64" && p.GOARCH != "arm64") {
		return errors.New("incomplete parent/native Go policy")
	}
	for _, v := range append(append([]string{}, p.Parents...), p.RunnerRevision, p.SetupGoRevision, p.CheckoutRevision) {
		if !revisionPattern.MatchString(v) {
			return errors.New("policy requires full immutable revisions")
		}
	}
	if p.RunnerImage == "" {
		return errors.New("missing runner image")
	}
	if err = verifyDistributions(p.Distributions); err != nil {
		return err
	}
	return verifyEnvironment(p.Environment)
}
func verifyDistributions(ds []distribution) error {
	names := []string{"go", "git-for-windows", "native-cgo", "powershell"}
	if len(ds) != len(names) {
		return errors.New("distribution inventory mismatch")
	}
	seen := map[string]bool{}
	for _, d := range ds {
		if !slices.Contains(names, d.Name) || seen[d.Name] || d.Revision == "" || d.Artifact == "" || !digestPattern.MatchString(d.SHA256) || len(d.Files) == 0 {
			return errors.New("invalid distribution binding")
		}
		seen[d.Name] = true
		files := map[string]bool{}
		for _, b := range d.Files {
			if files[b.Path] {
				return errors.New("duplicate runtime file")
			}
			files[b.Path] = true
			if _, err := checkedFile(b.Path, b.SHA256, filepath.Dir(b.Path), resultLimit); err != nil {
				return err
			}
		}
	}
	return requiredRuntimeFiles(ds)
}
func requiredRuntimeFiles(ds []distribution) error {
	expected := map[string][]string{"go": {"go.exe", "compile.exe", "link.exe"}, "git-for-windows": {"git.exe", "bash.exe", "sh.exe", "stat.exe", "xargs.exe", "cygpath.exe"}, "powershell": {"pwsh.exe"}}
	for _, d := range ds {
		names := map[string]bool{}
		dll := false
		for _, b := range d.Files {
			base := strings.ToLower(filepath.Base(b.Path))
			names[base] = true
			dll = dll || strings.HasSuffix(base, ".dll")
		}
		for _, name := range expected[d.Name] {
			if !names[name] {
				return fmt.Errorf("missing fixed runtime %s/%s", d.Name, name)
			}
		}
		if d.Name == "git-for-windows" && !dll {
			return errors.New("missing bundled Git runtime DLL identities")
		}
		if d.Name == "native-cgo" && !nativeCGOFiles(names, dll) {
			return errors.New("incomplete native CC/linker/DLL inventory")
		}
	}
	return nil
}
func nativeCGOFiles(names map[string]bool, dll bool) bool {
	return (names["gcc.exe"] || names["clang.exe"]) && (names["ld.exe"] || names["lld.exe"] || names["lld-link.exe"]) && dll
}
func verifyEnvironment(env []string) error {
	allowed := []string{"PATH", "SystemRoot", "WINDIR", "HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "TEMP", "TMP", "GOROOT", "GOTOOLCHAIN", "GOENV", "GOWORK", "GOFLAGS", "GOOS", "GOARCH", "CGO_ENABLED", "CC", "LOPPER_NATIVE_GIT", "LOPPER_NATIVE_BASH", "GOCACHE", "GOMODCACHE"}
	seen := map[string]string{}
	for _, v := range env {
		k, val, ok := strings.Cut(v, "=")
		if !ok || !slices.Contains(allowed, k) {
			return errors.New("unapproved child environment")
		}
		if _, ok = seen[k]; ok {
			return errors.New("duplicate environment key")
		}
		seen[k] = val
	}
	for k, v := range map[string]string{"GOTOOLCHAIN": "local", "GOENV": "off", "GOWORK": "off", "GOFLAGS": "", "GOOS": "windows", "CGO_ENABLED": "1"} {
		if actual, ok := seen[k]; !ok || actual != v {
			return fmt.Errorf("unsafe child %s", k)
		}
	}
	for _, k := range []string{"PATH", "SystemRoot", "USERPROFILE", "TEMP", "GOROOT", "GOARCH", "CC", "LOPPER_NATIVE_GIT", "LOPPER_NATIVE_BASH", "GOCACHE", "GOMODCACHE"} {
		if seen[k] == "" {
			return fmt.Errorf("missing child %s", k)
		}
	}
	return nil
}
func verifyCaches(env []string, o options) error {
	if !guidPattern.MatchString(o.invocation) {
		return errors.New("missing cache invocation")
	}
	for _, cache := range []struct{ key, path, leaf string }{{"GOCACHE", o.goCache, "go-build"}, {"GOMODCACHE", o.goModCache, "go-mod"}} {
		expected := filepath.Join(o.root, "cache", o.invocation, cache.leaf)
		if cache.path != expected || strings.HasPrefix(cache.path, `\\`) {
			return errors.New("cache differs from invocation-private root expectation")
		}
		if err := regularPath(cache.path, o.root, true); err != nil {
			return err
		}
		if !slices.Contains(env, cache.key+"="+cache.path) {
			return errors.New("cache differs from independent policy environment")
		}
	}
	return nil
}
func verifyBindings(bindings []binding, expected []string, root string) error {
	if len(bindings) != len(expected) {
		return errors.New("source/artifact inventory mismatch")
	}
	seen := map[string]bool{}
	for _, b := range bindings {
		if !slices.Contains(expected, b.Path) || seen[b.Path] {
			return errors.New("unexpected/duplicate source or artifact")
		}
		seen[b.Path] = true
		if _, err := checkedFile(filepath.Join(root, filepath.FromSlash(b.Path)), b.SHA256, root, resultLimit); err != nil {
			return err
		}
	}
	return nil
}
func verifyBuilds(c custody, o options) error {
	for _, name := range []string{"nativecleanupowner.exe", "nativecleanupcheck.exe"} {
		info, err := buildinfo.ReadFile(filepath.Join(o.root, "bin", name))
		if err != nil {
			return err
		}
		if err := validateBuildInfo(name, info, c, o); err != nil {
			return err
		}
	}
	return nil
}

// validateBuildInfo compares metadata returned by buildinfo.ReadFile. It is not
// an artifact-authentication substitute; verifyBuilds remains the only caller
// on the custody path and reads the exact bound executable files first.
func validateBuildInfo(name string, info *debug.BuildInfo, c custody, o options) error {
	settings := map[string]string{}
	for _, s := range info.Settings {
		if _, ok := settings[s.Key]; ok {
			return errors.New("duplicate build setting")
		}
		settings[s.Key] = s.Value
	}
	if info.GoVersion != c.Policy.GoVersion || settings["GOOS"] != "windows" || settings["GOARCH"] != c.Policy.GOARCH || settings["vcs.revision"] != o.sourceSHA || settings["vcs.modified"] != "false" || settings["vcs"] != "git" {
		return errors.New("native clean immutable buildinfo mismatch")
	}
	if name == "nativecleanupowner.exe" && settings["CGO_ENABLED"] != "1" {
		return errors.New("owner requires native CGO build")
	}
	return nil
}
func caseInventory() []string {
	var names []string
	for _, scheme := range []string{"drive", "unc"} {
		for _, leaf := range []string{"equivalent", "equivalent-backslashes", "equivalent-case", "unrelated", "shadowed", "missing", "drive-relative", "failed-config"} {
			names = append(names, rootTest+"/"+scheme+"/"+leaf)
		}
	}
	return names
}
func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func checkedFile(path, expected, root string, limit int64) (data []byte, returnErr error) {
	if !digestPattern.MatchString(expected) {
		return nil, errors.New("missing/malformed independent digest")
	}
	if err := regularPath(path, root, false); err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return nil, err
	}
	trustedRoot, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, trustedRoot.Close()) }()
	f, err := trustedRoot.Open(relative)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, f.Close()) }()
	data, err = io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit || digest(data) != expected {
		return nil, errors.New("artifact size/digest mismatch")
	}
	return data, nil
}
func regularPath(path, root string, directory bool) error {
	if !filepath.IsAbs(path) || !filepath.IsAbs(root) || filepath.Clean(path) != path {
		return errors.New("noncanonical absolute path")
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("path outside trusted layout")
	}
	for current := path; ; current = filepath.Dir(current) {
		if err := regularComponent(path, current, directory); err != nil {
			return err
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	return nil
}
func regularComponent(path, current string, directory bool) error {
	info, err := os.Lstat(current)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || hasReparseAttribute(info) {
		return errors.New("reparse component")
	}
	if current != path {
		return nil
	}
	if info.IsDir() != directory {
		return errors.New("artifact type mismatch")
	}
	if !directory && !info.Mode().IsRegular() {
		return errors.New("nonregular artifact")
	}
	return nil
}

func hasReparseAttribute(info os.FileInfo) bool {
	// os.FileInfo.Sys is Win32FileAttributeData on Windows; check every reparse tag,
	// including tags which os.ModeSymlink alone does not classify.
	v := reflect.ValueOf(info.Sys())
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return false
	}
	a := v.FieldByName("FileAttributes")
	return a.IsValid() && a.Kind() == reflect.Uint32 && a.Uint()&0x400 != 0
}
func strictJSON(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := uniqueValue(d); err != nil {
		return err
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	if err := requiredFields(data, reflect.TypeOf(out).Elem()); err != nil {
		return err
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(out)
}
func uniqueValue(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	delimiter, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		err = uniqueObject(d)
	case '[':
		err = uniqueArray(d)
	default:
		return errors.New("invalid JSON delimiter")
	}
	if err != nil {
		return err
	}
	_, err = d.Token()
	return err
}
func uniqueObject(d *json.Decoder) error {
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return err
		}
		s, ok := key.(string)
		if !ok || seen[s] {
			return errors.New("duplicate JSON field")
		}
		seen[s] = true
		if err := uniqueValue(d); err != nil {
			return err
		}
	}
	return nil
}
func uniqueArray(d *json.Decoder) error {
	for d.More() {
		if err := uniqueValue(d); err != nil {
			return err
		}
	}
	return nil
}
func requiredFields(data []byte, t reflect.Type) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("null required field")
	}
	switch t.Kind() {
	case reflect.Struct:
		return requiredStructFields(data, t)
	case reflect.Slice:
		return requiredSliceFields(data, t)
	}
	return nil
}
func requiredStructFields(data []byte, t reflect.Type) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if len(fields) != t.NumField() {
		return errors.New("unknown/missing/case-aliased JSON field")
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		key := strings.Split(f.Tag.Get("json"), ",")[0]
		raw, ok := fields[key]
		if !ok {
			return fmt.Errorf("missing JSON field %s", key)
		}
		if err := requiredFields(raw, f.Type); err != nil {
			return err
		}
	}
	return nil
}
func requiredSliceFields(data []byte, t reflect.Type) error {
	var elements []json.RawMessage
	if err := json.Unmarshal(data, &elements); err != nil {
		return err
	}
	for _, raw := range elements {
		if err := requiredFields(raw, t.Elem()); err != nil {
			return err
		}
	}
	return nil
}
func verifyOriginalControl(o options, c custody) error {
	if o.control != filepath.Join(c.ControlDirectory, "owner-control.json") {
		return errors.New("original control layout mismatch")
	}
	data, err := checkedFile(o.control, o.controlSHA, c.ControlDirectory, documentLimit)
	if err != nil {
		return err
	}
	var m controlMessage
	if err = strictJSON(data, &m); err != nil {
		return err
	}
	if m.Schema != "native-cleanup-control/v1" || m.JoinRecord != filepath.Join(c.ControlDirectory, "join.json") || !digestPattern.MatchString(m.JoinSHA256) {
		return errors.New("original control schema/path/digest mismatch")
	}
	o.join, o.joinSHA = m.JoinRecord, m.JoinSHA256
	j, err := readJoin(o, c)
	if err != nil {
		return err
	}
	if o.allocation != filepath.Join(c.ControlDirectory, "allocation.json") {
		return errors.New("original allocation layout mismatch")
	}
	// This expectation comes from the original owner channel's bound join,
	// never a newly calculated hash of an allocation/result on disk.
	o.allocationSHA = j.AllocationSHA256
	if err := verifyRecords(o, c); err != nil {
		return err
	}
	return originalAuthorityDeadline(j, time.Now())
}
func originalAuthorityDeadline(j joinRecord, now time.Time) error {
	start, err := time.Parse(time.RFC3339Nano, j.CleanupStarted)
	if err != nil {
		return err
	}
	if now.Before(start) || !now.Before(start.Add(30*time.Second)) {
		return errors.New("original recovery authority expired or clock inconsistent")
	}
	return nil
}
func verifyRecords(o options, c custody) error {
	if o.allocation == "" {
		if o.join != "" {
			return errors.New("join requires allocation")
		}
		return nil
	}
	a, err := readAllocation(o, c)
	if err != nil {
		return err
	}
	if o.join == "" {
		return nil
	}
	j, err := readJoin(o, c)
	if err != nil {
		return err
	}
	return matchJoin(j, a, o.allocationSHA)
}
func readAllocation(o options, c custody) (allocation, error) {
	var a allocation
	data, err := checkedFile(o.allocation, o.allocationSHA, c.ControlDirectory, documentLimit)
	if err != nil {
		return a, err
	}
	if err = strictJSON(data, &a); err != nil {
		return a, err
	}
	if err = validateAllocation(a, o); err != nil {
		return a, err
	}
	ownerDigest := ""
	for _, b := range c.Artifacts {
		if b.Path == "bin/nativecleanupowner.exe" {
			ownerDigest = b.SHA256
		}
	}
	if a.OwnerSHA256 != ownerDigest {
		return a, errors.New("allocation owner artifact mismatch")
	}
	return a, nil
}
func validateAllocation(a allocation, o options) error {
	if a.Schema != "native-cleanup-allocation/v1" || !guidPattern.MatchString(a.AllocationNonce) || a.InvocationNonce != o.invocation || !guidPattern.MatchString(o.invocation) || a.RunID != o.runID || a.RunAttempt != o.attempt || a.SID != o.sid || !strings.HasPrefix(a.SID, "S-1-") || a.OwnerSource != o.sourceSHA || a.ManifestSHA256 != o.manifestSHA {
		return errors.New("allocation identity mismatch")
	}
	if !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(a.RunID) || !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(a.RunAttempt) || a.Share != "lopper-"+a.AllocationNonce || !a.Temporary || !filepath.IsAbs(a.Path) || filepath.Base(a.Path) != "lopper-"+a.AllocationNonce || !digestPattern.MatchString(a.OwnerSHA256) || !digestPattern.MatchString(a.SentinelSHA256) {
		return errors.New("invalid allocation premise")
	}
	if !slices.Contains([]string{"registered", "directory-pending", "acl-pending", "share-pending", "ready"}, a.State) {
		return errors.New("invalid partial allocation state")
	}
	if a.State == "ready" && (a.VolumeFileID == "" || !digestPattern.MatchString(a.NTFSACL) || !digestPattern.MatchString(a.ShareACL)) {
		return errors.New("incomplete allocation descriptors")
	}
	return nil
}
func readJoin(o options, c custody) (joinRecord, error) {
	var j joinRecord
	data, err := checkedFile(o.join, o.joinSHA, c.ControlDirectory, documentLimit)
	if err != nil {
		return j, err
	}
	if err = strictJSON(data, &j); err != nil {
		return j, err
	}
	if err := validateJoin(j); err != nil {
		return j, err
	}
	for _, b := range []binding{j.Stdout, j.Stderr} {
		if _, err := checkedFile(b.Path, b.SHA256, c.ControlDirectory, resultLimit); err != nil {
			return j, err
		}
	}
	return j, nil
}
func validateJoin(j joinRecord) error {
	if j.OwnerPID <= 0 {
		return errors.New("missing original owner identity")
	}
	if j.Schema != "native-cleanup-join/v1" || !j.OriginalCleanup || !j.RootWait || !j.Drains || !j.HelpersJoined || !slices.Contains([]string{"subject-go", "no-workload-launched"}, j.Workload) || j.Status < 0 {
		return errors.New("incomplete original owner join")
	}
	start, err := time.Parse(time.RFC3339Nano, j.CleanupStarted)
	if err != nil {
		return err
	}
	end, err := time.Parse(time.RFC3339Nano, j.CleanupFinished)
	if err != nil {
		return err
	}
	if end.Before(start) || end.Sub(start) >= 30*time.Second {
		return errors.New("join exceeded single reserve")
	}
	return nil
}
func matchJoin(j joinRecord, a allocation, sha string) error {
	if j.AllocationSHA256 != sha || j.AllocationNonce != a.AllocationNonce || j.InvocationNonce != a.InvocationNonce || j.RunID != a.RunID || j.RunAttempt != a.RunAttempt || j.ManifestSHA256 != a.ManifestSHA256 || j.OwnerSource != a.OwnerSource || j.OwnerSHA256 != a.OwnerSHA256 {
		return errors.New("original join allocation binding mismatch")
	}
	return nil
}

// Go's stream fields are optional by definition; custody/record fields are not.
type testEvent struct {
	Time        string  `json:"Time"`
	Action      string  `json:"Action"`
	Package     string  `json:"Package"`
	Test        string  `json:"Test"`
	Elapsed     float64 `json:"Elapsed"`
	Output      string  `json:"Output"`
	FailedBuild string  `json:"FailedBuild"`
	ImportPath  string  `json:"ImportPath"`
}

type resultAudit struct {
	expected        []string
	runs, passes    map[string]bool
	started, passed bool
}

func auditResults(data []byte, status int) error {
	if status != 0 {
		return errors.New("go command exited nonzero")
	}
	a := resultAudit{expected: append([]string{rootTest, rootTest + "/drive", rootTest + "/unc"}, caseInventory()...), runs: map[string]bool{}, passes: map[string]bool{}}
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		e, err := decodeEvent(line)
		if err != nil {
			return err
		}
		if err = a.consume(e); err != nil {
			return err
		}
	}
	if !a.started || !a.passed || len(a.runs) != len(a.expected) || len(a.passes) != len(a.expected) {
		return errors.New("missing exact root/scheme/16 run/pass outcomes")
	}
	return nil
}
func (a *resultAudit) consume(e testEvent) error {
	if e.Package != subjectPackage || e.FailedBuild != "" || a.passed {
		return errors.New("unexpected package/build/trailing event")
	}
	if e.Test == "" {
		return packageEvent(e, &a.started, &a.passed)
	}
	if !a.started || !slices.Contains(a.expected, e.Test) {
		return errors.New("unexpected test inventory")
	}
	if err := a.hierarchy(e); err != nil {
		return err
	}
	return leafEvent(e, a.runs, a.passes)
}
func (a *resultAudit) hierarchy(e testEvent) error {
	if strings.Contains(e.Test, "/") {
		parent := e.Test[:strings.LastIndex(e.Test, "/")]
		if !a.runs[parent] || a.passes[parent] {
			return errors.New("child outside live parent test")
		}
	}
	if e.Action != "pass" {
		return nil
	}
	for _, name := range a.expected {
		if strings.HasPrefix(name, e.Test+"/") && !a.passes[name] {
			return errors.New("parent passed before required children")
		}
	}
	return nil
}
func decodeEvent(line []byte) (testEvent, error) {
	var e testEvent
	d := json.NewDecoder(bytes.NewReader(line))
	if err := uniqueValue(d); err != nil {
		return e, err
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return e, errors.New("trailing event JSON")
	}
	if err := eventFields(line); err != nil {
		return e, err
	}
	d = json.NewDecoder(bytes.NewReader(line))
	d.DisallowUnknownFields()
	return e, d.Decode(&e)
}
func eventFields(line []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return err
	}
	known := []string{"Time", "Action", "Package", "Test", "Elapsed", "Output", "FailedBuild", "ImportPath"}
	for key, raw := range fields {
		if !slices.Contains(known, key) || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return errors.New("unknown/case-aliased/null Go event field")
		}
	}
	return nil
}
func packageEvent(e testEvent, started, passed *bool) error {
	switch e.Action {
	case "start":
		if *started {
			return errors.New("duplicate package start")
		}
		*started = true
	case "pass":
		if !*started {
			return errors.New("package passed without start")
		}
		*passed = true
	case "output":
		if !*started {
			return errors.New("output before package start")
		}
	default:
		return errors.New("unexpected package outcome")
	}
	return nil
}
func leafEvent(e testEvent, runs, passes map[string]bool) error {
	switch e.Action {
	case "run":
		if runs[e.Test] {
			return errors.New("duplicate test run")
		}
		runs[e.Test] = true
	case "pass":
		if !runs[e.Test] || passes[e.Test] {
			return errors.New("duplicate/out-of-order test pass")
		}
		passes[e.Test] = true
	case "output":
		if !runs[e.Test] || passes[e.Test] {
			return errors.New("out-of-order test output")
		}
	default:
		return errors.New("skip/fail/unexpected test action")
	}
	return nil
}

// Current Windows-selected local closure; genuine-parent integration must reselect and review it.
var sourcePaths = []string{
	"go.mod",
	"go.sum",
	"internal/baseline/snapshot.go",
	"internal/baseline/store.go",
	"internal/baseline/store_symlink_other.go",
	"internal/csvsanitize/sanitize.go",
	"internal/errutil/sentinel.go",
	"internal/references/allocator.go",
	"internal/report/baseline_catalog.go",
	"internal/report/baseline_diff.go",
	"internal/report/baseline_snapshot.go",
	"internal/report/coverage.go",
	"internal/report/format.go",
	"internal/report/format_csv.go",
	"internal/report/format_cyclonedx.go",
	"internal/report/format_helpers.go",
	"internal/report/format_pr_comment.go",
	"internal/report/format_spdx.go",
	"internal/report/format_table.go",
	"internal/report/format_table_sections.go",
	"internal/report/format_table_values.go",
	"internal/report/format_vex.go",
	"internal/report/identity_normalization.go",
	"internal/report/license.go",
	"internal/report/model/dependency.go",
	"internal/report/model/metadata.go",
	"internal/report/model/report.go",
	"internal/report/model_aliases.go",
	"internal/report/ordering.go",
	"internal/report/osv_versions.go",
	"internal/report/recommendation.go",
	"internal/report/report.go",
	"internal/report/sarif.go",
	"internal/report/sarif_locations.go",
	"internal/report/sarif_schema.go",
	"internal/report/sarif_signals.go",
	"internal/report/scoring.go",
	"internal/report/scoring_findings.go",
	"internal/report/scoring_reachability.go",
	"internal/report/strings.go",
	"internal/report/summary.go",
	"internal/report/terminal.go",
	"internal/report/vulnerability.go",
	"internal/runtime/capture.go",
	"internal/runtime/capture_command.go",
	"internal/runtime/capture_command_python.go",
	"internal/runtime/capture_command_windows_parse.go",
	"internal/runtime/capture_env.go",
	"internal/runtime/capture_job_windows.go",
	"internal/runtime/capture_output.go",
	"internal/runtime/capture_process_windows.go",
	"internal/runtime/capture_state.go",
	"internal/runtime/trace_annotate.go",
	"internal/runtime/trace_load.go",
	"internal/runtime/trace_resolution.go",
	"internal/runtime/trace_types.go",
	"internal/safeio/atomic_write.go",
	"internal/safeio/atomic_write_fallback_windows.go",
	"internal/safeio/atomic_write_if_absent_windows.go",
	"internal/safeio/atomic_write_link_windows.go",
	"internal/safeio/atomic_write_target_windows.go",
	"internal/safeio/filesystem.go",
	"internal/safeio/move.go",
	"internal/safeio/path.go",
	"internal/safeio/pinned_child.go",
	"internal/safeio/read.go",
	"internal/safeio/rename_no_replace_windows.go",
	"internal/safeio/search_directory_unsupported.go",
	"internal/safeio/write.go",
	"internal/terminal/sanitize.go",
	"scripts/hook_cleanup_native_windows_test.go",
	"scripts/native-windows-hook-fixture.ps1",
	"tools/nativecleanupcheck/main.go",
	"tools/nativecleanupowner/main_windows.go",
}
