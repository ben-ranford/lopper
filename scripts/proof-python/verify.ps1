# W must authenticate this script inline against an independently retained O
# digest before execution. Parameters come from literal trusted step outputs.
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string] $Verifier,
    [Parameter(Mandatory)][string] $VerifierSha256,
    [Parameter(Mandatory)][string] $Bootstrap,
    [Parameter(Mandatory)][string] $BootstrapSha256,
    [Parameter(Mandatory)][string] $Receipt,
    [Parameter(Mandatory)][string] $ReceiptSha256,
    [Parameter(Mandatory)][string] $Go,
    [Parameter(Mandatory)][string] $GoSha256,
    [Parameter(Mandatory)][string] $CanonicalRoot,
    [Parameter(Mandatory)][string] $PythonSha256,
    [Parameter(Mandatory)][string] $Python3Sha256,
    [Parameter(Mandatory)][string] $Repository,
    [Parameter(Mandatory)][string[]] $ProofArguments
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if (-not $IsWindows) { throw 'Native Windows is required' }
function Assert-IndependentFile([string] $Path, [string] $Expected) {
    if ($Expected -cnotmatch '^[0-9a-f]{64}$') { throw 'Independent digest missing or malformed' }
    if (-not [System.IO.Path]::IsPathFullyQualified($Path)) { throw 'Independent file path must be absolute' }
    $item = Get-Item -LiteralPath $Path
    if ($item.PSIsContainer -or ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint)) { throw 'Independent file is not regular' }
    if ((Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant() -cne $Expected) { throw 'Independent file was substituted' }
}
$bin = Split-Path -Parent $Go
if ($CanonicalRoot -cne (Join-Path $bin 'lopper-proof-python')) { throw 'Independent runtime root binding differs' }
$python = Join-Path $bin 'python.exe'
$python3 = Join-Path $bin 'python3.exe'
Assert-IndependentFile $Verifier $VerifierSha256
Assert-IndependentFile $Bootstrap $BootstrapSha256
Assert-IndependentFile $Receipt $ReceiptSha256
Assert-IndependentFile $Go $GoSha256
Assert-IndependentFile $python $PythonSha256
Assert-IndependentFile $python3 $Python3Sha256
& $Verifier joined-command $Receipt $ReceiptSha256 $Go $GoSha256 $Repository @ProofArguments
if ($LASTEXITCODE -ne 0) { throw 'Proof, owned-descendant join or provider custody failed; provider retained' }
Assert-IndependentFile $Verifier $VerifierSha256
Assert-IndependentFile $Bootstrap $BootstrapSha256
Assert-IndependentFile $Receipt $ReceiptSha256
Assert-IndependentFile $Go $GoSha256
Assert-IndependentFile $python $PythonSha256
Assert-IndependentFile $python3 $Python3Sha256
# No deletion is performed here. W may perform scoped cleanup only after this
# joined result and its own independent inline post-authentication both succeed.
