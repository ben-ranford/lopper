[CmdletBinding()]
param(
  [Parameter(Mandatory)][ValidateSet('bootstrap','self-test','execute','finally','validate')][string]$Mode,
  [string]$EvidenceRoot = (Join-Path $env:RUNNER_TEMP "lopper-native-proof-$env:GITHUB_RUN_ID-$env:GITHUB_RUN_ATTEMPT"),
  [string]$TrustedRoot = (Join-Path $env:RUNNER_TEMP "lopper-native-root-$env:GITHUB_RUN_ID-$env:GITHUB_RUN_ATTEMPT")
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$Utf8 = [Text.UTF8Encoding]::new($false)
$script:ResultPath = Join-Path $EvidenceRoot 'result.json'
$script:StdoutPath = Join-Path $EvidenceRoot 'stdout.jsonl'
$script:StderrPath = Join-Path $EvidenceRoot 'stderr.log'
$script:Result = $null

function Get-SHA256([byte[]]$Bytes) {
  [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($Bytes)).ToLowerInvariant()
}
function Get-SHA1([byte[]]$Bytes) {
  $algorithm=[Security.Cryptography.SHA1]::Create()
  try { [Convert]::ToHexString($algorithm.ComputeHash($Bytes)).ToLowerInvariant() } finally { $algorithm.Dispose() }
}
function Write-Durable([string]$Path,[byte[]]$Bytes) {
  $stream = [IO.File]::Open($Path,[IO.FileMode]::Create,[IO.FileAccess]::Write,[IO.FileShare]::None)
  try { $stream.Write($Bytes,0,$Bytes.Length); $stream.Flush($true) } finally { $stream.Dispose() }
}
function Write-Receipt {
  param([Parameter(Mandatory)]$Value)
  $bytes = $Utf8.GetBytes(($Value | ConvertTo-Json -Depth 16 -Compress))
  $temporary = "$script:ResultPath.$PID.$([Guid]::NewGuid().ToString('N')).partial"
  $stream = [IO.File]::Open($temporary,[IO.FileMode]::CreateNew,[IO.FileAccess]::Write,[IO.FileShare]::None)
  try {
    $stream.Write($bytes,0,$bytes.Length); $stream.Flush($true)
    $stream.Dispose(); $stream=$null
    [IO.File]::Move($temporary,$script:ResultPath,$true)
  } catch {
    if ($null -ne $stream) { $stream.Dispose() }
    if (Test-Path -LiteralPath $temporary -PathType Leaf) { [IO.File]::Delete($temporary) }
    throw
  }
}
function New-Receipt([string]$Phase,[string]$Status,[Nullable[int]]$ExitCode,[string]$Failure) {
  [ordered]@{
    schema='lopper-native-windows-proof/v1'
    run_id=[string]$env:GITHUB_RUN_ID; run_attempt=[string]$env:GITHUB_RUN_ATTEMPT
    workflow=[string]$env:GITHUB_WORKFLOW; job='native-windows-hook-cleanup'
    event_name=[string]$env:GITHUB_EVENT_NAME; event_sha=[string]$env:GITHUB_SHA
    ref=[string]$env:GITHUB_REF; phase=$Phase; setup_status=$Status; launch_status='not_launched'; owner_launch_status='not_launched'
    test_status=if ($null -eq $ExitCode) {'not_launched'} elseif ($ExitCode -eq 0) {'passed'} else {'failed'}
    exit_code=$ExitCode; owner_exit_code=$null; failure=$Failure
    command='go test -tags=lopper_native_windows -json -count=1 -timeout=10m ./scripts -run ^TestHooksCleanupNativeWindows$'
    go=$null; git=$null; bash=$null; sid=$null; share=$null; cleanup_status='pending'
    stdout=[ordered]@{path='stdout.jsonl';bytes=0;sha256=(Get-SHA256 ([byte[]]@()))}
    stderr=[ordered]@{path='stderr.log';bytes=0;sha256=(Get-SHA256 ([byte[]]@()))}
  }
}
function Assert-EvidenceDirectory {
  if (-not [IO.Path]::IsPathFullyQualified($EvidenceRoot) -or -not [IO.Path]::IsPathFullyQualified($TrustedRoot)) { throw 'Evidence/root path must be absolute' }
  if ([IO.Path]::GetFullPath($EvidenceRoot) -cne $EvidenceRoot -or [IO.Path]::GetFullPath($TrustedRoot) -cne $TrustedRoot) { throw 'Evidence/root path must be canonical' }
  if ($EvidenceRoot.StartsWith($TrustedRoot,[StringComparison]::OrdinalIgnoreCase) -or $TrustedRoot.StartsWith($EvidenceRoot,[StringComparison]::OrdinalIgnoreCase)) { throw 'Evidence and fixture root must be disjoint' }
  $evidenceParent=[IO.Path]::GetDirectoryName($EvidenceRoot)
  $trustedParent=[IO.Path]::GetDirectoryName($TrustedRoot)
  if ([IO.Path]::GetFullPath($evidenceParent) -cne $evidenceParent -or [IO.Path]::GetFullPath($trustedParent) -cne $trustedParent) { throw 'Evidence/root parent must be canonical' }
  foreach($path in @($EvidenceRoot,$TrustedRoot)) { Assert-NoReparseAncestor $path }
}
function Assert-NoReparseAncestor([string]$Path) {
  $cursor=$Path
  while($cursor) {
    if (Test-Path -LiteralPath $cursor) {
      $item=Get-Item -LiteralPath $cursor -Force
      if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'Evidence/root path contains a reparse component' }
    }
    $parent=[IO.Directory]::GetParent($cursor)
    if ($null -eq $parent) { break }
    $cursor=$parent.FullName
  }
}
function Assert-ReceiptIdentity($Receipt) {
  foreach ($pair in @(@('schema','lopper-native-windows-proof/v1'),@('run_id',[string]$env:GITHUB_RUN_ID),@('run_attempt',[string]$env:GITHUB_RUN_ATTEMPT),@('workflow',[string]$env:GITHUB_WORKFLOW),@('job','native-windows-hook-cleanup'),@('event_name',[string]$env:GITHUB_EVENT_NAME),@('event_sha',[string]$env:GITHUB_SHA),@('ref',[string]$env:GITHUB_REF))) {
    if ($Receipt.($pair[0]) -cne $pair[1]) { throw "Receipt identity mismatch: $($pair[0])" }
  }
}
function Assert-ReceiptReadyToPublish($Receipt,[byte[]]$StdoutBytes,[byte[]]$StderrBytes) {
  Assert-ReceiptIdentity $Receipt
  if ($Receipt.phase -cne 'subject-go' -or $Receipt.launch_status -cne 'launched' -or $Receipt.owner_launch_status -cne 'started' -or $Receipt.test_status -cne 'passed' -or $Receipt.exit_code -ne 0 -or $Receipt.owner_exit_code -ne 0 -or $Receipt.cleanup_status -cne 'owner_completed' -or $null -eq $Receipt.self_test -or $Receipt.self_test.status -cne 'passed' -or -not [string]::IsNullOrEmpty([string]$Receipt.self_test.failure) -or -not [string]::IsNullOrEmpty([string]$Receipt.failure)) { throw 'Native owner result was not successful and cleaned' }
  foreach ($entry in @(@('stdout','stdout.jsonl',$StdoutBytes),@('stderr','stderr.log',$StderrBytes))) {
    $binding=$Receipt[$entry[0]]
    if ($binding.path -cne $entry[1] -or $binding.bytes -ne $entry[2].Length -or $binding.sha256 -cne (Get-SHA256 $entry[2])) { throw "Raw $($entry[0]) binding mismatch" }
  }
}
function Test-ReceiptProtocol {
  # Pure in-memory controls exercise the same final receipt predicate used by validate. They never invoke SMB or filesystem deletion.
  $stdoutBytes=$Utf8.GetBytes('{"Action":"pass"}' + "`n")
  $stderrBytes=[byte[]]@()
  $base = New-Receipt 'subject-go' 'completed' 0 ''
  $base.launch_status='launched';$base.owner_launch_status='started';$base.owner_exit_code=0;$base.cleanup_status='owner_completed'
  $base.self_test=[ordered]@{status='passed';controls='positive';allocator_controls='positive';failure=''}
  $base.stdout=[ordered]@{path='stdout.jsonl';bytes=$stdoutBytes.Length;sha256=(Get-SHA256 $stdoutBytes)}
  $base.stderr=[ordered]@{path='stderr.log';bytes=0;sha256=(Get-SHA256 $stderrBytes)}
  Assert-ReceiptReadyToPublish $base $stdoutBytes $stderrBytes
  $mutations = @(
    @{name='run id'; change={param($r) $r.run_id='0'}},
    @{name='attempt'; change={param($r) $r.run_attempt='0'}},
    @{name='event sha'; change={param($r) $r.event_sha=('0'*40)}},
    @{name='schema'; change={param($r) $r.schema='other'}},
    @{name='job'; change={param($r) $r.job='other'}},
    @{name='phase'; change={param($r) $r.phase='bootstrap'}},
    @{name='test status'; change={param($r) $r.test_status='not_launched'}},
    @{name='owner exit'; change={param($r) $r.owner_exit_code=1}},
    @{name='cleanup'; change={param($r) $r.cleanup_status='incomplete'}},
    @{name='self-test status'; change={param($r) $r.self_test.status='failed'}},
    @{name='self-test incomplete'; change={param($r) $r.self_test.status='incomplete'}},
    @{name='self-test failure'; change={param($r) $r.self_test.failure='recorded failure'}},
    @{name='failure'; change={param($r) $r.failure='recorded failure'}},
    @{name='stream path'; change={param($r) $r.stdout.path='other.jsonl'}},
    @{name='stream digest'; change={param($r) $r.stdout.sha256=('0'*64)}}
  )
  foreach ($case in $mutations) {
    $candidate = $base | ConvertTo-Json -Depth 8 | ConvertFrom-Json -AsHashtable
    & $case.change $candidate
    $rejected = $false
    try { Assert-ReceiptReadyToPublish $candidate $stdoutBytes $stderrBytes } catch { $rejected = $true }
    if (-not $rejected) { throw "Self-test accepted altered $($case.name)" }
  }
  'receipt-positive,run-id,attempt,event-sha,schema,job,phase,test-status,owner-exit,cleanup,self-test-status,self-test-incomplete,failure,stream-path,stream-digest'
}
function Save-EmptyStreams {
  Write-Durable $script:StdoutPath ([byte[]]@())
  Write-Durable $script:StderrPath ([byte[]]@())
}
function Update-StreamBindings($Receipt) {
  foreach ($entry in @(@('stdout',$script:StdoutPath),@('stderr',$script:StderrPath))) {
    $bytes = [IO.File]::ReadAllBytes($entry[1])
    $Receipt[$entry[0]] = [ordered]@{path=[IO.Path]::GetFileName($entry[1]);bytes=$bytes.Length;sha256=(Get-SHA256 $bytes)}
  }
}
function Initialize-BoundedOwnerCapture {
  if ('NativeCleanupOutputCapture' -as [type]) { return }
  Add-Type -TypeDefinition @'
using System;
using System.IO;
using System.Security.Cryptography;
using System.Threading.Tasks;
public sealed class NativeCleanupCapturedOutput {
 public long TotalBytes { get; set; }
 public byte[] Prefix { get; set; }
 public string SHA256 { get; set; }
 public bool Truncated { get; set; }
}
public static class NativeCleanupOutputCapture {
 public static Task<NativeCleanupCapturedOutput> Drain(Stream input, int limit) {
  return Task.Run(() => {
   byte[] buffer=new byte[8192]; byte[] prefix=new byte[limit]; int retained=0; long total=0;
   using(SHA256 hash=SHA256.Create()) {
    int count;
    while((count=input.Read(buffer,0,buffer.Length))>0) {
     hash.TransformBlock(buffer,0,count,buffer,0); total+=count;
     int copy=Math.Min(count,limit-retained);
     if(copy>0) { Buffer.BlockCopy(buffer,0,prefix,retained,copy); retained+=copy; }
    }
    hash.TransformFinalBlock(Array.Empty<byte>(),0,0);
    byte[] kept=new byte[retained]; Buffer.BlockCopy(prefix,0,kept,0,retained);
    return new NativeCleanupCapturedOutput { TotalBytes=total, Prefix=kept, SHA256=BitConverter.ToString(hash.Hash).Replace("-","").ToLowerInvariant(), Truncated=total>retained };
   }
  });
 }
}
'@
}
function Write-OwnerTerminalReceipt($OwnerStdout,$OwnerStderr,[int]$OwnerExit) {
  $script:Result.setup_status='completed'
  $script:Result.owner_exit_code=$OwnerExit
  $script:Result.exit_code=$null
  $script:Result.test_status='unknown'
  $script:Result.launch_status='unknown'
  $script:Result.phase='owner-terminal-join-pending'
  $script:Result.cleanup_status='incomplete'
  $script:Result.failure='Owner process terminated; subject status awaits authenticated join'
  $script:Result.owner_control=[ordered]@{}
  foreach($pair in @(@('stdout',$OwnerStdout),@('stderr',$OwnerStderr))) {
    $capture=$pair[1]
    $script:Result.owner_control[$pair[0]]=[ordered]@{
      bytes=$capture.TotalBytes;sha256=$capture.SHA256;captured_bytes=$capture.Prefix.Length
      truncated=$capture.Truncated;raw_prefix_base64=[Convert]::ToBase64String($capture.Prefix)
    }
  }
  if ($OwnerStdout.Truncated -or $OwnerStderr.Truncated) { $script:Result.failure='Owner control diagnostics exceeded the 64 KiB receipt cap' }
  Write-Receipt $script:Result
}
function Get-WorkflowSourcePaths([string]$Root) {
  $main = [IO.File]::ReadAllText((Join-Path $Root 'tools/nativecleanupcheck/main.go'))
  $match = [regex]::Match($main,'var sourcePaths = \[\]string\{(?<body>.*?)\n\}',[Text.RegularExpressions.RegexOptions]::Singleline)
  if (-not $match.Success) { throw 'Native checker source inventory not found' }
  $paths=@([regex]::Matches($match.Groups['body'].Value,'"(?<path>[^"\r\n]+)"') | ForEach-Object { $_.Groups['path'].Value })
  if ($paths.Count -eq 0 -or @($paths | Select-Object -Unique).Count -ne $paths.Count -or @($paths | Where-Object { [IO.Path]::IsPathRooted($_) -or $_ -match '(^|/)\.\.(/|$)' -or $_ -match '\\' }).Count -ne 0) { throw 'Invalid native checker source inventory' }
  $paths
}
function Get-FileBinding([string]$Root,[string]$Relative) {
  if (-not [IO.Path]::IsPathFullyQualified($Root) -or [IO.Path]::IsPathRooted($Relative) -or $Relative -match '(^|[\\/])\.\.([\\/]|$)') { throw "Noncanonical binding path: $Relative" }
  $rootFull=[IO.Path]::GetFullPath($Root).TrimEnd([IO.Path]::DirectorySeparatorChar,[IO.Path]::AltDirectorySeparatorChar)+[IO.Path]::DirectorySeparatorChar
  $path = [IO.Path]::GetFullPath((Join-Path $Root $Relative))
  if (-not $path.StartsWith($rootFull,[StringComparison]::OrdinalIgnoreCase)) { throw "Binding escaped root: $Relative" }
  $item = Get-Item -LiteralPath $path -Force
  if ($item.PSIsContainer -or (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0)) { throw "Non-regular bound file: $Relative" }
  [ordered]@{path=$Relative.Replace('\','/');sha256=(Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()}
}
function Get-AbsoluteToolBinding([string]$Path) {
  if (-not [IO.Path]::IsPathFullyQualified($Path) -or [IO.Path]::GetFullPath($Path) -cne $Path) { throw "Noncanonical tool path: $Path" }
  $cursor=$Path
  while ($cursor) {
    $item=Get-Item -LiteralPath $cursor -Force
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "Tool path contains a reparse component: $Path" }
    $parent=[IO.Directory]::GetParent($cursor)
    if ($null -eq $parent) { break }
    $cursor=$parent.FullName
  }
  $item=Get-Item -LiteralPath $Path -Force
  if ($item.PSIsContainer) { throw "Tool path is not a file: $Path" }
  [ordered]@{path=$Path;sha256=(Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()}
}
function Invoke-CheckedNative([string]$Executable,[string[]]$Arguments,[string]$Label) {
  $output=@(& $Executable @Arguments)
  $exitCode=$LASTEXITCODE
  if ($exitCode -ne 0) { throw "$Label exited $exitCode" }
  return [string]($output -join "`n")
}
function Initialize-ControlDirectory([string]$Path,[string]$SID) {
  [IO.Directory]::CreateDirectory($Path) | Out-Null
  $security=[Security.AccessControl.DirectorySecurity]::new()
  $identity=[Security.Principal.SecurityIdentifier]::new($SID)
  $security.SetOwner($identity);$security.SetAccessRuleProtection($true,$false)
  $inherit=[Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit
  $rule=[Security.AccessControl.FileSystemAccessRule]::new($identity,[Security.AccessControl.FileSystemRights]::FullControl,$inherit,[Security.AccessControl.PropagationFlags]::None,[Security.AccessControl.AccessControlType]::Allow)
  $security.AddAccessRule($rule);Set-Acl -LiteralPath $Path -AclObject $security
  Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
using Microsoft.Win32.SafeHandles;
public static class NativeCleanupControlIdentity {
 [StructLayout(LayoutKind.Sequential)] private struct Info { public uint A,C0,C1,A0,A1,W0,W1,V,S0,S1,L,I0,I1; }
 [DllImport("kernel32.dll",CharSet=CharSet.Unicode,SetLastError=true)] private static extern SafeFileHandle CreateFileW(string n,uint a,uint s,IntPtr p,uint c,uint f,IntPtr t);
 [DllImport("kernel32.dll",SetLastError=true)] private static extern bool GetFileInformationByHandle(SafeFileHandle h,out Info i);
 public static string Read(string p) { using(var h=CreateFileW(p,0x80,7,IntPtr.Zero,3,0x02200000,IntPtr.Zero)) { if(h.IsInvalid) throw new Win32Exception(Marshal.GetLastWin32Error()); Info i; if(!GetFileInformationByHandle(h,out i)) throw new Win32Exception(Marshal.GetLastWin32Error()); if((i.A&0x400)!=0) throw new InvalidOperationException("Control reparse"); return i.V.ToString("x8")+":"+i.I0.ToString("x8")+i.I1.ToString("x8"); } }
}
'@
  $sddl=(Get-Acl -LiteralPath $Path).GetSecurityDescriptorSddlForm([Security.AccessControl.AccessControlSections]::Owner -bor [Security.AccessControl.AccessControlSections]::Group -bor [Security.AccessControl.AccessControlSections]::Access)
  [ordered]@{id=[NativeCleanupControlIdentity]::Read($Path);acl=(Get-SHA256 $Utf8.GetBytes($sddl))}
}
function Test-DistributionFile([string]$Name,[string]$Root,$Item) {
  $base=$Item.Name.ToLowerInvariant()
  $isDLL=$Item.Extension -ieq '.dll'
  $isRequired= switch ($Name) {
    'go' { $base -in @('go.exe','compile.exe','link.exe') }
    'git-for-windows' { $base -in @('git.exe','bash.exe','sh.exe','stat.exe','xargs.exe','cygpath.exe') }
    'native-cgo' { $base -in @('gcc.exe','ld.exe','clang.exe','lld.exe','lld-link.exe') }
    'powershell' { $base -eq 'pwsh.exe' }
  }
  $includeToolExe=$false
  if ($Name -eq 'go' -and $Item.Extension -ieq '.exe') {
    $relative=$Item.FullName.Substring($Root.Length).TrimStart([IO.Path]::DirectorySeparatorChar,[IO.Path]::AltDirectorySeparatorChar)
    $includeToolExe=$relative.StartsWith("bin$([IO.Path]::DirectorySeparatorChar)",[StringComparison]::OrdinalIgnoreCase) -or $relative.StartsWith("pkg$([IO.Path]::DirectorySeparatorChar)tool$([IO.Path]::DirectorySeparatorChar)windows_amd64$([IO.Path]::DirectorySeparatorChar)",[StringComparison]::OrdinalIgnoreCase)
  } elseif ($Name -eq 'native-cgo' -and $Item.Extension -ieq '.exe') { $includeToolExe=$true }
  return [bool]($isDLL -or $isRequired -or $includeToolExe)
}
function Assert-DistributionRequiredFiles([string]$Name,$Paths) {
  $requiredNames = switch ($Name) {
    'go' { @('go.exe','compile.exe','link.exe') }
    'git-for-windows' { @('git.exe','bash.exe','sh.exe','stat.exe','xargs.exe','cygpath.exe') }
    'native-cgo' { @('gcc.exe','ld.exe') }
    'powershell' { @('pwsh.exe') }
  }
  foreach ($required in $requiredNames) {
    if (@($Paths | Where-Object { [IO.Path]::GetFileName($_) -ieq $required }).Count -eq 0) { throw "Missing selected runtime file: $Name/$required" }
  }
}
function Get-Distribution([string]$Name,[string]$Root,[string]$Revision,[string]$Artifact) {
  $paths=[Collections.Generic.List[string]]::new()
  $pending=[Collections.Generic.Stack[string]]::new();$pending.Push($Root)
  while($pending.Count -gt 0) {
    $directory=$pending.Pop()
    foreach($item in (Get-ChildItem -LiteralPath $directory -Force)) {
      if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "Runtime reparse entry: $($item.FullName)" }
      if ($item.PSIsContainer) { $pending.Push($item.FullName); continue }
      if (Test-DistributionFile $Name $Root $item) { $paths.Add($item.FullName) }
    }
  }
  Assert-DistributionRequiredFiles $Name $paths
  $files = @($paths | Sort-Object | ForEach-Object {
    [ordered]@{path=$_;sha256=(Get-FileHash -LiteralPath $_ -Algorithm SHA256).Hash.ToLowerInvariant()}
  })
  if ($files.Count -eq 0) { throw "Empty runtime inventory: $Name" }
  $inventory = $Utf8.GetBytes(($files | ConvertTo-Json -Depth 8 -Compress))
  [ordered]@{name=$Name;revision=$Revision;artifact=$Artifact;sha256=(Get-SHA256 $inventory);files=$files}
}
function Resolve-ToolPath([string]$Path) {
  # Hosted runners expose the tool cache through a C: junction to D:. Bind the
  # final target so every selected tool path is free of reparse components.
  $resolved=[IO.Path]::GetPathRoot($Path)
  foreach ($part in $Path.Substring($resolved.Length).Split([IO.Path]::DirectorySeparatorChar)) {
    $resolved=Join-Path $resolved $part
    $item=Get-Item -LiteralPath $resolved -Force
    if ($item.LinkTarget) { $resolved=$item.ResolveLinkTarget($true).FullName }
  }
  $resolved
}
function Get-OwnerToolchain {
  $go = Resolve-ToolPath (Get-Command go.exe -ErrorAction Stop).Source
  $git = Resolve-ToolPath (Get-Command git.exe -ErrorAction Stop).Source
  $bash = Get-ChildItem -Path "$env:ProgramFiles\Git\bin\bash.exe","$env:ProgramFiles\Git\usr\bin\bash.exe" -ErrorAction SilentlyContinue | Select-Object -First 1
  if ($null -eq $bash) { throw 'Git Bash executable unavailable' }
  $bash = Resolve-ToolPath $bash.FullName
  $gcc = Resolve-ToolPath (Get-Command gcc.exe -ErrorAction Stop).Source
  $pwsh = Resolve-ToolPath (Get-Command pwsh.exe -ErrorAction Stop).Source
  $toolBindings=@((Get-AbsoluteToolBinding $go),(Get-AbsoluteToolBinding $git),(Get-AbsoluteToolBinding $bash),(Get-AbsoluteToolBinding $gcc),(Get-AbsoluteToolBinding $pwsh))
  $goRoot = Resolve-ToolPath (Invoke-CheckedNative $go @('env','GOROOT') 'go env GOROOT').Trim()
  $goVersion = (Invoke-CheckedNative $go @('version') 'go version').Trim().Split(' ')[2]
  if ($goVersion -cne 'go1.27.2') { throw "Unexpected Go version: $goVersion" }
  if ([string]::IsNullOrWhiteSpace([string]$env:ImageOS) -or [string]::IsNullOrWhiteSpace([string]$env:ImageVersion)) { throw 'Hosted runner image identity unavailable' }
  [ordered]@{
    go=$go;git=$git;bash=$bash;gcc=$gcc;pwsh=$pwsh;bindings=$toolBindings;goRoot=$goRoot;goVersion=$goVersion
    gitRoot=(Split-Path (Split-Path $git -Parent) -Parent);gccRoot=(Split-Path $gcc -Parent);pwshRoot=(Split-Path $pwsh -Parent)
  }
}
function Copy-WorkflowSource([string]$Repo,[string]$SourceDir) {
  $sourcePaths=Get-WorkflowSourcePaths $Repo
  foreach ($relative in $sourcePaths) {
    $sourceBinding=Get-FileBinding $Repo $relative
    $destination=Join-Path $SourceDir $relative; [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($destination)) | Out-Null
    Copy-Item -LiteralPath (Join-Path $Repo $relative) -Destination $destination
    $copiedHash=(Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($sourceBinding.sha256 -cne $copiedHash) { throw "Copied source changed during custody capture: $relative" }
  }
  if (@(Get-ChildItem -LiteralPath $SourceDir -File -Recurse -Force).Count -ne $sourcePaths.Count) { throw 'Copied source inventory has unexpected or missing files' }
}
function Build-NativeCleanupTools([string]$Go,[string]$Owner,[string]$Verifier) {
  & $Go build -trimpath -buildvcs=true -o $Owner ./tools/nativecleanupowner
  if ($LASTEXITCODE -ne 0) { throw 'Owner build failed' }
  $env:CGO_ENABLED='0'
  & $Go build -trimpath -buildvcs=true -o $Verifier ./tools/nativecleanupcheck
  if ($LASTEXITCODE -ne 0) { throw 'Verifier build failed' }
  $env:CGO_ENABLED='1'
}
function New-RuntimePolicy($Tools,[string]$SourceSHA,[string]$CachePath,[string]$GoCache,[string]$GoModCache) {
  $git=$Tools.git;$gcc=$Tools.gcc;$pwsh=$Tools.pwsh;$goRoot=$Tools.goRoot;$goVersion=$Tools.goVersion
  $policy = [ordered]@{
    parents=@((Invoke-CheckedNative $git @('rev-list','--max-count=3',$SourceSHA) 'git rev-list') -split "`r?`n" | ForEach-Object { $_.Trim() } | Where-Object { $_ })
    go_version=$goVersion;goarch='amd64';distributions=@();environment=@()
    runner_image="$env:ImageOS/$env:ImageVersion";runner_revision=(Get-SHA1 $Utf8.GetBytes([string]$env:ImageVersion))
    setup_go_revision='b7ad1dad31e06c5925ef5d2fc7ad053ef454303e';checkout_revision='3d3c42e5aac5ba805825da76410c181273ba90b1'
  }
  $gccVersionLines=Invoke-CheckedNative $gcc @('--version') 'gcc --version'
  if ([string]::IsNullOrWhiteSpace($gccVersionLines)) { throw 'gcc version output missing' }
  $policy.distributions=@(
    (Get-Distribution 'go' $goRoot ("$goVersion/$env:GOARCH") 'actions/setup-go'),
    (Get-Distribution 'git-for-windows' $Tools.gitRoot (Invoke-CheckedNative $git @('--version') 'git --version').Trim() 'Git for Windows image'),
    (Get-Distribution 'native-cgo' $Tools.gccRoot $gccVersionLines.Trim() 'Windows hosted image'),
    (Get-Distribution 'powershell' $Tools.pwshRoot (Invoke-CheckedNative $pwsh @('-NoLogo','-NoProfile','-Command','$PSVersionTable.PSVersion.ToString()') 'pwsh version').Trim() 'Windows hosted image')
  )
  foreach ($binding in $Tools.bindings) {
    $matched=@($policy.distributions | ForEach-Object { $_.files } | Where-Object { $_.path -ceq $binding.path -and $_.sha256 -ceq $binding.sha256 })
    if ($matched.Count -ne 1) { throw "Selected tool changed or is absent from its distribution binding: $($binding.path)" }
  }
  $policy.environment=@("PATH=$env:PATH","SystemRoot=$env:SystemRoot","USERPROFILE=$env:USERPROFILE","TEMP=$CachePath","GOROOT=$goRoot","GOTOOLCHAIN=local","GOENV=off","GOWORK=off","GOFLAGS=","GOOS=windows","GOARCH=amd64","CGO_ENABLED=1","CC=$gcc","LOPPER_NATIVE_GIT=$git","LOPPER_NATIVE_BASH=$($Tools.bash)","GOCACHE=$GoCache","GOMODCACHE=$GoModCache")
  $policy
}
function Invoke-Owner {
  $repo = (Get-Location).Path
  $tools=Get-OwnerToolchain
  $go=$tools.go;$git=$tools.git;$gcc=$tools.gcc;$goRoot=$tools.goRoot;$goVersion=$tools.goVersion
  $sourceSHA = [string]$env:GITHUB_SHA
  if ($sourceSHA -cnotmatch '^[0-9a-f]{40}$') { throw 'Invalid event source SHA' }
  $sourceDir = Join-Path $TrustedRoot 'source'; $binDir=Join-Path $TrustedRoot 'bin'; $controlDir=Join-Path $TrustedRoot 'control'; $cacheDir=Join-Path $TrustedRoot 'cache'
  if (Test-Path $TrustedRoot) { throw 'Trusted root already exists' }
  foreach ($dir in @($sourceDir,$binDir,$cacheDir)) { [IO.Directory]::CreateDirectory($dir) | Out-Null }
  $sid=[Security.Principal.WindowsIdentity]::GetCurrent().User.Value
  $controlIdentity=Initialize-ControlDirectory $controlDir $sid
  Copy-WorkflowSource $repo $sourceDir
  $owner=Join-Path $binDir 'nativecleanupowner.exe'; $verifier=Join-Path $binDir 'nativecleanupcheck.exe'
  $invocationNonce=[Guid]::NewGuid().ToString('D')
  $cachePath=Join-Path $cacheDir $invocationNonce
  $goCache=Join-Path $cachePath 'go-build';$goModCache=Join-Path $cachePath 'go-mod';[IO.Directory]::CreateDirectory($goCache)|Out-Null;[IO.Directory]::CreateDirectory($goModCache)|Out-Null
  $env:PATH="$goRoot\bin;$($tools.gitRoot)\cmd;$($tools.gitRoot)\usr\bin;$($tools.gccRoot);$env:SystemRoot\System32"
  $env:TEMP=$cachePath;$env:GOCACHE=$goCache;$env:GOMODCACHE=$goModCache
  $env:GOOS='windows';$env:GOARCH='amd64';$env:CGO_ENABLED='1';$env:GOENV='off';$env:GOWORK='off';$env:GOFLAGS='';$env:GOTOOLCHAIN='local';$env:CC=$gcc
  Build-NativeCleanupTools $go $owner $verifier
  $verifierHash=(Get-FileHash $verifier -Algorithm SHA256).Hash.ToLowerInvariant()
  $policy=New-RuntimePolicy $tools $sourceSHA $cachePath $goCache $goModCache
  $policyBytes=$Utf8.GetBytes(($policy | ConvertTo-Json -Depth 16 -Compress));$policySHA=Get-SHA256 $policyBytes
  if ($policyBytes.Length -gt 900KB) { throw 'Bounded selected runtime policy exceeds custody document budget' }
  $control = Join-Path $TrustedRoot 'control'
  $sourceBindings=@(Get-WorkflowSourcePaths $repo | ForEach-Object { Get-FileBinding $sourceDir $_ })
  $artifacts=@((Get-FileBinding $TrustedRoot 'bin/nativecleanupowner.exe'),(Get-FileBinding $TrustedRoot 'bin/nativecleanupcheck.exe'))
  $cases=@(foreach($scheme in @('drive','unc')) { foreach($leaf in @('equivalent','equivalent-backslashes','equivalent-case','unrelated','shadowed','missing','drive-relative','failed-config')) { "TestHooksCleanupNativeWindows/$scheme/$leaf" } })
  $inventoryBytes=$Utf8.GetBytes(($cases -join "`n")+"`n")
  $manifest=[ordered]@{schema='native-cleanup-custody/v1';owner_source=$sourceSHA;policy=$policy;sources=$sourceBindings;artifacts=$artifacts;cases=$cases;inventory_sha256=(Get-SHA256 $inventoryBytes);control_volume_file_id=$controlIdentity.id;control_acl_sha256=$controlIdentity.acl;control_directory=$control}
  $manifestPath=Join-Path $TrustedRoot 'custody.json';$manifestBytes=$Utf8.GetBytes(($manifest|ConvertTo-Json -Depth 24 -Compress))
  if ($manifestBytes.Length -gt 1MB) { throw 'Custody manifest exceeds the fixed checker document limit' }
  Write-Durable $manifestPath $manifestBytes
  $manifestSHA=Get-SHA256 $manifestBytes
  # The subject is the event checkout itself, bound to the same revision the
  # custody manifest records; the cache root is private to this invocation.
  $ownerArgs=@('run','--trusted-root',$TrustedRoot,'--manifest',$manifestPath,'--manifest-sha256',$manifestSHA,'--verifier',$verifier,'--verifier-sha256',$verifierHash,'--expected-source-sha',$sourceSHA,'--expected-policy-sha256',$policySHA,'--subject',$repo,'--subject-sha',$sourceSHA,'--run-id',[string]$env:GITHUB_RUN_ID,'--run-attempt',[string]$env:GITHUB_RUN_ATTEMPT,'--invocation-nonce',$invocationNonce,'--sid',$sid,'--allocation-parent',$cachePath,'--go-cache',$goCache,'--go-mod-cache',$goModCache)
  $psi=[Diagnostics.ProcessStartInfo]::new();$psi.FileName=$owner;$psi.WorkingDirectory=$repo;$psi.UseShellExecute=$false;$psi.RedirectStandardOutput=$true;$psi.RedirectStandardError=$true
  foreach($arg in $ownerArgs){$psi.ArgumentList.Add($arg)}
  $process=[Diagnostics.Process]::new();$process.StartInfo=$psi
  try {
    Initialize-BoundedOwnerCapture
    if (-not $process.Start()) { throw 'Owner process did not start' }
    $script:Result.owner_launch_status='started'
    $outTask=[NativeCleanupOutputCapture]::Drain($process.StandardOutput.BaseStream,65536)
    $errTask=[NativeCleanupOutputCapture]::Drain($process.StandardError.BaseStream,65536)
    $process.WaitForExit();$outTask.Wait();$errTask.Wait()
    $exit=$process.ExitCode
    # Persist the observed owner exit and bounded raw control streams before
    # reading any owner-created join or control JSON. The subject remains
    # unknown until those independent records bind successfully.
    Write-OwnerTerminalReceipt $outTask.Result $errTask.Result $exit
    $script:Result.setup_status='completed';$script:Result.owner_exit_code=$exit
    $script:Result.go=[ordered]@{path=$go;version=$goVersion;sha256=(Get-FileHash $go -Algorithm SHA256).Hash.ToLowerInvariant()}
    $script:Result.git=[ordered]@{path=$git;version=(& $git --version).Trim();sha256=(Get-FileHash $git -Algorithm SHA256).Hash.ToLowerInvariant()}
    $script:Result.bash=[ordered]@{path=$tools.bash;sha256=(Get-FileHash $tools.bash -Algorithm SHA256).Hash.ToLowerInvariant()}
    $script:Result.sid=$sid
    $controlStdout=Join-Path $control 'subject.stdout.raw';$controlStderr=Join-Path $control 'subject.stderr.raw'
    Save-OwnerRawStreams $controlStdout $controlStderr
    $joinPath=Join-Path $control 'join.json'
    $join=Read-OwnerJoin $joinPath $sourceSHA $controlStdout $controlStderr
    $joinSHA=(Get-FileHash -LiteralPath $joinPath -Algorithm SHA256).Hash.ToLowerInvariant()
    Read-OwnerTeardown $control $joinSHA
    Assert-OwnerControlOutput $outTask.Result $errTask.Result $joinPath $joinSHA
    Complete-OwnerReceipt $join $joinSHA $exit
    if ($exit -ne 0) { throw "Owner exited $exit" }
  } finally { $process.Dispose() }
}
function Save-OwnerRawStreams([string]$ControlStdout,[string]$ControlStderr) {
  if ((Test-Path $ControlStdout) -and (Test-Path $ControlStderr)) {
    [IO.File]::Copy($ControlStdout,$script:StdoutPath,$true);[IO.File]::Copy($ControlStderr,$script:StderrPath,$true)
    foreach ($streamPath in @($script:StdoutPath,$script:StderrPath)) { $durable=[IO.File]::Open($streamPath,[IO.FileMode]::Open,[IO.FileAccess]::ReadWrite,[IO.FileShare]::Read);try{$durable.Flush($true)}finally{$durable.Dispose()} }
    $script:Result.cleanup_status='pending'
  } else { $script:Result.cleanup_status='incomplete';$script:Result.failure='owner did not retain both raw test streams' }
  if (-not (Test-Path $ControlStdout -PathType Leaf) -or -not (Test-Path $ControlStderr -PathType Leaf)) { throw 'Owner raw test streams missing; subject exit remains unknown' }
}
function Read-OwnerJoin([string]$joinPath,[string]$SourceSHA,[string]$ControlStdout,[string]$ControlStderr) {
  if (-not (Test-Path $joinPath -PathType Leaf)) { throw 'Owner join missing; subject exit remains unknown' }
  $join=Get-Content -LiteralPath $joinPath -Raw | ConvertFrom-Json -AsHashtable
  if ($join.schema -cne 'native-cleanup-join/v1' -or $join.run_id -cne [string]$env:GITHUB_RUN_ID -or $join.run_attempt -cne [string]$env:GITHUB_RUN_ATTEMPT -or $join.owner_source -cne $SourceSHA -or -not $join.original_cleanup -or -not $join.root_wait -or -not $join.drains -or -not $join.helpers_joined) { throw 'Owner join binding mismatch' }
  foreach ($pair in @(@('stdout',$ControlStdout),@('stderr',$ControlStderr))) {
    if ($join[$pair[0]].path -cne $pair[1] -or $join[$pair[0]].sha256 -cne (Get-FileHash -LiteralPath $pair[1] -Algorithm SHA256).Hash.ToLowerInvariant()) { throw "Owner $($pair[0]) stream binding mismatch" }
  }
  $join
}
function Read-OwnerTeardown([string]$Control,[string]$JoinSHA) {
  $allocationPath=Join-Path $Control 'allocation.json'
  if (Test-Path $allocationPath) {
    $allocation=Get-Content -LiteralPath $allocationPath -Raw | ConvertFrom-Json -AsHashtable
    $script:Result.share=[ordered]@{name=$allocation.share;path=$allocation.path;temporary=$allocation.temporary;sid=$allocation.sid;share_acl_sha256=$allocation.share_acl_sha256;ntfs_acl_sha256=$allocation.ntfs_acl_sha256}
  }
  $teardownPath=Join-Path $Control 'teardown-finally.json'
  if (Test-Path $teardownPath) {
    $teardown=Get-Content -LiteralPath $teardownPath -Raw | ConvertFrom-Json -AsHashtable
    if ($teardown.schema -cne 'native-cleanup-teardown/v1' -or -not $teardown.share_absent -or -not $teardown.directory_absent -or $teardown.join_sha256 -cne $JoinSHA) { throw 'Owner teardown receipt mismatch' }
    $script:Result.cleanup_status='owner_completed'
  } else { $script:Result.cleanup_status='incomplete' }
}
function Assert-OwnerControlOutput($OwnerStdout,$OwnerStderr,[string]$JoinPath,[string]$JoinSHA) {
  if ($OwnerStdout.Truncated -or $OwnerStderr.Truncated) { throw 'Owner control output exceeded bounded parse limit; subject status remains unknown' }
  $controlOutput=$Utf8.GetString($OwnerStdout.Prefix)
  $controlLines=@($controlOutput -split "`r?`n" | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
  if ($controlLines.Count -ne 1) { throw 'Owner original-control output is missing or ambiguous' }
  $controlMessage=$controlLines[0] | ConvertFrom-Json -AsHashtable
  if ($controlMessage.schema -cne 'native-cleanup-control/v1' -or $controlMessage.join_record -cne $JoinPath -or $controlMessage.join_record_sha256 -cne $JoinSHA) { throw 'Owner original-control does not bind the durable join record' }
}
function Complete-OwnerReceipt($Join,[string]$JoinSHA,[int]$Exit) {
  $subjectLaunched=$Join.workload -eq 'subject-go'
  $script:Result.exit_code=[int]$Join.status
  $script:Result.test_status=if($subjectLaunched -and $Join.status -eq 0){'passed'}elseif($subjectLaunched){'failed'}else{'not_launched'}
  $script:Result.launch_status=if($subjectLaunched){'launched'}else{'not_launched'}
  $script:Result.phase=$Join.workload
  $script:Result.join_record_sha256=$JoinSHA
  Update-StreamBindings $script:Result
  if ($Exit -eq 0 -and $script:Result.cleanup_status -eq 'owner_completed' -and $script:Result.test_status -eq 'passed') { $script:Result.failure='' }
  elseif ([string]::IsNullOrEmpty([string]$script:Result.failure) -or $script:Result.failure -eq 'Owner process terminated; subject status awaits authenticated join') { $script:Result.failure="Owner/test/cleanup did not complete successfully (owner exit $Exit)" }
  Write-Receipt $script:Result
}

try {
  Assert-EvidenceDirectory
  switch ($Mode) {
    'bootstrap' {
      if (Test-Path $EvidenceRoot) { throw 'Evidence output already exists' }
      [IO.Directory]::CreateDirectory($EvidenceRoot) | Out-Null
      Save-EmptyStreams
      $script:Result=New-Receipt 'bootstrap' 'ready' $null ''
      Write-Receipt $script:Result
    }
    'self-test' {
      if (-not (Test-Path $script:ResultPath)) { throw 'Bootstrap receipt missing' }
      $base=Get-Content -LiteralPath $script:ResultPath -Raw | ConvertFrom-Json -AsHashtable
      Assert-ReceiptIdentity $base
      $script:Result=$base
      $controls=Test-ReceiptProtocol
      $helper=Join-Path $PSScriptRoot 'native-windows-hook-fixture.ps1'
      $sid=[Security.Principal.WindowsIdentity]::GetCurrent().User.Value
      $allocatorControls=@(& $helper -Mode self-test -TrustedRoot 'C:\unused-root' -Manifest 'C:\unused-root\custody.json' -ManifestSHA256 ('0'*64) -Verifier 'C:\unused-root\bin\nativecleanupcheck.exe' -VerifierSHA256 ('0'*64) -ExpectedSourceSHA ('0'*40) -ExpectedPolicySHA256 ('0'*64) -AllocationRecord 'C:\unused-root\control\allocation.json' -GoCache 'C:\unused-cache\go-build' -GoModCache 'C:\unused-cache\go-mod' -InvocationNonce ('0'*36) -RunID 'self-test' -RunAttempt '1' -SID $sid -AllocationParent 'C:\unused-cache')
      if ($allocatorControls.Count -ne 1 -or -not ([string]$allocatorControls[0]).StartsWith('allocator-self-test:')) { throw 'Allocator predicate self-test did not return its authenticated control result' }
      $script:Result.self_test=[ordered]@{status='passed';controls=$controls;allocator_controls=[string]$allocatorControls[0];failure=''}
      Write-Receipt $script:Result
    }
    'execute' {
      if (-not (Test-Path $script:ResultPath)) { throw 'Bootstrap receipt missing' }
      $script:Result=Get-Content -LiteralPath $script:ResultPath -Raw | ConvertFrom-Json -AsHashtable
      Assert-ReceiptIdentity $script:Result
      Invoke-Owner
    }
    'finally' {
      if (-not (Test-Path $script:ResultPath)) { throw 'Receipt missing during finalization' }
      $receipt=Get-Content -LiteralPath $script:ResultPath -Raw | ConvertFrom-Json -AsHashtable
      Assert-ReceiptIdentity $receipt
      if ($receipt.test_status -eq 'not_launched') { $receipt.cleanup_status='not_required' }
      if ($receipt.test_status -ne 'not_launched' -and $receipt.cleanup_status -ne 'owner_completed') { throw 'Owner cleanup is not authenticated; retain fixture' }
      Write-Receipt $receipt
    }
    'validate' {
      foreach ($path in @($script:ResultPath,$script:StdoutPath,$script:StderrPath)) { if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Required proof file missing: $path" } }
      $actualNames=@(Get-ChildItem -LiteralPath $EvidenceRoot -Force | ForEach-Object { $_.Name } | Sort-Object)
      if (($actualNames -join '|') -cne 'result.json|stderr.log|stdout.jsonl') { throw 'Evidence directory has missing or unexpected entries' }
      foreach ($path in @($script:ResultPath,$script:StdoutPath,$script:StderrPath)) { if (((Get-Item -LiteralPath $path -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'Proof file is a reparse point' } }
      $receipt=Get-Content -LiteralPath $script:ResultPath -Raw | ConvertFrom-Json -AsHashtable
      $stdoutBytes=[IO.File]::ReadAllBytes($script:StdoutPath);$stderrBytes=[IO.File]::ReadAllBytes($script:StderrPath)
      Assert-ReceiptReadyToPublish $receipt $stdoutBytes $stderrBytes
      foreach($path in @($script:ResultPath,$script:StdoutPath,$script:StderrPath)) { if ((Get-Item -LiteralPath $path -Force).PSIsContainer) { throw 'Proof entry is not a regular file' } }
      Add-Content -LiteralPath $env:GITHUB_STEP_SUMMARY -Value "Native proof receipt SHA256: $((Get-FileHash $script:ResultPath -Algorithm SHA256).Hash.ToLowerInvariant())"
    }
  }
} catch {
  if ($Mode -in @('bootstrap','execute','self-test') -and (Test-Path $EvidenceRoot)) {
    try {
      if (-not (Test-Path $script:StdoutPath)) { Write-Durable $script:StdoutPath ([byte[]]@()) }
      if (-not (Test-Path $script:StderrPath)) { Write-Durable $script:StderrPath ([byte[]]@()) }
      if ($null -eq $script:Result -and (Test-Path $script:ResultPath)) { $script:Result=Get-Content -LiteralPath $script:ResultPath -Raw | ConvertFrom-Json -AsHashtable }
      if ($null -eq $script:Result) { $script:Result=New-Receipt 'failed' 'failed' $null $_.Exception.Message }
      if ($Mode -eq 'self-test') { $script:Result.self_test=[ordered]@{status='failed';failure=$_.Exception.Message} }
      if ($Mode -eq 'execute' -and $script:Result.test_status -eq 'passed') { $script:Result.test_status='unknown';$script:Result.exit_code=$null;$script:Result.cleanup_status='incomplete' }
      $script:Result.failure=$_.Exception.Message;Update-StreamBindings $script:Result;Write-Receipt $script:Result
    } catch { [Console]::Error.WriteLine("Could not finalize proof receipt: $($_.Exception.Message)") }
  }
  [Console]::Error.WriteLine("Native Windows fixture $Mode failed: $($_.Exception.Message)")
  exit 1
}
