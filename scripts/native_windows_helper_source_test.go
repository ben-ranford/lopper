package scripts

import (
	"os"
	"strings"
	"testing"
)

func sourceSection(t *testing.T, source, startMarker, endMarker string) string {
	t.Helper()
	start := strings.Index(source, startMarker)
	if start < 0 {
		t.Fatalf("missing source section start marker %q", startMarker)
	}
	if endMarker == "" {
		return source[start:]
	}
	endOffset := strings.Index(source[start:], endMarker)
	if endOffset < 0 {
		t.Fatalf("missing source section end marker %q after %q", endMarker, startMarker)
	}
	return source[start : start+endOffset]
}

// This portable text check does not run provisioning or claim AST/native proof.
func TestFixedNativeHelperSourceContract(t *testing.T) {
	data, err := os.ReadFile("native-windows-hook-fixture.ps1")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, required := range []string{"ValidateSet('setup','finally','recovery','self-test')", "Assert-Custody $true $true", "SetAccessRuleProtection($true,$false)", "$rules.Count -ne 1", "Assert-DirectoryIdentity", "Assert-ExpectedChildren", "Get-SmbOpenFile -IncludeHidden", "Get-SmbConnection", "Remove-SmbShare -Name $Name -Confirm:$false", "Remove-ObservedAllocationDirectory $script:allocation.path", "Remove-ObservedMarker (Join-Path $script:allocation.path '.allocation-nonce')", "Original owner join/deadline unavailable", "Single owner cleanup reserve expired", "fault.triggered:$Name@$state", "fault.triggered:$operation@$state"} {
		if !strings.Contains(source, required) {
			t.Fatal("missing contract", required)
		}
	}
	for _, required := range []string{"@{mutation='marker.delete';expected='SELFTEST_EXPECTED:marker.delete'}", "Self-test marker-delete failure did not retain post-share allocation state", "fault.triggered:marker.delete@ready"} {
		if !strings.Contains(source, required) {
			t.Fatal("missing post-share marker-delete transition control", required)
		}
	}
	setup := sourceSection(t, source, "function Setup-Allocation", "function Find-OwnedShare")
	saveIndex := strings.Index(setup, "Save-Allocation")
	createIndex := strings.Index(setup, "[IO.Directory]::CreateDirectory")
	if saveIndex < 0 || createIndex < 0 || saveIndex > createIndex {
		t.Fatal("partial allocation not registered before effects")
	}
	removal := sourceSection(t, source, "function Remove-Allocation", "")
	shareRemoval := strings.Index(removal, "Remove-ObservedShare $script:allocation.share")
	directoryRemoval := strings.Index(removal, "Remove-ObservedAllocationDirectory $script:allocation.path")
	if shareRemoval < 0 || directoryRemoval < 0 || directoryRemoval <= shareRemoval {
		t.Fatal("directory removed before share")
	}
	if strings.Count(removal[:shareRemoval], "Assert-NoObservedUse") != 2 {
		t.Fatal("share teardown must repeat every use observation immediately before removal")
	}
	markerRemoval := strings.Index(removal, "Remove-ObservedMarker (Join-Path $script:allocation.path '.allocation-nonce')")
	if markerRemoval < 0 || directoryRemoval <= markerRemoval {
		t.Fatal("allocation removal must delete only the authenticated marker before nonrecursive directory removal")
	}
	markerHelper := sourceSection(t, source, "function Remove-ObservedMarker", "function Remove-ObservedAllocationDirectory")
	directoryHelper := sourceSection(t, source, "function Remove-ObservedAllocationDirectory", "function Get-ObservedChildren")
	if !strings.Contains(markerHelper, "[IO.File]::Delete($Path)") || !strings.Contains(directoryHelper, "[IO.Directory]::Delete($Path,$false)") {
		t.Fatal("teardown wrappers must remove only the marker and use nonrecursive directory deletion")
	}
	for _, forbidden := range []string{"Start-Service", "Set-Service", "New-LocalUser", "Start-Process", "Invoke-Expression", "Remove-Item -Recurse", "Remove-SmbShare -Force", "Directory]::Delete($script:allocation.path,$true)", "Close-Smb", "GITHUB_OUTPUT", "net use", "Everyone", "Administrators", "-Credential"} {
		if strings.Contains(source, forbidden) {
			t.Fatal("unapproved helper mechanism", forbidden)
		}
	}
}

func TestNativeHelperPreservesCacheAndFinalDeadline(t *testing.T) {
	data, err := os.ReadFile("native-windows-hook-fixture.ps1")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, required := range []string{"'--go-cache',$GoCache", "'--go-mod-cache',$GoModCache", "if ($script:identityInitialized) { Assert-ControlIdentity }\n    Assert-Deadline"} {
		if !strings.Contains(source, required) {
			t.Fatal("missing cache/deadline handoff", required)
		}
	}
	durableWriter := sourceSection(t, source, "function Write-ExclusiveJsonDurable", "function Assert-ControlIdentity")
	if !strings.Contains(durableWriter, "$stream.Flush($true)") || !strings.Contains(durableWriter, "finally { $stream.Dispose() }") {
		t.Fatal("durable JSON writer must flush to disk and dispose its stream in finally")
	}
	cleanup := sourceSection(t, source, "function Remove-Allocation", "")
	if !strings.Contains(cleanup, "Assert-Deadline\n    Write-ExclusiveJsonDurable $receiptPath $receipt 'receipt'\n    Assert-Deadline") {
		t.Fatal("cleanup must check the deadline immediately before and after durable receipt writing")
	}
}

func TestNativeWindowsCIWrapperBindsOwnerEvidence(t *testing.T) {
	data, err := os.ReadFile("native-windows-ci-fixture.ps1")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, required := range []string{
		"ValidateSet('bootstrap','self-test','execute','finally','validate')",
		"lopper-native-windows-proof/v1",
		"Flush($true)",
		"native-cleanup-join/v1",
		"subject.stdout.raw",
		"subject.stderr.raw",
		"Assert-ReceiptReadyToPublish $candidate $stdoutBytes $stderrBytes",
		"$helper -Mode self-test",
		"allocator-self-test:",
		"Write-Receipt $script:Result",
		"$script:Result.self_test=[ordered]@{status='failed';failure=$_.Exception.Message}",
		"if ($receipt.test_status -ne 'not_launched' -and $receipt.cleanup_status -ne 'owner_completed')",
		"if ($Receipt.phase -cne 'subject-go' -or $Receipt.launch_status -cne 'launched'",
		"$Receipt.self_test.status -cne 'passed'",
		"$Receipt.self_test.failure) -or -not [string]::IsNullOrEmpty([string]$Receipt.failure)",
		"Write-OwnerTerminalReceipt $outTask.Result $errTask.Result $exit",
		"Owner join missing; subject exit remains unknown",
		"Owner raw test streams missing; subject exit remains unknown",
	} {
		if !strings.Contains(source, required) {
			t.Fatal("missing native wrapper contract", required)
		}
	}
	ownerTerminal := strings.Index(source, "Write-OwnerTerminalReceipt $outTask.Result $errTask.Result $exit")
	initializeCapture := strings.Index(source, "Initialize-BoundedOwnerCapture\n    if (-not $process.Start())")
	startOwner := strings.Index(source, "if (-not $process.Start())")
	joinRead := strings.Index(source, "$join=Get-Content -LiteralPath $joinPath -Raw | ConvertFrom-Json -AsHashtable")
	controlParse := strings.Index(source, "$controlMessage=$controlLines[0] | ConvertFrom-Json -AsHashtable")
	if initializeCapture < 0 || startOwner <= initializeCapture || ownerTerminal < 0 || joinRead <= ownerTerminal || controlParse <= joinRead {
		t.Fatal("capture setup must precede owner launch, and owner terminal receipt must precede join/control parsing")
	}
	for _, forbidden := range []string{"New-SmbShare", "Remove-SmbShare", "Start-Service", "Set-Service", "Remove-SmbShare -Force", "GITHUB_TOKEN", "GITHUB_OUTPUT"} {
		if strings.Contains(source, forbidden) {
			t.Fatal("wrapper must leave native allocation ownership in the fixed Go owner", forbidden)
		}
	}
}
