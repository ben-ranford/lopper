# Trusted O source only. W must authenticate this file and source archive before
# invoking it, before any subject Make/native code. No setup action is invoked.
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string] $TrustedSource,
    [Parameter(Mandatory)][string] $Go,
    [Parameter(Mandatory)][string] $GoSha256,
    [Parameter(Mandatory)][string] $RunnerCache,
    [AllowEmptyString()][string] $AgentCache = ''
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if (-not $IsWindows -or [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -ne 'X64') { throw 'Native Windows x64 is required' }
if ($GoSha256 -cnotmatch '^[0-9a-f]{64}$' -or (Get-FileHash -LiteralPath $Go -Algorithm SHA256).Hash.ToLowerInvariant() -cne $GoSha256) { throw 'Independent Go identity mismatch' }
if (-not [System.IO.Path]::IsPathFullyQualified($Go) -or [System.IO.Path]::GetFileName($Go) -cne 'go.exe') { throw 'Absolute captured Go executable is required' }
$bin = Split-Path -Parent $Go
$root = Join-Path $bin 'lopper-proof-python'
$verifier = Join-Path $bin 'lopper-proof-python-verify.exe'
$receipt = Join-Path $bin 'lopper-proof-python.receipt'
$aliases = @((Join-Path $bin 'python.exe'), (Join-Path $bin 'python3.exe'))
foreach ($path in @($root, $verifier, $receipt) + $aliases) { if (Test-Path -LiteralPath $path) { throw "Provider destination collision: $path" } }
$keys = @('GOROOT','GOENV','GOWORK','GOFLAGS','GOTOOLCHAIN','GOCACHEPROG','GOAUTH','GOPROXY','GOSUMDB','GOOS','GOARCH','CGO_ENABLED')
$saved = @{}
foreach ($key in $keys) { $saved[$key] = [Environment]::GetEnvironmentVariable($key, 'Process') }
$previous = Get-Location
try {
    $env:GOROOT = Split-Path -Parent $bin
    $env:GOENV = 'off'; $env:GOWORK = 'off'; $env:GOFLAGS = ''; $env:GOTOOLCHAIN = 'local'
    $env:GOCACHEPROG = ''; $env:GOAUTH = 'off'; $env:GOPROXY = 'off'; $env:GOSUMDB = 'off'
    $env:GOOS = 'windows'; $env:GOARCH = 'amd64'; $env:CGO_ENABLED = '0'
    Set-Location -LiteralPath $TrustedSource
    & $Go build -trimpath -buildvcs=false -o $verifier ./tools/proofpython
    if ($LASTEXITCODE -ne 0) { throw 'Trusted verifier build failed' }
    # This performs read-only presence/completion/canonical-root checks. A miss
    # returns before runtime copying; W must perform the same gate before setup-python.
    $cached = & $verifier cache $RunnerCache $AgentCache
    if ($LASTEXITCODE -ne 0) { throw 'Completed official cached Python is unavailable' }
    & $verifier copy $RunnerCache $AgentCache $root
    if ($LASTEXITCODE -ne 0) { throw 'Private runtime materialisation failed' }
    $interpreter = Join-Path $root 'python.exe'
    if ($interpreter.Contains('"') -or $interpreter.Contains("`n") -or $interpreter.Contains("`r")) { throw 'Invalid compiled interpreter path' }
    $linker = '-X "main.transportExecutable=' + $interpreter + '"'
    & $Go build -trimpath -buildvcs=false -ldflags $linker -o $aliases[0] ./tools/proofpython
    if ($LASTEXITCODE -ne 0) { throw 'Fixed Python transport build failed' }
    [System.IO.File]::Copy($aliases[0], $aliases[1], $false)
    & $verifier capture $root $bin $receipt
    if ($LASTEXITCODE -ne 0) { throw 'Native Python capture failed' }
    # W retains this result as independent pre-Make outputs, never as mutable
    # GITHUB_ENV defaults or a receipt-selected expected digest.
    [ordered]@{
        source_cache = $cached; root = $root; go = $Go; go_sha256 = $GoSha256; verifier = $verifier
        verifier_sha256 = (Get-FileHash -LiteralPath $verifier -Algorithm SHA256).Hash.ToLowerInvariant()
        bootstrap_sha256 = (Get-FileHash -LiteralPath $PSCommandPath -Algorithm SHA256).Hash.ToLowerInvariant()
        receipt = $receipt; receipt_sha256 = (Get-FileHash -LiteralPath $receipt -Algorithm SHA256).Hash.ToLowerInvariant()
        python_sha256 = (Get-FileHash -LiteralPath $aliases[0] -Algorithm SHA256).Hash.ToLowerInvariant()
        python3_sha256 = (Get-FileHash -LiteralPath $aliases[1] -Algorithm SHA256).Hash.ToLowerInvariant()
    } | ConvertTo-Json -Compress
} finally {
    Set-Location -LiteralPath $previous.Path
    foreach ($key in $keys) { [Environment]::SetEnvironmentVariable($key, $saved[$key], 'Process') }
}
