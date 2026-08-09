<#
.SYNOPSIS
Get a fresh checkout ready to build and test entirely offline: verify the Go
toolchain matches tools/go-version, then warm the module cache for the main
module and every separate go.work module.

.DESCRIPTION
This is the "toolchain lock" half of the stabilization baseline
(Sanetizer-todo.md phase 1, item 2) - distinct from scripts/local/bootstrap.ps1,
which builds installable binaries for end users, not a dev machine.
#>
$ErrorActionPreference = "Stop"
$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Set-Location $RepoRoot

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
  throw "Go is required on PATH - install the version in tools/go-version."
}

$RequiredVersion = (Get-Content (Join-Path $RepoRoot "tools/go-version")).Trim()
$GoVersionOutput = (& go version)
if ($GoVersionOutput -notmatch "^go version go([0-9.]+)") {
  throw "Could not parse 'go version' output: $GoVersionOutput"
}
$InstalledVersion = $Matches[1]
if ($InstalledVersion -ne $RequiredVersion) {
  throw "This repo is pinned to Go $RequiredVersion (tools/go-version), found $InstalledVersion on PATH. Install it from https://go.dev/dl/ or via your version manager, then re-run this script."
}
Write-Host "go toolchain: $InstalledVersion (matches tools/go-version)" -ForegroundColor Cyan

# Known, pre-existing gap (not introduced here): internal/hypervisor/sandbox's
# seccomp syscall table hardcodes x86_64 syscall numbers, so `go build ./...`
# fails specifically on linux/arm64 - unaffected on linux/amd64 and darwin/any.
$GoOS = (& go env GOOS).Trim()
$GoArch = (& go env GOARCH).Trim()
if ("$GoOS/$GoArch" -eq "linux/arm64") {
  Write-Warning "linux/arm64 host detected - internal/hypervisor/sandbox does not build here yet (known gap: its seccomp syscall table is x86_64-only; see containers/dev/Dockerfile)."
}

# go.work's own `use` block is the single source of truth for which
# directories are separate modules - read it instead of hardcoding the list
# a second time here, so this script can't silently drift from go.work.
$GoWorkContent = Get-Content (Join-Path $RepoRoot "go.work") -Raw
$UseBlock = [regex]::Match($GoWorkContent, "use \(([\s\S]*?)\)").Groups[1].Value
$Modules = $UseBlock -split "`n" | ForEach-Object { $_.Trim() } | Where-Object { $_ -and $_ -ne "." }

Write-Host "downloading main module dependencies..." -ForegroundColor Cyan
& go mod download
if ($LASTEXITCODE -ne 0) { throw "go mod download failed for the main module" }

foreach ($Module in $Modules) {
  Write-Host "downloading $Module dependencies..." -ForegroundColor Cyan
  Push-Location (Join-Path $RepoRoot $Module)
  try {
    $env:GOWORK = "off"
    & go mod download
    if ($LASTEXITCODE -ne 0) { throw "go mod download failed for $Module" }
  }
  finally {
    Remove-Item Env:GOWORK -ErrorAction SilentlyContinue
    Pop-Location
  }
}

Write-Host "bootstrap complete: main module + $($Modules.Count) separate modules ready to build offline" -ForegroundColor Green
Write-Host "next: make verify" -ForegroundColor Green
