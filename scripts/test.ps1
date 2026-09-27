<#
.SYNOPSIS
    Runs the Go test suite and prints a pass/fail scoreboard.

.DESCRIPTION
    Wraps `go test -json` so every test run ends with a clear summary:
    how many tests ran, how many passed, failed, or were skipped -- per
    package and overall. Plain `go test` only prints PASS/FAIL per test
    and ok/FAIL per package with no final tally, which gets hard to scan
    once there are several packages and dozens of tests.

.PARAMETER Path
    Package path to test. Defaults to ./... (everything).

.PARAMETER Race
    Enable the race detector. Defaults to $true -- this project's
    concurrency claims are only meaningful when checked with -race.

.EXAMPLE
    ./scripts/test.ps1
    ./scripts/test.ps1 -Path ./internal/booking/... -Race:$false
#>
param(
    [string]$Path = "./...",
    [bool]$Race = $true
)

# Point Go at the 64-bit mingw-w64 compiler that -race needs, without
# touching the system PATH (see DESIGN.md). Harmless to set even when
# -race is off.
$env:CC = "C:\Users\siddh\AppData\Local\Microsoft\WinGet\Packages\BrechtSanders.WinLibs.POSIX.UCRT_Microsoft.Winget.Source_8wekyb3d8bbwe\mingw64\bin\gcc.exe"

$goArgs = @("test", "-json", $Path)
if ($Race) { $goArgs += "-race" }

Write-Host "Running: go $($goArgs -join ' ')" -ForegroundColor DarkGray
Write-Host ""

$passed = 0
$failed = 0
$skipped = 0
$failedNames = @()

# `go test -json` prints one JSON object per line. A line with a non-empty
# Test field and Action pass/fail/skip is one individual test's result;
# lines with an empty Test field are package-level events, which we don't
# count here (a package can "fail" only because a test inside it failed,
# so counting the test itself is enough).
& go @goArgs 2>&1 | ForEach-Object {
    try {
        $event = $_ | ConvertFrom-Json -ErrorAction Stop
    } catch {
        # A line go itself couldn't wrap as JSON (rare, e.g. a build error) --
        # show it as-is rather than silently dropping it.
        Write-Host $_
        return
    }

    # Re-print the underlying text output (===RUN, --- PASS, container logs,
    # etc.) so this still reads like normal `go test -v`, just followed by
    # a scoreboard at the end -- the -json wrapping is invisible.
    if ($event.Output) {
        Write-Host -NoNewline $event.Output
    }

    if (-not $event.Test) { return }

    switch ($event.Action) {
        "pass" { $passed++ }
        "fail" { $failed++; $failedNames += "$($event.Package) :: $($event.Test)" }
        "skip" { $skipped++ }
    }
}

$total = $passed + $failed + $skipped

Write-Host ""
Write-Host "==================== TEST SCOREBOARD ====================" -ForegroundColor Cyan
Write-Host ("Total:   {0}" -f $total)
Write-Host ("Passed:  {0}" -f $passed) -ForegroundColor Green
Write-Host ("Failed:  {0}" -f $failed) -ForegroundColor $(if ($failed -gt 0) { "Red" } else { "Gray" })
Write-Host ("Skipped: {0}" -f $skipped) -ForegroundColor Yellow

if ($failed -gt 0) {
    Write-Host ""
    Write-Host "Failed tests:" -ForegroundColor Red
    $failedNames | ForEach-Object { Write-Host "  - $_" -ForegroundColor Red }
    exit 1
}

Write-Host "==========================================================" -ForegroundColor Cyan
exit 0
