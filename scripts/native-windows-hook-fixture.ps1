# Fixed #1753 allocation helper. Authoring/parsing is not permission to invoke it.
[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidateSet('setup','finally','recovery','self-test')][string]$Mode,
    [Parameter(Mandatory)][string]$TrustedRoot,
    [Parameter(Mandatory)][string]$Manifest,
    [Parameter(Mandatory)][string]$ManifestSHA256,
    [Parameter(Mandatory)][string]$Verifier,
    [Parameter(Mandatory)][string]$VerifierSHA256,
    [Parameter(Mandatory)][string]$ExpectedSourceSHA,
    [Parameter(Mandatory)][string]$ExpectedPolicySHA256,
    [Parameter(Mandatory)][string]$AllocationRecord,
    [Parameter(Mandatory)][string]$GoCache,
    [Parameter(Mandatory)][string]$GoModCache,
    [Parameter(Mandatory)][string]$InvocationNonce,
    [Parameter(Mandatory)][string]$RunID,
    [Parameter(Mandatory)][string]$RunAttempt,
    [Parameter(Mandatory)][string]$SID,
    [Parameter(Mandatory)][string]$AllocationParent,
    [string]$AllocationSHA256,
    [string]$JoinRecord,
    [string]$JoinRecordSHA256,
    [string]$DeadlineUTC
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$script:identityInitialized=$false
$script:SelfTestActive=$false
$script:SelfTestAdapter=$null

function Invoke-TestFault([string]$Name) {
    if ($script:SelfTestActive) {
        $script:SelfTestAdapter.trace.Add($Name)
        $stateMatches=([string]::IsNullOrEmpty([string]$script:SelfTestAdapter.faultState) -or $null -eq $script:allocation -or $script:allocation.state -ceq $script:SelfTestAdapter.faultState)
        if ($script:SelfTestAdapter.fault -ceq $Name -and $stateMatches) {
            $state=if ($null -eq $script:allocation) {'none'} else {[string]$script:allocation.state}
            $script:SelfTestAdapter.trace.Add("fault.triggered:$Name@$state")
            throw "SELFTEST_EXPECTED:$Name"
        }
    }
}
function Get-DirectoryIdentity([string]$Path) {
    if ($script:SelfTestActive) { $script:SelfTestAdapter.trace.Add('directory.identity'); if ($script:SelfTestAdapter.identityOverride) { return 'self-test:replacement' }; return "self-test:$Path" }
    return [NativeCleanupDirectoryIdentity]::Read($Path)
}
function Get-ObservedServerStatus {
    if ($script:SelfTestActive) { return 'Running' }
    return (Get-Service -Name LanmanServer -ErrorAction Stop).Status.ToString()
}
function Set-ObservedAllocationACL([string]$Path,$Security) {
    if ($script:SelfTestActive) { $script:SelfTestAdapter.trace.Add('acl.apply'); Invoke-TestFault 'acl.apply'; return }
    Set-Acl -LiteralPath $Path -AclObject $Security
}
function Read-ObservedMarker([string]$Path) {
    if ($script:SelfTestActive) { return $script:SelfTestAdapter.marker }
    return [IO.File]::ReadAllText($Path)
}
function Write-ObservedMarker([string]$Path,[string]$Text) {
    if ($script:SelfTestActive) { $script:SelfTestAdapter.trace.Add('marker.create'); Invoke-TestFault 'marker.create'; $script:SelfTestAdapter.marker=$Text }
    [IO.File]::WriteAllText($Path,$Text,[Text.UTF8Encoding]::new($false))
}
function Get-ObservedShare {
    if ($script:SelfTestActive) {
        $script:SelfTestAdapter.trace.Add('share.query')
        $script:SelfTestAdapter.shareQueryCount++
        if ($script:SelfTestAdapter.tamperOnShareQuery -eq $script:SelfTestAdapter.shareQueryCount) { $script:SelfTestAdapter.share.Path=Join-Path $script:allocation.path 'substituted' }
        if ($script:SelfTestAdapter.fault -ceq 'share.query') { throw 'SELFTEST_EXPECTED:share.query' }
        if ($script:SelfTestAdapter.shareAbsent) { return $null }
        return $script:SelfTestAdapter.share
    }
    return (Get-SmbShare -Name $script:allocation.share -ErrorAction Stop)
}
function Get-ObservedShareAccess {
    if ($script:SelfTestActive) {
        $script:SelfTestAdapter.trace.Add('share-acl.query')
        if ($script:SelfTestAdapter.fault -ceq 'share-acl.query') { throw 'SELFTEST_EXPECTED:share-acl.query' }
        return @($script:SelfTestAdapter.shareAccess)
    }
    return @(Get-SmbShareAccess -Name $script:allocation.share -ErrorAction Stop)
}
function Get-ObservedOpenFiles {
    if ($script:SelfTestActive) {
        $script:SelfTestAdapter.trace.Add('open-files.query')
        if ($script:SelfTestAdapter.fault -ceq 'open-files.query') { throw 'SELFTEST_EXPECTED:open-files.query' }
        return @($script:SelfTestAdapter.openFiles)
    }
    return @(Get-SmbOpenFile -IncludeHidden -ErrorAction Stop)
}
function Get-ObservedConnections {
    if ($script:SelfTestActive) {
        $script:SelfTestAdapter.trace.Add('connections.query')
        if ($script:SelfTestAdapter.fault -ceq 'connections.query') { throw 'SELFTEST_EXPECTED:connections.query' }
        return @($script:SelfTestAdapter.connections)
    }
    return @(Get-SmbConnection -ErrorAction Stop)
}
function New-ObservedShare([string]$Name,[string]$Path,[string]$Account) {
    if ($script:SelfTestActive) {
        Invoke-TestFault 'share.create'
        $script:SelfTestAdapter.shareAbsent=$false
        $script:SelfTestAdapter.share=[pscustomobject]@{Name=$Name;Path=$Path;Temporary=$true;CurrentUsers=0}
        $script:SelfTestAdapter.shareAccess=@([pscustomobject]@{AccountName=$Account;AccessControlType='Allow';AccessRight='Full'})
        return
    }
    New-SmbShare -Name $Name -Path $Path -Temporary -FullAccess $Account | Out-Null
}
function Remove-ObservedShare([string]$Name) {
    if ($script:SelfTestActive) {
        Invoke-TestFault 'share.remove'
        $script:SelfTestAdapter.shareAbsent=$true
        $script:SelfTestAdapter.share=$null
        return
    }
    Remove-SmbShare -Name $Name -Confirm:$false
}
function Remove-ObservedMarker([string]$Path) {
    Invoke-TestFault 'marker.delete'
    [IO.File]::Delete($Path)
}
function Remove-ObservedAllocationDirectory([string]$Path) {
    Invoke-TestFault 'directory.delete'
    if ($script:SelfTestActive -and $script:SelfTestAdapter.injectChildBeforeDelete) {
        $script:SelfTestAdapter.trace.Add('directory.delete.race-injected')
        [IO.File]::WriteAllText((Join-Path $Path 'late-child.txt'),'late child')
        try { [IO.Directory]::Delete($Path,$false) } catch { throw 'SELFTEST_EXPECTED:directory.delete.nonempty' }
        throw 'Self-test nonrecursive deletion unexpectedly succeeded'
    }
    [IO.Directory]::Delete($Path,$false)
}
function Get-ObservedChildren([string]$Root) {
    if ($script:SelfTestActive) {
        $script:SelfTestAdapter.trace.Add('children.query')
        if ($script:SelfTestAdapter.fault -ceq 'children.query') { throw 'SELFTEST_EXPECTED:children.query' }
        if ($script:SelfTestAdapter.childrenOverride) { return @($script:SelfTestAdapter.children) }
    }
    return @(Get-ChildItem -LiteralPath $Root -Force)
}

function Assert-Path([string]$Path, [bool]$Directory) {
    if (-not [IO.Path]::IsPathFullyQualified($Path) -or [IO.Path]::GetFullPath($Path) -cne $Path) { throw 'Noncanonical absolute path' }
    $item = Get-Item -LiteralPath $Path -Force
    if ([bool]$item.PSIsContainer -ne $Directory) { throw 'Artifact type mismatch' }
    $cursor=$Path
    while ($cursor -ne '') {
        $component=Get-Item -LiteralPath $cursor -Force
        if (($component.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'Reparse component' }
        $parent=[IO.Directory]::GetParent($cursor)
        if ($null -eq $parent) { break }
        $cursor=$parent.FullName
    }
}
function Get-Digest([string]$Path) { return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant() }
function Assert-Digest([string]$Path, [string]$Expected) {
    if ($Expected -cnotmatch '^[0-9a-f]{64}$' -or (Get-Digest $Path) -cne $Expected) { throw 'Independent digest mismatch' }
}
function Assert-Deadline {
    if ($Mode -notin @('setup','self-test') -and ([DateTimeOffset]::UtcNow -ge [DateTimeOffset]::Parse($DeadlineUTC))) { throw 'Single owner cleanup reserve expired; RETENTION' }
}
function Assert-Custody([bool]$WithAllocation, [bool]$WithJoin) {
    Assert-Deadline
    if ($Manifest -cne (Join-Path $TrustedRoot 'custody.json') -or $Verifier -cne (Join-Path $TrustedRoot 'bin/nativecleanupcheck.exe')) { throw 'Anchor layout mismatch' }
    Assert-Path $Manifest $false; Assert-Path $Verifier $false
    if ((Get-Item -LiteralPath $Manifest).Length -gt 1MB) { throw 'Manifest bound exceeded' }
    Assert-Digest $Manifest $ManifestSHA256; Assert-Digest $Verifier $VerifierSHA256
    $arguments = @('verify-custody','--trusted-root',$TrustedRoot,'--manifest',$Manifest,'--manifest-sha256',$ManifestSHA256,'--verifier',$Verifier,'--verifier-sha256',$VerifierSHA256,'--expected-source-sha',$ExpectedSourceSHA,'--expected-policy-sha256',$ExpectedPolicySHA256,'--invocation-nonce',$InvocationNonce,'--go-cache',$GoCache,'--go-mod-cache',$GoModCache)
    if ($WithAllocation) { $arguments += @('--allocation',$AllocationRecord,'--allocation-sha256',$AllocationSHA256,'--run-id',$RunID,'--run-attempt',$RunAttempt,'--sid',$SID) }
    if ($WithJoin) { $arguments += @('--join-record',$JoinRecord,'--join-record-sha256',$JoinRecordSHA256) }
    if ($script:SelfTestActive) {
        $script:SelfTestAdapter.trace.Add('custody.verify')
        if (-not [string]::IsNullOrEmpty([string]$script:SelfTestAdapter.verifierReject)) { throw "SELFTEST_EXPECTED:verifier.$($script:SelfTestAdapter.verifierReject)" }
        Invoke-TestFault 'custody.verify'
    } else {
        & $Verifier @arguments
        if ($LASTEXITCODE -ne 0) { throw 'Fixed custody/allocation/original-join verifier rejected operation' }
    }
    Assert-Deadline
    if ($script:identityInitialized) { Assert-ControlIdentity }
    Assert-Deadline
}
function Save-Allocation {
    # Exact fixed path, exclusive scratch file, same-directory atomic replacement.
    $scratch = "$AllocationRecord.partial"
    $bytes = [Text.UTF8Encoding]::new($false).GetBytes(($script:allocation | ConvertTo-Json -Depth 8 -Compress))
    Invoke-TestFault 'journal.create'
    $stream = [IO.File]::Open($scratch,[IO.FileMode]::CreateNew,[IO.FileAccess]::Write,[IO.FileShare]::None)
    try { Invoke-TestFault 'journal.write'; $stream.Write($bytes,0,$bytes.Length); Invoke-TestFault 'journal.flush'; $stream.Flush($true) } finally { $stream.Dispose() }
    Invoke-TestFault 'journal.move'
    [IO.File]::Move($scratch,$AllocationRecord,$true)
    $script:AllocationSHA256 = Get-Digest $AllocationRecord
}
function Write-ExclusiveJsonDurable([string]$Path,$Value,[string]$FaultPrefix) {
    Invoke-TestFault "$FaultPrefix.create"
    $stream=[IO.File]::Open($Path,[IO.FileMode]::CreateNew,[IO.FileAccess]::Write,[IO.FileShare]::None)
    try {
        $bytes=[Text.UTF8Encoding]::new($false).GetBytes(($Value | ConvertTo-Json -Depth 8 -Compress))
        Invoke-TestFault "$FaultPrefix.write"
        $stream.Write($bytes,0,$bytes.Length)
        Invoke-TestFault "$FaultPrefix.flush"
        $stream.Flush($true)
    } finally { $stream.Dispose() }
}
function Assert-ControlIdentity {
    if ($script:SelfTestActive) { $script:SelfTestAdapter.trace.Add('control.identity'); Invoke-TestFault 'control.identity'; return }
    $custody=Get-Content -LiteralPath $Manifest -Raw | ConvertFrom-Json
    $control=Join-Path $TrustedRoot 'control'
    Assert-Path $control $true
    if ((Get-DirectoryIdentity $control) -cne $custody.control_volume_file_id) { throw 'Control directory replacement' }
    $security=Get-Acl -LiteralPath $control
    $sections=[Security.AccessControl.AccessControlSections]::Owner -bor [Security.AccessControl.AccessControlSections]::Group -bor [Security.AccessControl.AccessControlSections]::Access
    if ((Get-TextDigest ($security.GetSecurityDescriptorSddlForm($sections))) -cne $custody.control_acl_sha256) { throw 'Control descriptor drift' }
}
function Assert-NTFS {
    Invoke-TestFault 'ntfs.query'
    if ($script:SelfTestActive) { $script:SelfTestAdapter.trace.Add('ntfs.query'); return $script:SelfTestAdapter.ntfsDigest }
    $security = Get-Acl -LiteralPath $script:allocation.path
    $owner = $security.GetOwner([Security.Principal.SecurityIdentifier]).Value
    $rules = @($security.GetAccessRules($true,$true,[Security.Principal.SecurityIdentifier]))
    if ($owner -cne $SID -or -not $security.AreAccessRulesProtected -or -not $security.AreAccessRulesCanonical -or $rules.Count -ne 1) { throw 'Protected current-SID-only NTFS premise failed' }
    $rule = $rules[0]
    $inherit = [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit
    if ($rule.IdentityReference.Value -cne $SID -or $rule.IsInherited -or $rule.AccessControlType -ne 'Allow' -or $rule.FileSystemRights -ne 'FullControl' -or $rule.InheritanceFlags -ne $inherit -or $rule.PropagationFlags -ne 'None') { throw 'Unexpected NTFS ACE' }
    return Get-TextDigest ($security.GetSecurityDescriptorSddlForm([Security.AccessControl.AccessControlSections]::All))
}
function Get-TextDigest([string]$Text) {
    return [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData([Text.Encoding]::UTF8.GetBytes($Text))).ToLowerInvariant()
}
function Assert-Share {
    $share = Get-ObservedShare
    if ($share.Name -cne $script:allocation.share -or $share.Path -cne $script:allocation.path -or -not $share.Temporary) { throw 'Share replacement/path/temporary mismatch' }
    $rules = @(Get-ObservedShareAccess)
    if ($rules.Count -ne 1) { throw 'Unexpected SMB ACE inventory' }
    $resolved = [Security.Principal.NTAccount]::new($rules[0].AccountName).Translate([Security.Principal.SecurityIdentifier]).Value
    if ($resolved -cne $SID -or $rules[0].AccessControlType -ne 'Allow' -or $rules[0].AccessRight -ne 'Full') { throw 'SMB current-SID-only premise failed' }
    return Get-TextDigest "$SID|Allow|Full"
}
function Test-PathWithinRoot([string]$Candidate,[string]$Root) {
    if ([string]::IsNullOrWhiteSpace($Candidate) -or -not [IO.Path]::IsPathFullyQualified($Candidate)) { throw 'SMB open-file path is not a known absolute path; RETENTION' }
    $candidateFull=[IO.Path]::GetFullPath($Candidate)
    $rootFull=[IO.Path]::GetFullPath($Root).TrimEnd([IO.Path]::DirectorySeparatorChar,[IO.Path]::AltDirectorySeparatorChar)
    $prefix=$rootFull+[IO.Path]::DirectorySeparatorChar
    return $candidateFull.StartsWith($prefix,[StringComparison]::OrdinalIgnoreCase) -or $candidateFull.Equals($rootFull,[StringComparison]::OrdinalIgnoreCase)
}
function Assert-NoObservedUse {
    # These are deliberately repeated immediately before Remove-SmbShare. SMB
    # queries are snapshots; failure or ambiguity retains the allocation.
    $share=Get-ObservedShare
    if ($null -eq $share -or $share.Name -cne $script:allocation.share -or $share.Path -cne $script:allocation.path -or -not $share.Temporary) { throw 'Share identity/Temporary observation failed; RETENTION' }
    $currentUsers=$share.PSObject.Properties['CurrentUsers']
    if ($null -eq $currentUsers -or $null -eq $currentUsers.Value -or [int]$currentUsers.Value -ne 0) { throw 'Share has current users or unknown user count; RETENTION' }
    $rules=@(Get-ObservedShareAccess)
    if ($rules.Count -ne 1) { throw 'Share ACL query ambiguous; RETENTION' }
    $resolved=[Security.Principal.NTAccount]::new($rules[0].AccountName).Translate([Security.Principal.SecurityIdentifier]).Value
    if ($resolved -cne $SID -or $rules[0].AccessControlType -ne 'Allow' -or $rules[0].AccessRight -ne 'Full' -or (Get-TextDigest "$SID|Allow|Full") -cne $script:allocation.share_acl_sha256) { throw 'Share ACL no longer current-SID-only; RETENTION' }
    Assert-NoObservedSessions
}
function Assert-NoObservedSessions {
    foreach ($open in (Get-ObservedOpenFiles)) {
        if ($null -eq $open.Path) { throw 'SMB open-file observation has no path; RETENTION' }
        if (Test-PathWithinRoot ([string]$open.Path) $script:allocation.path) { throw 'SMB open file remains under allocation; RETENTION' }
    }
    foreach ($connection in (Get-ObservedConnections)) {
        if ($null -eq $connection.ShareName) { throw 'SMB connection observation is malformed; RETENTION' }
        if ([string]$connection.ShareName -ceq $script:allocation.share) { throw 'Client connection remains for allocation share; RETENTION' }
    }
}
function Assert-DirectoryIdentity {
    Invoke-TestFault 'directory.identity'
    Assert-Path $script:allocation.path $true
    if ((Get-DirectoryIdentity $script:allocation.path) -cne $script:allocation.volume_file_id) { throw 'Opened volume/file identity mismatch' }
    if ((Assert-NTFS) -cne $script:allocation.ntfs_acl_sha256) { throw 'NTFS descriptor drift' }
    $sentinel = Join-Path $script:allocation.path '.allocation-nonce'
    Assert-Path $sentinel $false; Assert-Digest $sentinel $script:allocation.sentinel_sha256
}
function Assert-Allocation {
    Assert-Custody $true ($Mode -ne 'setup')
    if ($script:allocation.schema -cne 'native-cleanup-allocation/v1') { throw 'Allocation schema mismatch; RETENTION' }
    if ($script:allocation.sid -cne $SID -or $script:allocation.path -cne (Join-Path $AllocationParent "lopper-$($script:allocation.allocation_nonce)")) { throw 'Allocation owner/path mismatch' }
    if ($script:allocation.state -notin @('ready','share-pending')) { throw 'Partial allocation cannot prove exact descriptors; RETENTION' }
}
function Assert-NoChildReparse([string]$Root) {
    $pending = [Collections.Generic.Stack[string]]::new(); $pending.Push($Root)
    while ($pending.Count -gt 0) {
        foreach ($item in Get-ObservedChildren $pending.Pop()) {
            if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'Subtree reparse substitution; RETENTION' }
            if ($item.PSIsContainer) { $pending.Push($item.FullName) }
        }
    }
}
function Assert-ExpectedChildren([string]$Root) {
    Assert-NoChildReparse $Root
    $children=@(Get-ObservedChildren $Root)
    if ($children.Count -ne 1 -or $children[0].Name -cne '.allocation-nonce' -or $children[0].PSIsContainer) { throw 'Unexpected allocation child; RETENTION' }
    $marker=Join-Path $Root '.allocation-nonce'
    if ($children[0].FullName -cne $marker) { throw 'Allocation marker path mismatch; RETENTION' }
    Assert-Path $marker $false
    Assert-Digest $marker $script:allocation.sentinel_sha256
}
function Assert-SelfTestTrace([string]$Name,[string[]]$Required,[string[]]$Forbidden) {
    $cursor=-1
    foreach($operation in $Required) {
        $found=-1
        for($index=$cursor+1;$index -lt $script:SelfTestAdapter.trace.Count;$index++) {
            if ($script:SelfTestAdapter.trace[$index] -ceq $operation) { $found=$index;break }
        }
        if ($found -lt 0) { throw "Self-test $Name did not reach $operation in order; trace=$($script:SelfTestAdapter.trace -join ',')" }
        $cursor=$found
    }
    foreach($operation in $Forbidden) {
        if ($script:SelfTestAdapter.trace.Contains($operation)) { throw "Self-test $Name reached forbidden later operation $operation" }
    }
}
function Assert-SelfTestNoLaterTrace([string]$Name,[string]$StopOperation,[string[]]$Forbidden) {
    $stop=$script:SelfTestAdapter.trace.LastIndexOf($StopOperation)
    if ($stop -lt 0) { throw "Self-test $Name missing stop operation $StopOperation" }
    for($index=$stop+1;$index -lt $script:SelfTestAdapter.trace.Count;$index++) {
        if ($Forbidden -ccontains $script:SelfTestAdapter.trace[$index]) { throw "Self-test $Name reached forbidden operation after $StopOperation`: $($script:SelfTestAdapter.trace[$index])" }
    }
}
function Reset-SelfTestObservations([string]$Account,[string]$OutsidePath) {
    $script:SelfTestAdapter.trace.Clear();$script:SelfTestAdapter.fault='';$script:SelfTestAdapter.faultState='';$script:SelfTestAdapter.verifierReject=''
    $script:SelfTestAdapter.shareQueryCount=0;$script:SelfTestAdapter.tamperOnShareQuery=0;$script:SelfTestAdapter.identityOverride=$false;$script:SelfTestAdapter.injectChildBeforeDelete=$false
    $script:SelfTestAdapter.shareAbsent=$false
    $script:SelfTestAdapter.share=[pscustomobject]@{Name=$script:allocation.share;Path=$script:allocation.path;Temporary=$true;CurrentUsers=0}
    $script:SelfTestAdapter.shareAccess=@([pscustomobject]@{AccountName=$Account;AccessControlType='Allow';AccessRight='Full'})
    $script:SelfTestAdapter.openFiles=@([pscustomobject]@{Path=$OutsidePath});$script:SelfTestAdapter.connections=@()
    $script:SelfTestAdapter.childrenOverride=$false;$script:SelfTestAdapter.ntfsDigest='self-test-ntfs-acl-digest'
}
function Set-SelfTestSetupFault([string]$Mutation) {
    if ($Mutation -like 'setup.*') { $script:SelfTestAdapter.fault=$Mutation.Substring(6) }
    elseif ($Mutation -like 'journal.*') { $parts=$Mutation.Split('@');$script:SelfTestAdapter.fault=$parts[0];$script:SelfTestAdapter.faultState=$parts[1] }
}
function Get-SelfTestSetupTrace([string]$Mutation) {
    $required=@();$forbidden=@('directory.create','acl.apply','marker.create','share.create')
    if ($Mutation -notin @('allocation-record-collision')) { $required=@('custody.verify') }
    switch ($script:SelfTestAdapter.fault) {
        'directory.create' { $required+=@('directory.create');$forbidden=@('acl.apply','marker.create','share.create') }
        'acl.apply' { $required+=@('directory.create','acl.apply');$forbidden=@('marker.create','share.create') }
        'marker.create' { $required+=@('directory.create','acl.apply','marker.create');$forbidden=@('share.create') }
        'share.create' { $required+=@('directory.create','acl.apply','marker.create','share.create','fault.triggered:share.create@share-pending');$forbidden=@() }
    }
    if ($Mutation -like 'journal.*') {
        $parts=$Mutation.Split('@');$operation=$parts[0];$state=$parts[1]
        switch ($state) {
            'directory-pending' { $required+=@("fault.triggered:$operation@directory-pending");$forbidden=@('directory.create','acl.apply','marker.create','share.create') }
            'acl-pending' { $required+=@('directory.create',"fault.triggered:$operation@acl-pending");$forbidden=@('acl.apply','marker.create','share.create') }
            'share-pending' { $required+=@('directory.create','acl.apply','marker.create',"fault.triggered:$operation@share-pending");$forbidden=@('share.create') }
            'ready' { $required+=@('directory.create','acl.apply','marker.create','share.create',"fault.triggered:$operation@ready");$forbidden=@('receipt.create') }
            default { $required+=@("fault.triggered:$operation@registered");$forbidden=@('directory.create','acl.apply','marker.create','share.create') }
        }
    }
    [pscustomobject]@{Required=$required;Forbidden=$forbidden}
}
function Assert-SelfTestJournalScratch([string]$Name,[string]$Operation,[string]$State) {
    $previousState=@{registered=$null;'directory-pending'='registered';'acl-pending'='directory-pending';'share-pending'='acl-pending';ready='share-pending'}[$State]
    if ($previousState -and (Test-Path -LiteralPath $AllocationRecord -PathType Leaf)) {
        $record=Get-Content -LiteralPath $AllocationRecord -Raw | ConvertFrom-Json
        if ($record.state -cne $previousState) { throw "Self-test $Name published wrong prior journal state $($record.state)" }
    }
    $scratchRetained=Test-Path -LiteralPath "$AllocationRecord.partial" -PathType Leaf
    if (($Operation -in @('journal.write','journal.flush','journal.move')) -and -not $scratchRetained) { throw "Self-test $Name did not retain the failed scratch journal" }
    if (($Operation -eq 'journal.create') -and $scratchRetained) { throw "Self-test $Name unexpectedly created scratch for failed journal creation" }
}
function Assert-SelfTestSetupNoLaterTrace([string]$Name,[string]$Mutation) {
    if ($Mutation -like 'journal.*') {
        $parts=$Mutation.Split('@');$operation=$parts[0];$state=$parts[1]
        $later=switch ($state) {
            'acl-pending' { @('acl.apply','marker.create','share.create') }
            'share-pending' { @('share.create') }
            'ready' { @('custody.verify','directory.identity','ntfs.query','share.query','share-acl.query') }
            default { @('directory.create','acl.apply','marker.create','share.create') }
        }
        Assert-SelfTestNoLaterTrace $Name "fault.triggered:$operation@$state" $later
    } elseif ($Mutation -like 'setup.*') {
        $after=switch ($script:SelfTestAdapter.fault) {
            'directory.create' { @('acl.apply','marker.create','share.create') }
            'acl.apply' { @('marker.create','share.create') }
            'marker.create' { @('share.create') }
            default { @('directory.create','acl.apply','marker.create','share.create') }
        }
        Assert-SelfTestNoLaterTrace $Name $script:SelfTestAdapter.fault $after
    }
}
function Get-SelfTestSetupExpectedState([string]$Mutation) {
    switch ($script:SelfTestAdapter.fault) {
        'directory.create' { return 'directory-pending' }
        { $_ -in @('acl.apply','marker.create') } { return 'acl-pending' }
        'share.create' { return 'share-pending' }
    }
    if ($Mutation -like 'journal.*') { return $Mutation.Split('@')[1] }
    return $null
}
function Assert-SelfTestSetupRetention([string]$Name,[string]$Mutation,$Allocation) {
    $expectedState=Get-SelfTestSetupExpectedState $Mutation
    $recordRetained=Test-Path -LiteralPath $AllocationRecord -PathType Leaf
    if ($expectedState -and $recordRetained) {
        $retained=Get-Content -LiteralPath $AllocationRecord -Raw | ConvertFrom-Json
        $stateBeforeFailure=@{registered='';'directory-pending'='registered';'acl-pending'='directory-pending';'share-pending'='acl-pending';ready='share-pending'}[$expectedState]
        $stateExpected=if ($Mutation -like 'journal.*') {$stateBeforeFailure} else {$expectedState}
        if (-not $stateExpected -or $retained.state -cne $stateExpected) { throw "Self-test $Name retained journal state $($retained.state), expected '$stateExpected'" }
    } elseif ($expectedState -eq 'registered' -and $recordRetained) { throw "Self-test $Name published the failed initial journal" }
    $dirExpected=$expectedState -in @('acl-pending','share-pending','ready') -or ($Mutation -eq 'setup.share.create')
    if ($Allocation -and ((Test-Path -LiteralPath $Allocation.path -PathType Container) -ne $dirExpected)) { throw "Self-test $Name retained unexpected directory state" }
    if ($script:SelfTestAdapter.shareAbsent -eq ($expectedState -eq 'ready')) { throw "Self-test $Name retained unexpected share state" }
}
function Assert-SelfTestSetupFaultCase([string]$Name,[string]$Mutation,[string]$SetupObserved) {
    $expectedSetup=switch ($Mutation) {
        'allocation-record-collision' { 'Allocation record already exists' }
        'allocation-path-collision' { 'Fresh allocation collision' }
        default { "SELFTEST_EXPECTED:$($script:SelfTestAdapter.fault)" }
    }
    if ($SetupObserved -cne $expectedSetup) { throw "Self-test $Name setup expected '$expectedSetup' but observed '$SetupObserved'" }
    $trace=Get-SelfTestSetupTrace $Mutation
    if ($Mutation -like 'journal.*') { $parts=$Mutation.Split('@');Assert-SelfTestJournalScratch $Name $parts[0] $parts[1] }
    Assert-SelfTestTrace $Name $trace.Required $trace.Forbidden
    Assert-SelfTestSetupNoLaterTrace $Name $Mutation
    $allocation=$null
    if ($script:allocation.path.StartsWith($AllocationParent+[IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase)) { $allocation=$script:allocation }
    Assert-SelfTestSetupRetention $Name $Mutation $allocation
    return $allocation
}
function Set-SelfTestJournalField([string]$Field,$Value) {
    $row=[IO.File]::ReadAllText($AllocationRecord)|ConvertFrom-Json -AsHashtable
    $row[$Field]=$Value
    [IO.File]::WriteAllText($AllocationRecord,($row|ConvertTo-Json -Compress),[Text.UTF8Encoding]::new($false))
}
function Set-SelfTestRemovalMutation([string]$Mutation,$Allocation,[string]$MarkerPath,[string]$Account) {
    switch ($Mutation) {
        { $_ -like 'verifier.*' } { $script:SelfTestAdapter.verifierReject=$Mutation.Substring(9) }
        'journal.sid' { Set-SelfTestJournalField 'sid' 'S-1-5-18' }
        'journal.path' { Set-SelfTestJournalField 'path' (Join-Path $AllocationParent 'outside') }
        'journal.schema' { Set-SelfTestJournalField 'schema' 'other' }
        'journal.state' { Set-SelfTestJournalField 'state' 'registered' }
        'share.name' { $script:SelfTestAdapter.share.Name='other-share' }
        'share.path' { $script:SelfTestAdapter.share.Path=Join-Path $Allocation.path 'substituted' }
        'share.temporary' { $script:SelfTestAdapter.share.Temporary=$false }
        'share.users' { $script:SelfTestAdapter.share.CurrentUsers=1 }
        'share.users-unknown' { $script:SelfTestAdapter.share.PSObject.Properties.Remove('CurrentUsers') }
        'share.query' { $script:SelfTestAdapter.fault='share.query' }
        'share.acl-query' { $script:SelfTestAdapter.fault='share-acl.query' }
        'share.acl-drift' { $script:SelfTestAdapter.shareAccess=@([pscustomobject]@{AccountName=$Account;AccessControlType='Deny';AccessRight='Full'}) }
        'ntfs.drift' { $script:SelfTestAdapter.ntfsDigest='changed-ntfs-digest' }
        'volume.identity' { $script:SelfTestAdapter.identityOverride=$true }
        'marker.digest' { [IO.File]::WriteAllText($MarkerPath,'tampered',[Text.UTF8Encoding]::new($false)) }
        'open-file' { $script:SelfTestAdapter.openFiles=@([pscustomobject]@{Path=$MarkerPath}) }
        'open-file-malformed' { $script:SelfTestAdapter.openFiles=@([pscustomobject]@{Path=$null}) }
        'open-file-query' { $script:SelfTestAdapter.fault='open-files.query' }
        'connection' { $script:SelfTestAdapter.connections=@([pscustomobject]@{ShareName=$Allocation.share}) }
        'connection-malformed' { $script:SelfTestAdapter.connections=@([pscustomobject]@{ShareName=$null}) }
        'connection-query' { $script:SelfTestAdapter.fault='connections.query' }
        'share.tamper-final' { $script:SelfTestAdapter.tamperOnShareQuery=4 }
        'unexpected-child' { [IO.File]::WriteAllText((Join-Path $Allocation.path 'unexpected.txt'),'unexpected') }
        'reparse-child' { $script:SelfTestAdapter.childrenOverride=$true;$script:SelfTestAdapter.children=@([pscustomobject]@{Name='.allocation-nonce';FullName=$MarkerPath;PSIsContainer=$false;Attributes=[IO.FileAttributes]::ReparsePoint}) }
        'marker.path' { $script:SelfTestAdapter.childrenOverride=$true;$script:SelfTestAdapter.children=@([pscustomobject]@{Name='.allocation-nonce';FullName=(Join-Path $Allocation.path 'other-marker');PSIsContainer=$false;Attributes=[IO.FileAttributes]::Normal}) }
        'share.remove' { $script:SelfTestAdapter.fault='share.remove' }
        'marker.delete' { $script:SelfTestAdapter.fault='marker.delete' }
        'late-child' { $script:SelfTestAdapter.injectChildBeforeDelete=$true }
        { $_ -in @('receipt.create','receipt.write','receipt.flush') } { $script:SelfTestAdapter.fault=$Mutation }
    }
}
function Get-SelfTestRemovalTrace([string]$Mutation) {
    $verifiedCleanup=@('custody.verify','share.query','directory.identity','ntfs.query','children.query','share.query','share-acl.query','share.query','share-acl.query','open-files.query','connections.query','share.query','share-acl.query','open-files.query','connections.query')
    $required=@('custody.verify');$forbidden=@('share.remove','marker.delete','directory.delete','receipt.create','receipt.write','receipt.flush')
    switch -Wildcard ($Mutation) {
        'join.missing' { $required=@();$forbidden=@('custody.verify','share.query','share.remove','marker.delete','directory.delete','receipt.create') }
        'join.malformed' { $required=@();$forbidden=@('share.query','share.remove','marker.delete','directory.delete','receipt.create') }
        'verifier.*' { $forbidden=@('share.query','share.remove','marker.delete','directory.delete','receipt.create') }
        'share.remove' { $required=@($verifiedCleanup)+@('share.remove','fault.triggered:share.remove@ready');$forbidden=@('marker.delete','directory.delete','receipt.create') }
        'marker.delete' { $required=@($verifiedCleanup)+@('share.remove','custody.verify','share.query','directory.identity','children.query','marker.delete','fault.triggered:marker.delete@ready');$forbidden=@('directory.delete','receipt.create') }
        'receipt.*' { $required=@($verifiedCleanup)+@('share.remove','custody.verify','share.query','directory.identity','children.query','marker.delete','directory.delete',"fault.triggered:$Mutation@ready");$forbidden=@() }
        'late-child' { $required=@('share.remove','marker.delete','directory.delete','directory.delete.race-injected');$forbidden=@('receipt.create') }
    }
    [pscustomobject]@{Required=$required;Forbidden=$forbidden}
}
function Assert-SelfTestRemovalNoLaterTrace([string]$Name,[string]$Mutation) {
    if ($Mutation -like 'verifier.*') { Assert-SelfTestNoLaterTrace $Name 'custody.verify' @('share.query','share-acl.query','open-files.query','connections.query','share.remove','marker.delete','directory.delete','receipt.create') }
    $failureStop = switch -Regex ($Mutation) {
        '^journal\.(sid|path|schema|state)$' { 'custody.verify'; break }
        '^share\.(name|path|temporary|users|users-unknown|query|tamper-final)$' { 'share.query'; break }
        '^share\.acl-(query|drift)$' { 'share-acl.query'; break }
        '^ntfs\.drift$' { 'ntfs.query'; break }
        '^volume\.identity$' { 'directory.identity'; break }
        '^marker\.digest$' { 'ntfs.query'; break }
        '^marker\.path$|^unexpected-child$|^reparse-child$' { 'children.query'; break }
        '^open-file($|-malformed$|-query$)' { 'open-files.query'; break }
        '^connection($|-malformed$|-query$)' { 'connections.query'; break }
        '^share\.remove$' { 'fault.triggered:share.remove@ready'; break }
        '^marker\.delete$' { 'fault.triggered:marker.delete@ready'; break }
        '^receipt\.(create|write|flush)$' { "fault.triggered:$Mutation@ready"; break }
        '^late-child$' { 'directory.delete.race-injected'; break }
    }
    if ($failureStop) { Assert-SelfTestNoLaterTrace $Name $failureStop @('share.remove','marker.delete','directory.delete','receipt.create') }
}
function Get-SelfTestRetentionFailure([string]$Name,[string]$Mutation) {
    switch -Wildcard ($Mutation) {
        'share.remove' { return 'Self-test share-remove failure did not retain share and allocation' }
        'marker.delete' { return 'Self-test marker-delete failure did not retain post-share allocation state' }
        'late-child' { return 'Self-test late child race did not retain ambiguous state' }
        'receipt.*' { return "Self-test $Name receipt failure did not retain terminal cleanup state" }
    }
    return "Self-test $Name did not retain the ambiguous allocation"
}
function Assert-SelfTestRemovalRetention([string]$Name,[string]$Mutation,$Allocation,[string]$Control) {
    # Failures before share removal retain the share; later failures retain the
    # journal and whatever local state the failed step had not yet removed.
    $receiptFailure=$Mutation -in @('receipt.create','receipt.write','receipt.flush')
    $shareRemoved=$receiptFailure -or $Mutation -in @('late-child','marker.delete')
    $directoryRetained=Test-Path -LiteralPath $Allocation.path -PathType Container
    $pathPresent=Test-Path -LiteralPath $Allocation.path
    $receiptPath=Join-Path $Control 'teardown-finally.json'
    $failure=Get-SelfTestRetentionFailure $Name $Mutation
    if ($script:SelfTestAdapter.shareAbsent -ne $shareRemoved -or -not (Test-Path -LiteralPath $AllocationRecord -PathType Leaf)) { throw $failure }
    if (($receiptFailure -and $pathPresent) -or (-not $receiptFailure -and -not $directoryRetained)) { throw $failure }
    switch ($Mutation) {
        'marker.delete' { if (-not (Test-Path -LiteralPath (Join-Path $Allocation.path '.allocation-nonce') -PathType Leaf) -or (Test-Path -LiteralPath $receiptPath -PathType Leaf)) { throw $failure } }
        'late-child' { if (-not (Test-Path -LiteralPath (Join-Path $Allocation.path 'late-child.txt') -PathType Leaf)) { throw $failure } }
    }
    if ($receiptFailure -and (($Mutation -eq 'receipt.create') -eq (Test-Path -LiteralPath $receiptPath -PathType Leaf))) { throw "Self-test $Name receipt artifact state is inconsistent with failure point" }
}
function Invoke-SelfTestRemovalCase([string]$Name,[string]$Mutation,[string]$Expected,[string]$SetupObserved,[string]$Account,[string]$Control) {
    if ($SetupObserved) { throw "Self-test $Name could not prepare a clean allocation: $SetupObserved" }
    # Remove-Allocation reads these through PowerShell dynamic scope.
    $AllocationSHA256=$script:AllocationSHA256
    $joined=[ordered]@{schema='native-cleanup-join/v1';cleanup_started=[DateTimeOffset]::UtcNow.ToString('o');owner_pid=0;run_id=$RunID;run_attempt=$RunAttempt}
    [IO.File]::WriteAllText($JoinRecord,($joined|ConvertTo-Json -Compress),[Text.UTF8Encoding]::new($false))
    $JoinRecordSHA256=Get-Digest $JoinRecord;$DeadlineUTC=([DateTimeOffset]::Parse($joined.cleanup_started).AddSeconds(30)).ToString('o')
    $allocation=$script:allocation
    $markerPath=Join-Path $allocation.path '.allocation-nonce'
    Reset-SelfTestObservations $Account ([IO.Path]::GetTempPath())
    $script:SelfTestAdapter.trace.Clear()
    Set-SelfTestRemovalMutation $Mutation $allocation $markerPath $Account
    if ($Mutation -eq 'join.missing') { $JoinRecord='' }
    if ($Mutation -eq 'join.malformed') { [IO.File]::WriteAllText($JoinRecord,'{',[Text.UTF8Encoding]::new($false));$JoinRecordSHA256=Get-Digest $JoinRecord }
    $Mode='finally';$observed='';try { Remove-Allocation } catch { $observed=$_.Exception.Message }
    if ($observed -cne $Expected) { throw "Self-test $Name expected '$Expected' but observed '$observed'" }
    $trace=Get-SelfTestRemovalTrace $Mutation
    Assert-SelfTestTrace $Name $trace.Required $trace.Forbidden
    Assert-SelfTestRemovalNoLaterTrace $Name $Mutation
    Assert-SelfTestRemovalRetention $Name $Mutation $allocation $Control
    $script:SelfTestAdapter.fault='';$script:SelfTestAdapter.verifierReject='';$script:SelfTestAdapter.injectChildBeforeDelete=$false
    return $allocation
}
function Remove-SelfTestTransitionCase([string]$Name,$Allocation,[string]$CollisionPath,[string[]]$Files,[string[]]$Directories) {
    foreach($path in $Files) { if ($path -and (Test-Path -LiteralPath $path -PathType Leaf)) { [IO.File]::Delete($path) } }
    if ($Allocation -and $Allocation.path -and (Test-Path -LiteralPath $Allocation.path -PathType Container)) {
        foreach($child in (Get-ChildItem -LiteralPath $Allocation.path -Force)) { if ($child.PSIsContainer) { throw "Self-test $Name left an unexpected directory" };[IO.File]::Delete($child.FullName) }
        [IO.Directory]::Delete($Allocation.path,$false)
    }
    if ($CollisionPath -and (Test-Path -LiteralPath $CollisionPath -PathType Container)) { [IO.Directory]::Delete($CollisionPath,$false) }
    foreach($directory in $Directories) { if (Test-Path -LiteralPath $directory -PathType Container) { [IO.Directory]::Delete($directory,$false) } }
}
function Invoke-SelfTestTransitionCase([string]$Name,[string]$Mutation,[string]$Expected) {
    # Setup-Allocation and Remove-Allocation read these case-local values
    # through PowerShell dynamic scope in place of the script parameters.
    $caseRoot=Join-Path $script:SelfTestAdapter.scratch ("transition-$Name")
    $TrustedRoot=Join-Path $caseRoot 'trusted';$control=Join-Path $TrustedRoot 'control';$bin=Join-Path $TrustedRoot 'bin'
    $AllocationParent=Join-Path $caseRoot 'allocations';$Manifest=Join-Path $TrustedRoot 'custody.json';$Verifier=Join-Path $bin 'nativecleanupcheck.exe'
    $AllocationRecord=Join-Path $control 'allocation.json';$JoinRecord=Join-Path $control 'owner.join.json'
    $GoCache=Join-Path $caseRoot 'go-build';$GoModCache=Join-Path $caseRoot 'go-mod'
    $ManifestSHA256='';$VerifierSHA256='';$ExpectedSourceSHA='0'*40;$ExpectedPolicySHA256='0'*64
    $InvocationNonce=[Guid]::NewGuid().ToString('D');$RunID='self-test-run';$RunAttempt='1';$SID=[Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    $account=[Security.Principal.SecurityIdentifier]::new($SID).Translate([Security.Principal.NTAccount]).Value
    $Mode='setup';$AllocationSHA256='';$JoinRecordSHA256='';$DeadlineUTC='';$script:identityInitialized=$false
    foreach($directory in @($control,$bin,$AllocationParent,$GoCache,$GoModCache)) { [IO.Directory]::CreateDirectory($directory) | Out-Null }
    [IO.File]::WriteAllText($Verifier,'self-test-only verifier placeholder',[Text.UTF8Encoding]::new($false))
    $manifestObject=[ordered]@{policy=[ordered]@{environment=@("TEMP=$AllocationParent")};artifacts=@([ordered]@{path='bin/nativecleanupowner.exe';sha256=('0'*64)})}
    [IO.File]::WriteAllText($Manifest,($manifestObject|ConvertTo-Json -Depth 8 -Compress),[Text.UTF8Encoding]::new($false))
    $ManifestSHA256=Get-Digest $Manifest;$VerifierSHA256=Get-Digest $Verifier
    $script:SelfTestAdapter.trace.Clear();$script:SelfTestAdapter.fault='';$script:SelfTestAdapter.faultState='';$script:SelfTestAdapter.verifierReject=''
    $script:SelfTestAdapter.shareAbsent=$true;$script:SelfTestAdapter.share=$null;$script:SelfTestAdapter.shareAccess=@();$script:SelfTestAdapter.openFiles=@();$script:SelfTestAdapter.connections=@();$script:SelfTestAdapter.children=@();$script:SelfTestAdapter.marker='';$script:SelfTestAdapter.ntfsDigest='self-test-ntfs-acl-digest'
    $script:SelfTestAdapter.shareQueryCount=0;$script:SelfTestAdapter.tamperOnShareQuery=0;$script:SelfTestAdapter.identityOverride=$false;$script:SelfTestAdapter.injectChildBeforeDelete=$false;$script:SelfTestAdapter.childrenOverride=$false
    $setupFault=$Mutation -like 'setup.*' -or $Mutation -like 'journal.*@*' -or $Mutation -in @('allocation-record-collision','allocation-path-collision')
    if ($setupFault) { Set-SelfTestSetupFault $Mutation }
    $script:SelfTestAdapter.nonce='00000000-0000-0000-0000-000000000001'
    $collisionPath=Join-Path $AllocationParent "lopper-$($script:SelfTestAdapter.nonce)"
    if ($Mutation -eq 'allocation-record-collision') { [IO.File]::WriteAllText($AllocationRecord,'collision',[Text.UTF8Encoding]::new($false)) }
    if ($Mutation -eq 'allocation-path-collision') { [IO.Directory]::CreateDirectory($collisionPath) | Out-Null }
    $setupObserved=''
    try { Setup-Allocation } catch { $setupObserved=$_.Exception.Message }
    if ($setupFault) { $allocation=Assert-SelfTestSetupFaultCase $Name $Mutation $setupObserved }
    else { $allocation=Invoke-SelfTestRemovalCase $Name $Mutation $Expected $setupObserved $account $control }
    $script:SelfTestAdapter.fault=''
    $files=@($AllocationRecord,"$AllocationRecord.partial",$JoinRecord,(Join-Path $control 'teardown-finally.json'),$Manifest,$Verifier)
    $collision=if ($Mutation -eq 'allocation-path-collision') {$collisionPath} else {''}
    Remove-SelfTestTransitionCase $Name $allocation $collision $files @($control,$bin,$TrustedRoot,$AllocationParent,$GoCache,$GoModCache,$caseRoot)
    "transition-$Name"
}
function Invoke-SelfTestUseControls([Collections.Generic.List[string]]$Passed,[string]$Scratch,[string]$MarkerPath) {
    # The setup blocks read $Scratch and $MarkerPath through dynamic scope.
    $negativeCases=@(
        @{name='current-users'; setup={ $script:SelfTestAdapter.share.CurrentUsers=1 }; expected='Share has current users or unknown user count; RETENTION'},
        @{name='open-file'; setup={ $script:SelfTestAdapter.share.CurrentUsers=0;$script:SelfTestAdapter.openFiles=@([pscustomobject]@{Path=$MarkerPath}) }; expected='SMB open file remains under allocation; RETENTION'},
        @{name='client-connection'; setup={ $script:SelfTestAdapter.openFiles=@();$script:SelfTestAdapter.connections=@([pscustomobject]@{ShareName=$script:allocation.share}) }; expected='Client connection remains for allocation share; RETENTION'},
        @{name='query-error'; setup={ $script:SelfTestAdapter.connections=@();$script:SelfTestAdapter.fault='open-files.query' }; expected='SELFTEST_EXPECTED:open-files.query'},
        @{name='share-path'; setup={ $script:SelfTestAdapter.fault='';$script:SelfTestAdapter.share.Path=(Join-Path $Scratch 'other') }; expected='Share identity/Temporary observation failed; RETENTION'},
        @{name='share-temporary'; setup={ $script:SelfTestAdapter.share.Path=$Scratch;$script:SelfTestAdapter.share.Temporary=$false }; expected='Share identity/Temporary observation failed; RETENTION'},
        @{name='share-acl-query'; setup={ $script:SelfTestAdapter.share.Temporary=$true;$script:SelfTestAdapter.fault='share-acl.query' }; expected='SELFTEST_EXPECTED:share-acl.query'}
    )
    foreach($case in $negativeCases) {
        $script:SelfTestAdapter.share.CurrentUsers=0;$script:SelfTestAdapter.openFiles=@();$script:SelfTestAdapter.connections=@();$script:SelfTestAdapter.fault=''
        & $case.setup
        $observed=''
        try { Assert-NoObservedUse } catch { $observed=$_.Exception.Message }
        if ($observed -cne $case.expected) { throw "Self-test $($case.name) expected '$($case.expected)' but observed '$observed'" }
        if ($script:SelfTestAdapter.trace.Contains('share.remove')) { throw "Self-test $($case.name) reached share removal" }
        $Passed.Add($case.name)
    }
    $script:SelfTestAdapter.share.CurrentUsers=0;$script:SelfTestAdapter.fault=''
}
function Invoke-SelfTestChildControls([Collections.Generic.List[string]]$Passed,[string]$Scratch,[string]$MarkerPath,[string]$Marker) {
    $extra=[IO.Path]::Combine($Scratch,'unexpected-child.txt');[IO.File]::WriteAllText($extra,'unexpected')
    $script:SelfTestAdapter.children=@(Get-ChildItem -LiteralPath $Scratch -Force)
    $observed='';try { Assert-ExpectedChildren $Scratch } catch { $observed=$_.Exception.Message }
    if ($observed -cne 'Unexpected allocation child; RETENTION') { throw "Self-test unexpected child was not rejected precisely: $observed" }
    $Passed.Add('unexpected-ordinary-child-retained')
    [IO.File]::Delete($extra)

    $script:SelfTestAdapter.children=@([pscustomobject]@{Name='.allocation-nonce';FullName=$MarkerPath;PSIsContainer=$false;Attributes=[IO.FileAttributes]::ReparsePoint})
    $observed='';try { Assert-ExpectedChildren $Scratch } catch { $observed=$_.Exception.Message }
    if ($observed -cne 'Subtree reparse substitution; RETENTION') { throw "Self-test reparse child was not rejected precisely: $observed" }
    $Passed.Add('reparse-child-retained')
    $script:SelfTestAdapter.children=@(Get-Item -LiteralPath $MarkerPath -Force)
    [IO.File]::WriteAllText($MarkerPath,'tampered',[Text.UTF8Encoding]::new($false))
    $observed='';try { Assert-ExpectedChildren $Scratch } catch { $observed=$_.Exception.Message }
    if ($observed -cne 'Independent digest mismatch') { throw "Self-test marker tamper was not rejected precisely: $observed" }
    [IO.File]::WriteAllText($MarkerPath,$Marker,[Text.UTF8Encoding]::new($false))
    $Passed.Add('marker-tamper-retained')
}
function Invoke-SelfTestJournalFaults([Collections.Generic.List[string]]$Passed,[string]$Scratch,[string]$Nonce,[string]$TestSID,[string]$Marker) {
    $script:AllocationRecord=Join-Path $Scratch 'allocation.json'
    $script:allocation=[ordered]@{schema='native-cleanup-allocation/v1';allocation_nonce=$Nonce;run_id='self-test';state='ready'}
    Save-Allocation
    if (-not (Test-Path -LiteralPath $script:AllocationRecord -PathType Leaf) -or $script:AllocationSHA256 -cne (Get-Digest $script:AllocationRecord)) { throw 'Self-test positive journal was not durably bound' }
    [IO.File]::Delete($script:AllocationRecord)
    $script:SelfTestAdapter.share=[pscustomobject]@{Name="lopper-$Nonce";Path=$Scratch;Temporary=$true;CurrentUsers=0}
    $script:allocation=[ordered]@{share="lopper-$Nonce";path=$Scratch;temporary=$true;sid=$TestSID;share_acl_sha256=(Get-TextDigest "$TestSID|Allow|Full");sentinel_sha256=(Get-TextDigest $Marker);state='ready'}
    foreach($fault in @('journal.create','journal.write','journal.flush','journal.move')) {
        foreach($path in @($script:AllocationRecord,"$script:AllocationRecord.partial")) { if (Test-Path -LiteralPath $path -PathType Leaf) { [IO.File]::Delete($path) } }
        $script:SelfTestAdapter.fault=$fault;$observed=''
        try { Save-Allocation } catch { $observed=$_.Exception.Message }
        if ($observed -cne "SELFTEST_EXPECTED:$fault" -or (Test-Path -LiteralPath $script:AllocationRecord -PathType Leaf)) { throw "Self-test $fault did not fail before publishing a journal" }
        $Passed.Add($fault)
    }
}
function Invoke-SelfTestReceiptFaults([Collections.Generic.List[string]]$Passed,[string]$Scratch) {
    $receiptPath=Join-Path $Scratch 'teardown.json'
    foreach($fault in @('receipt.create','receipt.write','receipt.flush')) {
        if (Test-Path -LiteralPath $receiptPath -PathType Leaf) { [IO.File]::Delete($receiptPath) }
        $script:SelfTestAdapter.fault=$fault;$observed=''
        try { Write-ExclusiveJsonDurable $receiptPath ([ordered]@{schema='native-cleanup-teardown/v1'}) 'receipt' } catch { $observed=$_.Exception.Message }
        if ($observed -cne "SELFTEST_EXPECTED:$fault") { throw "Self-test $fault diagnostic mismatch: $observed" }
        $published=Test-Path -LiteralPath $receiptPath -PathType Leaf
        if ($fault -eq 'receipt.create' -and $published) { throw 'Receipt-create fault published a receipt' }
        if ($fault -ne 'receipt.create' -and -not $published) { throw 'Receipt write/flush fault lost its retained partial diagnostic' }
        $Passed.Add($fault)
    }
    $script:SelfTestAdapter.fault=''
    if (Test-Path -LiteralPath $receiptPath -PathType Leaf) { [IO.File]::Delete($receiptPath) }
    Write-ExclusiveJsonDurable $receiptPath ([ordered]@{schema='native-cleanup-teardown/v1';share_absent=$true}) 'receipt'
    $receiptRead=Get-Content -LiteralPath $receiptPath -Raw | ConvertFrom-Json
    if ($receiptRead.schema -cne 'native-cleanup-teardown/v1' -or -not $receiptRead.share_absent) { throw 'Self-test positive teardown receipt was invalid' }
    $Passed.Add('receipt-positive-flush')
}
function Invoke-SelfTestShareRemoval([Collections.Generic.List[string]]$Passed,[string]$Scratch) {
    Remove-ObservedShare $script:allocation.share
    if (-not $script:SelfTestAdapter.shareAbsent) { throw 'Self-test share removal transition did not complete' }
    $script:SelfTestAdapter.shareAbsent=$false;$script:SelfTestAdapter.share=[pscustomobject]@{Name=$script:allocation.share;Path=$Scratch;Temporary=$true;CurrentUsers=0}
    $script:SelfTestAdapter.fault='share.remove';$observed=''
    try { Remove-ObservedShare $script:allocation.share } catch { $observed=$_.Exception.Message }
    if ($observed -cne 'SELFTEST_EXPECTED:share.remove' -or $script:SelfTestAdapter.shareAbsent) { throw 'Self-test failed share removal did not retain state' }
    $Passed.Add('share-remove-positive-and-fault')
}
function Assert-SelfTestSetupFailure($Failure) {
    $Mode='setup';$script:SelfTestAdapter.shareAbsent=$true;$script:SelfTestAdapter.fault=$Failure.fault;$observed=''
    try { Setup-Allocation } catch { $observed=$_.Exception.Message }
    if ($observed -cne "SELFTEST_EXPECTED:$($Failure.fault)" -or -not $script:SelfTestAdapter.shareAbsent) { throw "Self-test setup $($Failure.fault) failure did not retain the allocation" }
    $partialAllocation=Get-Content -LiteralPath $script:AllocationRecord -Raw | ConvertFrom-Json
    if ($partialAllocation.state -cne $Failure.state) { throw "Self-test $($Failure.fault) recorded unexpected allocation state $($partialAllocation.state)" }
    $hasDirectory=Test-Path -LiteralPath $partialAllocation.path -PathType Container
    if ($hasDirectory -ne $Failure.directory) { throw "Self-test $($Failure.fault) created an unexpected directory state" }
    if ($hasDirectory) {
        if ($Failure.marker) { Assert-ExpectedChildren $partialAllocation.path;[IO.File]::Delete((Join-Path $partialAllocation.path '.allocation-nonce')) }
        elseif (@(Get-ChildItem -LiteralPath $partialAllocation.path -Force).Count -ne 0) { throw "Self-test $($Failure.fault) left unexpected children" }
        [IO.Directory]::Delete($partialAllocation.path,$false)
    }
    [IO.File]::Delete($script:AllocationRecord)
}
function Invoke-SelfTestTransitionFlow([Collections.Generic.List[string]]$Passed,[string]$TransitionRoot,[string]$TestSID) {
    # Exercise the same Setup-Allocation and Remove-Allocation transitions
    # against local scratch plus the fixed fake SMB/service/ACL adapter. Both
    # read these values through PowerShell dynamic scope.
    $TrustedRoot=Join-Path $TransitionRoot 'trusted'
    $AllocationParent=Join-Path $TransitionRoot 'allocations'
    $control=Join-Path $TrustedRoot 'control';$bin=Join-Path $TrustedRoot 'bin'
    foreach($directory in @($control,$bin,$AllocationParent)) { [IO.Directory]::CreateDirectory($directory) | Out-Null }
    $Manifest=Join-Path $TrustedRoot 'custody.json';$Verifier=Join-Path $bin 'nativecleanupcheck.exe'
    [IO.File]::WriteAllText($Verifier,'self-test-only verifier placeholder',[Text.UTF8Encoding]::new($false))
    $ExpectedSourceSHA='0'*40;$ExpectedPolicySHA256='0'*64
    $GoCache=Join-Path $TransitionRoot 'go-build';$GoModCache=Join-Path $TransitionRoot 'go-mod'
    $InvocationNonce=[Guid]::NewGuid().ToString('D');$RunID='self-test-run';$RunAttempt='1';$SID=$TestSID
    $manifestObject=[ordered]@{
        policy=[ordered]@{environment=@("TEMP=$AllocationParent")}
        artifacts=@([ordered]@{path='bin/nativecleanupowner.exe';sha256=('0'*64)})
    }
    [IO.File]::WriteAllText($Manifest,($manifestObject|ConvertTo-Json -Depth 8 -Compress),[Text.UTF8Encoding]::new($false))
    $ManifestSHA256=Get-Digest $Manifest;$VerifierSHA256=Get-Digest $Verifier
    $script:AllocationRecord=Join-Path $control 'allocation.json';$AllocationRecord=$script:AllocationRecord
    $script:SelfTestAdapter.ntfsDigest='self-test-ntfs-acl-digest'
    $script:SelfTestAdapter.shareAbsent=$true;$script:SelfTestAdapter.openFiles=@();$script:SelfTestAdapter.connections=@();$script:SelfTestAdapter.fault=''
    $script:SelfTestAdapter.childrenOverride=$false
    $setupFailures=@(
        @{fault='directory.create';state='directory-pending';directory=$false;marker=$false},
        @{fault='acl.apply';state='acl-pending';directory=$true;marker=$false},
        @{fault='marker.create';state='acl-pending';directory=$true;marker=$false},
        @{fault='share.create';state='share-pending';directory=$true;marker=$true}
    )
    foreach($failure in $setupFailures) {
        Assert-SelfTestSetupFailure $failure
        $Passed.Add("setup-$($failure.fault)-retained")
    }
    $script:SelfTestAdapter.fault=''
    $Mode='setup';Setup-Allocation
    if ($script:SelfTestAdapter.shareAbsent -or -not (Test-Path -LiteralPath $script:AllocationRecord -PathType Leaf)) { throw 'Self-test setup transition did not publish its allocated state' }
    $AllocationSHA256=$script:AllocationSHA256
    $JoinRecord=Join-Path $control 'owner.join.json'
    $joined=[ordered]@{schema='native-cleanup-join/v1';cleanup_started=[DateTimeOffset]::UtcNow.ToString('o');owner_pid=0;run_id=$RunID;run_attempt=$RunAttempt}
    [IO.File]::WriteAllText($JoinRecord,($joined|ConvertTo-Json -Compress),[Text.UTF8Encoding]::new($false))
    $JoinRecordSHA256=Get-Digest $JoinRecord
    $DeadlineUTC=([DateTimeOffset]::Parse($joined.cleanup_started).AddSeconds(30)).ToString('o')
    $allocatedPath=$script:allocation.path;$Mode='finally';$script:SelfTestAdapter.childrenOverride=$false;Remove-Allocation
    $teardownPath=Join-Path $control 'teardown-finally.json'
    $teardown=Get-Content -LiteralPath $teardownPath -Raw | ConvertFrom-Json
    if ((Test-Path -LiteralPath $allocatedPath) -or -not $teardown.share_absent -or -not $teardown.directory_absent) { throw 'Self-test cleanup transition did not remove only the empty allocation' }
    $Passed.Add('setup-finally-transition')
}
function Invoke-SelfTestTransitionMatrix([Collections.Generic.List[string]]$Passed) {
    $setupMatrix=@(
        @{mutation='allocation-record-collision';expected='Allocation record already exists'},
        @{mutation='allocation-path-collision';expected='Fresh allocation collision'},
        @{mutation='setup.directory.create';expected='SELFTEST_EXPECTED:directory.create'},
        @{mutation='setup.acl.apply';expected='SELFTEST_EXPECTED:acl.apply'},
        @{mutation='setup.marker.create';expected='SELFTEST_EXPECTED:marker.create'},
        @{mutation='setup.share.create';expected='SELFTEST_EXPECTED:share.create'}
    )
    foreach($case in $setupMatrix) { $Passed.Add((Invoke-SelfTestTransitionCase "setup-$($case.mutation.Replace('.','-'))" $case.mutation $case.expected)) }
    $journalOperations=@('journal.create','journal.write','journal.flush','journal.move')
    $journalStates=@('registered','directory-pending','acl-pending','share-pending','ready')
    foreach($state in $journalStates) { foreach($operation in $journalOperations) { $mutation="$operation@$state"; $Passed.Add((Invoke-SelfTestTransitionCase "${operation}-${state}" $mutation "SELFTEST_EXPECTED:$operation")) } }
    $removeMatrix=@(
        @{mutation='verifier.run_id';expected='SELFTEST_EXPECTED:verifier.run_id'},@{mutation='verifier.run_attempt';expected='SELFTEST_EXPECTED:verifier.run_attempt'},
        @{mutation='verifier.sid';expected='SELFTEST_EXPECTED:verifier.sid'},@{mutation='verifier.nonce';expected='SELFTEST_EXPECTED:verifier.nonce'},
        @{mutation='verifier.schema';expected='SELFTEST_EXPECTED:verifier.schema'},@{mutation='verifier.path';expected='SELFTEST_EXPECTED:verifier.path'},
        @{mutation='verifier.share';expected='SELFTEST_EXPECTED:verifier.share'},@{mutation='verifier.owner_source';expected='SELFTEST_EXPECTED:verifier.owner_source'},
        @{mutation='verifier.owner_hash';expected='SELFTEST_EXPECTED:verifier.owner_hash'},@{mutation='verifier.policy_sha256';expected='SELFTEST_EXPECTED:verifier.policy_sha256'},
        @{mutation='verifier.manifest_sha256';expected='SELFTEST_EXPECTED:verifier.manifest_sha256'},@{mutation='verifier.allocation_sha256';expected='SELFTEST_EXPECTED:verifier.allocation_sha256'},
        @{mutation='verifier.join_sha256';expected='SELFTEST_EXPECTED:verifier.join_sha256'},
        @{mutation='journal.sid';expected='Allocation owner/path mismatch'},@{mutation='journal.path';expected='Allocation owner/path mismatch'},
        @{mutation='journal.schema';expected='Allocation schema mismatch; RETENTION'},@{mutation='journal.state';expected='Partial allocation cannot prove exact descriptors; RETENTION'},
        @{mutation='share.name';expected='Share replacement/path/temporary mismatch'},@{mutation='share.path';expected='Share replacement/path/temporary mismatch'},@{mutation='share.temporary';expected='Share replacement/path/temporary mismatch'},
        @{mutation='share.users';expected='Share has current users or unknown user count; RETENTION'},@{mutation='share.users-unknown';expected='Share has current users or unknown user count; RETENTION'},
        @{mutation='share.query';expected='SELFTEST_EXPECTED:share.query'},@{mutation='share.acl-query';expected='SELFTEST_EXPECTED:share-acl.query'},@{mutation='share.acl-drift';expected='SMB current-SID-only premise failed'},
        @{mutation='ntfs.drift';expected='NTFS descriptor drift'},@{mutation='volume.identity';expected='Opened volume/file identity mismatch'},@{mutation='marker.digest';expected='Independent digest mismatch'},
        @{mutation='open-file';expected='SMB open file remains under allocation; RETENTION'},@{mutation='open-file-malformed';expected='SMB open-file observation has no path; RETENTION'},@{mutation='open-file-query';expected='SELFTEST_EXPECTED:open-files.query'},
        @{mutation='connection';expected='Client connection remains for allocation share; RETENTION'},@{mutation='connection-malformed';expected='SMB connection observation is malformed; RETENTION'},@{mutation='connection-query';expected='SELFTEST_EXPECTED:connections.query'},
        @{mutation='share.tamper-final';expected='Share identity/Temporary observation failed; RETENTION'},@{mutation='unexpected-child';expected='Unexpected allocation child; RETENTION'},
        @{mutation='reparse-child';expected='Subtree reparse substitution; RETENTION'},@{mutation='marker.path';expected='Allocation marker path mismatch; RETENTION'},
        @{mutation='join.missing';expected='Original owner join/deadline unavailable; RETENTION'},@{mutation='join.malformed';expected='Original owner join malformed; RETENTION'},
        @{mutation='share.remove';expected='SELFTEST_EXPECTED:share.remove'},@{mutation='marker.delete';expected='SELFTEST_EXPECTED:marker.delete'},@{mutation='late-child';expected='SELFTEST_EXPECTED:directory.delete.nonempty'},
        @{mutation='receipt.create';expected='SELFTEST_EXPECTED:receipt.create'},@{mutation='receipt.write';expected='SELFTEST_EXPECTED:receipt.write'},@{mutation='receipt.flush';expected='SELFTEST_EXPECTED:receipt.flush'}
    )
    foreach($case in $removeMatrix) { $Passed.Add((Invoke-SelfTestTransitionCase "remove-$($case.mutation.Replace('.','-'))" $case.mutation $case.expected)) }
}
function Remove-SelfTestScratch([string]$Scratch,[string]$TransitionRoot) {
    # This scratch is private to this process and contains no external
    # resources. Remove only known files and then the empty directory.
    foreach($name in @('.allocation-nonce','unexpected-child.txt','allocation.json','allocation.json.partial','teardown.json')) {
        $path=Join-Path $Scratch $name
        if (Test-Path -LiteralPath $path -PathType Leaf) { [IO.File]::Delete($path) }
    }
    foreach($relative in @('trusted/control/allocation.json','trusted/control/owner.join.json','trusted/custody.json','trusted/control/teardown-finally.json','trusted/bin/nativecleanupcheck.exe')) {
        $path=Join-Path $TransitionRoot $relative
        if (Test-Path -LiteralPath $path -PathType Leaf) { [IO.File]::Delete($path) }
    }
    foreach($path in @((Join-Path $TransitionRoot 'allocations'),(Join-Path $TransitionRoot 'trusted/control'),(Join-Path $TransitionRoot 'trusted/bin'),(Join-Path $TransitionRoot 'trusted'),$TransitionRoot)) {
        if (Test-Path -LiteralPath $path -PathType Container) { [IO.Directory]::Delete($path,$false) }
    }
    if (Test-Path -LiteralPath $Scratch -PathType Container) { [IO.Directory]::Delete($Scratch,$false) }
}
function Invoke-AllocationSelfTest {
    # This adapter is constructed only here and cannot be supplied by callers
    # or environment variables. It exercises the production predicates while
    # all SMB/service operations remain simulated.
    $scratch=[IO.Path]::Combine([IO.Path]::GetTempPath(),('lopper-native-selftest-'+[Guid]::NewGuid().ToString('N')))
    $transitionRoot=Join-Path $scratch 'transition'
    [IO.Directory]::CreateDirectory($scratch) | Out-Null
    try {
        $script:SelfTestActive=$true
        $script:SelfTestAdapter=[pscustomobject]@{
            trace=[Collections.Generic.List[string]]::new();fault='';faultState='';verifierReject='';shareAbsent=$false;childrenOverride=$true
            share=$null;shareAccess=@();openFiles=@();connections=@();children=@();marker='';ntfsDigest=''
            shareQueryCount=0;tamperOnShareQuery=0;identityOverride=$false;injectChildBeforeDelete=$false;scratch=$scratch;nonce=''
        }
        $testSID=[Security.Principal.WindowsIdentity]::GetCurrent().User.Value
        $account=[Security.Principal.SecurityIdentifier]::new($testSID).Translate([Security.Principal.NTAccount]).Value
        $nonce=[Guid]::NewGuid().ToString('D')
        $markerPath=Join-Path $scratch '.allocation-nonce'
        $marker='native-cleanup-self-test|'+$nonce
        [IO.File]::WriteAllText($markerPath,$marker,[Text.UTF8Encoding]::new($false))
        $script:SelfTestAdapter.marker=$marker
        $script:allocation=[ordered]@{share="lopper-$nonce";path=$scratch;temporary=$true;sid=$testSID;share_acl_sha256=(Get-TextDigest "$testSID|Allow|Full");sentinel_sha256=(Get-TextDigest $marker)}
        $script:SelfTestAdapter.share=[pscustomobject]@{Name=$script:allocation.share;Path=$scratch;Temporary=$true;CurrentUsers=0}
        $script:SelfTestAdapter.shareAccess=@([pscustomobject]@{AccountName=$account;AccessControlType='Allow';AccessRight='Full'})
        $script:SelfTestAdapter.openFiles=@([pscustomobject]@{Path=[IO.Path]::GetTempPath().TrimEnd([IO.Path]::DirectorySeparatorChar)})
        $script:SelfTestAdapter.children=@(Get-Item -LiteralPath $markerPath -Force)
        $passed=[Collections.Generic.List[string]]::new()

        Assert-NoObservedUse
        Assert-ExpectedChildren $scratch
        $passed.Add('positive-share-openfile-connection-marker')
        Invoke-SelfTestUseControls $passed $scratch $markerPath
        Invoke-SelfTestChildControls $passed $scratch $markerPath $marker
        Invoke-SelfTestJournalFaults $passed $scratch $nonce $testSID $marker
        Invoke-SelfTestReceiptFaults $passed $scratch
        Invoke-SelfTestShareRemoval $passed $scratch
        Invoke-SelfTestTransitionFlow $passed $transitionRoot $testSID
        Invoke-SelfTestTransitionMatrix $passed
        'allocator-self-test:' + ($passed -join ',')
    } finally {
        $script:SelfTestActive=$false;$script:SelfTestAdapter=$null
        Remove-SelfTestScratch $scratch $transitionRoot
    }
}
# This fixed in-source API reads identity only; it grants no process ownership.
function Initialize-DirectoryIdentity {
    Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
using Microsoft.Win32.SafeHandles;
public static class NativeCleanupDirectoryIdentity {
 [StructLayout(LayoutKind.Sequential)] private struct Info {
  public uint Attributes,CreationLow,CreationHigh,AccessLow,AccessHigh,WriteLow,WriteHigh;
  public uint Volume,SizeHigh,SizeLow,Links,IndexHigh,IndexLow;
 }
 [DllImport("kernel32.dll",CharSet=CharSet.Unicode,SetLastError=true)] private static extern SafeFileHandle CreateFileW(string name,uint access,uint share,IntPtr security,uint creation,uint flags,IntPtr template);
 [DllImport("kernel32.dll",SetLastError=true)] private static extern bool GetFileInformationByHandle(SafeFileHandle handle,out Info info);
 public static string Read(string path) {
  using(var h=CreateFileW(path,0x80,7,IntPtr.Zero,3,0x02200000,IntPtr.Zero)) {
   if(h.IsInvalid) throw new Win32Exception(Marshal.GetLastWin32Error());
   Info i; if(!GetFileInformationByHandle(h,out i)) throw new Win32Exception(Marshal.GetLastWin32Error());
   if((i.Attributes&0x400)!=0) throw new InvalidOperationException("Opened directory is reparse");
   return i.Volume.ToString("x8")+":"+i.IndexHigh.ToString("x8")+i.IndexLow.ToString("x8");
  }
 }
}
'@
}
function Setup-Allocation {
    if (Test-Path -LiteralPath $AllocationRecord) { throw 'Allocation record already exists' }
    Assert-Custody $false $false
    if ((Get-ObservedServerStatus) -ne 'Running') { throw 'Loopback SMB service unavailable; no service mutation permitted' }
    $nonce = if ($script:SelfTestActive -and -not [string]::IsNullOrEmpty([string]$script:SelfTestAdapter.nonce)) { [string]$script:SelfTestAdapter.nonce } else { [Guid]::NewGuid().ToString('D') }
    $path = Join-Path $AllocationParent "lopper-$nonce"
    if (Test-Path -LiteralPath $path) { throw 'Fresh allocation collision' }
    $custody = Get-Content -LiteralPath $Manifest -Raw | ConvertFrom-Json
    if (@($custody.policy.environment | Where-Object { $_ -ceq "TEMP=$AllocationParent" }).Count -ne 1) { throw 'Allocation parent differs from reviewed TEMP' }
    $owner = @($custody.artifacts | Where-Object path -CEQ 'bin/nativecleanupowner.exe')
    $marker = "$nonce|$InvocationNonce|$RunID|$RunAttempt|$SID|$ExpectedSourceSHA|$ManifestSHA256"
    $script:allocation = [ordered]@{schema='native-cleanup-allocation/v1';allocation_nonce=$nonce;invocation_nonce=$InvocationNonce;run_id=$RunID;run_attempt=$RunAttempt;sid=$SID;share="lopper-$nonce";temporary=$true;path=$path;volume_file_id='';ntfs_acl_sha256='';share_acl_sha256='';manifest_sha256=$ManifestSHA256;owner_source=$ExpectedSourceSHA;owner_sha256=$owner[0].sha256;state='registered';sentinel_sha256=(Get-TextDigest $marker)}
    Save-Allocation # Registered BEFORE directory, ACL or share side effects.
    Assert-Custody $true $false
    $script:allocation.state='directory-pending'; Save-Allocation
    Assert-Custody $true $false
    Invoke-TestFault 'directory.create'
    [IO.Directory]::CreateDirectory($path) | Out-Null
    $script:allocation.volume_file_id=Get-DirectoryIdentity $path
    $script:allocation.state='acl-pending'; Save-Allocation
    Assert-Custody $true $false
    $security = [Security.AccessControl.DirectorySecurity]::new()
    $security.SetOwner([Security.Principal.SecurityIdentifier]::new($SID))
    $security.SetAccessRuleProtection($true,$false)
    $inherit=[Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit
    $rule=[Security.AccessControl.FileSystemAccessRule]::new([Security.Principal.SecurityIdentifier]::new($SID),[Security.AccessControl.FileSystemRights]::FullControl,$inherit,[Security.AccessControl.PropagationFlags]::None,[Security.AccessControl.AccessControlType]::Allow)
    $security.AddAccessRule($rule)
    Invoke-TestFault 'acl.apply'
    Set-ObservedAllocationACL $path $security
    $script:allocation.ntfs_acl_sha256=Assert-NTFS
    Invoke-TestFault 'marker.create'
    Write-ObservedMarker (Join-Path $path '.allocation-nonce') $marker
    $script:allocation.share_acl_sha256=Get-TextDigest "$SID|Allow|Full"
    $script:allocation.state='share-pending'; Save-Allocation
    Assert-Custody $true $false; Assert-DirectoryIdentity
    $account=[Security.Principal.SecurityIdentifier]::new($SID).Translate([Security.Principal.NTAccount]).Value
    New-ObservedShare $script:allocation.share $path $account
    $script:allocation.share_acl_sha256=Assert-Share
    $unc="\\localhost\$($script:allocation.share)\.allocation-nonce"
    if ((Read-ObservedMarker $unc) -cne $marker) { throw 'Actual loopback UNC nonce mismatch' }
    $script:allocation.state='ready'; Save-Allocation
    Assert-Allocation; Assert-DirectoryIdentity
    if ((Assert-Share) -cne $script:allocation.share_acl_sha256) { throw 'SMB descriptor drift' }
}
function Find-OwnedShare {
    try { return Get-ObservedShare }
    catch {
        # Exact named-object absence only. Service/access/query errors retain.
        if ($_.CategoryInfo.Category -eq 'ObjectNotFound' -and $_.FullyQualifiedErrorId -like 'CmdletizationQuery_NotFound_Name*') { return $null }
        throw
    }
}
function Assert-OriginalJoin {
    # Read cleanup_started as the raw JSON string; ConvertFrom-Json would
    # coerce it to a culture-formatted DateTime and drop sub-second precision.
    try { $joinText=Get-Content -LiteralPath $JoinRecord -Raw; $originalJoin=$joinText | ConvertFrom-Json; $cleanupStarted=[Text.Json.JsonDocument]::Parse($joinText).RootElement.GetProperty('cleanup_started').GetString() }
    catch { throw 'Original owner join malformed; RETENTION' }
    $originalDeadline=([DateTimeOffset]::Parse($cleanupStarted,[Globalization.CultureInfo]::InvariantCulture)).AddSeconds(30)
    if ([DateTimeOffset]::Parse($DeadlineUTC) -ne $originalDeadline) { throw 'Fresh recovery deadline rejected; RETENTION' }
    if ($Mode -eq 'recovery') {
        $live=$null
        try { $live=[Diagnostics.Process]::GetProcessById([int]$originalJoin.owner_pid) } catch [ArgumentException] { $live=$null } # No such process: the original owner exited.
        if ($null -ne $live) { $live.Dispose(); throw 'Original owner PID still live or reused; RETENTION' }
    }
}
function Remove-Allocation {
    if ($DeadlineUTC -eq '' -or $JoinRecord -eq '' -or $JoinRecordSHA256 -eq '') { throw 'Original owner join/deadline unavailable; RETENTION' }
    Assert-Custody $true $true # Never infer quiescence from missing logs/owner PID.
    $script:allocation=Get-Content -LiteralPath $AllocationRecord -Raw | ConvertFrom-Json
    Assert-Allocation
    Assert-OriginalJoin
    $directoryExists=Test-Path -LiteralPath $script:allocation.path
    $share=Find-OwnedShare
    if ($null -ne $share -and -not $directoryExists) { throw 'Share exists without owned directory; RETENTION' }
    if ($directoryExists) { Assert-DirectoryIdentity; Assert-ExpectedChildren $script:allocation.path }
    if ($null -ne $share) {
        Assert-Custody $true $true; Assert-DirectoryIdentity
        if ((Assert-Share) -cne $script:allocation.share_acl_sha256) { throw 'SMB descriptor drift' }
        Assert-NoObservedUse
        Assert-Deadline
        # Repeat every selected observation immediately before removal. The
        # checks are snapshots; unique SID-only ownership remains a premise.
        Assert-NoObservedUse
        Remove-ObservedShare $script:allocation.share
    }
    Assert-Custody $true $true
    if ($null -ne (Find-OwnedShare)) { throw 'Share absence not observed' }
    if ($directoryExists) {
        Assert-DirectoryIdentity; Assert-ExpectedChildren $script:allocation.path; Assert-Deadline
        Remove-ObservedMarker (Join-Path $script:allocation.path '.allocation-nonce')
        Remove-ObservedAllocationDirectory $script:allocation.path
    }
    Assert-Deadline
    if (Test-Path -LiteralPath $script:allocation.path) { throw 'Directory absence not observed' }
    Assert-Custody $true $true
    $receipt=[ordered]@{schema='native-cleanup-teardown/v1';allocation_sha256=$AllocationSHA256;join_sha256=$JoinRecordSHA256;manifest_sha256=$ManifestSHA256;share_absent=$true;directory_absent=$true;finished=[DateTimeOffset]::UtcNow.ToString('o')}
    $receiptPath=Join-Path $TrustedRoot "control/teardown-$Mode.json"
    Assert-Deadline
    Write-ExclusiveJsonDurable $receiptPath $receipt 'receipt'
    Assert-Deadline
}
try {
    if ($Mode -eq 'self-test') { Invoke-AllocationSelfTest; return }
    if (-not $IsWindows) { throw 'Native Windows required' }
    if ([Security.Principal.WindowsIdentity]::GetCurrent().User.Value -cne $SID) { throw 'Current account SID mismatch' }
    Assert-Path $TrustedRoot $true; Assert-Path $AllocationParent $true; Assert-Path (Join-Path $TrustedRoot 'control') $true
    if ([IO.DriveInfo]::new([IO.Path]::GetPathRoot($AllocationParent)).DriveFormat -cne 'NTFS') { throw 'Allocation requires native NTFS' }
    if ($AllocationRecord -cne (Join-Path $TrustedRoot 'control/allocation.json')) { throw 'Allocation control layout mismatch' }
    Assert-Custody $false $false
    Initialize-DirectoryIdentity
    $script:identityInitialized=$true; Assert-Custody $false $false
    if ($Mode -eq 'setup') { Setup-Allocation } else { Remove-Allocation }
} catch { [Console]::Error.WriteLine("RETENTION: $($_.Exception.Message)");exit 1 }
