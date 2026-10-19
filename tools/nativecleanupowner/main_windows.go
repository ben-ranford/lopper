//go:build windows

// nativecleanupowner is the single foreground #1753 fixture driver.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"
	"unsafe"

	commandruntime "github.com/ben-ranford/lopper/internal/runtime"
	win "golang.org/x/sys/windows"
)

const commandAllowance = 5 * time.Second
const cleanupReserve = 30 * time.Second
const ownerBudget = 11 * time.Minute
const captureLimit = 64 << 20

type configuration struct {
	Root, Manifest, ManifestSHA, Verifier, VerifierSHA, SourceSHA, PolicySHA string
	Subject, SubjectSHA, RunID, Attempt, Invocation, SID, Temp               string
	GoCache, GoModCache                                                      string
}
type driver struct {
	admissionClosed               bool
	retentionRequired             bool
	cfg                           configuration
	env                           []string
	goTool, powershell, helper    string
	deadline                      time.Time
	cleanupStarted                time.Time
	control                       *os.File
	allocationPath, allocationSHA string
	joinPath, joinSHA             string
	sequence                      int
	allJoined                     bool
	cacheIDs                      [2]string
}
type commandResult struct {
	Status         int
	Joined         bool
	CollectedExit  bool
	Err            error
	Stdout, Stderr string
}
type capture struct {
	file    *os.File
	written int64
	err     error
}

func (c *capture) Write(p []byte) (int, error) {
	if c.err != nil {
		return len(p), nil
	}
	if c.written+int64(len(p)) > captureLimit {
		c.err = errors.New("raw child capture exceeds reviewed 64MiB bound")
		return len(p), nil
	}
	n, err := c.file.Write(p)
	c.written += int64(n)
	if err != nil {
		c.err = err
	}
	return len(p), nil
}
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "RETENTION:", err)
		os.Exit(1)
	}
}
func run(args []string) (returnErr error) {
	cfg, err := parseConfiguration(args)
	if err != nil {
		return err
	}
	control, err := nonInheritedControl()
	if err != nil {
		return err
	}
	d := &driver{cfg: cfg, deadline: time.Now().Add(ownerBudget), control: control, allJoined: true}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Recovery remains in this original driver. A panic cannot create join proof.
	defer func() {
		if p := recover(); p != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("owner panic, retain allocation: %v", p))
		}
	}()
	if err = d.initialPolicy(ctx); err != nil {
		return err
	}
	if err = d.verifySubject(ctx); err != nil {
		return err
	}
	setup := d.ownedCommand(ctx, "setup", d.powershell, d.helperArgs("setup")...)
	d.allJoined = setup.Joined
	if setup.Err != nil || !setup.Joined {
		d.beginCleanup()
	}
	if err = d.loadAllocation(context.WithoutCancel(ctx)); err != nil {
		return errors.Join(setup.Err, err)
	}
	if setup.Err != nil {
		return d.finish(ctx, setup, "no-workload-launched")
	}
	if err = d.check(ctx, true, false); err != nil {
		setup.Err = errors.Join(setup.Err, err)
		return d.finish(ctx, setup, "no-workload-launched")
	}
	subject := d.subjectCommand(ctx)
	return d.finish(ctx, subject, "subject-go")
}
func parseConfiguration(args []string) (configuration, error) {
	var c configuration
	if len(args) == 0 || args[0] != "run" {
		return c, errors.New("expected run")
	}
	f := flag.NewFlagSet("nativecleanupowner", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	fields := map[string]*string{"trusted-root": &c.Root, "manifest": &c.Manifest, "manifest-sha256": &c.ManifestSHA, "verifier": &c.Verifier, "verifier-sha256": &c.VerifierSHA, "expected-source-sha": &c.SourceSHA, "expected-policy-sha256": &c.PolicySHA, "subject": &c.Subject, "subject-sha": &c.SubjectSHA, "run-id": &c.RunID, "run-attempt": &c.Attempt, "invocation-nonce": &c.Invocation, "sid": &c.SID, "allocation-parent": &c.Temp, "go-cache": &c.GoCache, "go-mod-cache": &c.GoModCache}
	for name, p := range fields {
		f.StringVar(p, name, "", name)
	}
	if err := f.Parse(args[1:]); err != nil {
		return c, err
	}
	if f.NArg() != 0 {
		return c, errors.New("unexpected operand")
	}
	for name, p := range fields {
		if *p == "" {
			return c, fmt.Errorf("missing %s", name)
		}
	}
	if !filepath.IsAbs(c.Root) || !filepath.IsAbs(c.Subject) || !filepath.IsAbs(c.Temp) || !fullRevision(c.SubjectSHA) {
		return c, errors.New("invalid fixed path/subject expectation")
	}
	return c, nil
}
func nonInheritedControl() (*os.File, error) {
	h, err := win.GetStdHandle(win.STD_OUTPUT_HANDLE)
	if err != nil {
		return nil, err
	}
	if err = win.SetHandleInformation(h, win.HANDLE_FLAG_INHERIT, 0); err != nil {
		return nil, err
	}
	return os.Stdout, nil
}
func (d *driver) baseCheckArgs() []string {
	return []string{"verify-custody", "--trusted-root", d.cfg.Root, "--manifest", d.cfg.Manifest, "--manifest-sha256", d.cfg.ManifestSHA, "--verifier", d.cfg.Verifier, "--verifier-sha256", d.cfg.VerifierSHA, "--expected-source-sha", d.cfg.SourceSHA, "--expected-policy-sha256", d.cfg.PolicySHA, "--invocation-nonce", d.cfg.Invocation, "--go-cache", d.cfg.GoCache, "--go-mod-cache", d.cfg.GoModCache}
}
func (d *driver) recordArgs() []string {
	return []string{"--allocation", d.allocationPath, "--allocation-sha256", d.allocationSHA, "--run-id", d.cfg.RunID, "--run-attempt", d.cfg.Attempt, "--sid", d.cfg.SID}
}
func (d *driver) check(ctx context.Context, allocation, join bool) (returnErr error) {
	defer func() {
		if returnErr != nil {
			d.retentionRequired = true
			d.beginCleanup()
		}
	}()
	if err := d.anchorGuard(); err != nil {
		return err
	}
	args := d.baseCheckArgs()
	if allocation {
		args = append(args, d.recordArgs()...)
	}
	if join {
		args = append(args, "--join-record", d.joinPath, "--join-record-sha256", d.joinSHA)
	}
	r := d.ownedCommand(ctx, "verify", d.cfg.Verifier, args...)
	d.allJoined = d.allJoined && r.Joined
	return r.Err
}
func (d *driver) anchorGuard() error {
	if d.cfg.Manifest != filepath.Join(d.cfg.Root, "custody.json") || d.cfg.Verifier != filepath.Join(d.cfg.Root, "bin", "nativecleanupcheck.exe") {
		return errors.New("anchor layout mismatch")
	}
	if err := nativePath(d.cfg.Manifest, false); err != nil {
		return err
	}
	if err := nativePath(d.cfg.Verifier, false); err != nil {
		return err
	}
	if err := fileDigestMatches(d.cfg.Manifest, d.cfg.ManifestSHA, 1<<20); err != nil {
		return err
	}
	if err := fileDigestMatches(d.cfg.Verifier, d.cfg.VerifierSHA, captureLimit); err != nil {
		return err
	}
	if err := d.controlIdentity(); err != nil {
		return err
	}
	if d.cacheIDs[0] != "" {
		return d.cacheIdentity(false)
	}
	return nil
}
func (d *driver) initialPolicy(ctx context.Context) error {
	// Bootstrap must independently guard these anchors before starting this owner.
	// Owner repeats that guard before running its fixed checker.
	d.env = []string{"SystemRoot=" + os.Getenv("SystemRoot")}
	if err := d.check(ctx, false, false); err != nil {
		return err
	}
	data, err := os.ReadFile(d.cfg.Manifest)
	if err != nil {
		return err
	}
	var c struct {
		Policy struct {
			Environment   []string            `json:"environment"`
			Distributions []ownerDistribution `json:"distributions"`
		} `json:"policy"`
	}
	if err = json.Unmarshal(data, &c); err != nil {
		return err
	}
	d.env = c.Policy.Environment
	if err := d.cacheIdentity(true); err != nil {
		return err
	}
	d.selectTools(c.Policy.Distributions)
	if err := d.validateAllocationParent(); err != nil {
		return err
	}
	if d.goTool == "" || d.powershell == "" {
		return errors.New("fixed Go/PowerShell distribution entry missing")
	}
	d.helper = filepath.Join(d.cfg.Root, "source", "scripts", "native-windows-hook-fixture.ps1")
	d.allocationPath = filepath.Join(d.cfg.Root, "control", "allocation.json")
	return nativePath(filepath.Join(d.cfg.Root, "control"), true)
}

type ownerDistribution struct {
	Name  string `json:"name"`
	Files []struct {
		Path string `json:"path"`
	} `json:"files"`
}

func (d *driver) selectTools(distributions []ownerDistribution) {
	for _, dist := range distributions {
		for _, b := range dist.Files {
			base := strings.ToLower(filepath.Base(b.Path))
			if dist.Name == "go" && base == "go.exe" {
				d.goTool = b.Path
			}
			if dist.Name == "powershell" && base == "pwsh.exe" {
				d.powershell = b.Path
			}
		}
	}
}
func (d *driver) helperArgs(mode string) []string {
	args := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-File", d.helper, "-Mode", mode, "-TrustedRoot", d.cfg.Root, "-Manifest", d.cfg.Manifest, "-ManifestSHA256", d.cfg.ManifestSHA, "-Verifier", d.cfg.Verifier, "-VerifierSHA256", d.cfg.VerifierSHA, "-ExpectedSourceSHA", d.cfg.SourceSHA, "-ExpectedPolicySHA256", d.cfg.PolicySHA, "-AllocationRecord", d.allocationPath, "-InvocationNonce", d.cfg.Invocation, "-RunID", d.cfg.RunID, "-RunAttempt", d.cfg.Attempt, "-SID", d.cfg.SID, "-AllocationParent", d.cfg.Temp, "-GoCache", d.cfg.GoCache, "-GoModCache", d.cfg.GoModCache}
	if mode != "setup" {
		args = append(args, "-DeadlineUTC", d.cleanupStarted.Add(cleanupReserve).UTC().Format(time.RFC3339Nano), "-AllocationSHA256", d.allocationSHA, "-JoinRecord", d.joinPath, "-JoinRecordSHA256", d.joinSHA)
	}
	return args
}
func (d *driver) loadAllocation(ctx context.Context) (returnErr error) {
	defer func() {
		if returnErr != nil {
			d.beginCleanup()
		}
	}()
	if err := nativePath(d.allocationPath, false); err != nil {
		return err
	}
	sha, err := fileDigest(d.allocationPath)
	if err != nil {
		return err
	}
	d.allocationSHA = sha
	return d.check(ctx, true, false)
}
func (d *driver) subjectCommand(ctx context.Context) commandResult {
	if err := d.check(ctx, true, false); err != nil {
		return commandResult{Status: -1, Err: err}
	}
	data, err := os.ReadFile(d.allocationPath)
	if err != nil {
		return commandResult{Status: -1, Err: err}
	}
	var a struct {
		Path  string `json:"path"`
		Share string `json:"share"`
		State string `json:"state"`
	}
	if err = json.Unmarshal(data, &a); err != nil || a.State != "ready" {
		return commandResult{Status: -1, Err: errors.Join(err, errors.New("allocation not ready"))}
	}
	env := append(append([]string{}, d.env...), "LOPPER_NATIVE_SMB_LOCAL="+a.Path, `LOPPER_NATIVE_SMB_UNC=\\localhost\`+a.Share)
	return d.execute(ctx, "subject", d.cfg.Subject, env, d.goTool, "test", "-tags=lopper_native_windows", "-json", "-count=1", "-timeout=10m", "./scripts", "-run", "^TestHooksCleanupNativeWindows$")
}
func (d *driver) beginCleanup() {
	if d.cleanupStarted.IsZero() {
		d.cleanupStarted = time.Now()
	}
}
func (d *driver) remaining() time.Duration {
	if d.cleanupStarted.IsZero() {
		return time.Until(d.deadline)
	}
	return time.Until(d.cleanupStarted.Add(cleanupReserve))
}
func (d *driver) launchBudget() (time.Duration, error) {
	remaining := d.remaining()
	if remaining <= 2*commandAllowance {
		return 0, errors.New("insufficient single reserve for supervised command and drains")
	}
	return remaining - 2*commandAllowance, nil
}
func (d *driver) ownedCommand(ctx context.Context, label, tool string, args ...string) commandResult {
	r := d.execute(ctx, label, d.cfg.Root, d.env, tool, args...)
	// The fixed setup/finally helper has no semantic failure class. A collected
	// nonzero exit proves joining, not that its custody or inspection was safe.
	if (label == "setup" || label == "finally") && (r.Err != nil || !r.Joined || r.Status != 0) {
		d.retentionRequired = true
		d.beginCleanup()
	}
	return r
}
func (d *driver) execute(ctx context.Context, label, dir string, env []string, tool string, args ...string) (r commandResult) {
	r.Status = -1
	defer d.observeCommand(&r)
	if d.admissionClosed {
		r.Err = errors.New("original control handoff closed helper admission")
		return r
	}
	_, err := d.launchBudget()
	if err != nil {
		r.Err = err
		return r
	}
	// Command context is deliberately never cancelled. Only the original retained
	// closure responds to the separate driver deadline/interrupt.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), tool, args...)
	cmd.Dir = dir
	cmd.Env = env
	commandruntime.ConfigureCommandCancellation(cmd)
	cmd.WaitDelay = commandAllowance
	streams, err := d.openStreams(label, cmd)
	if err != nil {
		r.Err = err
		return r
	}
	r.Stdout, r.Stderr = streams[0].capture.file.Name(), streams[1].capture.file.Name()
	cleanup, err := commandruntime.StartCommand(cmd)
	if err != nil {
		d.beginCleanup()
		r.Err = errors.Join(err, closeUnstarted(streams))
		return r
	}
	writerCloseErr := d.startDrains(streams)
	waits := make(chan error, 1)
	go func() { waits <- cmd.Wait() }()
	var progress commandProgress
	defer func() {
		if p := recover(); p != nil {
			d.recoverOwnedCommand(p, &r, cleanup, waits, streams, progress)
		}
	}()

	budget, launchErr := d.launchBudget()
	if launchErr != nil {
		d.beginCleanup()
		r.Err = launchErr
		budget = 0
	}
	timer := time.NewTimer(budget)
	defer timer.Stop()
	var waitErr, cleanupErr error
	select {
	case waitErr = <-waits:
		if label == "subject" || waitErr != nil {
			d.beginCleanup()
		}
		cleanupErr = d.cleanupCommand(cleanup)
	case <-ctx.Done():
		d.beginCleanup()
		cleanupErr = d.cleanupCommand(cleanup)
		waitErr = <-waits
		r.Err = ctx.Err()
	case <-timer.C:
		d.beginCleanup()
		cleanupErr = d.cleanupCommand(cleanup)
		waitErr = <-waits
		r.Err = errors.New("driver execution budget expired")
	}
	progress.waitCollected, progress.cleanupDone = true, true
	r.Status, err = terminalStatus(cmd, waitErr)
	drainErr := d.joinStreams(streams)
	progress.drainsDone = true
	// These are separate error observations: an ExitError never hides a drain or
	// original cleanup failure. Joined errors containing another fault are unsafe.
	r.Joined = launchErr == nil && err == nil && cleanupErr == nil && drainErr == nil && writerCloseErr == nil && d.remaining() > 0
	r.CollectedExit = r.Joined && r.Err == nil && waitErr != nil
	r.Err = errors.Join(r.Err, waitErr, err, cleanupErr, drainErr, writerCloseErr)
	if !r.Joined {
		d.allJoined = false
		r.Err = errors.Join(r.Err, errors.New("uncertain command ownership/wait/drain; retain allocation"))
	}
	return r
}

func (d *driver) observeCommand(r *commandResult) {
	if r.Err != nil {
		d.beginCleanup()
	}
}
func (d *driver) startDrains(streams []*stream) error {
	var err error
	for _, s := range streams {
		closeErr := s.writer.Close()
		if closeErr != nil {
			d.beginCleanup()
		}
		err = errors.Join(err, closeErr)
		go s.drain()
	}
	return err
}
func (d *driver) cleanupCommand(cleanup func() error) error {
	err := cleanup()
	if err != nil {
		d.beginCleanup()
	}
	return err
}

type commandProgress struct {
	waitCollected, cleanupDone, drainsDone bool
}

func (d *driver) recoverOwnedCommand(p any, r *commandResult, cleanup func() error, waits chan error, streams []*stream, progress commandProgress) {
	d.beginCleanup()
	if !progress.cleanupDone {
		r.Err = errors.Join(r.Err, cleanup())
	}
	if !progress.waitCollected {
		r.Err = errors.Join(r.Err, <-waits)
	}
	if !progress.drainsDone {
		r.Err = errors.Join(r.Err, d.joinStreams(streams))
	}
	d.allJoined = false
	r.Joined = false
	r.Err = errors.Join(r.Err, fmt.Errorf("command panic after original cleanup attempt; retain: %v", p))
}

type stream struct {
	reader, writer *os.File
	capture        *capture
	done           chan error
}

func (s *stream) drain() {
	_, err := io.Copy(s.capture, s.reader)
	s.done <- errors.Join(err, s.capture.err, s.reader.Close(), s.capture.file.Sync(), s.capture.file.Close())
}
func (d *driver) openStreams(label string, cmd *exec.Cmd) ([]*stream, error) {
	var streams []*stream
	d.sequence++
	if label != "subject" && label != "setup" {
		label = fmt.Sprintf("%s-%d", label, d.sequence)
	}
	for _, suffix := range []string{"stdout", "stderr"} {
		// Only raw child bytes enter these files; they never reach owner stdout.
		f, err := os.OpenFile(filepath.Join(d.cfg.Root, "control", label+"."+suffix+".raw"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			d.beginCleanup()
			return nil, errors.Join(err, closeUnstarted(streams))
		}
		rd, wr, err := os.Pipe()
		if err != nil {
			d.beginCleanup()
			return nil, errors.Join(err, f.Close(), closeUnstarted(streams))
		}
		streams = append(streams, &stream{reader: rd, writer: wr, capture: &capture{file: f}, done: make(chan error, 1)})
	}
	cmd.Stdout, cmd.Stderr = streams[0].writer, streams[1].writer
	return streams, nil
}
func closeUnstarted(streams []*stream) error {
	var err error
	for _, s := range streams {
		err = errors.Join(err, s.reader.Close(), s.writer.Close(), s.capture.file.Close())
	}
	return err
}
func (d *driver) joinStreams(streams []*stream) error {
	var errs []error
	deadline := time.Now().Add(min(commandAllowance, d.remaining()))
	for _, s := range streams {
		timer := time.NewTimer(max(0, time.Until(deadline)))
		select {
		case err := <-s.done:
			errs = append(errs, err)
			if err != nil {
				d.beginCleanup()
			}
		case <-timer.C:
			d.beginCleanup()
			errs = append(errs, errors.New("raw drain deadline exceeded"), s.reader.Close())
			errs = append(errs, <-s.done)
		}
		timer.Stop()
	}
	return errors.Join(errs...)
}
func terminalStatus(cmd *exec.Cmd, err error) (int, error) {
	if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
		return -1, errors.New("no observed terminal root status")
	}
	status := cmd.ProcessState.ExitCode()
	if status < 0 {
		return status, errors.New("uncollected terminal status")
	}
	if err == nil {
		if status != 0 {
			return status, errors.New("nonzero status without ExitError")
		}
		return status, nil
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || reflect.TypeOf(err) != reflect.TypeFor[*exec.ExitError]() || exit.ProcessState != cmd.ProcessState || status == 0 {
		return status, errors.New("wait error is not solely a collected nonzero exit")
	}
	return status, nil
}
func (d *driver) finish(ctx context.Context, r commandResult, workload string) error {
	d.beginCleanup()
	if !r.Joined || !d.allJoined || d.retentionRequired || d.allocationSHA == "" {
		return errors.Join(r.Err, errors.New("missing original completed ownership observation"))
	}
	// Interrupt requests cleanup, but does not cancel trusted post-join operations.
	cleanupCtx := context.WithoutCancel(ctx)
	if err := d.check(cleanupCtx, true, false); err != nil {
		return errors.Join(r.Err, err)
	}
	if err := d.writeJoin(r, workload); err != nil {
		return errors.Join(r.Err, err)
	}
	if err := d.check(cleanupCtx, true, true); err != nil {
		return errors.Join(r.Err, err)
	}
	auditErr := error(nil)
	if workload == "subject-go" {
		var allowed bool
		allowed, auditErr = d.audit(cleanupCtx, r)
		if !allowed {
			return errors.Join(r.Err, auditErr, errors.New("audit uncertainty; retain allocation"))
		}
	}
	if err := d.check(cleanupCtx, true, true); err != nil {
		return errors.Join(r.Err, auditErr, err)
	}
	receipt := d.ownedCommand(cleanupCtx, "finally", d.powershell, d.helperArgs("finally")...)
	if !receipt.Joined || !d.allJoined {
		return errors.Join(r.Err, auditErr, receipt.Err, errors.New("teardown helper uncertainty; no recovery handoff"))
	}
	if err := d.emitOriginalControl(); err != nil {
		return errors.Join(r.Err, auditErr, receipt.Err, err)
	}
	return errors.Join(r.Err, auditErr, receipt.Err)

}
func (d *driver) audit(ctx context.Context, r commandResult) (allowed bool, auditErr error) {
	defer func() {
		if !allowed {
			d.retentionRequired = true
			d.beginCleanup()
		}
	}()
	if err := d.anchorGuard(); err != nil {
		return false, err
	}
	sha, err := fileDigest(r.Stdout)
	if err != nil {
		return false, err
	}
	args := d.baseCheckArgs()
	args[0] = "audit-results"
	args = append(args, d.recordArgs()...)
	args = append(args, "--join-record", d.joinPath, "--join-record-sha256", d.joinSHA, "--results", r.Stdout, "--results-sha256", sha, "--go-status", fmt.Sprint(r.Status))
	result := d.ownedCommand(ctx, "audit", d.cfg.Verifier, args...)
	d.allJoined = d.allJoined && result.Joined
	return auditTeardownAllowed(result), result.Err
}
func auditTeardownAllowed(r commandResult) bool {
	if !r.Joined {
		return false
	}
	return (r.Status == 0 && r.Err == nil) || (r.Status == 3 && r.CollectedExit)
}
func (d *driver) writeJoin(r commandResult, workload string) error {
	data, err := os.ReadFile(d.allocationPath)
	if err != nil {
		return err
	}
	var a map[string]any
	if err = json.Unmarshal(data, &a); err != nil {
		return err
	}
	stdout, err := fileDigest(r.Stdout)
	if err != nil {
		return err
	}
	stderr, err := fileDigest(r.Stderr)
	if err != nil {
		return err
	}
	j := map[string]any{"schema": "native-cleanup-join/v1", "allocation_sha256": d.allocationSHA, "allocation_nonce": a["allocation_nonce"], "invocation_nonce": d.cfg.Invocation, "run_id": d.cfg.RunID, "run_attempt": d.cfg.Attempt, "manifest_sha256": d.cfg.ManifestSHA, "owner_source": d.cfg.SourceSHA, "owner_sha256": a["owner_sha256"], "owner_pid": os.Getpid(), "workload": workload, "status": r.Status, "original_cleanup": true, "root_wait": true, "drains": true, "helpers_joined": true, "cleanup_started": d.cleanupStarted.UTC().Format(time.RFC3339Nano), "cleanup_finished": time.Now().UTC().Format(time.RFC3339Nano), "stdout": map[string]string{"path": r.Stdout, "sha256": stdout}, "stderr": map[string]string{"path": r.Stderr, "sha256": stderr}}
	d.joinPath = filepath.Join(d.cfg.Root, "control", "join.json")
	d.joinSHA, err = writeExclusiveJSON(d.joinPath, j)
	if err != nil {
		return err
	}
	return nil
}
func (d *driver) emitOriginalControl() error {
	if d.retentionRequired || !d.allJoined || d.remaining() <= 0 || d.joinSHA == "" {
		return errors.New("incomplete/late helper join; no recovery handoff")
	}
	if err := d.anchorGuard(); err != nil {
		return err
	}
	if err := fileDigestMatches(d.joinPath, d.joinSHA, 1<<20); err != nil {
		return err
	}
	if d.remaining() <= 0 {
		d.admissionClosed = true
		return errors.New("final guard exceeded original reserve")
	}
	// No command may be admitted after this message. A killed owner during any
	// post-join helper supplies no completed-control observation to recovery.
	d.admissionClosed = true
	message := map[string]string{"schema": "native-cleanup-control/v1", "join_record": d.joinPath, "join_record_sha256": d.joinSHA}
	err := json.NewEncoder(d.control).Encode(message)
	if d.remaining() <= 0 {
		return errors.Join(err, errors.New("original control write exceeded reserve"))
	}
	return err
}

func writeExclusiveJSON(path string, v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	_, writeErr := f.Write(data)
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
func fileDigest(path string) (value string, returnErr error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { returnErr = errors.Join(returnErr, f.Close()) }()
	h := sha256.New()
	_, err = io.Copy(h, io.LimitReader(f, captureLimit+1))
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func fileDigestMatches(path, expected string, limit int64) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() > limit {
		return errors.New("anchor exceeds source policy")
	}
	actual, err := fileDigest(path)
	if err != nil {
		return err
	}
	if len(expected) != 64 || actual != expected {
		return errors.New("independent anchor digest mismatch")
	}
	return nil
}
func nativePath(path string, directory bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("noncanonical native path")
	}
	for current := path; ; current = filepath.Dir(current) {
		p, err := win.UTF16PtrFromString(current)
		if err != nil {
			return err
		}
		attrs, err := win.GetFileAttributes(p)
		if err != nil {
			return err
		}
		if attrs&win.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return errors.New("native reparse component")
		}
		if current == path && (attrs&win.FILE_ATTRIBUTE_DIRECTORY != 0) != directory {
			return errors.New("native path type mismatch")
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	return nil
}

// Process cleanup above uses only the original StartCommand closure.

func (d *driver) validateAllocationParent() error {
	for _, v := range d.env {
		k, value, ok := strings.Cut(v, "=")
		if ok && k == "TEMP" && value == d.cfg.Temp {
			return nil
		}
	}
	return errors.New("allocation parent differs from reviewed child TEMP")
}

func (d *driver) controlIdentity() error {
	data, err := os.ReadFile(d.cfg.Manifest)
	if err != nil {
		return err
	}
	var c struct {
		ID  string `json:"control_volume_file_id"`
		ACL string `json:"control_acl_sha256"`
	}
	if err = json.Unmarshal(data, &c); err != nil {
		return err
	}
	path := filepath.Join(d.cfg.Root, "control")
	id, err := openedDirectoryIdentity(path)
	if err != nil {
		return err
	}
	if id != c.ID {
		return errors.New("control directory replacement")
	}
	sd, err := win.GetNamedSecurityInfo(path, win.SE_FILE_OBJECT, win.OWNER_SECURITY_INFORMATION|win.GROUP_SECURITY_INFORMATION|win.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(sd.String()))
	if hex.EncodeToString(sum[:]) != c.ACL {
		return errors.New("control descriptor drift")
	}
	return d.privateControl(sd)
}
func (d *driver) privateControl(sd *win.SECURITY_DESCRIPTOR) error {
	user, err := win.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	if user.User.Sid.String() != d.cfg.SID {
		return errors.New("driver current SID mismatch")
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if owner.String() != d.cfg.SID {
		return errors.New("control owner SID mismatch")
	}
	control, _, err := sd.Control()
	if err != nil {
		return err
	}
	if control&win.SE_DACL_PROTECTED == 0 {
		return errors.New("control DACL is inherited")
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if acl == nil || acl.AceCount != 1 {
		return errors.New("control DACL inventory mismatch")
	}
	var ace *win.ACCESS_ALLOWED_ACE
	if err = win.GetAce(acl, 0, &ace); err != nil {
		return err
	}
	sid := (*win.SID)(unsafe.Pointer(&ace.SidStart))
	if ace.Header.AceType != win.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != win.OBJECT_INHERIT_ACE|win.CONTAINER_INHERIT_ACE || sid.String() != d.cfg.SID || ace.Mask != 0x1f01ff {
		return errors.New("control requires exact current-SID-only FullControl ACE")
	}
	return nil
}
func openedDirectoryIdentity(path string) (value string, returnErr error) {
	if err := nativePath(path, true); err != nil {
		return "", err
	}
	p, err := win.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	h, err := win.CreateFile(p, win.FILE_READ_ATTRIBUTES, win.FILE_SHARE_READ|win.FILE_SHARE_WRITE|win.FILE_SHARE_DELETE, nil, win.OPEN_EXISTING, win.FILE_FLAG_BACKUP_SEMANTICS|win.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return "", err
	}
	defer func() { returnErr = errors.Join(returnErr, win.CloseHandle(h)) }()
	var info win.ByHandleFileInformation
	if err = win.GetFileInformationByHandle(h, &info); err != nil {
		return "", err
	}
	if info.FileAttributes&win.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return "", errors.New("opened control directory is reparse")
	}
	return fmt.Sprintf("%08x:%08x%08x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), nil
}

func fullRevision(value string) bool {
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == 20 && value == strings.ToLower(value)
}
func (d *driver) verifySubject(ctx context.Context) error {
	if err := nativePath(d.cfg.Subject, true); err != nil {
		return err
	}
	rel, err := filepath.Rel(d.cfg.Root, d.cfg.Subject)
	if err != nil || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..") {
		return errors.New("subject must be outside original owner layout")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe != filepath.Join(d.cfg.Root, "bin", "nativecleanupowner.exe") {
		return errors.New("original owner executable layout mismatch")
	}
	git := ""
	for _, v := range d.env {
		if strings.HasPrefix(v, "LOPPER_NATIVE_GIT=") {
			git = strings.TrimPrefix(v, "LOPPER_NATIVE_GIT=")
		}
	}
	r := d.ownedCommand(ctx, "subject-identity", git, "-C", d.cfg.Subject, "rev-parse", "--verify", "HEAD")
	if r.Err != nil || !r.Joined {
		return errors.Join(r.Err, errors.New("subject revision observation failed"))
	}
	data, err := os.ReadFile(r.Stdout)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(data)) != d.cfg.SubjectSHA {
		return errors.New("requested and observed subject revision differ")
	}
	return nil
}

func (d *driver) cacheIdentity(initial bool) error {
	for i, path := range []string{d.cfg.GoCache, d.cfg.GoModCache} {
		id, err := openedDirectoryIdentity(path)
		if err != nil {
			return err
		}
		if !initial && id != d.cacheIDs[i] {
			return errors.New("invocation cache directory replaced")
		}
		sd, err := win.GetNamedSecurityInfo(path, win.SE_FILE_OBJECT, win.OWNER_SECURITY_INFORMATION|win.GROUP_SECURITY_INFORMATION|win.DACL_SECURITY_INFORMATION)
		if err != nil {
			return err
		}
		if err := d.privateControl(sd); err != nil {
			return err
		}
		if initial {
			d.cacheIDs[i] = id
		}
	}
	return nil
}
