$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$repo = Join-Path $env:GITHUB_WORKSPACE 'historical-runtime'
$assets = Join-Path $env:GITHUB_WORKSPACE 'diagnostic-assets/.diagnostics/issue1604'
$evidence = Join-Path $env:GITHUB_WORKSPACE 'issue1604-evidence'
New-Item -ItemType Directory -Force -Path $evidence | Out-Null
$manifest = Get-Content -Raw (Join-Path $assets 'instrumentation-manifest.json') | ConvertFrom-Json
$encodedPatch = Get-Content -Raw (Join-Path $assets 'historical-instrumentation.json') | ConvertFrom-Json
if ($encodedPatch.encoding -ne 'UTF-8') { throw 'Unsupported historical patch encoding' }
$patch = Join-Path $evidence 'historical-instrumentation.patch'
[IO.File]::WriteAllText($patch, $encodedPatch.patch, [Text.UTF8Encoding]::new($false))
$patchHash = (Get-FileHash -Algorithm SHA256 $patch).Hash.ToLowerInvariant()
if ($patchHash -ne $manifest.patch_sha256) { throw 'Historical instrumentation patch hash mismatch' }
Set-Location $repo
$actual = (git rev-parse HEAD).Trim()
if ($LASTEXITCODE -ne 0 -or $actual -ne $manifest.historical_merge) { throw 'Historical checkout identity mismatch' }
git apply --check $patch
if ($LASTEXITCODE -ne 0) { throw 'Historical instrumentation does not apply cleanly' }
git apply $patch
if ($LASTEXITCODE -ne 0) { throw 'Historical instrumentation apply failed' }
Copy-Item (Join-Path $assets 'instrumentation-manifest.json') (Join-Path $evidence 'instrumentation-manifest.json')
$binary = Join-Path $env:RUNNER_TEMP 'issue1604-historical-runtime.test.exe'
go test -c -o $binary ./internal/runtime
if ($LASTEXITCODE -ne 0) { throw 'Historical instrumented runtime did not compile' }
$selection = '^(TestStartCommandConfiguresWindowsJobCancellation|TestWindowsCommandCancellationWithoutProcess|TestStartCommandCancellationTerminatesWindowsDescendant)$'
$runs = @()
$anyFailure = $false
try {
  for ($attempt = 1; $attempt -le 20; $attempt++) {
    $started = [DateTime]::UtcNow.ToString('o')
    $log = Join-Path $evidence ('attempt-{0:D2}.log' -f $attempt)
    & $binary "-test.run=$selection" '-test.count=1' '-test.timeout=30s' '-test.v' 2>&1 | Tee-Object -FilePath $log
    $testExitCode = $LASTEXITCODE
    $runs += [ordered]@{ attempt = $attempt; started_utc = $started; finished_utc = [DateTime]::UtcNow.ToString('o'); exit_code = $testExitCode; log = [IO.Path]::GetFileName($log) }
    if ($testExitCode -ne 0) { $anyFailure = $true }
  }
} finally {
  [ordered]@{ historical_merge = $actual; patch_sha256 = $patchHash; runner_image = $env:ImageVersion; runner_os = $env:RUNNER_OS; go_version = (go version); selection = $selection; repetitions = 20; per_process_timeout = '30s'; marker_deadline = '10s'; any_failure = $anyFailure; runs = $runs } | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $evidence 'summary.json')
}
if ($anyFailure) { exit 1 }
