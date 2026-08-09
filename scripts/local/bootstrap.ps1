<#
.SYNOPSIS
Build all platform-factory commands into an isolated Windows, Linux, or macOS environment.
#>
[CmdletBinding()]
param(
  [ValidateSet("linux", "darwin", "windows")]
  [string]$TargetOS = "",
  [ValidateSet("amd64", "arm64")]
  [string]$TargetArch = "",
  [string]$Environment = "",
  [string]$InstallPrefix = "",
  [switch]$Clean
)

$ErrorActionPreference = "Stop"
$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "../..")).Path
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
  throw "Go is required on PATH."
}
if (-not $TargetOS) { $TargetOS = (& go env GOOS).Trim() }
if (-not $TargetArch) { $TargetArch = (& go env GOARCH).Trim() }
$HostOS = (& go env GOHOSTOS).Trim()
$HostArch = (& go env GOHOSTARCH).Trim()
if (-not $Environment) { $Environment = Join-Path $RepoRoot ".platform-factory-env" }
$Environment = [IO.Path]::GetFullPath($Environment)
if ($Environment -eq [IO.Path]::GetPathRoot($Environment) -or $Environment -eq $RepoRoot) {
  throw "Refusing unsafe environment directory '$Environment'."
}
if (Test-Path $Environment) {
  if (-not $Clean) {
    throw "Environment already exists: $Environment. Pass -Clean to replace it."
  }
  Remove-Item -LiteralPath $Environment -Recurse -Force
}
$BinDir = Join-Path $Environment "bin"
New-Item -ItemType Directory -Path $BinDir -Force | Out-Null

$Suffix = if ($TargetOS -eq "windows") { ".exe" } else { "" }
$Version = (& git -C $RepoRoot describe --tags --always --dirty 2>$null)
if (-not $Version) { $Version = "dev" }
$Commands = @("platform-factory", "oci-builder", "example-service", "microvm-init", "platform-factory-control-plane", "platform-factory-worker")
$NativeVMM = $TargetOS -eq "darwin" -and $HostOS -eq $TargetOS -and $HostArch -eq $TargetArch
if ($TargetOS -eq "darwin") {
  if ($NativeVMM) {
    Write-Host "native macOS build: enabling CGO for Virtualization.framework support" -ForegroundColor Cyan
  }
  else {
    Write-Warning "Cross-building $TargetOS/$TargetArch from $HostOS/$HostArch; the resulting platform-factory binary does not include the native macOS VMM."
  }
}

$OldCGO = $env:CGO_ENABLED
$OldGOOS = $env:GOOS
$OldGOARCH = $env:GOARCH
try {
  foreach ($CommandName in $Commands) {
    Write-Host "building $CommandName for $TargetOS/$TargetArch..." -ForegroundColor Cyan
    $env:CGO_ENABLED = if ($CommandName -eq "platform-factory" -and $NativeVMM) { "1" } else { "0" }
    $env:GOOS = $TargetOS
    $env:GOARCH = $TargetArch
    $Ldflags = "-s -w"
    if ($CommandName -eq "platform-factory") { $Ldflags += " -X main.version=$Version" }
    & go build -trimpath "-ldflags=$Ldflags" `
      -o (Join-Path $BinDir "$CommandName$Suffix") `
      (Join-Path $RepoRoot "cmd/$CommandName")
    if ($LASTEXITCODE -ne 0) { throw "Build failed for $CommandName." }
  }
}
finally {
  $env:CGO_ENABLED = $OldCGO
  $env:GOOS = $OldGOOS
  $env:GOARCH = $OldGOARCH
}

@'
if (-not $env:PLATFORM_FACTORY_OLD_PATH) { $env:PLATFORM_FACTORY_OLD_PATH = $env:PATH }
$env:PLATFORM_FACTORY_ENV = $PSScriptRoot
$env:PATH = (Join-Path $PSScriptRoot 'bin') + [IO.Path]::PathSeparator + $env:PATH
function global:deactivate-platform-factory {
  if ($env:PLATFORM_FACTORY_OLD_PATH) { $env:PATH = $env:PLATFORM_FACTORY_OLD_PATH }
  Remove-Item Env:PLATFORM_FACTORY_OLD_PATH -ErrorAction SilentlyContinue
  Remove-Item Env:PLATFORM_FACTORY_ENV -ErrorAction SilentlyContinue
  Remove-Item Function:deactivate-platform-factory -ErrorAction SilentlyContinue
}
'@ | Set-Content -LiteralPath (Join-Path $Environment "Activate.ps1") -Encoding utf8

@'
@echo off
if not defined PLATFORM_FACTORY_OLD_PATH set "PLATFORM_FACTORY_OLD_PATH=%PATH%"
set "PLATFORM_FACTORY_ENV=%~dp0"
set "PATH=%~dp0bin;%PATH%"
echo platform-factory environment activated
'@ | Set-Content -LiteralPath (Join-Path $Environment "activate.bat") -Encoding ascii

@'
@echo off
if defined PLATFORM_FACTORY_OLD_PATH set "PATH=%PLATFORM_FACTORY_OLD_PATH%"
set "PLATFORM_FACTORY_OLD_PATH="
set "PLATFORM_FACTORY_ENV="
echo platform-factory environment deactivated
'@ | Set-Content -LiteralPath (Join-Path $Environment "deactivate.bat") -Encoding ascii

@'
# Source this file from Bash or Zsh.
if [ -n "${BASH_SOURCE:-}" ]; then
  _platform_factory_source=${BASH_SOURCE[0]}
else
  _platform_factory_source=$0
fi
PLATFORM_FACTORY_ENV=$(CDPATH= cd -- "$(dirname -- "$_platform_factory_source")" && pwd)
unset _platform_factory_source
if [ -z "${PLATFORM_FACTORY_OLD_PATH+x}" ]; then
  export PLATFORM_FACTORY_OLD_PATH="$PATH"
fi
export PLATFORM_FACTORY_ENV
export PATH="$PLATFORM_FACTORY_ENV/bin:$PATH"
deactivate_platform_factory() {
  if [ -n "${PLATFORM_FACTORY_OLD_PATH+x}" ]; then
    export PATH="$PLATFORM_FACTORY_OLD_PATH"
    unset PLATFORM_FACTORY_OLD_PATH
  fi
  unset PLATFORM_FACTORY_ENV
  unset -f deactivate_platform_factory
}
'@ | Set-Content -LiteralPath (Join-Path $Environment "activate") -Encoding utf8

[ordered]@{
  target_os = $TargetOS
  target_arch = $TargetArch
  version = $Version
  native_vmm = $NativeVMM
  commands = $Commands
} | ConvertTo-Json -Compress | Set-Content -LiteralPath (Join-Path $Environment "environment.json") -Encoding utf8

if ($InstallPrefix) {
  $InstallBin = Join-Path ([IO.Path]::GetFullPath($InstallPrefix)) "bin"
  New-Item -ItemType Directory -Path $InstallBin -Force | Out-Null
  foreach ($CommandName in $Commands) {
    Copy-Item -LiteralPath (Join-Path $BinDir "$CommandName$Suffix") -Destination $InstallBin -Force
  }
  Write-Host "installed commands in $InstallBin" -ForegroundColor Green
}

Write-Host "environment ready: $Environment" -ForegroundColor Green
Write-Host "activate with: . '$Environment/Activate.ps1'"
