$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$failure = $null
if (-not $IsWindows) { throw 'Runtime path proof requires native Windows' }
$baseline = 'c47703c6b1c7e0a9b813061beb44d98fb972b101'
$baselineHash = 'e9ea580923770e10f4ec3e62d09c473993d068f2f58139ee0714ae4c1b6c54e2'
$testName = 'TestTrustedSearchDirsWindowsExtendedPaths'
$repo = Split-Path -Parent $PSScriptRoot
$go = Join-Path $env:REGRESSION_PROOF_GO_ROOT 'bin/go.exe'
$work = Join-Path $env:RUNNER_TEMP ('runtime-path-proof-' + [guid]::NewGuid().ToString())
$originalLocation = Get-Location
$originalToolchain = $env:GOTOOLCHAIN
$originalFlags = $env:GOFLAGS
$sourceHashes = @{}
foreach ($relative in @('internal/runtime/capture_command.go', 'internal/runtime/capture_searchdirs_other.go', 'internal/runtime/capture_searchdirs_windows.go', 'internal/runtime/capture_searchdirs_windows_test.go')) {
    $path = Join-Path $repo $relative
    $sourceHashes[$path] = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash
}
function Assert-SourceCustody {
    foreach ($path in $sourceHashes.Keys) {
        if ((Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash -ne $sourceHashes[$path]) {
            throw "Runtime proof changed candidate source: $path"
        }
    }
    Write-Output ('Runtime source custody verified: ' + ($sourceHashes | ConvertTo-Json -Compress))
}
try {
    Set-Location -LiteralPath $repo
    $env:GOTOOLCHAIN = 'local'
    $env:GOFLAGS = '-buildvcs=false'
    New-Item -ItemType Directory -Path $work | Out-Null
    git cat-file -e "${baseline}^{commit}"
    if ($LASTEXITCODE -ne 0) {
        git fetch --no-tags origin $baseline
        if ($LASTEXITCODE -ne 0) { throw 'Cannot fetch immutable runtime baseline' }
        git cat-file -e "${baseline}^{commit}"
        if ($LASTEXITCODE -ne 0) { throw 'Runtime baseline commit is unavailable' }
    }
    $archive = Join-Path $work 'baseline.tar'
    git -c core.autocrlf=false -c core.eol=lf archive --format=tar "--output=$archive" $baseline -- internal/runtime/capture_command.go
    if ($LASTEXITCODE -ne 0) { throw 'Cannot archive immutable runtime baseline' }
    tar -xf $archive -C $work
    if ($LASTEXITCODE -ne 0) { throw 'Cannot extract runtime baseline' }
    $oldSource = Join-Path $work 'internal/runtime/capture_command.go'
    if ((Get-FileHash -LiteralPath $oldSource -Algorithm SHA256).Hash.ToLowerInvariant() -ne $baselineHash) {
        throw 'Runtime baseline source digest mismatch'
    }
    $replacements = @{}
    $replacements[(Join-Path $repo 'internal/runtime/capture_command.go')] = $oldSource
    $overlay = Join-Path $work 'overlay.json'
    @{ Replace = $replacements } | ConvertTo-Json | Set-Content -LiteralPath $overlay -Encoding utf8NoBOM
    Assert-SourceCustody
    & $go test '-overlay' $overlay ./internal/runtime -run '^$' -count=1
    if ($LASTEXITCODE -ne 0) { throw 'Baseline must compile before behavioral proof' }
    $oldOutput = & $go test -overlay $overlay ./internal/runtime -json -count=1 -run "^${testName}$" 2>&1
    $oldExit = $LASTEXITCODE
    $oldOutput | Write-Output
    $events = @($oldOutput | ForEach-Object { $_ | ConvertFrom-Json -ErrorAction Stop })
    $namedFailure = @($events | Where-Object { $_.Action -eq 'fail' -and $_.Test -eq $testName })
    $admissionFailure = @($events | Where-Object { $_.Test -eq $testName -and $_.Output -like '*valid root dropped:*' })
    if ($oldExit -ne 1 -or $namedFailure.Count -ne 1 -or $admissionFailure.Count -eq 0) {
        throw 'Baseline did not reproduce the named extended-root admission failure'
    }
    Assert-SourceCustody
    $headOutput = & $go test ./internal/runtime -run '^Test(TrustedSearchDirsWindows(ExtendedPaths|RootsAndUnusableEntries)|RuntimeSearchWindowsExecutableHelper)$' -count=1 -json 2>&1
    $headExit = $LASTEXITCODE
    $headOutput | Write-Output
    if ($headExit -ne 0) { throw 'Native head runtime path contracts failed' }
    $headEvents = @($headOutput | ForEach-Object { $_ | ConvertFrom-Json -ErrorAction Stop })
    foreach ($name in @($testName, 'TestTrustedSearchDirsWindowsRootsAndUnusableEntries', 'TestRuntimeSearchWindowsExecutableHelper')) {
        $passes = @($headEvents | Where-Object { $_.Action -eq 'pass' -and $_.Test -eq $name })
        if ($passes.Count -ne 1) { throw "Native head did not pass required test: $name" }
    }
    if (@($headEvents | Where-Object { $_.Action -eq 'skip' -or $_.Action -eq 'fail' }).Count -ne 0) {
        throw 'Native head proof cannot contain skipped or failed cases'
    }
    Assert-SourceCustody
} catch {
    $failure = $_
} finally {
    $env:GOTOOLCHAIN = $originalToolchain
    $env:GOFLAGS = $originalFlags
    try {
        Set-Location -LiteralPath $originalLocation
        if (Test-Path -LiteralPath $work) { Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction Stop }
    } catch {
        if ($null -ne $failure) {
            throw [System.AggregateException]::new('Runtime proof and cleanup failed', [System.Exception[]]@($failure.Exception, $_.Exception))
        }
        throw
    }
}
if ($null -ne $failure) { throw $failure }
